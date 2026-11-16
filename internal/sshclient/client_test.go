package sshclient

import (
	"errors"
	"io"
	"syscall"
	"testing"
)

func TestIsConnectionDropped(t *testing.T) {
	testCases := []struct {
		err      error
		expected bool
	}{
		{io.EOF, true},
		{errors.New("write: broken pipe"), true},
		{errors.New("read: connection reset by peer"), true},
		{errors.New("use of closed network connection"), true},
		{syscall.ECONNRESET, true},
		{syscall.EPIPE, true},
		{errors.New("file not found"), false},
		{nil, false},
	}

	for _, tc := range testCases {
		res := isConnectionDropped(tc.err)
		if res != tc.expected {
			t.Errorf("isConnectionDropped(%v): expected %v, got %v", tc.err, tc.expected, res)
		}
	}
}

func TestBuildCandidateHosts(t *testing.T) {
	cfg := Config{
		Host:          "192.168.1.50",
		FallbackHosts: []string{"remarkable.local", "10.11.99.1"},
	}

	var candidates []string
	addCandidate := func(h string) {
		if h == "" {
			return
		}
		for _, existing := range candidates {
			if existing == h {
				return
			}
		}
		candidates = append(candidates, h)
	}

	addCandidate(cfg.Host)
	for _, h := range cfg.FallbackHosts {
		addCandidate(h)
	}

	if len(candidates) != 3 {
		t.Fatalf("Expected 3 candidates, got %d", len(candidates))
	}
	if candidates[0] != "192.168.1.50" || candidates[1] != "remarkable.local" || candidates[2] != "10.11.99.1" {
		t.Errorf("Unexpected candidates order: %v", candidates)
	}
}

func TestScheduleXochitlRestartDebounce(t *testing.T) {
	client := &Client{}
	defer client.Close()

	// Call schedule multiple times in quick succession
	for i := 0; i < 5; i++ {
		client.ScheduleXochitlRestart()
	}

	client.restartMu.Lock()
	if client.restartTimer == nil {
		t.Errorf("Expected restartTimer to be active")
	}
	client.restartMu.Unlock()

	// Close cleanly stops the timer
	client.Close()
	client.restartMu.Lock()
	if client.restartTimer != nil {
		t.Errorf("Expected restartTimer to be nil after Close")
	}
	client.restartMu.Unlock()
}
