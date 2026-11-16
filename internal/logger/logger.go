package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"remarkable-fs/internal/paths"
)

// LogCategory represents the type of logged activity.
type LogCategory string

const (
	CategoryConnection    LogCategory = "CONNECTION"
	CategoryDisconnection LogCategory = "DISCONNECTION"
	CategoryAction        LogCategory = "USER_ACTION"
	CategoryError         LogCategory = "ERROR"
	CategoryWarn          LogCategory = "WARN"
	CategoryInfo          LogCategory = "INFO"
)

// Logger writes thread-safe timestamped activity logs to a file and optionally stdout.
type Logger struct {
	mu       sync.Mutex
	filePath string
	file     *os.File
	toStdout bool
}

var defaultLogger *Logger

// maxLogSize is the size above which Init moves the log aside to "<log>.1" (one old copy is kept).
const maxLogSize = 5 * 1024 * 1024

// Init initializes the global logger, appending to the specified path. The log survives restarts,
// so the history stays readable; a log over maxLogSize is first rotated to "<path>.1".
func Init(path string, toStdout bool) (*Logger, error) {
	expandedPath := paths.ExpandHome(path)
	if expandedPath == "" {
		home, _ := os.UserHomeDir()
		expandedPath = filepath.Join(home, ".config", "remarkable-fs", "log.txt")
	}

	dir := filepath.Dir(expandedPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory %s: %w", dir, err)
	}
	if err := rotateIfLarge(expandedPath, maxLogSize); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(expandedPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file %s: %w", expandedPath, err)
	}

	l := &Logger{
		filePath: expandedPath,
		file:     f,
		toStdout: toStdout,
	}
	defaultLogger = l
	return l, nil
}

// rotateIfLarge renames path to "<path>.1" (replacing an older copy) when it is larger than limit.
func rotateIfLarge(path string, limit int64) error {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= limit {
		return nil // a missing log is created by the caller
	}
	if err := os.Rename(path, path+".1"); err != nil {
		return fmt.Errorf("failed to rotate log file %s: %w", path, err)
	}
	return nil
}

// Clean truncates the log file to 0 bytes.
func (l *Logger) Clean() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		if err := l.file.Truncate(0); err != nil {
			return err
		}
		_, _ = l.file.Seek(0, 0)
	}
	return nil
}

// Clean truncates the active default log file.
func Clean() error {
	if defaultLogger != nil {
		return defaultLogger.Clean()
	}
	return nil
}

// Close closes the underlying log file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}

// Log writes a message with a category and timestamp.
func (l *Logger) Log(cat LogCategory, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	entry := fmt.Sprintf("[%s] [%-13s] %s\n", timestamp, cat, msg)

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		_, _ = l.file.WriteString(entry)
		_ = l.file.Sync()
	}

	if l.toStdout {
		fmt.Print(entry)
	}
}

// Global convenience wrappers

func Log(cat LogCategory, format string, args ...any) {
	if defaultLogger != nil {
		defaultLogger.Log(cat, format, args...)
	} else {
		msg := fmt.Sprintf(format, args...)
		fmt.Printf("[%s] [%-13s] %s\n", time.Now().Format("2006-01-02 15:04:05"), cat, msg)
	}
}

func Connection(format string, args ...any) {
	Log(CategoryConnection, format, args...)
}

func Disconnection(format string, args ...any) {
	Log(CategoryDisconnection, format, args...)
}

func Action(format string, args ...any) {
	Log(CategoryAction, format, args...)
}

func Error(format string, args ...any) {
	Log(CategoryError, format, args...)
}

func Warn(format string, args ...any) {
	Log(CategoryWarn, format, args...)
}

func Info(format string, args ...any) {
	Log(CategoryInfo, format, args...)
}

func Close() error {
	if defaultLogger != nil {
		return defaultLogger.Close()
	}
	return nil
}
