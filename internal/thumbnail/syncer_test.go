package thumbnail

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInjectPNGMetadata(t *testing.T) {
	// Create minimal 1x1 PNG image
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode failed: %v", err)
	}
	rawPNG := buf.Bytes()

	uri := "file:///home/user/document.pdf"
	mtime := time.Unix(1792336581, 0)

	tagged := injectPNGMetadata(rawPNG, uri, mtime)
	if len(tagged) <= len(rawPNG) {
		t.Fatalf("expected tagged PNG to be larger than raw PNG (%d <= %d)", len(tagged), len(rawPNG))
	}

	// Verify chunks exist
	if !bytes.Contains(tagged, []byte("Thumb::URI")) {
		t.Errorf("missing Thumb::URI chunk")
	}
	if !bytes.Contains(tagged, []byte("Thumb::MTime")) {
		t.Errorf("missing Thumb::MTime chunk")
	}
	if !bytes.Contains(tagged, []byte("Software\x00remarkable-fs")) {
		t.Errorf("missing Software chunk")
	}
}

func TestSyncerSyncAndInvalidate(t *testing.T) {
	tmpDir := t.TempDir()
	largeDir := filepath.Join(tmpDir, "large")
	normalDir := filepath.Join(tmpDir, "normal")
	_ = os.MkdirAll(largeDir, 0700)
	_ = os.MkdirAll(normalDir, 0700)

	s := &Syncer{
		largeDir:  largeDir,
		normalDir: normalDir,
	}

	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	pngBytes := buf.Bytes()

	filePath := "/mount/test/sample.pdf"
	modTime := time.Now()

	if err := s.SyncThumbnail(filePath, modTime, pngBytes); err != nil {
		t.Fatalf("SyncThumbnail failed: %v", err)
	}

	fileURI := "file://" + escapeURIPath(filePath)
	hash := md5Hex(fileURI)
	largeFile := filepath.Join(largeDir, hash+".png")
	normalFile := filepath.Join(normalDir, hash+".png")

	if _, err := os.Stat(largeFile); err != nil {
		t.Errorf("expected %s to exist", largeFile)
	}
	if _, err := os.Stat(normalFile); err != nil {
		t.Errorf("expected %s to exist", normalFile)
	}

	// Invalidate
	s.InvalidateThumbnail(filePath)
	if _, err := os.Stat(largeFile); err == nil {
		t.Errorf("expected %s to be removed", largeFile)
	}
	if _, err := os.Stat(normalFile); err == nil {
		t.Errorf("expected %s to be removed", normalFile)
	}
}
