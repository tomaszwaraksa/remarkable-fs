package converter

import (
	"os"
	"path/filepath"
	"testing"
)

// testDataPath returns name inside the folder REMARKABLE_TEST_DATA points at. Some tests render
// real tablet files (.rm pages, PDFs, a large Markdown file) that aren't in the repository: put
// them in a folder and set the variable to run those tests. They write their output for a look
// into the same folder. Without the variable those tests are skipped.
func testDataPath(t *testing.T, name string) string {
	t.Helper()
	dir := os.Getenv("REMARKABLE_TEST_DATA")
	if dir == "" {
		t.Skipf("Needs %s in the folder REMARKABLE_TEST_DATA points at", name)
	}
	return filepath.Join(dir, name)
}
