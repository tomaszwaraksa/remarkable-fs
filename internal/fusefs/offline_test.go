package fusefs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"remarkable-fs/internal/logger"
	"remarkable-fs/internal/remarkable"
)

type fakeInfo struct {
	name  string
	size  int64
	isDir bool
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) Mode() fs.FileMode  { return 0644 }
func (f fakeInfo) ModTime() time.Time { return time.Unix(1672531200, 0) }
func (f fakeInfo) IsDir() bool        { return f.isDir }
func (f fakeInfo) Sys() any           { return nil }

// fakeTablet serves a xochitl folder from memory.
type fakeTablet map[string][]byte

func (t fakeTablet) ReadDir(string) ([]os.FileInfo, error) {
	var list []os.FileInfo
	for p, data := range t {
		list = append(list, fakeInfo{name: filepath.Base(p), size: int64(len(data))})
	}
	return list, nil
}

func (t fakeTablet) ReadFile(p string) ([]byte, error) {
	if data, ok := t[p]; ok {
		return data, nil
	}
	return nil, fmt.Errorf("file not found: %s", p)
}

func (t fakeTablet) Stat(p string) (os.FileInfo, error) {
	if p == remarkable.XochitlDirectory {
		return fakeInfo{name: "xochitl", isDir: true}, nil
	}
	if data, ok := t[p]; ok {
		return fakeInfo{name: filepath.Base(p), size: int64(len(data))}, nil
	}
	return nil, os.ErrNotExist
}

func TestMountWhileOfflineThenAttachTree(t *testing.T) {
	if _, err := logger.Init(filepath.Join(t.TempDir(), "log.txt"), false); err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	dir := remarkable.XochitlDirectory
	tablet := fakeTablet{
		dir + "/doc-1.metadata": []byte(`{"visibleName": "Report", "type": "DocumentType", "parent": "", "lastModified": "1672531300000"}`),
		dir + "/doc-1.pdf":      []byte("PDF"),
	}
	repo := remarkable.NewRepository(tablet)
	mountDir := t.TempDir()
	mounted, err := Mount(mountDir, repo.Root(), nil, repo, 10*1024*1024, "epub")
	if err != nil {
		t.Skipf("Skipping FUSE mount test: %v", err)
	}
	defer func() { _ = Unmount(mounted, mountDir) }()

	// Offline: an empty, read-only folder that refuses changes instead of writing to the local disk
	info, err := os.Stat(mountDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0555 {
		t.Errorf("Expected the offline folder to be read-only (0555), got %v", info.Mode().Perm())
	}
	entries, err := os.ReadDir(mountDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != trackerIgnoreName {
		t.Errorf("Expected an empty folder (only %s) while offline, got %v (err %v)", trackerIgnoreName, entries, err)
	}
	if err := os.WriteFile(filepath.Join(mountDir, "notes.txt"), []byte("x"), 0644); !errors.Is(err, syscall.EROFS) {
		t.Errorf("Expected EROFS when saving while offline, got %v", err)
	}
	if err := os.Mkdir(filepath.Join(mountDir, "New"), 0755); !errors.Is(err, syscall.EROFS) {
		t.Errorf("Expected EROFS when creating a folder while offline, got %v", err)
	}

	// Connected: the documents appear in the same mounted folder, without a remount
	if _, err := repo.LoadTree(); err != nil {
		t.Fatalf("LoadTree failed: %v", err)
	}
	mounted.AttachTree()
	if _, err := os.Stat(filepath.Join(mountDir, "Report.pdf")); err != nil {
		t.Errorf("Expected Report.pdf to be visible after loading, got %v", err)
	}
	info, err = os.Stat(mountDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Errorf("Expected the folder to be writable once loaded, got %v", info.Mode().Perm())
	}
}
