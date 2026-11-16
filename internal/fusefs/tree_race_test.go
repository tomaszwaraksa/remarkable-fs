package fusefs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"remarkable-fs/internal/logger"
	"remarkable-fs/internal/remarkable"
)

// changingTablet is a fakeTablet whose documents change while the file system reads them.
type changingTablet struct {
	mu    sync.Mutex
	files fakeTablet
}

func (t *changingTablet) set(p string, data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.files[p] = data
}

func (t *changingTablet) remove(p string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.files, p)
}

func (t *changingTablet) ReadDir(p string) ([]os.FileInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.files.ReadDir(p)
}

func (t *changingTablet) ReadFile(p string) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.files.ReadFile(p)
}

func (t *changingTablet) Stat(p string) (os.FileInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.files.Stat(p)
}

// TestTreeChangesWhileListing lists the folder and reads file details while the check for tablet
// changes, uploads, renames and deletes all change the document tree at the same time (IDEAS C9).
// Meant for `go test -race`, which reports any unguarded access.
func TestTreeChangesWhileListing(t *testing.T) {
	if _, err := logger.Init(filepath.Join(t.TempDir(), "log.txt"), false); err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	dir := remarkable.XochitlDirectory
	tablet := &changingTablet{files: fakeTablet{
		dir + "/doc-1.metadata": []byte(`{"visibleName": "Report", "type": "DocumentType", "parent": "", "lastModified": "1672531300000"}`),
		dir + "/doc-1.pdf":      []byte("PDF"),
	}}
	repo := remarkable.NewRepository(tablet)
	if _, err := repo.LoadTree(); err != nil {
		t.Fatal(err)
	}
	mountDir := t.TempDir()
	mounted, err := Mount(mountDir, repo.Root(), nil, repo, 10*1024*1024, "epub")
	if err != nil {
		t.Skipf("Skipping FUSE mount test: %v", err)
	}
	defer func() { _ = Unmount(mounted, mountDir) }()
	mounted.AttachTree()

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// The tablet: documents appear, change and disappear, as when another computer works on it
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			doc := fmt.Sprintf("%s/tab-%d", dir, i%4)
			tablet.set(doc+".metadata", fmt.Appendf(nil, `{"visibleName": "Tablet %d", "type": "DocumentType", "parent": "", "lastModified": "%d"}`, i%4, 1672531300000+int64(i)))
			tablet.set(doc+".pdf", []byte("PDF"))
			gone := fmt.Sprintf("%s/tab-%d", dir, (i+2)%4)
			tablet.remove(gone + ".metadata")
			tablet.remove(gone + ".pdf")
			if _, err := repo.CheckForChanges(true); err != nil {
				t.Errorf("CheckForChanges failed: %v", err)
				return
			}
		}
	})

	// A file manager: lists the folder and reads each entry's details, over and over
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			entries, err := os.ReadDir(mountDir)
			if err != nil {
				t.Errorf("Listing failed: %v", err)
				return
			}
			for _, e := range entries {
				_, _ = os.Stat(filepath.Join(mountDir, e.Name()))
			}
		}
	})

	// This computer: uploads, renames and deletes documents. Uploads don't reach this fake
	// tablet (no SSH client), so a tablet check may drop one at any moment, as it would a
	// document deleted on the tablet: a missing file is fine, anything else is not.
	samplePDF := []byte("%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Count 1/Kids[3 0 R]>>endobj\n3 0 obj<</Type/Page/MediaBox[0 0 595 842]/Parent 2 0 R/Resources<<>>>>endobj\nxref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000052 00000 n \n0000000102 00000 n \ntrailer<</Size 4/Root 1 0 R>>\nstartxref\n187\n%%EOF\n")
	failed := func(step string, i int, err error) bool {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s %d failed: %v", step, i, err)
			return true
		}
		return false
	}
	for i := range 20 {
		uploaded := filepath.Join(mountDir, fmt.Sprintf("Upload %d.pdf", i))
		renamed := filepath.Join(mountDir, fmt.Sprintf("Renamed %d.pdf", i))
		if failed("Upload", i, os.WriteFile(uploaded, samplePDF, 0644)) ||
			failed("Rename", i, os.Rename(uploaded, renamed)) ||
			failed("Delete", i, os.Remove(renamed)) {
			break
		}
	}
	close(stop)
	wg.Wait()

	if _, err := os.Stat(filepath.Join(mountDir, "Report.pdf")); err != nil {
		t.Errorf("Expected Report.pdf to stay visible, got %v", err)
	}
}
