package fusefs

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"remarkable-fs/internal/logger"
)

// fsType is the filesystem type the kernel lists for a remarkable-fs mount.
const fsType = "fuse.remarkable-fs"

// releaseStaleMount prepares mountPoint for mounting. A remarkable-fs mount left by a crashed run
// (it answers "transport endpoint is not connected", or not at all) is detached. A remarkable-fs
// mount that still works, or any other filesystem mounted there, is an error: it is never touched.
func releaseStaleMount(mountPoint string) error {
	mountinfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("reading the list of mounts: %w", err)
	}
	fstype, isMounted := mountFSType(mountinfo, resolveMountPath(mountPoint))
	statErr := statWithTimeout(mountPoint, 3*time.Second)
	isDead := errors.Is(statErr, syscall.ENOTCONN) || errors.Is(statErr, os.ErrDeadlineExceeded)

	switch {
	case !isMounted && !isDead:
		return nil
	case isMounted && fstype != fsType:
		return fmt.Errorf("%s already has another filesystem mounted on it (%s); remarkable-fs won't mount over it", mountPoint, fstype)
	case !isDead:
		return fmt.Errorf("%s is already mounted by another running remarkable-fs", mountPoint)
	}
	if err := lazyUnmount(mountPoint); err != nil {
		return fmt.Errorf("releasing the stuck mount at %s: %w", mountPoint, err)
	}
	logger.Info("Released a stuck mount left by an earlier run at %s", mountPoint)
	return nil
}

// resolveMountPath returns the absolute path of mountPoint as the kernel lists it. Only the parent
// folder's symlinks are resolved, because the mount point itself may be a dead mount.
func resolveMountPath(mountPoint string) string {
	abs, err := filepath.Abs(mountPoint)
	if err != nil {
		return mountPoint
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return abs
	}
	return filepath.Join(parent, filepath.Base(abs))
}

// statWithTimeout stats path, giving up after timeout (a hung FUSE mount never answers).
func statWithTimeout(path string, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { // may stay blocked on a hung mount; it ends with the process
		_, err := os.Stat(path)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return os.ErrDeadlineExceeded
	}
}

// mountFSType returns the filesystem type mounted at path according to /proc/self/mountinfo
// (the last, i.e. topmost, entry wins), and whether anything is mounted there at all.
func mountFSType(mountinfo []byte, path string) (string, bool) {
	fstype, found := "", false
	scanner := bufio.NewScanner(bytes.NewReader(mountinfo))
	for scanner.Scan() {
		// "36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw,errors=continue"
		fields := strings.Fields(scanner.Text())
		sep := indexOf(fields, "-")
		if len(fields) < 5 || sep < 0 || sep+1 >= len(fields) {
			continue
		}
		if unescapeMountPath(fields[4]) == path {
			fstype, found = fields[sep+1], true
		}
	}
	return fstype, found
}

func indexOf(fields []string, want string) int {
	for i := 6; i < len(fields); i++ { // the separator comes after the 6 fixed fields
		if fields[i] == want {
			return i
		}
	}
	return -1
}

// unescapeMountPath decodes the octal escapes the kernel uses in mount paths (e.g. \040 for a space).
func unescapeMountPath(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// lazyUnmount detaches the FUSE mount at mountPoint with fusermount3 (or fusermount) -u -z.
func lazyUnmount(mountPoint string) error {
	for _, tool := range []string{"fusermount3", "fusermount"} {
		bin, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		out, err := exec.Command(bin, "-u", "-z", mountPoint).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s -u -z %s: %v: %s", tool, mountPoint, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return errors.New("neither fusermount3 nor fusermount was found")
}
