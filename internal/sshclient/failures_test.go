package sshclient

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remarkable-fs/internal/logger"
)

func TestClassifyFailure(t *testing.T) {
	cases := []struct {
		err  error
		want failureKind
	}{
		{fmt.Errorf("ssh: handshake failed: %w", &IdentityChangedError{Fingerprint: "x"}), failureIdentityChanged},
		{errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey]"), failureRejectedKey},
		{errors.New("dial tcp 192.168.1.50:22: connect: connection refused"), failureRefused},
		{errors.New("dial tcp 192.168.1.50:22: i/o timeout"), failureUnreachable},
	}
	for _, tc := range cases {
		if got := classifyFailure(tc.err); got != tc.want {
			t.Errorf("classifyFailure(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestNoteFailureLogsOncePerState(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log.txt")
	l, err := logger.Init(logPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	c := New(Config{Host: "remarkable.local"})
	changed := fmt.Errorf("ssh: handshake failed: %w", &IdentityChangedError{Fingerprint: "x"})
	rejected := errors.New("ssh: unable to authenticate")
	for range 3 {
		c.noteFailure("remarkable.local", changed)
		c.noteFailure("remarkable.local", errors.New("i/o timeout"))
	}
	c.noteFailure("remarkable.local", rejected)
	c.clearFailures()
	c.noteFailure("remarkable.local", rejected)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if n := strings.Count(log, "tablet identity changed"); n != 1 {
		t.Errorf("Expected the identity line once, got %d times:\n%s", n, log)
	}
	if n := strings.Count(log, "rejected the key"); n != 2 {
		t.Errorf("Expected the rejected-key line twice (before and after a success), got %d:\n%s", n, log)
	}
	if strings.Contains(log, "can't be reached") {
		t.Errorf("Unreachable hosts must not be logged here:\n%s", log)
	}
}
