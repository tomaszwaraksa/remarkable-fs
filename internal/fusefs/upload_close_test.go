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
	// newBuffers lists upload buffers made since the test started and still there.
	before := map[string]bool{}
	buffers := func() []string {
		found, _ := filepath.Glob(filepath.Join(os.TempDir(), "rm_upload_*"))
		return found
	}
	for _, b := range buffers() {
		before[b] = true
	}
	newBuffers := func() []string {
		var left []string
		for _, b := range buffers() {
			if !before[b] {
				left = append(left, b)
			}
		}
		return left
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

	t.Run("an existing document opened for writing without writing stays as it is (C16)", func(t *testing.T) {
		shell(`touch Once.pdf; exec 3>>Once.pdf; exec 3>&-`)
		settle()
		if got := uploads("Once.pdf"); !equal(got, len(pdf)) {
			t.Errorf("Expected no new upload, got %v", got)
		}
		if got := size("Once.pdf"); got != int64(len(pdf)) {
			t.Errorf("Expected Once.pdf to keep its %d bytes, got %d", len(pdf), got)
		}
	})

	t.Run("an existing document emptied with O_TRUNC is emptied, once (C16)", func(t *testing.T) {
		shell(`: > Twice.pdf`)
		settle()
		if got := uploads("Twice.pdf"); !equal(got, 100, len(pdf), 0) {
			t.Errorf("Expected a third upload of 0 bytes, got %v", got)
		}
	})

	t.Run("truncate: own size nothing, other sizes refused, 0 empties the document (C15)", func(t *testing.T) {
		// coreutils truncate opens the file and truncates it open; Python's os.truncate does it by name.
		shell(`truncate -s ` + strconv.Itoa(len(pdf)) + ` Redirected.pdf
			if truncate -s 10 Redirected.pdf 2>/dev/null; then echo "truncate -s 10 was allowed"; exit 1; fi
			if python3 -c 'import os; os.truncate("Redirected.pdf", 10)' 2>/dev/null; then echo "os.truncate to 10 was allowed"; exit 1; fi`)
		settle()
		if got := uploads("Redirected.pdf"); !equal(got, len(pdf)) {
			t.Errorf("Expected no new upload so far, got %v", got)
		}
		if got := size("Redirected.pdf"); got != int64(len(pdf)) {
			t.Errorf("Expected Redirected.pdf to keep its %d bytes, got %d", len(pdf), got)
		}
		shell(`truncate -s 0 Redirected.pdf`)
		settle()
		if got := uploads("Redirected.pdf"); !equal(got, len(pdf), 0) {
			t.Errorf("Expected an upload of 0 bytes (truncate -s 0), got %v", got)
		}
		shell(`python3 -c 'import os; os.truncate("Once.pdf", 0)'`)
		settle()
		if got := uploads("Once.pdf"); !equal(got, len(pdf), 0) {
			t.Errorf("Expected an upload of 0 bytes (os.truncate), got %v", got)
		}
		for _, name := range []string{"Redirected.pdf", "Once.pdf"} {
			if got := size(name); got != 0 {
				t.Errorf("Expected %s to be empty, got %d bytes", name, got)
			}
		}
		if left := newBuffers(); len(left) != 0 {
			t.Errorf("Expected no upload buffers left behind, got %v", left)
		}
	})

	t.Run("cat > and cp onto an existing document: one upload, the full content", func(t *testing.T) {
		shell(`cat "$SAMPLE" > Redirected.pdf; cp "$SAMPLE" Once.pdf`)
		settle()
		if got := uploads("Redirected.pdf"); !equal(got, len(pdf), 0, len(pdf)) {
			t.Errorf("Expected one more upload of %d bytes (no empty one first), got %v", len(pdf), got)
		}
		if got := uploads("Once.pdf"); !equal(got, len(pdf), 0, len(pdf)) {
			t.Errorf("Expected one more upload of %d bytes, got %v", len(pdf), got)
		}
		for _, name := range []string{"Redirected.pdf", "Once.pdf"} {
			if got := size(name); got != int64(len(pdf)) {
				t.Errorf("Expected %s to be %d bytes, got %d", name, len(pdf), got)
			}
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

// An upload that fails at the last close can't be reported to the program any more: what was
// written is kept in ~/.cache/remarkable-fs/unsent/ instead of being lost (0.ideas.md C14).
func TestFailedUploadIsKept(t *testing.T) {
	if _, err := logger.Init(filepath.Join(t.TempDir(), "log.txt"), false); err != nil {
		t.Fatalf("Failed to init logger: %v", err)
	}
	defer logger.Close()
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)

	tmp, err := os.CreateTemp(t.TempDir(), "rm_upload_*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	defer tmp.Close()
	if _, err := tmp.WriteString("my notes"); err != nil {
		t.Fatal(err)
	}
	h := &UploadFileHandle{name: "Notes.md", tmpFile: tmp, tmpPath: tmp.Name(), size: 8}
	h.keepUnsentLocked()

	kept, _ := filepath.Glob(filepath.Join(cache, "remarkable-fs", "unsent", "* Notes.md"))
	if len(kept) != 1 {
		t.Fatalf("Expected one kept copy of Notes.md, got %v", kept)
	}
	if data, _ := os.ReadFile(kept[0]); string(data) != "my notes" {
		t.Errorf("Expected the kept copy to hold what was written, got %q", data)
	}
}
