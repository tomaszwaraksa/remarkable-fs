package remarkable

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"remarkable-fs/internal/logger"
	"remarkable-fs/internal/model"
)

const (
	XochitlDirectory = "/home/root/.local/share/remarkable/xochitl"
)

// RemoteFS defines the remote filesystem operations needed by the repository.
type RemoteFS interface {
	ReadDir(path string) ([]os.FileInfo, error)
	ReadFile(path string) ([]byte, error)
	Stat(path string) (os.FileInfo, error)
}

// Repository handles reading and parsing reMarkable document structures over SFTP.
type Repository struct {
	mu           sync.Mutex
	fs           RemoteFS
	baseDir      string
	rootNode     *model.FSNode
	recentNode   *model.FSNode
	uuidNodes    map[string]*model.FSNode
	folderMap    map[string]*model.FSNode
	metaMTime    map[string]time.Time
	lastDirMTime time.Time
	isLoaded     atomic.Bool // set once the first tree is complete; read without r.mu
}

// NewRepository creates a new reMarkable document repository.
func NewRepository(remoteFS RemoteFS) *Repository {
	return &Repository{
		fs:        remoteFS,
		baseDir:   XochitlDirectory,
		rootNode:  model.NewDirectoryNode("", "root", time.Now(), nil),
		uuidNodes: make(map[string]*model.FSNode),
		folderMap: make(map[string]*model.FSNode),
		metaMTime: make(map[string]time.Time),
	}
}

// Root returns the root folder node. It exists before the first LoadTree (empty), so the folder
// can be mounted while the tablet is still asleep; LoadTree fills this same node in place.
func (r *Repository) Root() *model.FSNode {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rootNode
}

// IsLoaded reports whether LoadTree has read the documents from the tablet at least once.
// It doesn't take r.mu, which LoadTree and CheckForChanges hold during slow tablet reads:
// the file system asks this on every folder lookup.
func (r *Repository) IsLoaded() bool {
	return r.isLoaded.Load()
}

// isSystemFolderName returns true if the folder name corresponds to desktop metadata or trash (e.g. .Trash-1000, .Trash, .git, etc.)
func isSystemFolderName(name string) bool {
	return strings.HasPrefix(name, ".Trash") || strings.HasPrefix(name, ".")
}

// LoadTree reads the xochitl directory, parses metadata files, and constructs the folder hierarchy.
func (r *Repository) LoadTree() (*model.FSNode, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	logger.Info("Reading documents from reMarkable at %s...", r.baseDir)

	if dirStat, err := r.fs.Stat(r.baseDir); err == nil {
		r.lastDirMTime = dirStat.ModTime()
	}

	entries, err := r.fs.ReadDir(r.baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to list directory %s: %w", r.baseDir, err)
	}

	// Create set of available filenames and directories for fast lookup
	filesInDir := make(map[string]os.FileInfo)
	dirsInDir := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() {
			dirsInDir[entry.Name()] = true
		} else {
			filesInDir[entry.Name()] = entry
		}
	}

	// Filter metadata files
	var metadataFiles []string
	for name := range filesInDir {
		if strings.HasSuffix(name, ".metadata") {
			metadataFiles = append(metadataFiles, name)
		}
	}

	logger.Info("Found %d items in reMarkable storage", len(metadataFiles))

	root := model.NewDirectoryNode("", "root", time.Now(), nil)
	folderMap := make(map[string]*model.FSNode)
	var allNodes []*model.FSNode

	type parsedItem struct {
		uuid     string
		meta     model.Metadata
		metaFile string
	}
	var parsedList []parsedItem
	ignoredFolders := make(map[string]bool)

	// First pass: Parse all metadata files and filter out trash and system folders
	for _, metaFile := range metadataFiles {
		uuid := strings.TrimSuffix(metaFile, ".metadata")
		metaPath := path.Join(r.baseDir, metaFile)

		metaData, err := r.fs.ReadFile(metaPath)
		if err != nil {
			logger.Error("Failed to read metadata for %s: %v", uuid, err)
			continue
		}

		if fi, ok := filesInDir[metaFile]; ok {
			r.metaMTime[uuid] = fi.ModTime()
		}

		var meta model.Metadata
		if err := json.Unmarshal(metaData, &meta); err != nil {
			logger.Error("Failed to parse metadata JSON for %s: %v", uuid, err)
			continue
		}

		// Skip deleted items or items in trash
		if meta.Deleted || meta.Parent == "trash" {
			continue
		}

		// Skip desktop system/trash folders (e.g. .Trash-1000, .Trash)
		if meta.Type == model.TypeCollection && isSystemFolderName(meta.VisibleName) {
			ignoredFolders[uuid] = true
			continue
		}

		parsedList = append(parsedList, parsedItem{uuid: uuid, meta: meta, metaFile: metaFile})
	}

	// Propagate ignored folders to any child folders (e.g. .Trash-1000/files or info)
	for {
		changed := false
		for _, item := range parsedList {
			if item.meta.Type == model.TypeCollection && ignoredFolders[item.meta.Parent] && !ignoredFolders[item.uuid] {
				ignoredFolders[item.uuid] = true
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	// Create nodes for valid items
	for _, item := range parsedList {
		if ignoredFolders[item.uuid] || ignoredFolders[item.meta.Parent] {
			continue
		}

		uuid := item.uuid
		meta := item.meta
		metaFile := item.metaFile

		safeName := sanitizeName(meta.VisibleName)
		if safeName == "" {
			safeName = "Untitled"
		}

		modTime := meta.LastModified.Time()
		if modTime.IsZero() {
			if fi, ok := filesInDir[metaFile]; ok {
				modTime = fi.ModTime()
			} else {
				modTime = time.Now()
			}
		}

		if meta.Type == model.TypeCollection {
			// Folder
			folderNode := model.NewDirectoryNode(uuid, safeName, modTime, &meta)
			folderMap[uuid] = folderNode
			r.folderMap[uuid] = folderNode
			r.uuidNodes[uuid] = folderNode
			allNodes = append(allNodes, folderNode)
		} else {
			// Document / Notebook
			isNotebook := false
			hasAnnotations := false
			var backingExt string
			var backingPath string
			var fileSize int64
			fileType := "notebook"

			pdfFile := uuid + ".pdf"
			epubFile := uuid + ".epub"
			contentFile := uuid + ".content"

			if fi, ok := filesInDir[pdfFile]; ok {
				backingExt = ".pdf"
				backingPath = path.Join(r.baseDir, pdfFile)
				fileSize = fi.Size()
				fileType = "pdf"
			} else if fi, ok := filesInDir[epubFile]; ok {
				backingExt = ".epub"
				backingPath = path.Join(r.baseDir, epubFile)
				fileSize = fi.Size()
				fileType = "epub"
			} else {
				// Native notebook - present as .pdf in the filesystem!
				isNotebook = true
				backingExt = ".pdf"
				fileType = "notebook"
				fileSize = 1024 * 1024 // Estimated size prior to rendering
			}

			// Check if stroke directory {UUID} exists on the tablet
			if dirsInDir[uuid] {
				hasAnnotations = true
			}

			// Ensure file name has .pdf (or .epub) extension
			displayName := safeName
			if backingExt != "" && !strings.HasSuffix(strings.ToLower(displayName), backingExt) {
				displayName = displayName + backingExt
			}

			// Parse content and extract page UUID list
			var content *model.Content
			var pages []string
			if _, ok := filesInDir[contentFile]; ok {
				cData, err := r.fs.ReadFile(path.Join(r.baseDir, contentFile))
				if err == nil {
					pages = extractPagesFromContent(cData)
					var c model.Content
					if err := json.Unmarshal(cData, &c); err == nil {
						content = &c
						if c.FileType != "" && fileType == "notebook" {
							fileType = c.FileType
						}
					}
				}
			}

			// Determine thumbnail path on tablet (first page thumbnail)
			var thumbPath string
			if len(pages) > 0 {
				firstPageUUID := pages[0]
				thumbDir := uuid + ".thumbnails"
				if dirsInDir[thumbDir] {
					thumbPath = path.Join(r.baseDir, thumbDir, firstPageUUID+".png")
				}
			}

			docNode := model.NewFileNode(uuid, displayName, backingPath, fileSize, modTime, fileType, &meta, content)
			docNode.IsNotebook = isNotebook
			docNode.HasAnnotations = hasAnnotations
			docNode.Pages = pages
			docNode.ThumbnailPath = thumbPath
			r.uuidNodes[uuid] = docNode
			allNodes = append(allNodes, docNode)
		}
	}

	// Second pass: Assemble hierarchy
	for _, n := range allNodes {
		parentUUID := ""
		if n.Metadata != nil {
			parentUUID = n.Metadata.Parent
		}

		if parentUUID == "" {
			root.AddChild(n)
		} else if parentFolder, exists := folderMap[parentUUID]; exists {
			parentFolder.AddChild(n)
		} else {
			// Orphaned item - place under root
			root.AddChild(n)
		}
	}

	model.LockTree()
	r.rootNode.ReplaceChildren(root)
	model.UnlockTree()
	r.folderMap = folderMap

	// Assemble virtual Recent folder containing 15 most recently opened items
	r.updateRecentNodeLocked()

	// Sort children recursively for consistent display
	model.LockTree()
	sortChildren(r.rootNode)
	model.UnlockTree()
	// Only now: until then the folder counts as offline and shows nothing.
	r.isLoaded.Store(true)

	logger.Info("Successfully constructed virtual file tree with %d items", len(allNodes))
	return r.rootNode, nil
}

// sortChildren recursively sorts ordered children (folders first, then alphabetically, with special folders pinned first)
func sortChildren(n *model.FSNode) {
	if !n.IsDir || len(n.OrderedChildren) == 0 {
		return
	}

	if n.IsSpecial || n.Name == "Recent" {
		// Retain recency ordering for Recent folder
		return
	}

	sort.Slice(n.OrderedChildren, func(i, j int) bool {
		a := n.OrderedChildren[i]
		b := n.OrderedChildren[j]
		if a.IsSpecial != b.IsSpecial {
			return a.IsSpecial // Special virtual folders like Recent go first
		}
		if a.IsDir != b.IsDir {
			return a.IsDir // folders before files
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})

	for _, ch := range n.OrderedChildren {
		sortChildren(ch)
	}
}

// sanitizeName replaces illegal path characters with safe alternatives.
func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "\x00", "")
	name = strings.ReplaceAll(name, "\\", "-")
	return filepath.Clean(name)
}

func extractPagesFromContent(cData []byte) []string {
	var pages []string
	var rawMap map[string]any
	if err := json.Unmarshal(cData, &rawMap); err != nil {
		return nil
	}
	// Format 1: "pages": ["uuid1", "uuid2"]
	if pList, ok := rawMap["pages"].([]any); ok {
		for _, p := range pList {
			if pStr, ok := p.(string); ok {
				pages = append(pages, pStr)
			}
		}
	}
	// Format 2 (firmware 3.x): "cPages": {"pages": [{"id": "uuid1"}]}
	if len(pages) == 0 {
		if cObj, ok := rawMap["cPages"].(map[string]any); ok {
			if pList, ok := cObj["pages"].([]any); ok {
				for _, p := range pList {
					if pMap, ok := p.(map[string]any); ok {
						if id, ok := pMap["id"].(string); ok {
							pages = append(pages, id)
						}
					}
				}
			}
		}
	}
	return pages
}

// CheckForChanges queries the tablet for updated, new, or deleted documents and updates the in-memory tree.
// Returns a list of FSNodes that were updated or newly added.
func (r *Repository) CheckForChanges(forceScan bool) ([]*model.FSNode, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	dirStat, err := r.fs.Stat(r.baseDir)
	if err != nil {
		return nil, err
	}

	// Fast path: if directory modification time hasn't changed and forceScan is false, nothing was modified
	if !forceScan && !r.lastDirMTime.IsZero() && !dirStat.ModTime().After(r.lastDirMTime) {
		return nil, nil
	}

	entries, err := r.fs.ReadDir(r.baseDir)
	if err != nil {
		return nil, err
	}
	r.lastDirMTime = dirStat.ModTime()

	filesInDir := make(map[string]os.FileInfo)
	dirsInDir := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() {
			dirsInDir[entry.Name()] = true
		} else {
			filesInDir[entry.Name()] = entry
		}
	}

	var updatedNodes []*model.FSNode

	for name, fileInfo := range filesInDir {
		if !strings.HasSuffix(name, ".metadata") {
			continue
		}
		uuid := strings.TrimSuffix(name, ".metadata")
		cachedMTime, known := r.metaMTime[uuid]

		// Check if this metadata file is new or modified
		if !known || fileInfo.ModTime().After(cachedMTime) {
			metaPath := path.Join(r.baseDir, name)
			metaData, err := r.fs.ReadFile(metaPath)
			if err != nil {
				continue
			}

			var meta model.Metadata
			if err := json.Unmarshal(metaData, &meta); err != nil {
				continue
			}

			// If deleted, trashed, or desktop system folder, remove from in-memory tree
			if meta.Deleted || meta.Parent == "trash" || (meta.Type == model.TypeCollection && isSystemFolderName(meta.VisibleName)) {
				if node, exists := r.uuidNodes[uuid]; exists {
					detachNode(node)
					delete(r.uuidNodes, uuid)
					delete(r.folderMap, uuid)
					delete(r.metaMTime, uuid)
				}
				continue
			}

			safeName := sanitizeName(meta.VisibleName)
			if safeName == "" {
				safeName = "Untitled"
			}
			modTime := meta.LastModified.Time()
			if modTime.IsZero() {
				modTime = fileInfo.ModTime()
			}

			r.metaMTime[uuid] = fileInfo.ModTime()

			existingNode, exists := r.uuidNodes[uuid]
			if exists {
				// Existing node - update its mod time and metadata. The tablet is read first, so
				// the tree is locked only for the in-memory update.
				var pages []string
				if !existingNode.IsDir {
					contentFile := uuid + ".content"
					if _, hasContent := filesInDir[contentFile]; hasContent {
						if cData, cErr := r.fs.ReadFile(path.Join(r.baseDir, contentFile)); cErr == nil {
							pages = extractPagesFromContent(cData)
						}
					}
				}

				model.LockTree()
				existingNode.ModTime = modTime
				existingNode.Metadata = &meta
				if !existingNode.IsDir {
					if len(pages) > 0 {
						existingNode.Pages = pages
					}
					existingNode.HasAnnotations = dirsInDir[uuid]
					if len(existingNode.Pages) > 0 {
						firstPageUUID := existingNode.Pages[0]
						thumbDir := uuid + ".thumbnails"
						if dirsInDir[thumbDir] {
							existingNode.ThumbnailPath = path.Join(r.baseDir, thumbDir, firstPageUUID+".png")
						}
					}
				}
				name := existingNode.Name
				model.UnlockTree()

				updatedNodes = append(updatedNodes, existingNode)
				logger.Info("Detected tablet modification for \"%s\" (UUID: %s), updated modTime to %v", name, uuid, modTime)
			} else {
				// New item created on tablet!
				var newNode *model.FSNode
				if meta.Type == model.TypeCollection {
					folderNode := model.NewDirectoryNode(uuid, safeName, modTime, &meta)
					r.folderMap[uuid] = folderNode
					newNode = folderNode
				} else {
					isNotebook := false
					hasAnnotations := dirsInDir[uuid]
					var backingExt string
					var backingPath string
					var fileSize int64
					fileType := "notebook"

					pdfFile := uuid + ".pdf"
					epubFile := uuid + ".epub"
					contentFile := uuid + ".content"

					if fi, ok := filesInDir[pdfFile]; ok {
						backingExt = ".pdf"
						backingPath = path.Join(r.baseDir, pdfFile)
						fileSize = fi.Size()
						fileType = "pdf"
					} else if fi, ok := filesInDir[epubFile]; ok {
						backingExt = ".epub"
						backingPath = path.Join(r.baseDir, epubFile)
						fileSize = fi.Size()
						fileType = "epub"
					} else {
						isNotebook = true
						backingExt = ".pdf"
						fileType = "notebook"
						fileSize = 1024 * 1024
					}

					displayName := safeName
					if backingExt != "" && !strings.HasSuffix(strings.ToLower(displayName), backingExt) {
						displayName = displayName + backingExt
					}

					var content *model.Content
					var pages []string
					if _, ok := filesInDir[contentFile]; ok {
						if cData, cErr := r.fs.ReadFile(path.Join(r.baseDir, contentFile)); cErr == nil {
							pages = extractPagesFromContent(cData)
							var c model.Content
							if err := json.Unmarshal(cData, &c); err == nil {
								content = &c
								if c.FileType != "" && fileType == "notebook" {
									fileType = c.FileType
								}
							}
						}
					}

					var thumbPath string
					if len(pages) > 0 {
						firstPageUUID := pages[0]
						thumbDir := uuid + ".thumbnails"
						if dirsInDir[thumbDir] {
							thumbPath = path.Join(r.baseDir, thumbDir, firstPageUUID+".png")
						}
					}

					docNode := model.NewFileNode(uuid, displayName, backingPath, fileSize, modTime, fileType, &meta, content)
					docNode.IsNotebook = isNotebook
					docNode.HasAnnotations = hasAnnotations
					docNode.Pages = pages
					docNode.ThumbnailPath = thumbPath
					newNode = docNode
				}

				r.uuidNodes[uuid] = newNode
				parent := r.rootNode
				if parentFolder, exists := r.folderMap[meta.Parent]; exists && meta.Parent != "" {
					parent = parentFolder
				}
				model.LockTree()
				parent.AddChild(newNode)
				name := newNode.Name
				model.UnlockTree()
				updatedNodes = append(updatedNodes, newNode)
				logger.Info("Detected new tablet item \"%s\" (UUID: %s), added to virtual filesystem", name, uuid)
			}
		}
	}

	// Prune UUIDs that were removed from the tablet
	for uuid, node := range r.uuidNodes {
		metaFile := uuid + ".metadata"
		if _, existsOnTablet := filesInDir[metaFile]; !existsOnTablet {
			name := detachNode(node)
			delete(r.uuidNodes, uuid)
			delete(r.folderMap, uuid)
			delete(r.metaMTime, uuid)
			logger.Info("Detected tablet deletion for \"%s\" (UUID: %s), removed from virtual filesystem", name, uuid)
		}
	}

	r.updateRecentNodeLocked()
	return updatedNodes, nil
}

// detachNode takes node out of its folder in the tree and returns its name.
func detachNode(node *model.FSNode) string {
	model.LockTree()
	defer model.UnlockTree()
	if node.Parent != nil {
		node.Parent.RemoveChild(node)
	}
	return node.Name
}

// RemoveUUID removes a UUID from the repository tracking maps.
func (r *Repository) RemoveUUID(uuid string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.uuidNodes, uuid)
	delete(r.folderMap, uuid)
	delete(r.metaMTime, uuid)
	r.updateRecentNodeLocked()
}

// RegisterNode registers a new node into the repository tracking maps.
func (r *Repository) RegisterNode(uuid string, node *model.FSNode) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.uuidNodes[uuid] = node
	if node.IsDir {
		r.folderMap[uuid] = node
	}
	model.RLockTree()
	r.metaMTime[uuid] = node.ModTime
	model.RUnlockTree()
	r.updateRecentNodeLocked()
}

// GetRecentDocuments returns up to limit most recently opened documents.
func (r *Repository) GetRecentDocuments(limit int) []*model.FSNode {
	r.mu.Lock()
	defer r.mu.Unlock()
	model.RLockTree()
	defer model.RUnlockTree()
	return r.getRecentDocumentsLocked(limit)
}

func (r *Repository) getRecentDocumentsLocked(limit int) []*model.FSNode {
	if limit <= 0 {
		limit = 15
	}

	var candidates []*model.FSNode
	for _, node := range r.uuidNodes {
		if node.IsDir || node.IsSpecial {
			continue
		}
		if node.Metadata != nil && (node.Metadata.Deleted || node.Metadata.Parent == "trash") {
			continue
		}
		candidates = append(candidates, node)
	}

	sort.Slice(candidates, func(i, j int) bool {
		a := candidates[i]
		b := candidates[j]

		aLO := a.LastOpenedTime()
		bLO := b.LastOpenedTime()
		aHasLO := !aLO.IsZero()
		bHasLO := !bLO.IsZero()

		if aHasLO != bHasLO {
			return aHasLO
		}
		if aHasLO && bHasLO {
			if !aLO.Equal(bLO) {
				return aLO.After(bLO)
			}
		}
		if !a.ModTime.Equal(b.ModTime) {
			return a.ModTime.After(b.ModTime)
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}

// UpdateRecentNode refreshes the virtual Recent directory with the 15 most recently opened documents.
func (r *Repository) UpdateRecentNode() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateRecentNodeLocked()
}

// updateRecentNodeLocked rebuilds the Recent folder. Needs r.mu; takes the tree lock itself.
func (r *Repository) updateRecentNodeLocked() {
	if r.rootNode == nil {
		return
	}
	model.LockTree()
	defer model.UnlockTree()

	topDocs := r.getRecentDocumentsLocked(15)

	if r.recentNode == nil {
		if existing, ok := r.rootNode.Children["Recent"]; ok && existing.IsSpecial {
			r.recentNode = existing
		} else {
			r.recentNode = model.NewDirectoryNode("virtual-recent-uuid", "Recent", time.Now(), nil)
			r.recentNode.IsSpecial = true
			r.rootNode.AddChild(r.recentNode)
		}
	}

	// Reset children of Recent
	r.recentNode.Children = make(map[string]*model.FSNode)
	r.recentNode.OrderedChildren = nil

	if len(topDocs) > 0 {
		mostRecentTime := topDocs[0].LastOpenedTime()
		if mostRecentTime.IsZero() {
			mostRecentTime = topDocs[0].ModTime
		}
		r.recentNode.ModTime = mostRecentTime
	}

	seenNames := make(map[string]int)
	for _, orig := range topDocs {
		baseName := orig.Name
		ext := filepath.Ext(baseName)
		stem := strings.TrimSuffix(baseName, ext)
		name := baseName

		key := strings.ToLower(name)
		if count := seenNames[key]; count > 0 {
			name = fmt.Sprintf("%s (%d)%s", stem, count+1, ext)
		}
		seenNames[key]++

		alias := &model.FSNode{
			UUID:           orig.UUID,
			Name:           name,
			IsDir:          false,
			IsSpecial:      true,
			Parent:         r.recentNode,
			RemotePath:     orig.RemotePath,
			Size:           orig.Size,
			ModTime:        orig.ModTime,
			FileType:       orig.FileType,
			IsNotebook:     orig.IsNotebook,
			HasAnnotations: orig.HasAnnotations,
			Pages:          orig.Pages,
			ThumbnailPath:  orig.ThumbnailPath,
			Metadata:       orig.Metadata,
			Content:        orig.Content,
		}
		r.recentNode.AddChild(alias)
	}
}
