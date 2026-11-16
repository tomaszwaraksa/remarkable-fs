package fusefs

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"remarkable-fs/internal/logger"
	"remarkable-fs/internal/model"
)

// Uploads happen when a file is closed. Shell redirection (cat x.pdf > ~/remarkable/x.pdf) closes
// a copy of the file before writing anything, and programs may write again after a close: the
// tablet must always end up with everything written (0.ideas.md C13).
//
// The writing is done by bash in a process of its own, as in real use. Not from this test
// process: the filesystem runs here, and a file of the mount held open here would be flushed
// (and wait for the upload) whenever the filesystem starts a helper such as pdftoppm.
func TestUploadKeepsWritesAroundCloses(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("needs bash")
	}
	logPath := filepath.Join(t.TempDir(), "log.txt")
	if _, err := logger.Init(logPath, false); err != nil {
		t.Fatalf("Failed to init logger: %v", err)
	}
	defer logger.Close()
	mountDir := t.TempDir()
	root := model.NewDirectoryNode("", "root", time.Now(), nil)
	server, err := Mount(mountDir, root, nil, nil, 0, "epub")
	if err != nil {
		t.Skipf("FUSE not available: %v", err)
	}
	defer func() { _ = Unmount(server, mountDir) }()

	pdf := []byte("%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Count 1/Kids[3 0 R]>>endobj\n3 0 obj<</Type/Page/MediaBox[0 0 595 842]/Parent 2 0 R/Resources<<>>>>endobj\nxref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000052 00000 n \n0000000102 00000 n \ntrailer<</Size 4/Root 1 0 R>>\nstartxref\n187\n%%EOF\n")
	sample := filepath.Join(t.TempDir(), "sample.pdf")
	if err := os.WriteFile(sample, pdf, 0644); err != nil {
		t.Fatal(err)
	}

	// shell runs a bash script in the mount folder, with $SAMPLE the sample PDF and $LOG the log.
	shell := func(script string) {
		t.Helper()
		cmd := exec.Command("bash", "-c", script)
		cmd.Dir = mountDir
		cmd.Env = append(os.Environ(), "SAMPLE="+sample, "LOG="+logPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bash failed: %v\n%s", err, out)
		}
	}
	// uploads returns the byte counts of the uploads of name so far, from the log.
	uploads := func(name string) []int {
		t.Helper()
		data, _ := os.ReadFile(logPath)
		re := regexp.MustCompile(`Successfully uploaded "` + regexp.QuoteMeta(name) + `" \(\d+ pages, (\d+) bytes\)`)
		var sizes []int
		for _, m := range re.FindAllStringSubmatch(string(data), -1) {
			n, _ := strconv.Atoi(m[1])
			sizes = append(sizes, n)
		}
		return sizes
	}
	size := func(name string) int64 {
		t.Helper()
		info, err := os.Stat(filepath.Join(mountDir, name))
		if err != nil {
			t.Fatalf("Stat %s failed: %v", name, err)
		}
		return info.Size()
	}
	// settle waits for the release of the last close, which reaches the filesystem after close()
	// has returned, so a wrong extra upload there would show up in the checks.
	settle := func() { time.Sleep(300 * time.Millisecond) }
	equal := func(got []int, want ...int) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	t.Run("shell redirection", func(t *testing.T) {
		shell(`cat "$SAMPLE" > Redirected.pdf`)
		settle()
		if got := uploads("Redirected.pdf"); !equal(got, len(pdf)) {
			t.Errorf("Expected one upload of %d bytes, got %v", len(pdf), got)
		}
		if got := size("Redirected.pdf"); got != int64(len(pdf)) {
			t.Errorf("Expected the document to be %d bytes, got %d", len(pdf), got)
		}
	})

	t.Run("closed once before writing: no upload until something is written", func(t *testing.T) {
		shell(`exec 3>Late.pdf; exec 4>&3; exec 3>&-
			if grep -q 'uploaded "Late.pdf"' "$LOG"; then echo "uploaded before anything was written"; exit 1; fi
			cat "$SAMPLE" >&4; exec 4>&-`)
		settle()
		if got := uploads("Late.pdf"); !equal(got, len(pdf)) {
			t.Errorf("Expected one upload of %d bytes, got %v", len(pdf), got)
		}
	})

	t.Run("written again after an upload: uploaded again, whole", func(t *testing.T) {
		shell(`exec 3>Twice.pdf; exec 4>&3; head -c 100 "$SAMPLE" >&3; exec 3>&-; tail -c +101 "$SAMPLE" >&4; exec 4>&-`)
		settle()
		if got := uploads("Twice.pdf"); !equal(got, 100, len(pdf)) {
			t.Errorf("Expected uploads of 100 and %d bytes, got %v", len(pdf), got)
		}
		if got := size("Twice.pdf"); got != int64(len(pdf)) {
			t.Errorf("Expected the document to be %d bytes, got %d", len(pdf), got)
		}
	})

	t.Run("renamed while still open: the next upload keeps the new name", func(t *testing.T) {
		shell(`exec 3>Draft.pdf; exec 4>&3; head -c 100 "$SAMPLE" >&3; exec 3>&-; mv Draft.pdf Final.pdf; tail -c +101 "$SAMPLE" >&4; exec 4>&-`)
		settle()
		if got := uploads("Draft.pdf"); !equal(got, 100) {
			t.Errorf("Expected the first upload as Draft.pdf (100 bytes), got %v", got)
		}
		if got := uploads("Final.pdf"); !equal(got, len(pdf)) {
			t.Errorf("Expected the second upload as Final.pdf (%d bytes), got %v", len(pdf), got)
		}
		if got := size("Final.pdf"); got != int64(len(pdf)) {
			t.Errorf("Expected Final.pdf to be %d bytes, got %d", len(pdf), got)
		}
		if _, err := os.Stat(filepath.Join(mountDir, "Draft.pdf")); !os.IsNotExist(err) {
			t.Errorf("Expected Draft.pdf to be gone, got %v", err)
		}
	})

	t.Run("nothing written after an upload: no second upload", func(t *testing.T) {
		shell(`exec 3>Once.pdf; exec 4>&3; cat "$SAMPLE" >&3; exec 3>&-; exec 4>&-`)
		settle()
		if got := uploads("Once.pdf"); !equal(got, len(pdf)) {
			t.Errorf("Expected exactly one upload, got %v", got)
		}
	})

	t.Run("Markdown written again: converted again from the whole text", func(t *testing.T) {
		shell(`exec 3>Notes.md; exec 4>&3; printf '# Notes\n\nFirst part.\n' >&3; exec 3>&-; printf '\nSecond part, which makes the book a good deal longer than before.\n' >&4; exec 4>&-`)
		got := uploads("Notes.md")
		if len(got) != 2 || got[1] <= got[0] {
			t.Errorf("Expected two uploads, the second one bigger (the whole text), got %v", got)
		}
		if _, err := os.Stat(filepath.Join(mountDir, "Notes.epub")); err != nil {
			t.Errorf("Expected Notes.epub: %v", err)
		}
	})

	t.Run("an empty file is still uploaded once, at the last close", func(t *testing.T) {
		shell(`: > Empty.pdf`)
		// The last close's release reaches the filesystem after close() has returned.
		for i := 0; i < 50 && len(uploads("Empty.pdf")) == 0; i++ {
			time.Sleep(20 * time.Millisecond)
		}
		if got := uploads("Empty.pdf"); !equal(got, 0) {
			t.Errorf("Expected one upload of 0 bytes, got %v", got)
		}
	})
}
