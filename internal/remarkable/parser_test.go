package remarkable

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type mockFileInfo struct {
	name    string
	size    int64
	modTime time.Time
}

func (m mockFileInfo) Name() string      { return m.name }
func (m mockFileInfo) Size() int64       { return m.size }
func (m mockFileInfo) Mode() fs.FileMode { return 0644 }
func (m mockFileInfo) ModTime() time.Time {
	if !m.modTime.IsZero() {
		return m.modTime
	}
	return time.Now()
}
func (m mockFileInfo) IsDir() bool { return false }
func (m mockFileInfo) Sys() any    { return nil }

type mockRemoteFS struct {
	files    map[string][]byte
	modTimes map[string]time.Time
}

func (m *mockRemoteFS) ReadDir(p string) ([]os.FileInfo, error) {
	var list []os.FileInfo
	for path, data := range m.files {
		mt := time.Now()
		if m.modTimes != nil {
			if t, ok := m.modTimes[path]; ok {
				mt = t
			}
		}
		list = append(list, mockFileInfo{
			name:    filepath.Base(path),
			size:    int64(len(data)),
			modTime: mt,
		})
	}
	return list, nil
}

func (m *mockRemoteFS) ReadFile(p string) ([]byte, error) {
	data, ok := m.files[p]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", p)
	}
	return data, nil
}

func (m *mockRemoteFS) Stat(p string) (os.FileInfo, error) {
	if p == "/home/root/.local/share/remarkable/xochitl" {
		return mockFileInfo{name: "xochitl", size: 4096}, nil
	}
	data, ok := m.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return mockFileInfo{name: filepath.Base(p), size: int64(len(data))}, nil
}

func TestParserLoadTree(t *testing.T) {
	mockFS := &mockRemoteFS{
		files: map[string][]byte{
			// Folder: "Work Projects"
			"/home/root/.local/share/remarkable/xochitl/folder-1.metadata": []byte(`{
				"visibleName": "Work Projects",
				"type": "CollectionType",
				"parent": "",
				"deleted": false,
				"lastModified": "1672531200000"
			}`),

			// Document inside "Work Projects": "Q1 Report.pdf"
			"/home/root/.local/share/remarkable/xochitl/doc-1.metadata": []byte(`{
				"visibleName": "Q1 Report",
				"type": "DocumentType",
				"parent": "folder-1",
				"deleted": false,
				"lastModified": "1672531300000"
			}`),
			"/home/root/.local/share/remarkable/xochitl/doc-1.pdf": []byte("PDF-CONTENT-BYTES"),

			// EPUB at Root: "Ebook"
			"/home/root/.local/share/remarkable/xochitl/doc-2.metadata": []byte(`{
				"visibleName": "Classic Novel",
				"type": "DocumentType",
				"parent": "",
				"deleted": false,
				"lastModified": "1672531400000"
			}`),
			"/home/root/.local/share/remarkable/xochitl/doc-2.epub": []byte("EPUB-CONTENT-BYTES"),

			// Native notebook at Root
			"/home/root/.local/share/remarkable/xochitl/doc-3.metadata": []byte(`{
				"visibleName": "Quick Notes",
				"type": "DocumentType",
				"parent": "",
				"deleted": false,
				"lastModified": "1672531500000"
			}`),
			"/home/root/.local/share/remarkable/xochitl/doc-3.content": []byte(`{"fileType": "notebook", "pageCount": 5}`),

			// Deleted item (must be ignored)
			"/home/root/.local/share/remarkable/xochitl/doc-del.metadata": []byte(`{
				"visibleName": "Deleted File",
				"type": "DocumentType",
				"parent": "",
				"deleted": true,
				"lastModified": "1672531600000"
			}`),

			// Trash item (must be ignored)
			"/home/root/.local/share/remarkable/xochitl/doc-trash.metadata": []byte(`{
				"visibleName": "Trash File",
				"type": "DocumentType",
				"parent": "trash",
				"deleted": false,
				"lastModified": "1672531700000"
			}`),

			// Desktop .Trash folder and subfolder (must be ignored)
			"/home/root/.local/share/remarkable/xochitl/folder-trash.metadata": []byte(`{
				"visibleName": ".Trash-1000",
				"type": "CollectionType",
				"parent": "",
				"deleted": false,
				"lastModified": "1672531800000"
			}`),
			"/home/root/.local/share/remarkable/xochitl/folder-trash-files.metadata": []byte(`{
				"visibleName": "files",
				"type": "CollectionType",
				"parent": "folder-trash",
				"deleted": false,
				"lastModified": "1672531850000"
			}`),
		},
	}

	repo := NewRepository(mockFS)
	mountedRoot := repo.Root()
	if repo.IsLoaded() || len(mountedRoot.OrderedChildren) != 0 {
		t.Fatalf("Expected an empty, unloaded root before LoadTree")
	}
	root, err := repo.LoadTree()
	if err != nil {
		t.Fatalf("LoadTree failed: %v", err)
	}
	if root != mountedRoot || !repo.IsLoaded() {
		t.Fatalf("Expected LoadTree to fill the root returned by Root() in place")
	}
	for _, child := range root.OrderedChildren {
		if child.Parent != mountedRoot {
			t.Errorf("Expected %q to point at the mounted root as its parent", child.Name)
		}
	}

	if root == nil {
		t.Fatal("Root node is nil")
	}

	// Verify Root has 4 items: "Recent" (virtual folder), "Work Projects" (folder), "Classic Novel.epub", "Quick Notes.pdf"
	if len(root.Children) != 4 {
		t.Fatalf("Expected 4 items in root, got %d (%v)", len(root.Children), root.Children)
	}
	if _, ok := root.Children["Quick Notes.pdf"]; !ok {
		t.Errorf("Expected Quick Notes.pdf in root children, got: %v", root.Children)
	}

	recentFolder := root.Children["Recent"]
	if recentFolder == nil || !recentFolder.IsDir || !recentFolder.IsSpecial {
		t.Fatalf("Recent folder not found or not marked special")
	}

	// Verify "Recent" is pinned as the first child of root
	if root.OrderedChildren[0].Name != "Recent" {
		t.Errorf("Expected first child of root to be 'Recent', got %s", root.OrderedChildren[0].Name)
	}

	// Verify folders sorted first (Recent, then Work Projects)
	if !root.OrderedChildren[1].IsDir || root.OrderedChildren[1].Name != "Work Projects" {
		t.Errorf("Expected second child to be directory 'Work Projects', got %s", root.OrderedChildren[1].Name)
	}

	workDir := root.Children["Work Projects"]
	if workDir == nil || !workDir.IsDir {
		t.Fatalf("Work Projects folder not found or not a directory")
	}

	// Verify child of Work Projects: "Q1 Report.pdf"
	if len(workDir.Children) != 1 {
		t.Fatalf("Expected 1 file in Work Projects, got %d", len(workDir.Children))
	}

	doc1 := workDir.Children["Q1 Report.pdf"]
	if doc1 == nil {
		t.Fatalf("Q1 Report.pdf not found in Work Projects")
	}
	if doc1.Size != int64(len("PDF-CONTENT-BYTES")) {
		t.Errorf("Expected size %d, got %d", len("PDF-CONTENT-BYTES"), doc1.Size)
	}
	if doc1.FileType != "pdf" {
		t.Errorf("Expected file type 'pdf', got %s", doc1.FileType)
	}

	// Verify deleted and trash files were excluded
	if _, exists := root.Children["Deleted File"]; exists {
		t.Errorf("Deleted File should not appear in root")
	}
	if _, exists := root.Children["Trash File"]; exists {
		t.Errorf("Trash File should not appear in root")
	}
	if _, exists := root.Children[".Trash-1000"]; exists {
		t.Errorf(".Trash-1000 folder should not appear in root")
	}
	if _, exists := root.Children["files"]; exists {
		t.Errorf("files subfolder of .Trash-1000 should not appear in root")
	}
}

func TestRepositoryCheckForChanges(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	metaPath := "/home/root/.local/share/remarkable/xochitl/doc-test.metadata"
	contentPath := "/home/root/.local/share/remarkable/xochitl/doc-test.content"

	mockFS := &mockRemoteFS{
		files: map[string][]byte{
			metaPath: []byte(`{
				"visibleName": "Live Notes",
				"type": "DocumentType",
				"parent": "",
				"deleted": false,
				"lastModified": "1767261600000"
			}`),
			contentPath: []byte(`{"fileType": "notebook", "pages": ["page-1"]}`),
		},
		modTimes: map[string]time.Time{
			metaPath: baseTime,
		},
	}

	repo := NewRepository(mockFS)
	root, err := repo.LoadTree()
	if err != nil {
		t.Fatalf("LoadTree failed: %v", err)
	}

	node, ok := root.Children["Live Notes.pdf"]
	if !ok {
		t.Fatalf("Expected Live Notes.pdf in root")
	}
	if len(node.Pages) != 1 || node.Pages[0] != "page-1" {
		t.Errorf("Unexpected pages: %v", node.Pages)
	}

	// 1. CheckForChanges with no modifications should return nil
	updated, err := repo.CheckForChanges(false)
	if err != nil {
		t.Fatalf("CheckForChanges failed: %v", err)
	}
	if len(updated) != 0 {
		t.Fatalf("Expected 0 updated nodes, got %d", len(updated))
	}

	// 2. Modify document on tablet
	newTime := baseTime.Add(1 * time.Hour)
	mockFS.files[metaPath] = []byte(`{
		"visibleName": "Live Notes",
		"type": "DocumentType",
		"parent": "",
		"deleted": false,
		"lastModified": "1767265200000"
	}`)
	mockFS.files[contentPath] = []byte(`{"fileType": "notebook", "pages": ["page-1", "page-2"]}`)
	mockFS.modTimes[metaPath] = newTime

	// 3. CheckForChanges should detect update and update node properties
	updated, err = repo.CheckForChanges(true)
	if err != nil {
		t.Fatalf("CheckForChanges with forceScan failed: %v", err)
	}
	if len(updated) != 1 {
		t.Fatalf("Expected 1 updated node, got %d", len(updated))
	}
	if updated[0].UUID != "doc-test" {
		t.Errorf("Expected doc-test, got %s", updated[0].UUID)
	}
	if len(node.Pages) != 2 || node.Pages[1] != "page-2" {
		t.Errorf("Expected 2 pages, got: %v", node.Pages)
	}
	if !node.ModTime.Equal(time.UnixMilli(1767265200000)) {
		t.Errorf("Expected updated ModTime, got: %v", node.ModTime)
	}
}

func TestRecentDocumentsOrder(t *testing.T) {
	mockFS := &mockRemoteFS{
		files: map[string][]byte{
			// Document 1: lastOpened = 1000
			"/home/root/.local/share/remarkable/xochitl/doc-1.metadata": []byte(`{
				"visibleName": "Doc 1",
				"type": "DocumentType",
				"parent": "",
				"lastOpened": "1000",
				"lastModified": "1000"
			}`),
			// Document 2: lastOpened = 3000 (most recent!)
			"/home/root/.local/share/remarkable/xochitl/doc-2.metadata": []byte(`{
				"visibleName": "Doc 2",
				"type": "DocumentType",
				"parent": "",
				"lastOpened": "3000",
				"lastModified": "1000"
			}`),
			// Document 3: lastOpened = 2000
			"/home/root/.local/share/remarkable/xochitl/doc-3.metadata": []byte(`{
				"visibleName": "Doc 3",
				"type": "DocumentType",
				"parent": "",
				"lastOpened": "2000",
				"lastModified": "1000"
			}`),
			// Document 4: no lastOpened (lastOpened: "0"), lastModified = 500
			"/home/root/.local/share/remarkable/xochitl/doc-4.metadata": []byte(`{
				"visibleName": "Doc 4",
				"type": "DocumentType",
				"parent": "",
				"lastOpened": "0",
				"lastModified": "500"
			}`),
		},
	}

	repo := NewRepository(mockFS)
	root, err := repo.LoadTree()
	if err != nil {
		t.Fatalf("LoadTree failed: %v", err)
	}

	recentFolder := root.Children["Recent"]
	if recentFolder == nil {
		t.Fatalf("Recent folder missing")
	}

	// Should have 4 items ordered: Doc 2, Doc 3, Doc 1, Doc 4
	if len(recentFolder.OrderedChildren) != 4 {
		t.Fatalf("Expected 4 items in Recent, got %d", len(recentFolder.OrderedChildren))
	}

	expectedOrder := []string{"Doc 2.pdf", "Doc 3.pdf", "Doc 1.pdf", "Doc 4.pdf"}
	for i, expected := range expectedOrder {
		actual := recentFolder.OrderedChildren[i].Name
		if actual != expected {
			t.Errorf("At index %d: expected %s, got %s", i, expected, actual)
		}
	}

	// Verify alias node does not change original document's parent
	doc2Orig := repo.uuidNodes["doc-2"]
	if doc2Orig == nil {
		t.Fatalf("doc-2 not found in repo")
	}
	if doc2Orig.Parent != root {
		t.Errorf("Original doc-2 parent should be root, got: %v", doc2Orig.Parent)
	}

	// Verify alias node parent is recentFolder
	alias2 := recentFolder.OrderedChildren[0]
	if alias2.Parent != recentFolder {
		t.Errorf("Alias doc-2 parent should be Recent folder, got: %v", alias2.Parent)
	}
	if alias2.VirtualPath() != "/Recent/Doc 2.pdf" {
		t.Errorf("Expected virtual path /Recent/Doc 2.pdf, got: %s", alias2.VirtualPath())
	}
}
