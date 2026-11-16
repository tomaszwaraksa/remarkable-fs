package fusefs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"remarkable-fs/internal/logger"
	"remarkable-fs/internal/model"
)

func TestFUSEMountAndRead(t *testing.T) {
	// Initialize logger in temp directory
	tempLogDir, err := os.MkdirTemp("", "remarkable-fuse-log-*")
	if err != nil {
		t.Fatalf("Failed to create temp log dir: %v", err)
	}
	defer os.RemoveAll(tempLogDir)

	_, err = logger.Init(filepath.Join(tempLogDir, "test-log.txt"), false)
	if err != nil {
		t.Fatalf("Failed to init logger: %v", err)
	}
	defer logger.Close()

	// Create mount point directory
	mountDir, err := os.MkdirTemp("", "remarkable-fuse-mount-*")
	if err != nil {
		t.Fatalf("Failed to create mount temp dir: %v", err)
	}
	defer os.RemoveAll(mountDir)

	// Build in-memory FSNode tree
	root := model.NewDirectoryNode("", "root", time.Now(), nil)
	folder1 := model.NewDirectoryNode("folder-uuid-1", "My Notes", time.Now(), nil)
	root.AddChild(folder1)

	file1 := model.NewFileNode("file-uuid-1", "QuickNote.notebook", "", 100, time.Now(), "notebook", nil, nil)
	folder1.AddChild(file1)

	file2 := model.NewFileNode("file-uuid-2", "Guide.notebook", "", 200, time.Now(), "notebook", nil, nil)
	root.AddChild(file2)

	// Mount FUSE filesystem with 10MB limit and epub conversion for testing
	server, err := Mount(mountDir, root, nil, nil, 10*1024*1024, "epub")
	if err != nil {
		t.Skipf("Skipping FUSE mount test: /dev/fuse or fusermount may not permit unprivileged mounting: %v", err)
		return
	}

	defer func() {
		_ = Unmount(server, mountDir)
	}()

	// Verify filesystem statistics (statfs) reporting Namelen = 255 for Nautilus / file managers
	var stat syscall.Statfs_t
	if err := syscall.Statfs(mountDir, &stat); err != nil {
		t.Fatalf("syscall.Statfs failed on mount directory: %v", err)
	}
	if stat.Namelen != 255 {
		t.Fatalf("Expected statfs Namelen 255, got %d", stat.Namelen)
	}
	if stat.Bsize != 4096 {
		t.Fatalf("Expected statfs Bsize 4096, got %d", stat.Bsize)
	}
	if stat.Blocks == 0 || stat.Bfree == 0 {
		t.Fatalf("Expected non-zero Blocks and Bfree, got Blocks=%d, Bfree=%d", stat.Blocks, stat.Bfree)
	}

	// Read root directory entries
	entries, err := readDocuments(mountDir)
	if err != nil {
		t.Fatalf("Failed to read mount directory: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("Expected 2 entries in root mount directory, got %d", len(entries))
	}

	names := []string{entries[0].Name(), entries[1].Name()}
	t.Logf("Root entries: %v", names)

	// Verify subfolder
	subEntries, err := os.ReadDir(filepath.Join(mountDir, "My Notes"))
	if err != nil {
		t.Fatalf("Failed to read subfolder: %v", err)
	}
	if len(subEntries) != 1 || subEntries[0].Name() != "QuickNote.notebook" {
		t.Fatalf("Unexpected subfolder contents: %v", subEntries)
	}

	// Read file contents
	content, err := os.ReadFile(filepath.Join(mountDir, "My Notes", "QuickNote.notebook"))
	if err != nil {
		t.Fatalf("Failed to read file content: %v", err)
	}
	if len(content) == 0 {
		t.Errorf("Expected non-empty file content")
	}
	t.Logf("Read file content: %s", string(content))

	// Verify that GLib / FreeDesktop detects trashing is unsupported on this volume
	if gioPath, err := exec.LookPath("gio"); err == nil {
		out, err := exec.Command(gioPath, "info", "-a", "access::can-trash", filepath.Join(mountDir, "Guide.notebook")).CombinedOutput()
		if err == nil {
			t.Logf("gio info output: %s", string(out))
			if strings.Contains(string(out), "access::can-trash: TRUE") {
				t.Errorf("Expected access::can-trash to be FALSE, but got TRUE: %s", string(out))
			}
		}
	}

	// Verify unsupported file format rejection (.docx)
	err = os.WriteFile(filepath.Join(mountDir, "test.docx"), []byte("test"), 0644)
	if err == nil {
		t.Errorf("Expected write to fail with EINVAL for unsupported format .docx, but succeeded")
	}

	// Verify plain text upload and on-the-fly conversion to EPUB
	txtPath := filepath.Join(mountDir, "Note.txt")
	err = os.WriteFile(txtPath, []byte("Hello plain text on reMarkable\nLine 2"), 0644)
	if err != nil {
		t.Fatalf("Failed to write .txt file: %v", err)
	}
	// Verify converted Note.epub exists
	convertedTxtEPUB := filepath.Join(mountDir, "Note.epub")
	if _, statErr := os.Stat(convertedTxtEPUB); statErr != nil {
		t.Fatalf("Expected converted Note.epub to exist: %v", statErr)
	}
	_ = os.Remove(convertedTxtEPUB)

	// Verify markdown upload and on-the-fly conversion to EPUB
	mdPath := filepath.Join(mountDir, "Doc.md")
	err = os.WriteFile(mdPath, []byte("# Header\n\nParagraph with **bold** text.\n"), 0644)
	if err != nil {
		t.Fatalf("Failed to write .md file: %v", err)
	}
	convertedMDEEPUB := filepath.Join(mountDir, "Doc.epub")
	if _, statErr := os.Stat(convertedMDEEPUB); statErr != nil {
		t.Fatalf("Expected converted Doc.epub to exist: %v", statErr)
	}
	_ = os.Remove(convertedMDEEPUB)

	// Verify folder creation (Mkdir)
	newFolderPath := filepath.Join(mountDir, "Projects")
	err = os.Mkdir(newFolderPath, 0755)
	if err != nil {
		t.Fatalf("Failed to create directory Projects: %v", err)
	}

	// Verify virtual .Trash-<uid> exists as a regular file to signal GLib/GVFS not to create trash folders
	uidTrashName := fmt.Sprintf(".Trash-%d", os.Getuid())
	trashStat, err := os.Stat(filepath.Join(mountDir, uidTrashName))
	if err != nil {
		t.Fatalf("Expected virtual %s to exist: %v", uidTrashName, err)
	}
	if trashStat.IsDir() {
		t.Fatalf("Expected virtual %s to be a regular file, but got directory", uidTrashName)
	}

	// Verify rejection of hidden/trash folder creation
	err = os.Mkdir(filepath.Join(mountDir, ".Trash-1000"), 0755)
	if err == nil {
		t.Errorf("Expected Mkdir on .Trash-1000 to be rejected, but succeeded")
	}

	// Verify PDF upload (Create + Write + Flush)
	samplePDF := []byte("%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Count 1/Kids[3 0 R]>>endobj\n3 0 obj<</Type/Page/MediaBox[0 0 595 842]/Parent 2 0 R/Resources<<>>>>endobj\nxref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000052 00000 n \n0000000102 00000 n \ntrailer<</Size 4/Root 1 0 R>>\nstartxref\n187\n%%EOF\n")
	pdfPath := filepath.Join(newFolderPath, "Document.pdf")
	err = os.WriteFile(pdfPath, samplePDF, 0644)
	if err != nil {
		t.Fatalf("Failed to upload PDF: %v", err)
	}

	// Verify uploaded PDF can be read back
	readPDF, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatalf("Failed to read uploaded PDF: %v", err)
	}
	if len(readPDF) == 0 {
		t.Errorf("Expected uploaded PDF to have content")
	}

	// Verify overwriting existing PDF
	updatedPDF := append(samplePDF, []byte("% Updated\n")...)
	err = os.WriteFile(pdfPath, updatedPDF, 0644)
	if err != nil {
		t.Fatalf("Failed to overwrite existing PDF: %v", err)
	}

	// Verify rejecting write/overwrite on reMarkable notebook
	err = os.WriteFile(filepath.Join(mountDir, "Guide.notebook"), samplePDF, 0644)
	if err == nil {
		t.Errorf("Expected writing/overwriting a notebook to fail, but succeeded")
	}

	// Verify renaming uploaded file (Rename)
	renamedPDFPath := filepath.Join(newFolderPath, "RenamedDocument.pdf")
	err = os.Rename(pdfPath, renamedPDFPath)
	if err != nil {
		t.Fatalf("Failed to rename PDF: %v", err)
	}

	// Clean up uploaded file
	err = os.Remove(renamedPDFPath)
	if err != nil {
		t.Fatalf("Failed to delete renamed PDF: %v", err)
	}

	// Clean up created folder
	err = os.Remove(newFolderPath)
	if err != nil {
		t.Fatalf("Failed to remove empty folder Projects: %v", err)
	}

	// Verify file deletion (Unlink)
	err = os.Remove(filepath.Join(mountDir, "Guide.notebook"))
	if err != nil {
		t.Fatalf("Failed to remove Guide.notebook: %v", err)
	}

	entriesAfterUnlink, err := readDocuments(mountDir)
	if err != nil {
		t.Fatalf("Failed to read mount directory after unlink: %v", err)
	}
	if len(entriesAfterUnlink) != 1 || entriesAfterUnlink[0].Name() != "My Notes" {
		t.Fatalf("Expected only 'My Notes' after unlinking Guide.notebook, got: %v", entriesAfterUnlink)
	}

	// Verify non-empty directory deletion fails (ENOTEMPTY)
	err = os.Remove(filepath.Join(mountDir, "My Notes"))
	if err == nil {
		t.Fatalf("Expected removing non-empty folder to fail, but succeeded")
	}

	// Verify deleting child and then deleting empty folder (Rmdir)
	err = os.Remove(filepath.Join(mountDir, "My Notes", "QuickNote.notebook"))
	if err != nil {
		t.Fatalf("Failed to remove QuickNote.notebook: %v", err)
	}

	err = os.Remove(filepath.Join(mountDir, "My Notes"))
	if err != nil {
		t.Fatalf("Failed to remove empty folder My Notes: %v", err)
	}

	entriesFinal, err := readDocuments(mountDir)
	if err != nil {
		t.Fatalf("Failed to read mount directory after all deletes: %v", err)
	}
	if len(entriesFinal) != 0 {
		t.Fatalf("Expected 0 entries after all deletes, got: %v", entriesFinal)
	}
}

func TestFUSERecentFolderRestrictions(t *testing.T) {
	tempLogDir, err := os.MkdirTemp("", "remarkable-fuse-log-*")
	if err != nil {
		t.Fatalf("Failed to create temp log dir: %v", err)
	}
	defer os.RemoveAll(tempLogDir)

	_, err = logger.Init(filepath.Join(tempLogDir, "test-log.txt"), false)
	if err != nil {
		t.Fatalf("Failed to init logger: %v", err)
	}
	defer logger.Close()

	mountDir, err := os.MkdirTemp("", "remarkable-fuse-mount-*")
	if err != nil {
		t.Fatalf("Failed to create mount temp dir: %v", err)
	}
	defer os.RemoveAll(mountDir)

	// Build FSNode tree with virtual Recent folder
	root := model.NewDirectoryNode("", "root", time.Now(), nil)
	recentFolder := model.NewDirectoryNode("virtual-recent-uuid", "Recent", time.Now(), nil)
	recentFolder.IsSpecial = true
	root.AddChild(recentFolder)

	// Add an alias file inside Recent
	recentDoc := model.NewFileNode("file-1", "RecentDoc.pdf", "", 100, time.Now(), "pdf", nil, nil)
	recentDoc.IsSpecial = true
	recentFolder.AddChild(recentDoc)

	// Add a regular folder and document outside Recent
	normalFolder := model.NewDirectoryNode("folder-normal", "Work", time.Now(), nil)
	root.AddChild(normalFolder)
	normalDoc := model.NewFileNode("file-2", "NormalDoc.pdf", "", 200, time.Now(), "pdf", nil, nil)
	normalFolder.AddChild(normalDoc)

	server, err := Mount(mountDir, root, nil, nil, 10*1024*1024, "epub")
	if err != nil {
		t.Skipf("Skipping FUSE mount test: %v", err)
		return
	}
	defer func() {
		_ = Unmount(server, mountDir)
	}()

	recentPath := filepath.Join(mountDir, "Recent")
	recentDocPath := filepath.Join(recentPath, "RecentDoc.pdf")

	// 1. Verify Recent directory exists and has 0555 permissions (read-only)
	stat, err := os.Stat(recentPath)
	if err != nil {
		t.Fatalf("Failed to stat Recent directory: %v", err)
	}
	if !stat.IsDir() {
		t.Fatalf("Expected Recent to be a directory")
	}
	if stat.Mode().Perm() != 0555 {
		t.Errorf("Expected Recent permissions 0555, got %o", stat.Mode().Perm())
	}

	// 2. Verify Mkdir inside Recent fails with EPERM
	err = os.Mkdir(filepath.Join(recentPath, "SubFolder"), 0755)
	if err == nil {
		t.Errorf("Expected Mkdir inside Recent to fail with EPERM, but succeeded")
	}

	// 3. Verify creating a new file inside Recent fails with EPERM
	samplePDF := []byte("%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Count 1/Kids[3 0 R]>>endobj\n3 0 obj<</Type/Page/MediaBox[0 0 595 842]/Parent 2 0 R/Resources<<>>>>endobj\nxref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000052 00000 n \n0000000102 00000 n \ntrailer<</Size 4/Root 1 0 R>>\nstartxref\n187\n%%EOF\n")
	err = os.WriteFile(filepath.Join(recentPath, "Upload.pdf"), samplePDF, 0644)
	if err == nil {
		t.Errorf("Expected creating a file inside Recent to fail with EPERM, but succeeded")
	}

	// 4. Verify deleting a file inside Recent fails with EPERM
	err = os.Remove(recentDocPath)
	if err == nil {
		t.Errorf("Expected deleting a file inside Recent to fail with EPERM, but succeeded")
	}

	// 5. Verify deleting the Recent directory itself fails with EPERM
	err = os.Remove(recentPath)
	if err == nil {
		t.Errorf("Expected deleting Recent directory to fail with EPERM, but succeeded")
	}

	// 6. Verify renaming/moving the Recent directory fails with EPERM
	err = os.Rename(recentPath, filepath.Join(mountDir, "OldRecent"))
	if err == nil {
		t.Errorf("Expected renaming Recent directory to fail with EPERM, but succeeded")
	}

	// 7. Verify moving a file into Recent fails with EPERM
	err = os.Rename(filepath.Join(mountDir, "Work", "NormalDoc.pdf"), filepath.Join(recentPath, "NormalDoc.pdf"))
	if err == nil {
		t.Errorf("Expected moving a file into Recent to fail with EPERM, but succeeded")
	}

	// 8. Verify moving a file out of Recent fails with EPERM
	err = os.Rename(recentDocPath, filepath.Join(mountDir, "Work", "RecentDoc.pdf"))
	if err == nil {
		t.Errorf("Expected moving a file out of Recent to fail with EPERM, but succeeded")
	}

	// 9. Verify creating a folder named "Recent" at root fails with EEXIST
	err = os.Mkdir(filepath.Join(mountDir, "Recent"), 0755)
	if err == nil {
		t.Errorf("Expected creating an existing folder Recent to fail, but succeeded")
	}
}
