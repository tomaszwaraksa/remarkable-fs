package converter

import (
	"archive/zip"
	"bytes"
	"fmt"
	"html"
	"time"

	"github.com/google/uuid"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	rendererhtml "github.com/yuin/goldmark/renderer/html"
)

const epubCSS = `@charset "utf-8";
body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
  line-height: 1.55;
  margin: 4% 4%;
  color: #111;
}
h1, h2, h3, h4, h5, h6 {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
  font-weight: bold;
  line-height: 1.25;
  margin-top: 1.4em;
  margin-bottom: 0.5em;
  page-break-after: avoid;
}
h1 {
  font-size: 1.8em;
  border-bottom: 1px solid #ccc;
  padding-bottom: 0.25em;
}
h2 {
  font-size: 1.4em;
  border-bottom: 1px solid #e0e0e0;
  padding-bottom: 0.15em;
}
h3 { font-size: 1.2em; }
h4 { font-size: 1.05em; }
p {
  margin-top: 0;
  margin-bottom: 0.9em;
  text-align: justify;
}
blockquote {
  margin: 1em 0;
  padding-left: 1em;
  border-left: 3px solid #777;
  color: #444;
  font-style: italic;
}
pre, code {
  font-family: "DejaVu Sans Mono", "Courier New", Courier, monospace;
  font-size: 0.9em;
  background-color: #f5f6f8;
}
code {
  padding: 0.15em 0.3em;
  border-radius: 3px;
}
pre {
  padding: 0.8em;
  border: 1px solid #d8d8d8;
  overflow-x: auto;
  white-space: pre-wrap;
  word-wrap: break-word;
  border-radius: 4px;
}
pre code {
  padding: 0;
  background-color: transparent;
}
ul, ol {
  margin-top: 0;
  margin-bottom: 0.9em;
  padding-left: 1.6em;
}
li {
  margin-bottom: 0.35em;
}
table {
  border-collapse: collapse;
  width: 100%;
  margin: 1.2em 0;
}
th, td {
  border: 1px solid #bbb;
  padding: 6px 10px;
  text-align: left;
}
th {
  background-color: #eee;
  font-weight: bold;
}
hr {
  border: 0;
  height: 1px;
  background: #ccc;
  margin: 2em 0;
}
a {
  color: #004499;
  text-decoration: underline;
}
`

// MarkdownToEPUB converts CommonMark/Markdown content into a valid EPUB 3 document suitable for reMarkable.
func MarkdownToEPUB(source []byte, title string) ([]byte, error) {
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM, // Tables, task lists, autolinks, strikethrough
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			rendererhtml.WithXHTML(),
			rendererhtml.WithUnsafe(),
		),
	)

	var htmlBuf bytes.Buffer
	if err := md.Convert(source, &htmlBuf); err != nil {
		return nil, fmt.Errorf("failed to convert markdown to html: %w", err)
	}

	docTitle := title
	if docTitle == "" {
		docTitle = "Document"
	}

	return buildEPUBArchive(docTitle, htmlBuf.String())
}

// TextToEPUB converts plain text content into a clean EPUB 3 document.
func TextToEPUB(content []byte, title string) ([]byte, error) {
	docTitle := title
	if docTitle == "" {
		docTitle = "Document"
	}

	escapedText := html.EscapeString(string(content))
	htmlBody := fmt.Sprintf("<h1>%s</h1>\n<pre class=\"plaintext\">%s</pre>", html.EscapeString(docTitle), escapedText)

	return buildEPUBArchive(docTitle, htmlBody)
}

func buildEPUBArchive(title, htmlBody string) ([]byte, error) {
	docUUID := uuid.New().String()
	modTime := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	cleanTitle := html.EscapeString(title)

	var buf bytes.Buffer
	zipWriter := zip.NewWriter(&buf)

	// 1. mimetype (MUST be first file in ZIP, stored uncompressed)
	mimeHeader := &zip.FileHeader{
		Name:   "mimetype",
		Method: zip.Store,
	}
	mimeWriter, err := zipWriter.CreateHeader(mimeHeader)
	if err != nil {
		return nil, fmt.Errorf("failed to write mimetype: %w", err)
	}
	if _, err := mimeWriter.Write([]byte("application/epub+zip")); err != nil {
		return nil, err
	}

	// 2. META-INF/container.xml
	containerXML := `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`
	if err := writeZipFile(zipWriter, "META-INF/container.xml", []byte(containerXML)); err != nil {
		return nil, err
	}

	// 3. OEBPS/style.css
	if err := writeZipFile(zipWriter, "OEBPS/style.css", []byte(epubCSS)); err != nil {
		return nil, err
	}

	// 4. OEBPS/content.xhtml
	contentXHTML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml">
<head>
  <meta charset="utf-8"/>
  <title>%s</title>
  <link rel="stylesheet" type="text/css" href="style.css"/>
</head>
<body>
%s
</body>
</html>`, cleanTitle, htmlBody)
	if err := writeZipFile(zipWriter, "OEBPS/content.xhtml", []byte(contentXHTML)); err != nil {
		return nil, err
	}

	// 5. OEBPS/nav.xhtml (EPUB 3 nav)
	navXHTML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head>
  <meta charset="utf-8"/>
  <title>%s</title>
  <link rel="stylesheet" type="text/css" href="style.css"/>
</head>
<body>
  <nav epub:type="toc" id="toc">
    <h1>Table of Contents</h1>
    <ol>
      <li><a href="content.xhtml">%s</a></li>
    </ol>
  </nav>
</body>
</html>`, cleanTitle, cleanTitle)
	if err := writeZipFile(zipWriter, "OEBPS/nav.xhtml", []byte(navXHTML)); err != nil {
		return nil, err
	}

	// 6. OEBPS/toc.ncx (EPUB 2 NCX compatibility for e-readers)
	tocNCX := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <head>
    <meta name="dtb:uid" content="urn:uuid:%s"/>
    <meta name="dtb:depth" content="1"/>
    <meta name="dtb:totalPageCount" content="0"/>
    <meta name="dtb:maxPageNumber" content="0"/>
  </head>
  <docTitle>
    <text>%s</text>
  </docTitle>
  <navMap>
    <navPoint id="navpoint-1" playOrder="1">
      <navLabel>
        <text>%s</text>
      </navLabel>
      <content src="content.xhtml"/>
    </navPoint>
  </navMap>
</ncx>`, docUUID, cleanTitle, cleanTitle)
	if err := writeZipFile(zipWriter, "OEBPS/toc.ncx", []byte(tocNCX)); err != nil {
		return nil, err
	}

	// 7. OEBPS/content.opf (Package Manifest)
	contentOPF := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="pub-id">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="pub-id">urn:uuid:%s</dc:identifier>
    <dc:title>%s</dc:title>
    <dc:language>en</dc:language>
    <meta property="dcterms:modified">%s</meta>
  </metadata>
  <manifest>
    <item id="style" href="style.css" media-type="text/css"/>
    <item id="content" href="content.xhtml" media-type="application/xhtml+xml"/>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
  </manifest>
  <spine toc="ncx">
    <itemref idref="content"/>
  </spine>
</package>`, docUUID, cleanTitle, modTime)
	if err := writeZipFile(zipWriter, "OEBPS/content.opf", []byte(contentOPF)); err != nil {
		return nil, err
	}

	if err := zipWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to finalize epub zip: %w", err)
	}

	return buf.Bytes(), nil
}

func writeZipFile(w *zip.Writer, name string, data []byte) error {
	header := &zip.FileHeader{
		Name:     name,
		Method:   zip.Deflate,
		Modified: time.Now().UTC(),
	}
	f, err := w.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return err
}
