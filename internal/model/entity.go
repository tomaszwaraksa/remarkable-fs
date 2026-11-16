package model

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// ItemType defines whether a reMarkable entity is a collection (folder) or a document.
type ItemType string

const (
	TypeCollection ItemType = "CollectionType"
	TypeDocument   ItemType = "DocumentType"
)

// FlexibleTimestamp handles both integer timestamp and string timestamp formats in reMarkable metadata.
type FlexibleTimestamp time.Time

func (ft *FlexibleTimestamp) UnmarshalJSON(b []byte) error {
	if string(b) == "null" || string(b) == `""` || len(b) == 0 {
		*ft = FlexibleTimestamp(time.Time{})
		return nil
	}

	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		if s == "" || s == "0" {
			*ft = FlexibleTimestamp(time.Time{})
			return nil
		}
		ms, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			if ms <= 0 {
				*ft = FlexibleTimestamp(time.Time{})
			} else {
				*ft = FlexibleTimestamp(time.UnixMilli(ms))
			}
			return nil
		}
	}

	var n int64
	if err := json.Unmarshal(b, &n); err == nil {
		if n <= 0 {
			*ft = FlexibleTimestamp(time.Time{})
		} else {
			*ft = FlexibleTimestamp(time.UnixMilli(n))
		}
		return nil
	}

	*ft = FlexibleTimestamp(time.Time{})
	return nil
}

func (ft FlexibleTimestamp) Time() time.Time {
	return time.Time(ft)
}

// Metadata represents the contents of a {UUID}.metadata JSON file.
type Metadata struct {
	VisibleName      string            `json:"visibleName"`
	Type             ItemType          `json:"type"`
	Parent           string            `json:"parent"`
	Deleted          bool              `json:"deleted"`
	LastModified     FlexibleTimestamp `json:"lastModified"`
	LastOpened       FlexibleTimestamp `json:"lastOpened"`
	LastOpenedPage   int               `json:"lastOpenedPage"`
	Version          int               `json:"version"`
	Pinned           bool              `json:"pinned"`
	Synced           bool              `json:"synced"`
	MetadataModified bool              `json:"metadatamodified"`
	Modified         bool              `json:"modified"`
}

// Content represents the contents of a {UUID}.content JSON file.
type Content struct {
	FileType    string   `json:"fileType"`
	PageCount   int      `json:"pageCount"`
	Orientation string   `json:"orientation"`
	Pages       []string `json:"pages,omitempty"`
}

// FSNode represents a file or directory node in our in-memory filesystem hierarchy.
type FSNode struct {
	UUID            string
	Name            string
	IsDir           bool
	IsSpecial       bool // Special virtual folder (e.g. Recent)
	Parent          *FSNode
	Children        map[string]*FSNode
	OrderedChildren []*FSNode
	RemotePath      string // Absolute path to backing file on reMarkable (e.g. .pdf or .epub)
	Size            int64
	ModTime         time.Time
	FileType        string // "pdf", "epub", "notebook"
	IsNotebook      bool
	HasAnnotations  bool
	Pages           []string
	ThumbnailPath   string
	Metadata        *Metadata
	Content         *Content
}

// LastOpenedTime returns the last opened timestamp if available.
func (n *FSNode) LastOpenedTime() time.Time {
	if n.Metadata != nil {
		t := n.Metadata.LastOpened.Time()
		if !t.IsZero() && t.Unix() > 0 {
			return t
		}
	}
	return time.Time{}
}

// NewDirectoryNode creates a new directory node.
func NewDirectoryNode(uuid, name string, modTime time.Time, meta *Metadata) *FSNode {
	return &FSNode{
		UUID:     uuid,
		Name:     name,
		IsDir:    true,
		Children: make(map[string]*FSNode),
		ModTime:  modTime,
		Metadata: meta,
	}
}

// NewFileNode creates a new regular file node.
func NewFileNode(uuid, name, remotePath string, size int64, modTime time.Time, fileType string, meta *Metadata, content *Content) *FSNode {
	return &FSNode{
		UUID:       uuid,
		Name:       name,
		IsDir:      false,
		RemotePath: remotePath,
		Size:       size,
		ModTime:    modTime,
		FileType:   fileType,
		Metadata:   meta,
		Content:    content,
	}
}

// ReplaceChildren moves all children of other into n, replacing n's own children, so that
// whoever holds a pointer to n (e.g. the mounted root folder) sees the new contents.
func (n *FSNode) ReplaceChildren(other *FSNode) {
	for _, child := range other.OrderedChildren {
		child.Parent = n
	}
	n.Children = other.Children
	n.OrderedChildren = other.OrderedChildren
	n.ModTime = other.ModTime
}

// AddChild adds a child node, ensuring no duplicate names exist by modifying name if needed.
func (n *FSNode) AddChild(child *FSNode) {
	child.Parent = n
	if n.Children == nil {
		n.Children = make(map[string]*FSNode)
	}

	origName := child.Name
	counter := 1
	for {
		if _, exists := n.Children[child.Name]; !exists {
			break
		}
		// Disambiguate duplicate names
		ext := ""
		base := origName
		if !child.IsDir {
			ext = getExtension(origName)
			base = origName[:len(origName)-len(ext)]
		}
		child.Name = fmt.Sprintf("%s (%d)%s", base, counter, ext)
		counter++
	}

	n.Children[child.Name] = child
	n.OrderedChildren = append(n.OrderedChildren, child)
}

// RemoveChild removes a child node from Children and OrderedChildren. Another node that has
// taken the same name in the meantime is left alone.
func (n *FSNode) RemoveChild(child *FSNode) {
	if n.Children[child.Name] == child {
		delete(n.Children, child.Name)
	}
	for i, c := range n.OrderedChildren {
		if c == child {
			n.OrderedChildren = append(n.OrderedChildren[:i], n.OrderedChildren[i+1:]...)
			break
		}
	}
}

// VirtualPath returns the slash-separated virtual path of this node from the root.
func (n *FSNode) VirtualPath() string {
	if n == nil || n.Parent == nil || n.Parent == n {
		return "/"
	}
	parentPath := n.Parent.VirtualPath()
	if parentPath == "/" {
		return "/" + n.Name
	}
	return parentPath + "/" + n.Name
}

func getExtension(filename string) string {
	for i := len(filename) - 1; i >= 0 && filename[i] != '/'; i-- {
		if filename[i] == '.' {
			return filename[i:]
		}
	}
	return ""
}
