package fusefs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/pkg/sftp"

	"remarkable-fs/internal/converter"
	"remarkable-fs/internal/logger"
	"remarkable-fs/internal/model"
	"remarkable-fs/internal/remarkable"
	"remarkable-fs/internal/sshclient"
	"remarkable-fs/internal/thumbnail"
)

// DirNode represents a virtual directory in the FUSE filesystem.
type DirNode struct {
	fs.Inode
	fsNode           *model.FSNode
	client           *sshclient.Client
	repo             *remarkable.Repository
	syncer           *thumbnail.Syncer
	virtualPath      string
	mountPoint       string
	maxUploadSize    int64
	textUploadFormat string
	isRecent         bool
}

// TrashBlockNode represents a virtual regular file (e.g. .Trash-1000 or .Trash) at the mount root.
// Exposing .Trash-<uid> as a regular non-directory file signals to FreeDesktop / GLib / GVFS (Nautilus)
// that trashing is unsupported on this volume, directing Nautilus to perform direct permanent
// deletion rather than creating .Trash-1000 folders and uploading .trashinfo files.
type TrashBlockNode struct {
	fs.Inode
}

var _ fs.NodeGetattrer = (*TrashBlockNode)(nil)
var _ fs.NodeOpener = (*TrashBlockNode)(nil)

func (t *TrashBlockNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFREG | 0644
	out.Uid = uint32(os.Getuid())
	out.Gid = uint32(os.Getgid())
	out.Size = 0
	return 0
}

func (t *TrashBlockNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, 0, 0
}

// trackerIgnoreName is a hidden, empty, read-only file at the mount root. GNOME's search indexer
// (Tracker / LocalSearch) skips folders that contain it, so it never downloads and renders every
// document over Wi-Fi. It exists only in this filesystem, never on the tablet.
const trackerIgnoreName = ".trackerignore"

// isRootMarker reports whether name in dir d is the virtual .trackerignore.
func isRootMarker(d *DirNode, name string) bool {
	return d.virtualPath == "/" && name == trackerIgnoreName
}

// MarkerFileNode is an empty, read-only regular file that exists only in this filesystem.
type MarkerFileNode struct {
	fs.Inode
}

var _ fs.NodeGetattrer = (*MarkerFileNode)(nil)
var _ fs.NodeSetattrer = (*MarkerFileNode)(nil)
var _ fs.NodeOpener = (*MarkerFileNode)(nil)
var _ fs.NodeReader = (*MarkerFileNode)(nil)

func (m *MarkerFileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFREG | 0444
	out.Uid = uint32(os.Getuid())
	out.Gid = uint32(os.Getgid())
	out.Size = 0
	return 0
}

// Setattr refuses changes (truncate, chmod, touch): the file is fixed.
func (m *MarkerFileNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	return syscall.EPERM
}

func (m *MarkerFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_TRUNC|syscall.O_APPEND) != 0 {
		return nil, 0, syscall.EPERM
	}
	return nil, 0, 0
}

func (m *MarkerFileNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	return fuse.ReadResultData(nil), 0
}

var _ fs.NodeOnAdder = (*DirNode)(nil)
var _ fs.NodeGetattrer = (*DirNode)(nil)
var _ fs.NodeReaddirer = (*DirNode)(nil)
var _ fs.NodeUnlinker = (*DirNode)(nil)
var _ fs.NodeRmdirer = (*DirNode)(nil)
var _ fs.NodeMkdirer = (*DirNode)(nil)
var _ fs.NodeCreater = (*DirNode)(nil)
var _ fs.NodeRenamer = (*DirNode)(nil)
var _ fs.NodeStatfser = (*DirNode)(nil)

// OnAdd is invoked by go-fuse when the directory inode is registered.
func (d *DirNode) OnAdd(ctx context.Context) {
	if d.virtualPath == "/" {
		// Register virtual .Trash and .Trash-<uid> regular files so that GLib/GVFS/Nautilus
		// detects that trashing is unsupported on this volume and switches to direct permanent deletion.
		// These inodes are NOT added to fsNode.OrderedChildren, so they remain invisible in directory listings.
		uidStr := fmt.Sprintf(".Trash-%d", os.Getuid())
		for _, trashName := range []string{".Trash", uidStr} {
			blockNode := &TrashBlockNode{}
			chInode := d.NewPersistentInode(ctx, blockNode, fs.StableAttr{Mode: fuse.S_IFREG})
			d.AddChild(trashName, chInode, false)
		}
		// Unlike the .Trash files, .trackerignore is listed too: the indexer looks for it in listings.
		marker := d.NewPersistentInode(ctx, &MarkerFileNode{}, fs.StableAttr{Mode: fuse.S_IFREG})
		d.AddChild(trackerIgnoreName, marker, false)
	}

	d.attachChildren(ctx)
}

// nodeInfo is a copy of a node's fields, taken under the tree lock (see model.LockTree), for
// work done without it: tablet reads, rendering, go-fuse calls. Its Parent, Children and
// OrderedChildren must not be used, as they keep changing.
type nodeInfo struct {
	model.FSNode
	node       *model.FSNode // the node itself, for later changes under the tree lock
	path       string        // where the node is in the tree (VirtualPath)
	readOnly   bool          // the Recent folder or a document in it
	childCount int
}

// infoOf copies n's fields. The caller holds the tree lock.
func infoOf(n *model.FSNode) nodeInfo {
	return nodeInfo{
		FSNode:     *n,
		node:       n,
		path:       n.VirtualPath(),
		readOnly:   n.IsSpecial || (n.Parent != nil && n.Parent.IsSpecial),
		childCount: len(n.OrderedChildren),
	}
}

// childInfo looks up a child of folder by name.
func childInfo(folder *model.FSNode, name string) (nodeInfo, bool) {
	model.RLockTree()
	defer model.RUnlockTree()
	child, ok := folder.Children[name]
	if !ok {
		return nodeInfo{}, false
	}
	return infoOf(child), true
}

// childInfos lists the children of folder in display order.
func childInfos(folder *model.FSNode) []nodeInfo {
	model.RLockTree()
	defer model.RUnlockTree()
	children := make([]nodeInfo, 0, len(folder.OrderedChildren))
	for _, child := range folder.OrderedChildren {
		children = append(children, infoOf(child))
	}
	return children
}

// hasChild reports whether folder has a child called name.
func hasChild(folder *model.FSNode, name string) bool {
	model.RLockTree()
	defer model.RUnlockTree()
	_, ok := folder.Children[name]
	return ok
}

// addNode adds child to folder in the tree.
func addNode(folder, child *model.FSNode) {
	model.LockTree()
	defer model.UnlockTree()
	folder.AddChild(child)
}

// removeNode removes child from folder in the tree.
func removeNode(folder, child *model.FSNode) {
	model.LockTree()
	defer model.UnlockTree()
	folder.RemoveChild(child)
}

// moveNode moves child from one folder to another under newName. With updateMeta, the metadata
// gets the new visible name and parent too, as a new copy (see model.LockTree).
func moveNode(from, to, child *model.FSNode, newName string, updateMeta bool, visibleName, parentUUID string) {
	model.LockTree()
	defer model.UnlockTree()
	from.RemoveChild(child)
	child.Name = newName
	to.AddChild(child)
	if updateMeta && child.Metadata != nil {
		meta := *child.Metadata
		meta.VisibleName = visibleName
		meta.Parent = parentUUID
		child.Metadata = &meta
	}
}

// attachChildren registers an inode for every child of the folder that has none yet, so new
// documents (and the whole tree once it is loaded after an offline start) can be looked up.
func (d *DirNode) attachChildren(ctx context.Context) {
	for _, child := range childInfos(d.fsNode) {
		if d.GetChild(child.Name) == nil {
			d.AddChild(child.Name, d.newChildInode(ctx, child), false)
		}
	}
}

// newChildInode creates the inode for one child node of the folder.
func (d *DirNode) newChildInode(ctx context.Context, child nodeInfo) *fs.Inode {
	subPath := path.Join(d.virtualPath, child.Name)
	if child.IsDir {
		childDir := &DirNode{
			fsNode:           child.node,
			client:           d.client,
			repo:             d.repo,
			syncer:           d.syncer,
			virtualPath:      subPath,
			mountPoint:       d.mountPoint,
			maxUploadSize:    d.maxUploadSize,
			textUploadFormat: d.textUploadFormat,
			isRecent:         child.IsSpecial || child.Name == "Recent",
		}
		return d.NewPersistentInode(ctx, childDir, fs.StableAttr{Mode: fuse.S_IFDIR})
	}
	childFile := &FileNode{
		fsNode:           child.node,
		client:           d.client,
		repo:             d.repo,
		syncer:           d.syncer,
		virtualPath:      subPath,
		mountPoint:       d.mountPoint,
		maxUploadSize:    d.maxUploadSize,
		textUploadFormat: d.textUploadFormat,
	}
	return d.NewPersistentInode(ctx, childFile, fs.StableAttr{Mode: fuse.S_IFREG})
}

// isOffline reports whether the documents haven't been read from the tablet yet (it was asleep
// or away when the folder was mounted). Changes are refused then, so nothing lands on the local disk.
func (d *DirNode) isOffline() bool {
	return d.repo != nil && !d.repo.IsLoaded()
}

// Getattr returns the directory attributes with write permissions enabled for deletion.
func (d *DirNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	mode := uint32(0755)
	offline := d.isOffline()
	if d.isRecent || offline {
		mode = 0555
	}
	out.Mode = fuse.S_IFDIR | mode
	out.Uid = uint32(os.Getuid())
	out.Gid = uint32(os.Getgid())
	// While offline the tree may be filled in right now, so its fields aren't read until loaded.
	if offline {
		return 0
	}
	model.RLockTree()
	modTime := d.fsNode.ModTime
	model.RUnlockTree()
	if !modTime.IsZero() {
		out.Mtime = uint64(modTime.Unix())
		out.Atime = out.Mtime
		out.Ctime = out.Mtime
	}
	return 0
}

// populateStatfs fills out the filesystem statistics for FUSE.
// Standard POSIX NameLen (255) ensures file managers like Nautilus do not falsely reject file or folder names.
func populateStatfs(out *fuse.StatfsOut) {
	const blockSize = 4096
	out.Bsize = blockSize
	out.Frsize = blockSize
	out.NameLen = 255
	// Total space ~6.7 GB (reMarkable 2 user partition on /home)
	out.Blocks = 6722700 / (blockSize / 1024)
	// Free space ~4.4 GB
	out.Bfree = 4443940 / (blockSize / 1024)
	out.Bavail = out.Bfree
	out.Files = 1000000
	out.Ffree = 900000
}

// Statfs returns filesystem statistics for virtual directories.
func (d *DirNode) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	populateStatfs(out)
	return 0
}

// Unlink removes a document or notebook file cleanly from the reMarkable tablet and local caches.
func (d *DirNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if d.isRecent {
		return syscall.EPERM
	}
	if d.isOffline() {
		return syscall.EROFS
	}
	if name == ".Trash" || strings.HasPrefix(name, ".Trash-") || isRootMarker(d, name) {
		return syscall.EPERM
	}

	child, exists := childInfo(d.fsNode, name)
	if !exists {
		return syscall.ENOENT
	}
	if child.IsDir {
		return syscall.EISDIR
	}

	uuid := child.UUID
	logger.Action("Deleting file \"%s\" (UUID: %s) from reMarkable...", child.path, uuid)

	// Clean up any active uncommitted upload buffer
	if chInode := d.GetChild(name); chInode != nil {
		if fn, isFile := chInode.Operations().(*FileNode); isFile {
			if h := fn.uploadHandle.Load(); h != nil {
				h.cleanupLocked()
			}
		}
	}

	// One change at a time on the tablet, also across computers (see LockTablet).
	if d.client != nil {
		defer d.client.LockTablet()()
	}

	// 1. Delete all residue from tablet over SSH (metadata, content, pagedata, pdf/epub, strokes, thumbnails, text)
	// and create a .tombstone marker file. The reMarkable cloud synchronizer (rm-sync) requires <uuid>.tombstone
	// to confirm the deletion was intentional; without it, rm-sync treats missing files as data corruption
	// and automatically un-deletes/re-downloads all files from the cloud on service restart.
	if d.client != nil {
		cmd := fmt.Sprintf("rm -rf /home/root/.local/share/remarkable/xochitl/%s* /home/root/.local/share/remarkable/xochitl/%s && touch /home/root/.local/share/remarkable/xochitl/%s.tombstone && touch /home/root/.local/share/remarkable/xochitl", uuid, uuid, uuid)
		out, err := d.client.ExecuteCommand(cmd)
		if err != nil {
			logger.Error("Failed to delete %s on tablet: %v (%s)", uuid, err, out)
			return syscall.EIO
		}
		d.client.ScheduleXochitlRestart()
	}

	// 2. Invalidate local rendered PDF cache
	cacheMgr := converter.GetCache()
	if cacheMgr != nil {
		cacheMgr.Invalidate(uuid)
	}

	// 3. Invalidate Freedesktop desktop thumbnails
	filePath := filepath.Join(d.mountPoint, child.path)
	if d.syncer != nil {
		d.syncer.InvalidateThumbnail(filePath)
	}

	// 4. Clean up in-memory filesystem hierarchy
	removeNode(d.fsNode, child.node)
	if d.repo != nil {
		d.repo.RemoveUUID(uuid)
	}

	logger.Info("Successfully deleted \"%s\" (UUID: %s) and all associated files/drawings from tablet", name, uuid)
	return 0
}

// Rmdir removes an empty folder cleanly from the reMarkable tablet.
func (d *DirNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	if d.isRecent {
		return syscall.EPERM
	}
	if d.isOffline() {
		return syscall.EROFS
	}
	child, exists := childInfo(d.fsNode, name)
	if !exists {
		return syscall.ENOENT
	}
	if name == "Recent" || child.IsSpecial {
		return syscall.EPERM
	}
	if !child.IsDir {
		return syscall.ENOTDIR
	}
	if child.childCount > 0 {
		return syscall.ENOTEMPTY
	}

	uuid := child.UUID
	logger.Action("Deleting folder \"%s\" (UUID: %s) from reMarkable...", child.path, uuid)

	// One change at a time on the tablet, also across computers (see LockTablet).
	if d.client != nil {
		defer d.client.LockTablet()()
	}

	// 1. Delete folder metadata from tablet over SSH, write tombstone marker, and schedule debounced UI refresh
	if d.client != nil {
		cmd := fmt.Sprintf("rm -rf /home/root/.local/share/remarkable/xochitl/%s* && touch /home/root/.local/share/remarkable/xochitl/%s.tombstone && touch /home/root/.local/share/remarkable/xochitl", uuid, uuid)
		out, err := d.client.ExecuteCommand(cmd)
		if err != nil {
			logger.Error("Failed to delete folder %s on tablet: %v (%s)", uuid, err, out)
			return syscall.EIO
		}
		d.client.ScheduleXochitlRestart()
	}

	// 2. Clean up in-memory hierarchy
	removeNode(d.fsNode, child.node)
	if d.repo != nil {
		d.repo.RemoveUUID(uuid)
	}

	logger.Info("Successfully deleted folder \"%s\" (UUID: %s) from tablet", name, uuid)
	return 0
}

// Mkdir creates a new folder on the reMarkable tablet and in the virtual filesystem.
func (d *DirNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if d.isRecent {
		return nil, syscall.EPERM
	}
	if d.isOffline() {
		return nil, syscall.EROFS
	}
	if d.virtualPath == "/" && (name == "Recent" || name == trackerIgnoreName) {
		return nil, syscall.EEXIST
	}
	if strings.HasPrefix(name, ".") {
		logger.Info("Rejecting creation of hidden/system folder \"%s\"", name)
		return nil, syscall.EPERM
	}

	if hasChild(d.fsNode, name) {
		return nil, syscall.EEXIST
	}

	folderUUID := uuid.New().String()
	parentUUID := ""
	if d.fsNode != nil && d.fsNode.UUID != "" {
		parentUUID = d.fsNode.UUID
	}

	logger.Action("Creating folder \"%s\" (UUID: %s) on reMarkable...", path.Join(d.virtualPath, name), folderUUID)

	now := time.Now()
	nowMs := fmt.Sprintf("%d", now.UnixMilli())

	// One change at a time on the tablet, also across computers (see LockTablet).
	if d.client != nil {
		defer d.client.LockTablet()()
	}
	if d.client != nil {
		metaMap := map[string]interface{}{
			"createdTime":      nowMs,
			"deleted":          false,
			"lastModified":     nowMs,
			"metadatamodified": false,
			"modified":         false,
			"parent":           parentUUID,
			"pinned":           false,
			"synced":           true,
			"type":             "CollectionType",
			"version":          1,
			"visibleName":      name,
		}
		metaBytes, _ := json.MarshalIndent(metaMap, "", "    ")
		if err := d.client.WriteFile(path.Join(remarkable.XochitlDirectory, folderUUID+".metadata"), metaBytes); err != nil {
			logger.Error("Failed to write folder metadata for \"%s\": %v", name, err)
			return nil, syscall.EIO
		}
		_, _ = d.client.ExecuteCommand("touch /home/root/.local/share/remarkable/xochitl")
		d.client.ScheduleXochitlRestart()
	}

	meta := &model.Metadata{
		VisibleName:  name,
		Type:         model.TypeCollection,
		Parent:       parentUUID,
		LastModified: model.FlexibleTimestamp(now),
		Version:      1,
	}

	folderNode := model.NewDirectoryNode(folderUUID, name, now, meta)
	addNode(d.fsNode, folderNode)
	if d.repo != nil {
		d.repo.RegisterNode(folderUUID, folderNode)
	}

	subPath := path.Join(d.virtualPath, name)
	childDir := &DirNode{
		fsNode:           folderNode,
		client:           d.client,
		repo:             d.repo,
		syncer:           d.syncer,
		virtualPath:      subPath,
		mountPoint:       d.mountPoint,
		maxUploadSize:    d.maxUploadSize,
		textUploadFormat: d.textUploadFormat,
	}

	chInode := d.NewPersistentInode(ctx, childDir, fs.StableAttr{Mode: fuse.S_IFDIR})

	out.Mode = fuse.S_IFDIR | 0755
	out.Uid = uint32(os.Getuid())
	out.Gid = uint32(os.Getgid())
	out.Mtime = uint64(now.Unix())
	out.Atime = out.Mtime
	out.Ctime = out.Mtime

	logger.Info("Successfully created folder \"%s\" on reMarkable", name)
	return chInode, 0
}

// Create prepares a new file for upload and returns a write-buffering handle.
func (d *DirNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if d.isRecent {
		return nil, nil, 0, syscall.EPERM
	}
	if d.isOffline() {
		return nil, nil, 0, syscall.EROFS
	}
	if name == ".Trash" || strings.HasPrefix(name, ".Trash-") {
		return nil, nil, 0, syscall.EPERM
	}
	if isRootMarker(d, name) {
		return nil, nil, 0, syscall.EEXIST
	}

	fileType, isDoc := isSupportedDocumentExtension(name)
	isTemp := isTemporaryUploadName(name)

	if !isDoc && !isTemp {
		logger.Error("Unsupported file type for upload: \"%s\" (supported formats: .pdf, .epub, .txt, .md)", name)
		return nil, nil, 0, syscall.EINVAL
	}

	if hasChild(d.fsNode, name) {
		return nil, nil, 0, syscall.EEXIST
	}

	docUUID := uuid.New().String()
	parentUUID := ""
	if d.fsNode != nil && d.fsNode.UUID != "" {
		parentUUID = d.fsNode.UUID
	}

	tmpFile, err := os.CreateTemp("", fmt.Sprintf("rm_upload_%s_*.tmp", docUUID))
	if err != nil {
		logger.Error("Failed to create temporary upload buffer for \"%s\": %v", name, err)
		return nil, nil, 0, syscall.EIO
	}

	now := time.Now()
	node := model.NewFileNode(docUUID, name, "", 0, now, fileType, nil, nil)
	subPath := path.Join(d.virtualPath, name)

	fileNode := &FileNode{
		fsNode:           node,
		client:           d.client,
		repo:             d.repo,
		syncer:           d.syncer,
		virtualPath:      subPath,
		mountPoint:       d.mountPoint,
		maxUploadSize:    d.maxUploadSize,
		textUploadFormat: d.textUploadFormat,
	}

	handle := &UploadFileHandle{
		uuid:             docUUID,
		name:             name,
		fileType:         fileType,
		parentUUID:       parentUUID,
		tmpFile:          tmpFile,
		tmpPath:          tmpFile.Name(),
		maxSize:          d.maxUploadSize,
		textUploadFormat: d.textUploadFormat,
		fileNode:         fileNode,
		dirNode:          d,
		client:           d.client,
		repo:             d.repo,
		syncer:           d.syncer,
		mountPoint:       d.mountPoint,
		uploadEmpty:      true,
		existingSize:     -1,
		isTemporary:      isTemp,
	}
	fileNode.uploadHandle.Store(handle)

	chInode := d.NewPersistentInode(ctx, fileNode, fs.StableAttr{Mode: fuse.S_IFREG})
	addNode(d.fsNode, node)

	out.Mode = fuse.S_IFREG | 0644
	out.Uid = uint32(os.Getuid())
	out.Gid = uint32(os.Getgid())
	out.Mtime = uint64(now.Unix())
	out.Atime = out.Mtime
	out.Ctime = out.Mtime
	out.Size = 0

	logger.Action("Initiated upload buffering for \"%s\" (UUID: %s)", path.Join(d.virtualPath, name), docUUID)
	return chInode, handle, 0, 0
}

// Rename handles moving or renaming files/folders, including committing temporary staging uploads.
func (d *DirNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if d.isOffline() {
		return syscall.EROFS
	}
	targetDir, ok := newParent.(*DirNode)
	if !ok {
		return syscall.EINVAL
	}

	if d.isRecent || targetDir.isRecent {
		return syscall.EPERM
	}
	if name == "Recent" || (d.virtualPath == "/" && newName == "Recent") {
		return syscall.EPERM
	}
	if strings.HasPrefix(targetDir.virtualPath, "/.Trash") || strings.HasPrefix(newName, ".Trash") {
		return syscall.EPERM
	}
	if isRootMarker(d, name) || isRootMarker(targetDir, newName) {
		return syscall.EPERM
	}

	child, exists := childInfo(d.fsNode, name)
	if !exists {
		return syscall.ENOENT
	}
	if child.IsSpecial {
		return syscall.EPERM
	}

	targetParentUUID := ""
	if targetDir.fsNode != nil && targetDir.fsNode.UUID != "" {
		targetParentUUID = targetDir.fsNode.UUID
	}

	// One change at a time on the tablet, also across computers (see LockTablet).
	if d.client != nil {
		defer d.client.LockTablet()()
	}

	// If destination already exists, clean it up cleanly from the tablet and cache
	if existingDest, destExists := childInfo(targetDir.fsNode, newName); destExists && existingDest.node != child.node {
		if existingDest.IsDir {
			if !child.IsDir {
				return syscall.EISDIR
			}
			if existingDest.childCount > 0 {
				return syscall.ENOTEMPTY
			}
		} else if child.IsDir {
			return syscall.ENOTDIR
		}

		if targetDir.client != nil {
			cmd := fmt.Sprintf("rm -rf /home/root/.local/share/remarkable/xochitl/%s* /home/root/.local/share/remarkable/xochitl/%s && touch /home/root/.local/share/remarkable/xochitl/%s.tombstone && touch /home/root/.local/share/remarkable/xochitl", existingDest.UUID, existingDest.UUID, existingDest.UUID)
			_, _ = targetDir.client.ExecuteCommand(cmd)
		}
		cacheMgr := converter.GetCache()
		if cacheMgr != nil {
			cacheMgr.Invalidate(existingDest.UUID)
		}
		if targetDir.syncer != nil {
			targetDir.syncer.InvalidateThumbnail(filepath.Join(targetDir.mountPoint, existingDest.path))
		}
		removeNode(targetDir.fsNode, existingDest.node)
		if targetDir.repo != nil {
			targetDir.repo.RemoveUUID(existingDest.UUID)
		}
	}

	// 1. Check if child is an uncommitted upload (e.g. from Nautilus or temporary staging file)
	var fileNode *FileNode
	if chInode := d.GetChild(name); chInode != nil {
		if fn, isFile := chInode.Operations().(*FileNode); isFile {
			fileNode = fn
		}
	}

	var staging *UploadFileHandle
	if fileNode != nil {
		staging = fileNode.uploadHandle.Load()
	}
	if staging != nil && staging.lockIfUncommitted() {
		handle := staging
		defer handle.mu.Unlock()

		fileType, isDoc := isSupportedDocumentExtension(newName)
		if !isDoc {
			logger.Error("Cannot rename staging file \"%s\" to unsupported type \"%s\" (supported formats: .pdf, .epub, .txt, .md)", name, newName)
			return syscall.EINVAL
		}

		handle.name = newName
		handle.fileType = fileType
		handle.parentUUID = targetParentUUID
		handle.dirNode = targetDir
		handle.client = targetDir.client
		handle.repo = targetDir.repo
		handle.syncer = targetDir.syncer
		handle.mountPoint = targetDir.mountPoint
		handle.textUploadFormat = targetDir.textUploadFormat
		model.LockTree()
		handle.fileNode.virtualPath = path.Join(targetDir.virtualPath, newName)
		model.UnlockTree()

		if errno := handle.commitLocked(ctx); errno != 0 {
			return errno
		}
		handle.cleanupLocked()

		moveNode(d.fsNode, targetDir.fsNode, child.node, newName, false, "", "")
		logger.Info("Successfully committed staging upload \"%s\" as \"%s\"", name, newName)
		return 0
	}

	// 2. Existing folder rename / move
	if child.IsDir {
		logger.Action("Renaming folder \"%s\" to \"%s\" (UUID: %s)...", child.path, path.Join(targetDir.virtualPath, newName), child.UUID)
		if d.client != nil {
			nowMs := fmt.Sprintf("%d", time.Now().UnixMilli())
			metaMap := map[string]interface{}{
				"deleted":          false,
				"lastModified":     nowMs,
				"metadatamodified": true,
				"modified":         true,
				"parent":           targetParentUUID,
				"pinned":           false,
				"synced":           false,
				"type":             "CollectionType",
				"version":          1,
				"visibleName":      newName,
			}
			metaBytes, _ := json.MarshalIndent(metaMap, "", "    ")
			_ = d.client.WriteFile(path.Join(remarkable.XochitlDirectory, child.UUID+".metadata"), metaBytes)
			_, _ = d.client.ExecuteCommand("touch /home/root/.local/share/remarkable/xochitl")
			d.client.ScheduleXochitlRestart()
		}

		moveNode(d.fsNode, targetDir.fsNode, child.node, newName, true, newName, targetParentUUID)
		logger.Info("Successfully renamed folder to \"%s\"", newName)
		return 0
	}

	// 3. Existing document rename / move
	newExt := filepath.Ext(newName)
	oldExt := filepath.Ext(child.Name)
	if strings.ToLower(newExt) != strings.ToLower(oldExt) {
		logger.Error("Cannot change document extension from \"%s\" to \"%s\"", oldExt, newExt)
		return syscall.EINVAL
	}

	newVisibleName := strings.TrimSuffix(newName, newExt)
	logger.Action("Renaming document \"%s\" to \"%s\" (UUID: %s)...", child.path, path.Join(targetDir.virtualPath, newName), child.UUID)

	if d.client != nil {
		nowMs := fmt.Sprintf("%d", time.Now().UnixMilli())
		metaMap := map[string]interface{}{
			"deleted":          false,
			"lastModified":     nowMs,
			"metadatamodified": true,
			"modified":         true,
			"parent":           targetParentUUID,
			"pinned":           false,
			"synced":           false,
			"type":             "DocumentType",
			"version":          1,
			"visibleName":      newVisibleName,
		}
		metaBytes, _ := json.MarshalIndent(metaMap, "", "    ")
		_ = d.client.WriteFile(path.Join(remarkable.XochitlDirectory, child.UUID+".metadata"), metaBytes)
		_, _ = d.client.ExecuteCommand("touch /home/root/.local/share/remarkable/xochitl")
		d.client.ScheduleXochitlRestart()
	}

	// Invalidate old thumbnail cache and sync new one
	if d.syncer != nil {
		oldFilePath := filepath.Join(d.mountPoint, child.path)
		d.syncer.InvalidateThumbnail(oldFilePath)
	}

	moveNode(d.fsNode, targetDir.fsNode, child.node, newName, true, newVisibleName, targetParentUUID)

	if d.syncer != nil {
		model.RLockTree()
		newFilePath := filepath.Join(d.mountPoint, child.node.VirtualPath())
		model.RUnlockTree()
		if child.ThumbnailPath != "" && d.client != nil {
			pngBytes, err := d.client.ReadFile(child.ThumbnailPath)
			if err == nil && len(pngBytes) > 0 {
				_ = d.syncer.SyncThumbnail(newFilePath, child.ModTime, pngBytes)
			}
		}
	}

	// The document may still be open for writing, with writes since its upload that go to the
	// tablet at the next close: that upload must use the new name and folder.
	if fileNode != nil {
		if h := fileNode.uploadHandle.Load(); h != nil {
			h.moved(newName, targetParentUUID, targetDir)
		}
	}

	logger.Info("Successfully renamed document to \"%s\"", newName)
	return 0
}

// Readdir streams directory contents and logs the user action.
func (d *DirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	if d.isOffline() {
		// The tree is still being read (or the tablet hasn't been reached yet): show it empty,
		// except for .trackerignore, so the indexer skips the folder from the start.
		return fs.NewListDirStream(d.markerEntries()), 0
	}
	triggerBackgroundSync(d.repo, d.client, d.syncer, d.mountPoint)

	if d.isRecent && d.repo != nil {
		d.repo.UpdateRecentNode()
	}
	children := childInfos(d.fsNode)
	if d.isRecent && d.repo != nil {
		// Prune child inodes that dropped out of top 15
		activeMap := make(map[string]bool)
		for _, ch := range children {
			activeMap[ch.Name] = true
		}
		for name := range d.Children() {
			if !activeMap[name] {
				d.RmChild(name)
			}
		}
	}

	logger.Action("Browsed folder: \"%s\" (%d items)", d.virtualPath, len(children))

	entries := d.markerEntries()
	for _, child := range children {
		mode := uint32(fuse.S_IFREG)
		if child.IsDir {
			mode = fuse.S_IFDIR
		}
		entries = append(entries, fuse.DirEntry{
			Mode: mode,
			Name: child.Name,
		})
	}
	d.attachChildren(ctx)
	return fs.NewListDirStream(entries), 0
}

// markerEntries lists the virtual files shown in this folder: .trackerignore at the root.
func (d *DirNode) markerEntries() []fuse.DirEntry {
	if d.virtualPath != "/" {
		return nil
	}
	return []fuse.DirEntry{{Mode: fuse.S_IFREG, Name: trackerIgnoreName}}
}

// FileNode represents a document or notebook file in the FUSE filesystem.
type FileNode struct {
	fs.Inode
	fsNode           *model.FSNode
	client           *sshclient.Client
	repo             *remarkable.Repository
	syncer           *thumbnail.Syncer
	virtualPath      string
	mountPoint       string
	maxUploadSize    int64
	textUploadFormat string
	uploadHandle     atomic.Pointer[UploadFileHandle] // the open upload of this document, if any
}

var _ fs.NodeGetattrer = (*FileNode)(nil)
var _ fs.NodeOpener = (*FileNode)(nil)
var _ fs.NodeSetattrer = (*FileNode)(nil)
var _ fs.NodeStatfser = (*FileNode)(nil)

// Setattr forwards truncation/attribute changes to active upload handle or initializes one if sizing an existing file.
func (f *FileNode) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	info, vpath := f.info()
	if info.readOnly {
		return syscall.EPERM
	}
	if ufh, ok := fh.(*UploadFileHandle); ok {
		return ufh.Setattr(ctx, in, out)
	}
	if h := f.uploadHandle.Load(); h != nil {
		return h.Setattr(ctx, in, out)
	}

	// A standalone truncate() of a closed document (0.ideas.md C15): to its own size, nothing to
	// do; to 0, the document is emptied on the tablet, like opening it with O_TRUNC; any other
	// size would leave a broken PDF or EPUB, so it's refused.
	if in.Valid&fuse.FATTR_SIZE != 0 {
		if info.IsNotebook {
			logger.Error("Cannot truncate reMarkable notebook \"%s\"", vpath)
			return syscall.EACCES
		}
		if info.FileType != "pdf" && info.FileType != "epub" && info.FileType != "txt" && info.FileType != "md" {
			logger.Error("Cannot truncate file \"%s\": unsupported format", vpath)
			return syscall.EINVAL
		}
		if int64(in.Size) == info.Size || int64(in.Size) == shownSize(info) {
			return f.Getattr(ctx, fh, out)
		}
		if in.Size != 0 {
			logger.Error("Cannot truncate \"%s\" to %d bytes: documents on the tablet can only be emptied (truncated to 0)", vpath, in.Size)
			return syscall.EOPNOTSUPP
		}

		tmpFile, err := os.CreateTemp("", fmt.Sprintf("rm_upload_%s_*.tmp", info.UUID))
		if err != nil {
			logger.Error("Failed to create temporary upload buffer for truncate of \"%s\": %v", vpath, err)
			return syscall.EIO
		}

		parentUUID := ""
		if info.Metadata != nil {
			parentUUID = info.Metadata.Parent
		}

		handle := &UploadFileHandle{
			uuid:             info.UUID,
			name:             info.Name,
			fileType:         info.FileType,
			parentUUID:       parentUUID,
			tmpFile:          tmpFile,
			tmpPath:          tmpFile.Name(),
			maxSize:          f.maxUploadSize,
			textUploadFormat: f.textUploadFormat,
			fileNode:         f,
			client:           f.client,
			repo:             f.repo,
			syncer:           f.syncer,
			mountPoint:       f.mountPoint,
			uploadEmpty:      true,
			existingSize:     -1,
			isTemporary:      false,
		}
		logger.Action("Emptying \"%s\" (UUID: %s) on reMarkable (truncated to 0)...", vpath, info.UUID)
		handle.mu.Lock()
		defer handle.mu.Unlock()
		defer handle.cleanupLocked()
		if errno := handle.commitLocked(ctx); errno != 0 {
			return errno
		}
		return f.Getattr(ctx, fh, out)
	}

	return f.Getattr(ctx, fh, out)
}

// info copies the document's fields and its path in the mount, under the tree lock.
func (f *FileNode) info() (nodeInfo, string) {
	model.RLockTree()
	defer model.RUnlockTree()
	return infoOf(f.fsNode), f.virtualPath
}

// Getattr returns file metadata (size, permissions, timestamps).
func (f *FileNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	info, _ := f.info()
	mode := uint32(0644)
	if info.readOnly {
		mode = 0444
	}
	out.Mode = fuse.S_IFREG | mode
	out.Uid = uint32(os.Getuid())
	out.Gid = uint32(os.Getgid())

	out.Size = uint64(shownSize(info))
	if !info.ModTime.IsZero() {
		out.Mtime = uint64(info.ModTime.Unix())
		out.Atime = out.Mtime
		out.Ctime = out.Mtime
	}
	return 0
}

// shownSize is the size the folder shows for a document: for notebooks and annotated PDFs,
// the rendered PDF's once it's been rendered.
func shownSize(info nodeInfo) int64 {
	size := info.Size
	if info.IsNotebook || info.HasAnnotations {
		cacheMgr := converter.GetCache()
		if p, hit := cacheMgr.GetRenderedPath(info.UUID, info.ModTime); hit {
			if stat, err := os.Stat(p); err == nil {
				size = stat.Size()
			}
		}
	}
	return size
}

// Statfs returns filesystem statistics for virtual files.
func (f *FileNode) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	populateStatfs(out)
	return 0
}

// Open handles opening files for reading or for in-place overwrite uploads.
func (f *FileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	info, vpath := f.info()
	// Handle write attempts (in-place overwriting of existing document)
	if flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_APPEND|syscall.O_TRUNC) != 0 {
		if info.readOnly {
			return nil, 0, syscall.EPERM
		}
		if info.IsNotebook {
			logger.Error("Cannot overwrite reMarkable notebook \"%s\" with raw bytes", vpath)
			return nil, 0, syscall.EACCES
		}

		if info.FileType != "pdf" && info.FileType != "epub" && info.FileType != "txt" && info.FileType != "md" {
			logger.Error("Cannot overwrite file \"%s\": unsupported format", vpath)
			return nil, 0, syscall.EINVAL
		}

		tmpFile, err := os.CreateTemp("", fmt.Sprintf("rm_upload_%s_*.tmp", info.UUID))
		if err != nil {
			logger.Error("Failed to create temporary upload buffer for overwrite of \"%s\": %v", vpath, err)
			return nil, 0, syscall.EIO
		}

		parentUUID := ""
		if info.Metadata != nil {
			parentUUID = info.Metadata.Parent
		}

		handle := &UploadFileHandle{
			uuid:             info.UUID,
			name:             info.Name,
			fileType:         info.FileType,
			parentUUID:       parentUUID,
			tmpFile:          tmpFile,
			tmpPath:          tmpFile.Name(),
			maxSize:          f.maxUploadSize,
			textUploadFormat: f.textUploadFormat,
			fileNode:         f,
			client:           f.client,
			repo:             f.repo,
			syncer:           f.syncer,
			mountPoint:       f.mountPoint,
			uploadEmpty:      flags&syscall.O_TRUNC != 0, // the kernel usually truncates separately, see Setattr
			existingSize:     shownSize(info),
			isTemporary:      false,
		}
		f.uploadHandle.Store(handle)
		logger.Action("Initiated overwrite upload buffering for \"%s\" (UUID: %s)", vpath, info.UUID)
		return handle, 0, 0
	}

	logger.Action("Opened file: \"%s\" (type: %s)", vpath, info.FileType)

	// 1. Handle on-the-fly rendering for pure notebooks and annotated PDFs
	if info.IsNotebook || (info.HasAnnotations && info.FileType == "pdf") {
		cachedPath, err := f.renderOrGetCachedPDF(ctx, info, vpath)
		if err == nil {
			go f.refreshThumbnailForcefully(info, vpath, cachedPath)
			localFile, oErr := os.Open(cachedPath)
			if oErr == nil {
				stat, _ := localFile.Stat()
				model.LockTree()
				f.fsNode.Size = stat.Size()
				model.UnlockTree()
				return &LocalFileHandle{
					file:        localFile,
					virtualPath: vpath,
					size:        stat.Size(),
				}, fuse.FOPEN_DIRECT_IO, 0
			}
		}
		logger.Error("Failed to render/open cached PDF for %s: %v, falling back", vpath, err)
	}

	// 2. Handle standard remote files via SFTP streaming
	if info.RemotePath != "" && f.client != nil {
		go f.refreshThumbnailForcefully(info, vpath, "")
		sFile, err := f.client.Open(info.RemotePath)
		if err != nil {
			logger.Error("Failed to open remote file %s for %s: %v", info.RemotePath, vpath, err)
			return nil, 0, syscall.EIO
		}
		handle := &SFTPFileHandle{
			client:      f.client,
			remotePath:  info.RemotePath,
			file:        sFile,
			virtualPath: vpath,
			size:        info.Size,
		}
		return handle, fuse.FOPEN_KEEP_CACHE, 0
	}

	// 3. In-memory fallback
	content := []byte(fmt.Sprintf("Title: %s\nType: %s\nUUID: %s\n", info.Name, info.FileType, info.UUID))
	return &MemFileReader{data: content}, 0, 0
}

// refreshThumbnailForcefully forcefully purges stale cached thumbnails and syncs fresh ones.
// info and vpath are the document as it was when opened (see FileNode.info).
func (f *FileNode) refreshThumbnailForcefully(info nodeInfo, vpath, cachedPDFPath string) {
	if f.syncer == nil {
		s, err := thumbnail.NewSyncer()
		if err == nil {
			f.syncer = s
		} else {
			return
		}
	}

	filePath := filepath.Join(f.mountPoint, vpath)

	// 1. Forcefully invalidate stale thumbnails (including any failed markers)
	f.syncer.InvalidateThumbnail(filePath)

	// 2. If cached PDF path is provided, sync using the rendered PDF
	if cachedPDFPath != "" {
		_ = f.syncer.SyncRenderedPDFThumbnail(filePath, info.ModTime, cachedPDFPath, info.ThumbnailPath, f.client)
		return
	}

	// 3. For notebooks and annotated PDFs, check if cached PDF exists
	if info.IsNotebook || (info.HasAnnotations && info.FileType == "pdf") {
		cacheMgr := converter.GetCache()
		if cacheMgr != nil {
			if p, hit := cacheMgr.GetRenderedPath(info.UUID, info.ModTime); hit {
				_ = f.syncer.SyncRenderedPDFThumbnail(filePath, info.ModTime, p, info.ThumbnailPath, f.client)
				return
			}
		}
	}

	// 4. Fallback to tablet's native thumbnail
	if info.ThumbnailPath != "" && f.client != nil {
		pngBytes, err := f.client.ReadFile(info.ThumbnailPath)
		if err == nil && len(pngBytes) > 0 {
			_ = f.syncer.SyncThumbnail(filePath, info.ModTime, pngBytes)
			return
		}
	}
}

// renderOrGetCachedPDF checks cache validity against document modification time and renders on miss.
// info and vpath are the document as it was when opened (see FileNode.info).
func (f *FileNode) renderOrGetCachedPDF(ctx context.Context, info nodeInfo, vpath string) (string, error) {
	cacheMgr := converter.GetCache()
	cachedPath, hit := cacheMgr.GetRenderedPath(info.UUID, info.ModTime)
	if hit {
		localPath := filepath.Join(f.mountPoint, vpath)
		if f.syncer != nil {
			go func() {
				_ = f.syncer.SyncRenderedPDFThumbnail(localPath, info.ModTime, cachedPath, info.ThumbnailPath, f.client)
			}()
		}
		return cachedPath, nil
	}

	logger.Info("Rendering notebook / doodle overlay for \"%s\" (UUID: %s)...", vpath, info.UUID)
	xochitlDir := "/home/root/.local/share/remarkable/xochitl"

	var pdfBuf bytes.Buffer

	if info.IsNotebook {
		// Pure notebook: render all pages as a multi-page PDF
		var strokeSVGs []string
		for _, pageUUID := range info.Pages {
			strokePath := fmt.Sprintf("%s/%s/%s.rm", xochitlDir, info.UUID, pageUUID)
			rmBytes, err := f.client.ReadFile(strokePath)
			if err == nil && len(rmBytes) > 0 {
				svg, sErr := converter.RenderNotebookRMToSVG(rmBytes, converter.NotebookWidthPt, converter.NotebookHeightPt)
				if sErr == nil {
					strokeSVGs = append(strokeSVGs, svg)
					continue
				}
			}
			strokeSVGs = append(strokeSVGs, "")
		}

		if len(strokeSVGs) == 0 {
			strokeSVGs = append(strokeSVGs, "")
		}
		if err := converter.SVGsToPDF(strokeSVGs, &pdfBuf); err != nil {
			return "", fmt.Errorf("failed to render notebook to PDF: %w", err)
		}
	} else {
		// Annotated PDF: download base PDF, inspect page dimensions, and overlay strokes
		basePDFBytes, err := f.client.ReadFile(info.RemotePath)
		if err != nil {
			return "", fmt.Errorf("failed to fetch base PDF: %w", err)
		}

		baseRS := bytes.NewReader(basePDFBytes)
		dims, dErr := converter.GetPDFPageDims(ctx, baseRS)
		if dErr != nil {
			logger.Error("Failed to inspect page dimensions for %s: %v", vpath, dErr)
		}

		var strokeSVGs []string
		for i, pageUUID := range info.Pages {
			var wPt, hPt float64
			if i < len(dims) {
				wPt = dims[i].Width
				hPt = dims[i].Height
			}
			strokePath := fmt.Sprintf("%s/%s/%s.rm", xochitlDir, info.UUID, pageUUID)
			rmBytes, err := f.client.ReadFile(strokePath)
			if err == nil && len(rmBytes) > 0 {
				svg, sErr := converter.RenderRMToSVG(rmBytes, wPt, hPt)
				if sErr == nil {
					strokeSVGs = append(strokeSVGs, svg)
					continue
				}
			}
			strokeSVGs = append(strokeSVGs, "")
		}

		if err := converter.OverlayStrokesOnPDF(ctx, bytes.NewReader(basePDFBytes), strokeSVGs, dims, &pdfBuf); err != nil {
			logger.Error("Failed to overlay strokes on %s: %v, falling back to base PDF", vpath, err)
			pdfBuf.Reset()
			pdfBuf.Write(basePDFBytes)
		}
	}

	// Store rendered PDF in cache
	cachedPath, storeErr := cacheMgr.StoreRendered(info.UUID, info.ModTime, &pdfBuf)
	if storeErr == nil {
		localPath := filepath.Join(f.mountPoint, vpath)
		go func(p string, m time.Time, pdfP string, thumbP string, cl *sshclient.Client) {
			s, err := thumbnail.NewSyncer()
			if err == nil {
				_ = s.SyncRenderedPDFThumbnail(p, m, pdfP, thumbP, cl)
			}
		}(localPath, info.ModTime, cachedPath, info.ThumbnailPath, f.client)
	}
	return cachedPath, storeErr
}

// LocalFileHandle wraps a local cached file for fast zero-latency reading.
type LocalFileHandle struct {
	mu          sync.Mutex
	file        *os.File
	virtualPath string
	size        int64
	closed      bool
}

var _ fs.FileReader = (*LocalFileHandle)(nil)
var _ fs.FileReleaser = (*LocalFileHandle)(nil)
var _ fs.FileGetattrer = (*LocalFileHandle)(nil)

func (h *LocalFileHandle) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()

	size := h.size
	if h.file != nil {
		if st, err := h.file.Stat(); err == nil {
			size = st.Size()
		}
	}
	out.Size = uint64(size)
	out.Mode = fuse.S_IFREG | 0444
	return 0
}

func (h *LocalFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed || h.file == nil {
		return nil, syscall.EBADF
	}

	n, err := h.file.ReadAt(dest, off)
	if err != nil && err != io.EOF {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *LocalFileHandle) Release(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.closed && h.file != nil {
		_ = h.file.Close()
		h.closed = true
		logger.Action("Closed file: \"%s\"", h.virtualPath)
	}
	return 0
}

// MemFileReader implements fs.FileReader for in-memory byte slices.
type MemFileReader struct {
	data []byte
}

var _ fs.FileReader = (*MemFileReader)(nil)

func (m *MemFileReader) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if off >= int64(len(m.data)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(m.data)) {
		end = int64(len(m.data))
	}
	return fuse.ReadResultData(m.data[off:end]), 0
}

// SFTPFileHandle wraps a remote SFTP file to stream reads directly into FUSE buffers with reconnection support.
type SFTPFileHandle struct {
	mu          sync.Mutex
	client      *sshclient.Client
	remotePath  string
	file        *sftp.File
	virtualPath string
	size        int64
	closed      bool
}

var _ fs.FileReader = (*SFTPFileHandle)(nil)
var _ fs.FileReleaser = (*SFTPFileHandle)(nil)
var _ fs.FileGetattrer = (*SFTPFileHandle)(nil)

func (h *SFTPFileHandle) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	out.Size = uint64(h.size)
	out.Mode = fuse.S_IFREG | 0444
	return 0
}

// Read reads bytes from the remote file at the given offset, seamlessly reconnecting if the session was lost.
func (h *SFTPFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return nil, syscall.EBADF
	}

	if h.file != nil {
		n, err := h.file.ReadAt(dest, off)
		if err == nil || err == io.EOF {
			return fuse.ReadResultData(dest[:n]), 0
		}
		_ = h.file.Close()
		h.file = nil
	}

	// Reopen if connection dropped
	if h.client != nil && h.remotePath != "" {
		newFile, err := h.client.Open(h.remotePath)
		if err == nil {
			h.file = newFile
			n, rErr := h.file.ReadAt(dest, off)
			if rErr == nil || rErr == io.EOF {
				return fuse.ReadResultData(dest[:n]), 0
			}
		}
		logger.Error("Failed to read after reconnection at offset %d in %s", off, h.virtualPath)
	}

	return nil, syscall.EIO
}

// Release closes the remote file handle and logs user action.
func (h *SFTPFileHandle) Release(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.closed && h.file != nil {
		_ = h.file.Close()
		h.closed = true
		logger.Action("Closed file: \"%s\"", h.virtualPath)
	}
	return 0
}

// UploadFileHandle manages buffered writes during file uploads to the reMarkable tablet.
type UploadFileHandle struct {
	mu               sync.Mutex
	uuid             string
	name             string
	fileType         string // "pdf", "epub", or "" if temporary
	parentUUID       string
	tmpFile          *os.File
	tmpPath          string
	size             int64
	maxSize          int64
	fileNode         *FileNode
	dirNode          *DirNode
	client           *sshclient.Client
	repo             *remarkable.Repository
	syncer           *thumbnail.Syncer
	mountPoint       string
	textUploadFormat string
	committed        bool
	// dirty: data was written (or the size changed) since the last upload, or since opening.
	// A close uploads only then: shell redirection closes a copy of the file once before
	// writing anything, and writes after an upload must reach the tablet too (0.ideas.md C13).
	dirty bool
	// uploadEmpty: upload at the last close even if nothing was written, because the empty file
	// is what was asked for: a new file, or a document opened with O_TRUNC. Not for a document
	// merely opened for writing (touch), which stays as it is (0.ideas.md C16).
	uploadEmpty bool
	// existingSize: for an existing document opened for writing, its size on the tablet (the
	// buffer starts empty, without its content); -1 for a new file.
	existingSize int64
	isTemporary  bool
}

// go-fuse interface assertions for upload file operations:
//   - FileWriter: Writes incoming data chunks to the temporary local buffer.
//   - FileReader: Reads back data from the buffer (enables tools opening in O_RDWR mode).
//   - FileSetattrer: Named after POSIX setattr; acts as the file truncation and attribute setter
//     when an open file is resized (e.g., ftruncate).
//   - FileGetattrer: Returns current file size and permissions (fstat).
//   - FileFlusher: Handles POSIX flush when an application closes the file descriptor.
//   - FileReleaser: Handles final handle teardown and temp buffer cleanup.
var _ fs.FileWriter = (*UploadFileHandle)(nil)
var _ fs.FileReader = (*UploadFileHandle)(nil)
var _ fs.FileSetattrer = (*UploadFileHandle)(nil)
var _ fs.FileGetattrer = (*UploadFileHandle)(nil)
var _ fs.FileFlusher = (*UploadFileHandle)(nil)
var _ fs.FileReleaser = (*UploadFileHandle)(nil)

// Read allows reading back data from the staging buffer (supporting O_RDWR tools).
func (h *UploadFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.tmpFile == nil {
		return nil, syscall.EBADF
	}

	n, err := h.tmpFile.ReadAt(dest, off)
	if err != nil && err != io.EOF {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(dest[:n]), 0
}

// Write writes data chunks into the local temporary buffer while enforcing size limits.
func (h *UploadFileHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.tmpFile == nil {
		return 0, syscall.EBADF
	}

	targetEnd := off + int64(len(data))
	if h.maxSize > 0 && targetEnd > h.maxSize {
		logger.Error("Upload size limit exceeded for \"%s\": would reach %d bytes, limit is %d bytes (%d MB)",
			h.name, targetEnd, h.maxSize, h.maxSize/(1024*1024))
		return 0, syscall.EFBIG
	}

	n, err := h.tmpFile.WriteAt(data, off)
	if err != nil && err != io.EOF {
		logger.Error("Failed to write upload buffer for \"%s\": %v", h.name, err)
		return 0, syscall.EIO
	}

	h.dirty = true
	if targetEnd > h.size {
		h.size = targetEnd
		if h.fileNode != nil && h.fileNode.fsNode != nil {
			model.LockTree()
			h.fileNode.fsNode.Size = targetEnd
			model.UnlockTree()
		}
	}

	return uint32(n), 0
}

// Setattr adjusts file size (truncate) while enforcing size limits.
func (h *UploadFileHandle) Setattr(ctx context.Context, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()

	// An existing document with nothing written to it yet: its content isn't in the buffer, so
	// only emptying it (O_TRUNC, truncate to 0) or keeping its size make sense (0.ideas.md C15/C16).
	if in.Valid&fuse.FATTR_SIZE != 0 && h.existingSize >= 0 && !h.dirty && !h.committed && h.size == 0 {
		switch {
		case in.Size == 0:
			h.uploadEmpty = true
		case int64(in.Size) == h.existingSize: // what the folder shows for it
			out.Mode = fuse.S_IFREG | 0644
			out.Uid = uint32(os.Getuid())
			out.Gid = uint32(os.Getgid())
			out.Size = in.Size
			return 0
		default:
			logger.Error("Cannot truncate \"%s\" to %d bytes: documents on the tablet can only be emptied (truncated to 0)", h.name, in.Size)
			return syscall.EOPNOTSUPP
		}
	}

	if in.Valid&fuse.FATTR_SIZE != 0 {
		if h.maxSize > 0 && int64(in.Size) > h.maxSize {
			logger.Error("Upload size limit exceeded for \"%s\" in truncate: %d bytes > %d bytes limit",
				h.name, in.Size, h.maxSize)
			return syscall.EFBIG
		}
		if h.tmpFile != nil {
			if err := h.tmpFile.Truncate(int64(in.Size)); err != nil {
				return syscall.EIO
			}
		}
		// Only a real change counts: opening with O_TRUNC truncates the empty buffer to 0.
		if int64(in.Size) != h.size {
			h.dirty = true
		}
		h.size = int64(in.Size)
		if h.fileNode != nil && h.fileNode.fsNode != nil {
			model.LockTree()
			h.fileNode.fsNode.Size = h.size
			model.UnlockTree()
		}
	}

	out.Mode = fuse.S_IFREG | 0644
	out.Uid = uint32(os.Getuid())
	out.Gid = uint32(os.Getgid())
	out.Size = uint64(h.size)
	return 0
}

// Getattr returns current buffered file size and attributes.
func (h *UploadFileHandle) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()

	out.Mode = fuse.S_IFREG | 0644
	out.Uid = uint32(os.Getuid())
	out.Gid = uint32(os.Getgid())
	out.Size = uint64(h.size)
	return 0
}

// Flush is invoked on every close(fd) and uploads to the tablet what was written since the last
// upload (temporary staging files wait for their rename). Nothing new written: nothing to do, so
// a close before any writes (shell redirection) doesn't upload an empty document.
func (h *UploadFileHandle) Flush(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.tmpFile != nil {
		_ = h.tmpFile.Sync()
	}

	if h.isTemporary || !h.dirty {
		return 0
	}

	return h.commitLocked(ctx)
}

// Release follows the last close: it uploads anything still unsent (also a new file left
// empty, see uploadEmpty) and removes the local buffer. close() has already returned, so a
// failed upload can't be reported to the program: the written file is kept instead.
func (h *UploadFileHandle) Release(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.isTemporary && (h.dirty || (!h.committed && h.uploadEmpty)) {
		if errno := h.commitLocked(ctx); errno != 0 {
			h.keepUnsentLocked()
		}
	}
	if h.committed || !h.isTemporary {
		h.cleanupLocked()
	}

	return 0
}

// keepUnsentLocked saves what was written, which couldn't be uploaded, in
// ~/.cache/remarkable-fs/unsent/, so nothing is lost silently (0.ideas.md C14).
func (h *UploadFileHandle) keepUnsentLocked() {
	if h.tmpPath == "" || h.size == 0 {
		return
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	dir := filepath.Join(cache, "remarkable-fs", "unsent")
	dest := filepath.Join(dir, time.Now().Format("2006-01-02 15.04.05.000")+" "+h.name)
	if err = os.MkdirAll(dir, 0700); err == nil {
		if err = copyFile(h.tmpPath, dest); err == nil {
			logger.Error("Couldn't upload \"%s\" to the tablet; what was written is kept in %s. Copy it into the folder again once the tablet is connected.", h.name, dest)
			return
		}
	}
	logger.Error("Couldn't upload \"%s\" to the tablet, and couldn't keep a copy in %s either: %v", h.name, dir, err)
}

// copyFile copies from into a new file to; it never overwrites one.
func copyFile(from, to string) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := out.Write(data); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// lockIfUncommitted takes the handle's lock if it was never uploaded (a staging file, or a
// file not closed yet) and says so; otherwise it leaves the lock free.
func (h *UploadFileHandle) lockIfUncommitted() bool {
	h.mu.Lock()
	if h.committed {
		h.mu.Unlock()
		return false
	}
	return true
}

// moved follows a rename or move of the uploaded document, so a later upload keeps its new
// name and folder.
func (h *UploadFileHandle) moved(name, parentUUID string, dir *DirNode) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.name, h.parentUUID, h.dirNode = name, parentUUID, dir
}

// commitLocked uploads the buffered file to reMarkable tablet, writes metadata/content/pagedata, and
// notifies xochitl. Called again after more writes, it uploads the document again, whole.
func (h *UploadFileHandle) commitLocked(ctx context.Context) syscall.Errno {
	if h.tmpFile == nil {
		return 0
	}

	_ = h.tmpFile.Sync()
	stat, err := h.tmpFile.Stat()
	if err != nil {
		logger.Error("Failed to stat temp upload buffer for \"%s\": %v", h.name, err)
		return syscall.EIO
	}
	fileSize := stat.Size()

	if h.maxSize > 0 && fileSize > h.maxSize {
		logger.Error("File \"%s\" size %d bytes exceeds configured upload limit of %d bytes (%d MB)",
			h.name, fileSize, h.maxSize, h.maxSize/(1024*1024))
		return syscall.EFBIG
	}

	logger.Action("Uploading \"%s\" (%d bytes) to reMarkable tablet (UUID: %s)...", h.name, fileSize, h.uuid)

	// 0. Handle on-the-fly conversion for txt and md, into a file of its own: the buffer keeps
	// the text as written, so later writes and another upload start from it.
	targetFileType := h.fileType
	src, srcPath := h.tmpFile, h.tmpPath
	if h.fileType == "txt" || h.fileType == "md" {
		rawBytes, rErr := os.ReadFile(h.tmpPath)
		if rErr != nil {
			logger.Error("Failed to read raw upload buffer for \"%s\": %v", h.name, rErr)
			return syscall.EIO
		}

		title := strings.TrimSuffix(h.name, filepath.Ext(h.name))
		targetFormat := strings.ToLower(strings.TrimSpace(h.textUploadFormat))
		if targetFormat == "" {
			targetFormat = "epub"
		}

		var convertedBytes []byte
		var cErr error

		if targetFormat == "epub" {
			if h.fileType == "txt" {
				convertedBytes, cErr = converter.TextToEPUB(rawBytes, title)
			} else {
				convertedBytes, cErr = converter.MarkdownToEPUB(rawBytes, title)
			}
			targetFileType = "epub"
		} else {
			if h.fileType == "txt" {
				convertedBytes, cErr = converter.TextToPDF(rawBytes, title)
			} else {
				convertedBytes, cErr = converter.MarkdownToPDF(rawBytes, h.name)
			}
			targetFileType = "pdf"
		}

		if cErr != nil {
			logger.Error("Failed to convert %s to %s for \"%s\": %v", h.fileType, targetFileType, h.name, cErr)
			return syscall.EIO
		}

		converted, err := os.CreateTemp("", fmt.Sprintf("rm_upload_%s_*.%s", h.uuid, targetFileType))
		if err != nil {
			logger.Error("Failed to create a buffer for the converted %s: %v", targetFileType, err)
			return syscall.EIO
		}
		defer func() {
			_ = converted.Close()
			_ = os.Remove(converted.Name())
		}()
		if _, err := converted.Write(convertedBytes); err != nil {
			logger.Error("Failed to write converted %s to upload buffer: %v", targetFileType, err)
			return syscall.EIO
		}
		src, srcPath = converted, converted.Name()

		fileSize = int64(len(convertedBytes))
		logger.Action("Converted %s (\"%s\") on-the-fly to %s (%d bytes)", h.fileType, h.name, strings.ToUpper(targetFileType), fileSize)
	}

	// 1. Inspect page count
	pageCount := 1
	if targetFileType == "pdf" && fileSize > 0 {
		if _, err := src.Seek(0, io.SeekStart); err == nil {
			dims, pErr := converter.GetPDFPageDims(ctx, src)
			if pErr == nil && len(dims) > 0 {
				pageCount = len(dims)
			} else {
				logger.Warn("Could not inspect PDF page count for \"%s\" (%v), defaulting to 1", h.name, pErr)
			}
		}
	}

	pages := make([]string, pageCount)
	redirectionMap := make([]int, pageCount)
	for i := 0; i < pageCount; i++ {
		pages[i] = uuid.New().String()
		redirectionMap[i] = i
	}

	// One change at a time on the tablet, also across computers (see LockTablet).
	if h.client != nil {
		defer h.client.LockTablet()()
	}

	// 2. Stream upload binary file to tablet over SFTP
	remotePath := path.Join(remarkable.XochitlDirectory, fmt.Sprintf("%s.%s", h.uuid, targetFileType))
	if h.client != nil {
		// Clean up any old annotation drawings folder for this UUID if it existed
		_, _ = h.client.ExecuteCommand(fmt.Sprintf("rm -rf /home/root/.local/share/remarkable/xochitl/%s", h.uuid))

		remoteFile, cErr := h.client.Create(remotePath)
		if cErr != nil {
			logger.Error("Failed to create remote file %s on tablet: %v", remotePath, cErr)
			return syscall.EIO
		}

		if _, sErr := src.Seek(0, io.SeekStart); sErr != nil {
			_ = remoteFile.Close()
			return syscall.EIO
		}

		if _, cpErr := io.Copy(remoteFile, src); cpErr != nil {
			_ = remoteFile.Close()
			logger.Error("Failed to stream upload to tablet for \"%s\": %v", h.name, cpErr)
			return syscall.EIO
		}
		_ = remoteFile.Close()

		// 3. Write metadata file
		now := time.Now()
		nowMs := fmt.Sprintf("%d", now.UnixMilli())
		visibleName := strings.TrimSuffix(h.name, filepath.Ext(h.name))
		metaMap := map[string]interface{}{
			"createdTime":      nowMs,
			"deleted":          false,
			"lastModified":     nowMs,
			"lastOpened":       "0",
			"lastOpenedPage":   0,
			"metadatamodified": false,
			"modified":         false,
			"new":              true,
			"parent":           h.parentUUID,
			"pinned":           false,
			"synced":           true,
			"type":             "DocumentType",
			"version":          1,
			"visibleName":      visibleName,
		}
		metaBytes, _ := json.MarshalIndent(metaMap, "", "    ")
		if wErr := h.client.WriteFile(path.Join(remarkable.XochitlDirectory, h.uuid+".metadata"), metaBytes); wErr != nil {
			logger.Error("Failed to write metadata on tablet for \"%s\": %v", h.name, wErr)
			return syscall.EIO
		}

		// 4. Write content file
		margins := 180
		if targetFileType == "epub" {
			margins = 125
		}
		contentMap := map[string]interface{}{
			"coverPageNumber":    -1,
			"dummyDocument":      false,
			"extraMetadata":      map[string]interface{}{"originalType": h.fileType},
			"fileType":           targetFileType,
			"fontName":           "",
			"formatVersion":      1,
			"lineHeight":         -1,
			"margins":            margins,
			"orientation":        "portrait",
			"originalPageCount":  pageCount,
			"pageCount":          pageCount,
			"pages":              pages,
			"redirectionPageMap": redirectionMap,
			"sizeInBytes":        fmt.Sprintf("%d", fileSize),
			"textAlignment":      "justify",
			"textScale":          1,
		}
		contentBytes, _ := json.MarshalIndent(contentMap, "", "    ")
		if wErr := h.client.WriteFile(path.Join(remarkable.XochitlDirectory, h.uuid+".content"), contentBytes); wErr != nil {
			logger.Error("Failed to write content on tablet for \"%s\": %v", h.name, wErr)
			return syscall.EIO
		}

		// 5. Write pagedata file
		if wErr := h.client.WriteFile(path.Join(remarkable.XochitlDirectory, h.uuid+".pagedata"), []byte("\n")); wErr != nil {
			logger.Error("Failed to write pagedata on tablet for \"%s\": %v", h.name, wErr)
			return syscall.EIO
		}

		// 6. Touch directory and schedule debounced UI refresh
		_, _ = h.client.ExecuteCommand("touch /home/root/.local/share/remarkable/xochitl")
		h.client.ScheduleXochitlRestart()
	}

	// 7. Update in-memory models
	targetExt := "." + targetFileType
	targetName := strings.TrimSuffix(h.name, filepath.Ext(h.name)) + targetExt
	renamed := false

	model.LockTree()
	node := h.fileNode.fsNode
	node.RemotePath = remotePath
	node.Size = fileSize
	node.ModTime = time.Now()
	node.FileType = targetFileType
	node.Pages = pages
	node.HasAnnotations = false
	node.IsNotebook = false
	if (h.fileType == "txt" || h.fileType == "md") && h.dirNode != nil && h.dirNode.fsNode != nil && targetName != h.name {
		if _, exists := h.dirNode.fsNode.Children[targetName]; !exists {
			h.dirNode.fsNode.RemoveChild(node)
			node.Name = targetName
			h.fileNode.virtualPath = path.Join(h.dirNode.virtualPath, targetName)
			h.dirNode.fsNode.AddChild(node)
			renamed = true
		}
	}
	nodePath := node.VirtualPath()
	modTime := node.ModTime
	model.UnlockTree()

	if renamed {
		if chInode := h.dirNode.GetChild(h.name); chInode != nil {
			h.dirNode.AddChild(targetName, chInode, false)
		}
	}

	if h.repo != nil {
		h.repo.RegisterNode(h.uuid, h.fileNode.fsNode)
	}

	// Invalidate any old rendered PDF cache
	cacheMgr := converter.GetCache()
	if cacheMgr != nil {
		cacheMgr.Invalidate(h.uuid)
	}

	// 8. Generate desktop thumbnail if PDF
	if h.syncer != nil && targetFileType == "pdf" {
		filePath := filepath.Join(h.mountPoint, nodePath)
		_ = h.syncer.SyncRenderedPDFThumbnail(filePath, modTime, srcPath, "", nil)
	}

	h.committed = true
	h.dirty = false
	logger.Info("Successfully uploaded \"%s\" (%d pages, %d bytes) to reMarkable", h.name, pageCount, fileSize)
	return 0
}

func (h *UploadFileHandle) cleanupLocked() {
	// Done with this upload: later truncates and opens of the document start afresh (C15).
	if h.fileNode != nil {
		h.fileNode.uploadHandle.CompareAndSwap(h, nil)
	}
	if h.tmpFile != nil {
		_ = h.tmpFile.Close()
		h.tmpFile = nil
	}
	if h.tmpPath != "" {
		_ = os.Remove(h.tmpPath)
		h.tmpPath = ""
	}
}

func isTemporaryUploadName(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, ".") {
		return true
	}
	if strings.HasSuffix(lower, ".tmp") || strings.HasSuffix(lower, ".part") || strings.Contains(lower, "goutputstream") {
		return true
	}
	return false
}

func isSupportedDocumentExtension(name string) (string, bool) {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".pdf") {
		return "pdf", true
	}
	if strings.HasSuffix(lower, ".epub") {
		return "epub", true
	}
	if strings.HasSuffix(lower, ".txt") {
		return "txt", true
	}
	if strings.HasSuffix(lower, ".md") {
		return "md", true
	}
	return "", false
}

// Mounted is a mounted remarkable-fs folder.
type Mounted struct {
	*fuse.Server
	root *DirNode
}

// Mount mounts the document tree at mountPoint. rootNode may still be empty (the tablet is
// asleep): call AttachTree once the repository has loaded it. A stuck mount left by an earlier
// run is released first; another filesystem mounted there is an error.
func Mount(mountPoint string, rootNode *model.FSNode, client *sshclient.Client, repo *remarkable.Repository, maxUploadSize int64, textUploadFormat string) (*Mounted, error) {
	if err := releaseStaleMount(mountPoint); err != nil {
		return nil, err
	}
	if _, err := os.Stat(mountPoint); os.IsNotExist(err) {
		if err := os.MkdirAll(mountPoint, 0755); err != nil {
			return nil, fmt.Errorf("failed to create mount point %s: %w", mountPoint, err)
		}
	}

	if textUploadFormat == "" {
		textUploadFormat = "epub"
	}

	// The cache and thumbnail syncer are optional: without them documents are rendered every time.
	_, _ = converter.InitCache()
	syncer, _ := thumbnail.NewSyncer()

	rootInode := &DirNode{
		fsNode:           rootNode,
		client:           client,
		repo:             repo,
		syncer:           syncer,
		virtualPath:      "/",
		mountPoint:       mountPoint,
		maxUploadSize:    maxUploadSize,
		textUploadFormat: textUploadFormat,
	}

	zeroTimeout := 0 * time.Second
	entryTimeout := 2 * time.Second
	opts := &fs.Options{
		EntryTimeout: &entryTimeout,
		AttrTimeout:  &zeroTimeout,
		MountOptions: fuse.MountOptions{
			FsName:        "remarkable-fs",
			Name:          "remarkable-fs",
			Debug:         false,
			DisableSplice: true,
		},
	}

	server, err := fs.Mount(mountPoint, rootInode, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to mount FUSE filesystem at %s: %w", mountPoint, err)
	}

	logger.Connection("Mounted reMarkable FUSE filesystem at %s", mountPoint)
	return &Mounted{Server: server, root: rootInode}, nil
}

// AttachTree makes the documents the repository has just loaded visible in the mounted folder,
// and starts syncing their thumbnails into the desktop's thumbnail cache.
func (m *Mounted) AttachTree() {
	m.root.attachChildren(context.Background())
	if m.root.client != nil {
		go syncThumbnailsBackground(m.root.fsNode, m.root.client, m.root.mountPoint)
	}
}

var (
	syncUpdatesMu      sync.Mutex
	syncUpdatesRunning bool
)

// triggerBackgroundSync asynchronously updates the tree without blocking FUSE filesystem operations.
func triggerBackgroundSync(repo *remarkable.Repository, client *sshclient.Client, syncer *thumbnail.Syncer, mountPoint string) {
	if repo == nil || client == nil || !client.IsConnected() || !repo.IsLoaded() {
		return
	}

	syncUpdatesMu.Lock()
	if syncUpdatesRunning {
		syncUpdatesMu.Unlock()
		return
	}
	syncUpdatesRunning = true
	syncUpdatesMu.Unlock()

	go func() {
		defer func() {
			syncUpdatesMu.Lock()
			syncUpdatesRunning = false
			syncUpdatesMu.Unlock()
		}()
		syncUpdates(repo, client, syncer, mountPoint, false)
	}()
}

// syncUpdates polls the repository for changes and synchronizes cache invalidation and thumbnails.
func syncUpdates(repo *remarkable.Repository, client *sshclient.Client, syncer *thumbnail.Syncer, mountPoint string, forceScan bool) {
	if repo == nil {
		return
	}
	updatedNodes, err := repo.CheckForChanges(forceScan)
	if err != nil || len(updatedNodes) == 0 {
		return
	}

	cacheMgr := converter.GetCache()
	for _, updated := range updatedNodes {
		model.RLockTree()
		node := infoOf(updated)
		model.RUnlockTree()
		if node.IsDir {
			continue
		}
		// 1. Invalidate render cache for this document so next read renders fresh strokes
		if cacheMgr != nil {
			cacheMgr.Invalidate(node.UUID)
		}

		// 2. Invalidate Freedesktop thumbnail cache
		filePath := filepath.Join(mountPoint, node.path)
		if syncer != nil {
			syncer.InvalidateThumbnail(filePath)

			// 3. Download fresh tablet thumbnail if available
			if node.ThumbnailPath != "" && client != nil {
				pngBytes, rErr := client.ReadFile(node.ThumbnailPath)
				if rErr == nil && len(pngBytes) > 0 {
					_ = syncer.SyncThumbnail(filePath, node.ModTime, pngBytes)
				}
			}
		}
	}
}

// syncThumbnailsBackground syncs document thumbnail PNGs into ~/.cache/thumbnails/
func syncThumbnailsBackground(root *model.FSNode, client *sshclient.Client, mountPoint string) {
	syncer, err := thumbnail.NewSyncer()
	if err != nil {
		return
	}
	cacheMgr := converter.GetCache()

	// The documents are listed under the tree lock, the thumbnails fetched without it.
	type document struct {
		info     nodeInfo
		filePath string
	}
	var documents []document
	var walk func(n *model.FSNode, currentDir string)
	walk = func(n *model.FSNode, currentDir string) {
		if n.IsDir {
			dirPath := filepath.Join(currentDir, n.Name)
			for _, ch := range n.OrderedChildren {
				walk(ch, dirPath)
			}
			return
		}
		documents = append(documents, document{info: infoOf(n), filePath: filepath.Join(currentDir, n.Name)})
	}
	model.RLockTree()
	for _, ch := range root.OrderedChildren {
		walk(ch, mountPoint)
	}
	model.RUnlockTree()

	for _, doc := range documents {
		n, filePath := doc.info, doc.filePath

		// 1. If rendered PDF is already cached, sync thumbnail from the rendered PDF
		if (n.IsNotebook || n.HasAnnotations) && cacheMgr != nil {
			if cachedPDF, hit := cacheMgr.GetRenderedPath(n.UUID, n.ModTime); hit {
				_ = syncer.SyncRenderedPDFThumbnail(filePath, n.ModTime, cachedPDF, n.ThumbnailPath, client)
				continue
			}
		}

		// 2. Otherwise sync tablet's native thumbnail if present
		if n.ThumbnailPath != "" {
			pngBytes, err := client.ReadFile(n.ThumbnailPath)
			if err == nil && len(pngBytes) > 0 {
				_ = syncer.SyncThumbnail(filePath, n.ModTime, pngBytes)
			}
		}
	}
}

// Unmount unmounts the folder. If it is busy (e.g. a document is still open in a viewer), it is
// detached lazily instead: it disappears now and is released once the last file is closed, so
// the program can still exit.
func Unmount(m *Mounted, mountPoint string) error {
	logger.Disconnection("Unmounting FUSE filesystem from %s...", mountPoint)
	err := m.Unmount()
	if err == nil {
		logger.Disconnection("Unmounted FUSE filesystem from %s successfully", mountPoint)
		return nil
	}
	logger.Warn("Could not unmount %s (%s); detaching it lazily instead", mountPoint, strings.Join(strings.Fields(err.Error()), " "))
	if lazyErr := lazyUnmount(mountPoint); lazyErr != nil {
		logger.Error("Failed to unmount %s: %v", mountPoint, lazyErr)
		return lazyErr
	}
	logger.Disconnection("Detached FUSE filesystem from %s; it is released once open files are closed", mountPoint)
	return nil
}
