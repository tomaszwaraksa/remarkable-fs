package instance

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSecondCopyIsRefusedAndNamesTheFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "lock")
	first, err := Acquire(path, "/home/me/remarkable")
	if err != nil {
		t.Fatalf("First copy must get the lock: %v", err)
	}

	_, err = Acquire(path, "/home/me/other")
	var running *AlreadyRunningError
	if !errors.As(err, &running) {
		t.Fatalf("Expected AlreadyRunningError for the second copy, got %v", err)
	}
	if running.PID != os.Getpid() || running.MountPoint != "/home/me/remarkable" {
		t.Errorf("Expected the first copy's PID %d and folder, got %+v", os.Getpid(), running)
	}
	if msg := err.Error(); !strings.Contains(msg, "already running") || !strings.Contains(msg, strconv.Itoa(os.Getpid())) {
		t.Errorf("Expected a message naming the running copy, got %q", msg)
	}

	first.Release()
	second, err := Acquire(path, "/home/me/other")
	if err != nil {
		t.Fatalf("Once released, the lock must be free again: %v", err)
	}
	defer second.Release()
	data, _ := os.ReadFile(path)
	if want := strconv.Itoa(os.Getpid()) + "\n/home/me/other\n"; string(data) != want {
		t.Errorf("Expected the lock file to name the new holder %q, got %q", want, data)
	}
}

func TestStaleLockFileIsNotAnObstacle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	// Left behind by a copy that crashed: the file exists, but nobody holds the lock.
	if err := os.WriteFile(path, []byte("999999\n/old/folder\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := Acquire(path, "/home/me/remarkable")
	if err != nil {
		t.Fatalf("A lock file nobody holds must not block a new copy: %v", err)
	}
	lock.Release()
}
