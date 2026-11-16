package converter

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestMarkdownToEPUB(t *testing.T) {
	mdContent := `# Test Markdown Document

Welcome to **EPUB testing**!

| Header 1 | Header 2 |
| :--- | :--- |
| Row 1 Col 1 | Row 1 Col 2 |
| Row 2 Col 1 | Row 2 Col 2 |

Here is a list:
1. First item
2. Second item
3. Third item

` + "```go\nfunc hello() {\n    fmt.Println(\"EPUB code\")\n}\n```" + `

> Blockquote testing with *italics*.
`

	epubBytes, err := MarkdownToEPUB([]byte(mdContent), "Test Document")
	if err != nil {
		t.Fatalf("MarkdownToEPUB failed: %v", err)
	}

	if len(epubBytes) == 0 {
		t.Fatalf("Generated EPUB is empty")
	}

	// Verify zip contents
	zipReader, err := zip.NewReader(bytes.NewReader(epubBytes), int64(len(epubBytes)))
	if err != nil {
		t.Fatalf("Failed to open generated EPUB as ZIP: %v", err)
	}

	// 1. Verify mimetype is first file and stored uncompressed
	if len(zipReader.File) == 0 || zipReader.File[0].Name != "mimetype" {
		t.Fatalf("Expected first file to be mimetype, got %s", zipReader.File[0].Name)
	}
	if zipReader.File[0].Method != zip.Store {
		t.Errorf("Expected mimetype compression method to be Store (0), got %d", zipReader.File[0].Method)
	}

	mimetypeRC, err := zipReader.File[0].Open()
	if err != nil {
		t.Fatalf("Failed to open mimetype: %v", err)
	}
	mimeData, _ := io.ReadAll(mimetypeRC)
	_ = mimetypeRC.Close()
	if string(mimeData) != "application/epub+zip" {
		t.Errorf("Unexpected mimetype content: %s", string(mimeData))
	}

	// 2. Verify expected EPUB structure
	expectedFiles := map[string]bool{
		"mimetype":               false,
		"META-INF/container.xml": false,
		"OEBPS/content.opf":      false,
		"OEBPS/style.css":        false,
		"OEBPS/content.xhtml":    false,
		"OEBPS/nav.xhtml":        false,
		"OEBPS/toc.ncx":          false,
	}

	for _, f := range zipReader.File {
		expectedFiles[f.Name] = true
		if f.Name == "OEBPS/content.xhtml" {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("Failed to open content.xhtml: %v", err)
			}
			contentData, _ := io.ReadAll(rc)
			_ = rc.Close()
			contentStr := string(contentData)

			// Check table is present
			if !strings.Contains(contentStr, "<table>") {
				t.Errorf("Expected content.xhtml to contain table")
			}
			// Check list is present
			if !strings.Contains(contentStr, "<ol>") {
				t.Errorf("Expected content.xhtml to contain <ol>")
			}
			// Check code is present
			if !strings.Contains(contentStr, "<pre><code") {
				t.Errorf("Expected content.xhtml to contain <pre><code>")
			}
		}
	}

	for name, found := range expectedFiles {
		if !found {
			t.Errorf("Missing expected file in EPUB: %s", name)
		}
	}

	t.Logf("Generated EPUB size: %d bytes across %d files", len(epubBytes), len(zipReader.File))
}

func TestTextToEPUB(t *testing.T) {
	sampleText := "Line 1 of plain text\nLine 2 with \t tabs\nLine 3"
	epubBytes, err := TextToEPUB([]byte(sampleText), "Text Note")
	if err != nil {
		t.Fatalf("TextToEPUB failed: %v", err)
	}

	if len(epubBytes) == 0 {
		t.Fatalf("Generated EPUB is empty")
	}

	zipReader, err := zip.NewReader(bytes.NewReader(epubBytes), int64(len(epubBytes)))
	if err != nil {
		t.Fatalf("Failed to open generated EPUB as ZIP: %v", err)
	}

	var foundContent bool
	for _, f := range zipReader.File {
		if f.Name == "OEBPS/content.xhtml" {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("Failed to open content.xhtml: %v", err)
			}
			data, _ := io.ReadAll(rc)
			_ = rc.Close()
			if !strings.Contains(string(data), "<pre class=\"plaintext\">") {
				t.Errorf("Expected plaintext pre block in content.xhtml")
			}
			foundContent = true
		}
	}

	if !foundContent {
		t.Errorf("Did not find OEBPS/content.xhtml in generated text EPUB")
	}
}

// TestLargeMarkdownToEPUB converts a large real-world Markdown file (tables, code, long lines).
func TestLargeMarkdownToEPUB(t *testing.T) {
	mdPath := testDataPath(t, "large.md")
	source, err := os.ReadFile(mdPath)
	if err != nil {
		t.Skipf("large.md not found: %v", err)
	}

	epubBytes, err := MarkdownToEPUB(source, "Large Markdown")
	if err != nil {
		t.Fatalf("MarkdownToEPUB failed on large.md: %v", err)
	}

	outPath := testDataPath(t, "large.epub")
	if err := os.WriteFile(outPath, epubBytes, 0644); err != nil {
		t.Fatalf("Failed to write output EPUB: %v", err)
	}

	t.Logf("Generated debug EPUB: %s (%d bytes)", outPath, len(epubBytes))
}
