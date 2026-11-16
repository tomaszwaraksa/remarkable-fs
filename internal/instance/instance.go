// Package instance makes sure only one copy of remarkable-fs runs on a computer at a time.
package instance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// DefaultLockPath is where the lock lives: ~/.cache/remarkable-fs/lock. The installer's
// --status reads the same file to tell whether a copy is running outside the service.
func DefaultLockPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "remarkable-fs.lock")
	}
	return filepath.Join(home, ".cache", "remarkable-fs", "lock")
}

// Lock is held by the running copy for as long as it runs. The kernel releases it when the
// process ends, also after a crash, so there's never a stale lock to clean up.
type Lock struct {
	file *os.File
}

// AlreadyRunningError reports the copy that holds the lock.
type AlreadyRunningError struct {
	PID        int    // 0 if unknown
	MountPoint string // "" if unknown
}

func (e *AlreadyRunningError) Error() string {
	who := "another copy"
	if e.PID > 0 {
		who += fmt.Sprintf(" (process %d", e.PID)
		if e.MountPoint != "" {
			who += ", folder " + e.MountPoint
		}
		who += ")"
	}
	return "remarkable-fs is already running on this computer: " + who +
		". Only one copy can run at a time; stop the other one first (for the background service: systemctl --user stop remarkable-fs)"
}

// Acquire takes the lock at path for this process, which serves mountPoint. If another copy
// holds it, it returns an *AlreadyRunningError naming that copy.
func Acquire(path, mountPoint string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("creating the folder for %s: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		defer file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, readHolder(file)
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	// Who holds it, for the message a second copy shows and for the installer's --status.
	if err := file.Truncate(0); err == nil {
		_, _ = file.WriteAt([]byte(fmt.Sprintf("%d\n%s\n", os.Getpid(), mountPoint)), 0)
	}
	return &Lock{file: file}, nil
}

// readHolder reads the PID and folder the running copy wrote into the lock file.
func readHolder(file *os.File) *AlreadyRunningError {
	data := make([]byte, 4096)
	n, _ := file.ReadAt(data, 0)
	lines := strings.SplitN(string(data[:n]), "\n", 3)
	holder := &AlreadyRunningError{}
	if pid, err := strconv.Atoi(strings.TrimSpace(lines[0])); err == nil {
		holder.PID = pid
	}
	if len(lines) > 1 {
		holder.MountPoint = strings.TrimSpace(lines[1])
	}
	return holder
}

// Release gives the lock up. The file stays; it only matters while it's locked.
func (l *Lock) Release() {
	if l == nil || l.file == nil {
		return
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	_ = l.file.Close()
	l.file = nil
}
