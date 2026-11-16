package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogger(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "remarkable-test-log-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logFile := filepath.Join(tempDir, "test-log.txt")
	l, err := Init(logFile, false)
	if err != nil {
		t.Fatalf("Failed to init logger: %v", err)
	}
	defer l.Close()

	Connection("Connected to %s", "192.168.1.100")
	Action("Browsed folder /Books")
	Disconnection("Disconnected from %s", "192.168.1.100")

	_ = l.Close()

	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	text := string(content)
	if !strings.Contains(text, "[CONNECTION   ]") || !strings.Contains(text, "Connected to 192.168.1.100") {
		t.Errorf("Expected connection log not found in: %s", text)
	}
	if !strings.Contains(text, "[USER_ACTION  ]") || !strings.Contains(text, "Browsed folder /Books") {
		t.Errorf("Expected action log not found in: %s", text)
	}
	if !strings.Contains(text, "[DISCONNECTION]") || !strings.Contains(text, "Disconnected from 192.168.1.100") {
		t.Errorf("Expected disconnection log not found in: %s", text)
	}
}

func TestLoggerAppendsOnInitAndClean(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "test-clean-log.txt")
	if err := os.WriteFile(logFile, []byte("old log line 1\nold log line 2\n"), 0644); err != nil {
		t.Fatalf("Failed to write initial log: %v", err)
	}

	// Init keeps the earlier history, so restarts don't wipe what --status reads
	l, err := Init(logFile, false)
	if err != nil {
		t.Fatalf("Failed to init logger: %v", err)
	}
	defer l.Close()

	Info("New mount session started")

	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}
	text := string(content)
	if !strings.HasPrefix(text, "old log line 1\nold log line 2\n") {
		t.Errorf("Expected old log lines to be kept, got: %s", text)
	}
	if !strings.Contains(text, "New mount session started") {
		t.Errorf("Expected new session log, but got: %s", text)
	}

	if err := Clean(); err != nil {
		t.Fatalf("Failed to clean logger: %v", err)
	}
	Info("after clean")
	contentAfterClean, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}
	if strings.Contains(string(contentAfterClean), "old log line") || !strings.Contains(string(contentAfterClean), "after clean") {
		t.Errorf("Expected only the new line after Clean(), got %q", contentAfterClean)
	}
}

func TestRotateIfLarge(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "log.txt")
	if err := rotateIfLarge(logFile, 10); err != nil {
		t.Fatalf("A missing log must not be an error: %v", err)
	}
	if err := os.WriteFile(logFile, []byte("small"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := rotateIfLarge(logFile, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(logFile + ".1"); err == nil {
		t.Errorf("A small log must not be rotated")
	}
	if err := os.WriteFile(logFile, []byte("larger than ten bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := rotateIfLarge(logFile, 10); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(logFile + ".1"); err != nil || string(got) != "larger than ten bytes" {
		t.Errorf("Expected the large log moved to .1, got %q (err %v)", got, err)
	}
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Errorf("Expected the log itself to be gone after rotation, err %v", err)
	}
}
