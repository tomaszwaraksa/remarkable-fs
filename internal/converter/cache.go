package converter

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"remarkable-fs/internal/logger"
)

// CacheManager manages cached rendered PDFs and invalidates stale versions.
type CacheManager struct {
	mu       sync.RWMutex
	cacheDir string
}

var defaultCache *CacheManager

// InitCache initializes the global cache manager.
func InitCache() (*CacheManager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".cache", "remarkable-fs", "rendered")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory %s: %w", dir, err)
	}

	cm := &CacheManager{
		cacheDir: dir,
	}
	defaultCache = cm
	return cm, nil
}

// GetCache returns the default cache manager.
func GetCache() *CacheManager {
	if defaultCache == nil {
		cm, err := InitCache()
		if err == nil {
			return cm
		}
	}
	return defaultCache
}

// GetRenderedPath returns the path to a cached rendered PDF if valid, or empty string and false if cache miss.
func (cm *CacheManager) GetRenderedPath(uuid string, lastModified time.Time) (string, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	key := fmt.Sprintf("%s_%d.pdf", uuid, lastModified.UnixMilli())
	target := filepath.Join(cm.cacheDir, key)

	info, err := os.Stat(target)
	if err == nil && info.Size() > 0 {
		return target, true
	}
	return target, false
}

// Invalidate removes any older cached files for the given UUID.
func (cm *CacheManager) Invalidate(uuid string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	entries, err := os.ReadDir(cm.cacheDir)
	if err != nil {
		return
	}

	prefix := uuid + "_"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), ".pdf") {
			oldPath := filepath.Join(cm.cacheDir, entry.Name())
			_ = os.Remove(oldPath)
			logger.Info("Invalidated stale cache file: %s", oldPath)
		}
	}
}

// InvalidateAll purges all cached rendered PDF files.
func (cm *CacheManager) InvalidateAll() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	entries, err := os.ReadDir(cm.cacheDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".pdf") || strings.HasSuffix(entry.Name(), ".tmp") {
			_ = os.Remove(filepath.Join(cm.cacheDir, entry.Name()))
		}
	}
	logger.Info("Invalidated all rendered cache files")
}

// StoreRendered writes the rendered PDF stream to the cache atomically.
func (cm *CacheManager) StoreRendered(uuid string, lastModified time.Time, r io.Reader) (string, error) {
	cm.Invalidate(uuid) // Purge old versions first

	cm.mu.Lock()
	defer cm.mu.Unlock()

	key := fmt.Sprintf("%s_%d.pdf", uuid, lastModified.UnixMilli())
	target := filepath.Join(cm.cacheDir, key)
	tmp := target + ".tmp"

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to create cache file %s: %w", tmp, err)
	}

	_, err = io.Copy(f, r)
	_ = f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("failed to write cache file: %w", err)
	}

	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("failed to finalize cache file: %w", err)
	}

	logger.Info("Cached rendered PDF: %s", target)
	return target, nil
}
