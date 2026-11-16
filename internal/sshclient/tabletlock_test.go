package sshclient

import (
	"path/filepath"
	"testing"

	"remarkable-fs/internal/logger"
)

// Without a connection the lock can't be taken: changes go ahead as before (they fail on their
// own if the tablet is really gone), and releasing stays safe, also twice and when nested.
func TestLockTabletGoesAheadWithoutConnection(t *testing.T) {
	if _, err := logger.Init(filepath.Join(t.TempDir(), "log.txt"), false); err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	c := New(Config{Host: "127.0.0.1", Port: 1})
	outer := c.LockTablet()
	inner := c.LockTablet()
	if c.tabletLock.holders != 2 {
		t.Errorf("Expected nested changes to share the lock (2 holders), got %d", c.tabletLock.holders)
	}
	inner()
	inner()
	outer()
	if c.tabletLock.holders != 0 || c.tabletLock.session != nil {
		t.Errorf("Expected the lock to be free again, got %d holders", c.tabletLock.holders)
	}
}
