package fusefs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"remarkable-fs/internal/logger"
	"remarkable-fs/internal/model"
)

// readDocuments lists dir without the virtual .trackerignore.
func readDocuments(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	var documents []os.DirEntry
	for _, entry := range entries {
		if entry.Name() != trackerIgnoreName {
			documents = append(documents, entry)
		}
	}
	return documents, err
}

// The mount root lists a hidden, empty, read-only .trackerignore, so GNOME's indexer skips the
// folder. It can't be changed, removed, renamed or replaced, and only exists at the root.
func TestTrackerIgnoreAtMountRoot(t *testing.T) {
	if _, err := logger.Init(filepath.Join(t.TempDir(), "log.txt"), false); err != nil {
		t.Fatalf("Failed to init logger: %v", err)
	}
	defer logger.Close()
	mountDir := t.TempDir()

	root := model.NewDirectoryNode("", "root", time.Now(), nil)
	root.AddChild(model.NewDirectoryNode("folder-uuid", "Books", time.Now(), nil))
	server, err := Mount(mountDir, root, nil, nil, 0, "epub")
	if err != nil {
		t.Skipf("FUSE not available: %v", err)
	}
	defer func() { _ = Unmount(server, mountDir) }()

	marker := filepath.Join(mountDir, trackerIgnoreName)
	entries, err := os.ReadDir(mountDir)
	if err != nil {
		t.Fatalf("Listing the root failed: %v", err)
	}
	listed := false
	for _, entry := range entries {
		listed = listed || entry.Name() == trackerIgnoreName
	}
	if !listed {
		t.Errorf("Expected %s in the root listing, got %v", trackerIgnoreName, entries)
	}

	info, err := os.Stat(marker)
	if err != nil {
		t.Fatalf("Expected %s to exist: %v", trackerIgnoreName, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0444 || info.Size() != 0 {
		t.Errorf("Expected an empty read-only regular file, got mode %v size %d", info.Mode(), info.Size())
	}
	if data, err := os.ReadFile(marker); err != nil || len(data) != 0 {
		t.Errorf("Expected to read it as empty, got %q (err %v)", data, err)
	}

	refused := func(what string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("Expected %s to be refused", what)
		}
	}
	refused("writing", os.WriteFile(marker, []byte("x"), 0644))
	_, err = os.OpenFile(marker, os.O_WRONLY|os.O_APPEND, 0)
	refused("appending", err)
	refused("truncating", os.Truncate(marker, 0))
	refused("chmod", os.Chmod(marker, 0644))
	refused("deleting", os.Remove(marker))
	refused("renaming it", os.Rename(marker, filepath.Join(mountDir, "x.txt")))
	refused("moving it into a folder", os.Rename(marker, filepath.Join(mountDir, "Books", trackerIgnoreName)))
	refused("creating a folder with its name", os.Mkdir(marker, 0755))
	_, err = os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if !errors.Is(err, syscall.EEXIST) {
		t.Errorf("Expected EEXIST when creating it anew, got %v", err)
	}
	if info, err := os.Stat(marker); err != nil || info.Size() != 0 {
		t.Errorf("Expected it unchanged after all that, got %v (err %v)", info, err)
	}

	if _, err := os.Stat(filepath.Join(mountDir, "Books", trackerIgnoreName)); !os.IsNotExist(err) {
		t.Errorf("Expected no %s inside folders, got %v", trackerIgnoreName, err)
	}
}
