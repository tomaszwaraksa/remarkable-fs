package thumbnail

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"remarkable-fs/internal/logger"
	"remarkable-fs/internal/sshclient"
)

// Syncer syncs reMarkable thumbnail PNGs into the Freedesktop/GNOME thumbnail cache (~/.cache/thumbnails/).
type Syncer struct {
	normalDir  string
	largeDir   string
	xLargeDir  string
	xxLargeDir string
	failDir    string
}

// NewSyncer creates a new thumbnail syncer targeting ~/.cache/thumbnails directories.
func NewSyncer() (*Syncer, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	normalDir := filepath.Join(home, ".cache", "thumbnails", "normal")
	largeDir := filepath.Join(home, ".cache", "thumbnails", "large")
	xLargeDir := filepath.Join(home, ".cache", "thumbnails", "x-large")
	xxLargeDir := filepath.Join(home, ".cache", "thumbnails", "xx-large")
	failDir := filepath.Join(home, ".cache", "thumbnails", "fail")

	_ = os.MkdirAll(normalDir, 0700)
	_ = os.MkdirAll(largeDir, 0700)

	return &Syncer{
		normalDir:  normalDir,
		largeDir:   largeDir,
		xLargeDir:  xLargeDir,
		xxLargeDir: xxLargeDir,
		failDir:    failDir,
	}, nil
}

// InvalidateThumbnail removes any cached thumbnails in all Freedesktop sizes (including fail markers) for the given file path.
func (s *Syncer) InvalidateThumbnail(filePath string) {
	fileURI := "file://" + escapeURIPath(filePath)
	hash := md5Hex(fileURI)
	dirs := []string{
		s.normalDir,
		s.largeDir,
		s.xLargeDir,
		s.xxLargeDir,
		s.failDir,
		filepath.Join(s.failDir, "gnome-thumbnail-factory"),
	}
	for _, dir := range dirs {
		p := filepath.Join(dir, hash+".png")
		_ = os.Remove(p)
	}
}

// SyncThumbnail saves PNG data to the Freedesktop thumbnail cache with standard metadata tags.
func (s *Syncer) SyncThumbnail(filePath string, modTime time.Time, pngData []byte) error {
	if len(pngData) == 0 {
		return nil
	}

	fileURI := "file://" + escapeURIPath(filePath)
	hash := md5Hex(fileURI)

	// Inject Freedesktop metadata (Thumb::URI, Thumb::MTime, Software)
	taggedPNG := injectPNGMetadata(pngData, fileURI, modTime)

	largePath := filepath.Join(s.largeDir, hash+".png")
	normalPath := filepath.Join(s.normalDir, hash+".png")

	if err := os.WriteFile(largePath, taggedPNG, 0600); err != nil {
		return err
	}
	_ = os.WriteFile(normalPath, taggedPNG, 0600)

	logger.Info("Updated desktop thumbnail cache for %s (%s.png)", filepath.Base(filePath), hash)
	return nil
}

// SyncRenderedPDFThumbnail ensures stale desktop thumbnails are purged and updates with fresh rendered PDF or tablet thumbnail.
func (s *Syncer) SyncRenderedPDFThumbnail(filePath string, modTime time.Time, pdfPath string, tabletThumbPath string, client *sshclient.Client) error {
	// Invalidate stale thumbnails first so desktop file managers don't use old cached versions
	s.InvalidateThumbnail(filePath)

	// 1. If pdfPath is provided and pdftoppm is available, render page 1 of the rendered PDF directly
	if pdfPath != "" {
		if pdftoppmPath, err := exec.LookPath("pdftoppm"); err == nil && pdftoppmPath != "" {
			tmpDir, tErr := os.MkdirTemp("", "remarkable-thumb-*")
			if tErr == nil {
				defer os.RemoveAll(tmpDir)
				outPrefix := filepath.Join(tmpDir, "thumb")
				cmd := exec.Command(pdftoppmPath, "-png", "-f", "1", "-l", "1", "-scale-to", "256", pdfPath, outPrefix)
				if err := cmd.Run(); err == nil {
					matches, _ := filepath.Glob(filepath.Join(tmpDir, "thumb-*.png"))
					if len(matches) > 0 {
						pngData, rErr := os.ReadFile(matches[0])
						if rErr == nil && len(pngData) > 0 {
							return s.SyncThumbnail(filePath, modTime, pngData)
						}
					}
				}
			}
		}
	}

	// 2. Fallback to tablet's pre-rendered thumbnail if available
	if tabletThumbPath != "" && client != nil {
		data, err := client.ReadFile(tabletThumbPath)
		if err == nil && len(data) > 0 {
			return s.SyncThumbnail(filePath, modTime, data)
		}
	}

	return nil
}

func makePNGTextChunk(keyword, text string) []byte {
	data := []byte(keyword + "\x00" + text)
	length := uint32(len(data))
	buf := make([]byte, 4+4+len(data)+4)
	binary.BigEndian.PutUint32(buf[0:4], length)
	copy(buf[4:8], []byte("tEXt"))
	copy(buf[8:8+len(data)], data)
	crc := crc32.ChecksumIEEE(buf[4 : 8+len(data)])
	binary.BigEndian.PutUint32(buf[8+len(data):], crc)
	return buf
}

func injectPNGMetadata(pngData []byte, uri string, modTime time.Time) []byte {
	if len(pngData) < 8 || string(pngData[:8]) != "\x89PNG\r\n\x1a\n" {
		return pngData
	}
	iendIdx := bytes.LastIndex(pngData, []byte("IEND"))
	if iendIdx < 4 {
		return pngData
	}
	insertPos := iendIdx - 4 // Before length prefix of IEND chunk

	var chunks []byte
	chunks = append(chunks, makePNGTextChunk("Thumb::URI", uri)...)
	chunks = append(chunks, makePNGTextChunk("Thumb::MTime", fmt.Sprintf("%d", modTime.Unix()))...)
	chunks = append(chunks, makePNGTextChunk("Software", "remarkable-fs")...)

	var out bytes.Buffer
	out.Grow(len(pngData) + len(chunks))
	out.Write(pngData[:insertPos])
	out.Write(chunks)
	out.Write(pngData[insertPos:])
	return out.Bytes()
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func escapeURIPath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
