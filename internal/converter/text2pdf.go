package converter

import (
	"bytes"
	"fmt"
	"strings"
	"sync"

	"codeberg.org/go-pdf/fpdf"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/goregular"
)

// Standard paper sizes in millimeters.
const (
	A4WidthMM      = 210.0
	A4HeightMM     = 297.0
	A5WidthMM      = 148.0
	A5HeightMM     = 210.0
	LetterWidthMM  = 215.9
	LetterHeightMM = 279.4
	LegalWidthMM   = 215.9
	LegalHeightMM  = 355.6
)

// PDFOptions defines rendering dimensions, margins, and typography for PDF conversion.
type PDFOptions struct {
	PageSize       string  // "remarkable", "a4", "a5", "letter", "legal", "custom"
	Orientation    string  // "portrait" or "landscape"
	PageWidthMM    float64 // Width in millimeters (custom or resolved)
	PageHeightMM   float64 // Height in millimeters (custom or resolved)
	MarginTopMM    float64 // Top margin in mm
	MarginBottomMM float64 // Bottom margin in mm
	MarginLeftMM   float64 // Left margin in mm
	MarginRightMM  float64 // Right margin in mm
	FontSize       float64 // Base body text font size in points
	LineHeight     float64 // Body line height in mm
	CodeFontSize   float64 // Monospace code font size in points
}

// DefaultPDFOptions returns the default PDF rendering settings tailored to reMarkable 2.
func DefaultPDFOptions() PDFOptions {
	return PDFOptions{
		PageSize:       "remarkable",
		Orientation:    "portrait",
		PageWidthMM:    NotebookWidthMM,
		PageHeightMM:   NotebookHeightMM,
		MarginTopMM:    18.0,
		MarginBottomMM: 16.0,
		MarginLeftMM:   16.0,
		MarginRightMM:  16.0,
		FontSize:       10.0,
		LineHeight:     5.2,
		CodeFontSize:   8.5,
	}
}

// ResolvePDFOptions normalizes and validates PDFOptions, filling missing fields with defaults.
func ResolvePDFOptions(opts PDFOptions) PDFOptions {
	resolved := DefaultPDFOptions()

	pageSize := strings.ToLower(strings.TrimSpace(opts.PageSize))
	switch pageSize {
	case "a4":
		resolved.PageSize = "a4"
		resolved.PageWidthMM = A4WidthMM
		resolved.PageHeightMM = A4HeightMM
	case "a5":
		resolved.PageSize = "a5"
		resolved.PageWidthMM = A5WidthMM
		resolved.PageHeightMM = A5HeightMM
	case "letter":
		resolved.PageSize = "letter"
		resolved.PageWidthMM = LetterWidthMM
		resolved.PageHeightMM = LetterHeightMM
	case "legal":
		resolved.PageSize = "legal"
		resolved.PageWidthMM = LegalWidthMM
		resolved.PageHeightMM = LegalHeightMM
	case "custom":
		resolved.PageSize = "custom"
		if opts.PageWidthMM > 0 {
			resolved.PageWidthMM = opts.PageWidthMM
		}
		if opts.PageHeightMM > 0 {
			resolved.PageHeightMM = opts.PageHeightMM
		}
	default:
		resolved.PageSize = "remarkable"
		resolved.PageWidthMM = NotebookWidthMM
		resolved.PageHeightMM = NotebookHeightMM
		if opts.PageWidthMM > 0 && opts.PageHeightMM > 0 {
			resolved.PageSize = "custom"
			resolved.PageWidthMM = opts.PageWidthMM
			resolved.PageHeightMM = opts.PageHeightMM
		}
	}

	// Base dimensions
	baseW := resolved.PageWidthMM
	baseH := resolved.PageHeightMM
	if baseW > baseH {
		baseW, baseH = baseH, baseW
	}

	// Orientation
	orient := strings.ToLower(strings.TrimSpace(opts.Orientation))
	if orient == "landscape" || orient == "l" {
		resolved.Orientation = "landscape"
		resolved.PageWidthMM = baseH
		resolved.PageHeightMM = baseW
	} else {
		resolved.Orientation = "portrait"
		resolved.PageWidthMM = baseW
		resolved.PageHeightMM = baseH
	}

	// Margins
	if opts.MarginTopMM > 0 {
		resolved.MarginTopMM = opts.MarginTopMM
	}
	if opts.MarginBottomMM > 0 {
		resolved.MarginBottomMM = opts.MarginBottomMM
	}
	if opts.MarginLeftMM > 0 {
		resolved.MarginLeftMM = opts.MarginLeftMM
	}
	if opts.MarginRightMM > 0 {
		resolved.MarginRightMM = opts.MarginRightMM
	}

	// Typography
	if opts.FontSize > 0 {
		resolved.FontSize = opts.FontSize
	}
	if opts.LineHeight > 0 {
		resolved.LineHeight = opts.LineHeight
	} else if opts.FontSize > 0 {
		resolved.LineHeight = 5.2 * (opts.FontSize / 10.0)
	}

	if opts.CodeFontSize > 0 {
		resolved.CodeFontSize = opts.CodeFontSize
	} else if opts.FontSize > 0 {
		resolved.CodeFontSize = opts.FontSize * 0.85
	}

	return resolved
}

var (
	pdfOptionsMu  sync.RWMutex
	activePDFOpts = DefaultPDFOptions()
)

// SetPDFOptions sets the global default PDF generation options used by TextToPDF and MarkdownToPDF.
func SetPDFOptions(opts PDFOptions) {
	pdfOptionsMu.Lock()
	defer pdfOptionsMu.Unlock()
	activePDFOpts = ResolvePDFOptions(opts)
}

// GetPDFOptions returns the currently active global default PDF generation options.
func GetPDFOptions() PDFOptions {
	pdfOptionsMu.RLock()
	defer pdfOptionsMu.RUnlock()
	return activePDFOpts
}

// newBasePDF initializes an fpdf.Fpdf instance configured with the specified PDF options.
func newBasePDF(title string, opts PDFOptions) (*fpdf.Fpdf, error) {
	baseW := opts.PageWidthMM
	baseH := opts.PageHeightMM
	if baseW > baseH {
		baseW, baseH = baseH, baseW
	}

	orientationStr := "P"
	if opts.Orientation == "landscape" {
		orientationStr = "L"
	}

	pdf := fpdf.NewCustom(&fpdf.InitType{
		OrientationStr: orientationStr,
		UnitStr:        "mm",
		Size:           fpdf.SizeType{Wd: baseW, Ht: baseH},
	})

	// Add embedded Go Fonts for clean typography and complete UTF-8 Unicode support
	pdf.AddUTF8FontFromBytes("GoSans", "", goregular.TTF)
	pdf.AddUTF8FontFromBytes("GoSans", "B", gobold.TTF)
	pdf.AddUTF8FontFromBytes("GoSans", "I", goitalic.TTF)
	pdf.AddUTF8FontFromBytes("GoMono", "", gomono.TTF)
	pdf.AddUTF8FontFromBytes("GoMono", "B", gomonobold.TTF)

	displayTitle := strings.TrimSpace(title)
	if len(displayTitle) > 50 {
		displayTitle = displayTitle[:47] + "..."
	}

	contentW := opts.PageWidthMM - opts.MarginLeftMM - opts.MarginRightMM

	// Header on pages 2+
	pdf.SetHeaderFunc(func() {
		if pdf.PageNo() > 1 && displayTitle != "" {
			pdf.SetY(opts.MarginTopMM / 2.0)
			pdf.SetFont("GoSans", "", 8)
			pdf.SetTextColor(128, 128, 128)
			pdf.CellFormat(0, 5, displayTitle, "", 0, "L", false, 0, "")

			// Thin divider rule
			pdf.SetDrawColor(220, 220, 220)
			pdf.SetLineWidth(0.2)
			lineY := (opts.MarginTopMM / 2.0) + 6.0
			pdf.Line(opts.MarginLeftMM, lineY, opts.MarginLeftMM+contentW, lineY)
			pdf.SetY(opts.MarginTopMM)
		}
	})

	// Footer on all pages: centered page number
	pdf.SetFooterFunc(func() {
		pdf.SetY(-opts.MarginBottomMM * 0.7)
		pdf.SetFont("GoSans", "", 8)
		pdf.SetTextColor(140, 140, 140)
		pdf.CellFormat(0, 6, fmt.Sprintf("%d", pdf.PageNo()), "", 0, "C", false, 0, "")
	})

	pdf.SetMargins(opts.MarginLeftMM, opts.MarginTopMM, opts.MarginRightMM)
	pdf.SetAutoPageBreak(true, opts.MarginBottomMM)

	return pdf, nil
}

// TextToPDF converts plain text into a multi-page, formatted PDF using active default options.
func TextToPDF(content []byte, title string) ([]byte, error) {
	return TextToPDFWithOptions(content, title, GetPDFOptions())
}

// TextToPDFWithOptions converts plain text into a formatted PDF using custom PDF options.
func TextToPDFWithOptions(content []byte, title string, opts PDFOptions) ([]byte, error) {
	opts = ResolvePDFOptions(opts)
	pdf, err := newBasePDF(title, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize PDF generator: %w", err)
	}

	pdf.AddPage()

	contentW := opts.PageWidthMM - opts.MarginLeftMM - opts.MarginRightMM

	// Document Title Header on Page 1
	if strings.TrimSpace(title) != "" {
		pdf.SetFont("GoSans", "B", opts.FontSize*1.5)
		pdf.SetTextColor(25, 25, 25)
		pdf.CellFormat(0, opts.LineHeight*1.5, title, "", 1, "L", false, 0, "")

		pdf.SetDrawColor(200, 200, 200)
		pdf.SetLineWidth(0.3)
		pdf.Line(opts.MarginLeftMM, pdf.GetY()+1, opts.MarginLeftMM+contentW, pdf.GetY()+1)
		pdf.Ln(opts.LineHeight * 1.1)
	}

	pdf.SetFont("GoMono", "", opts.CodeFontSize)
	pdf.SetTextColor(35, 35, 35)

	textStr := string(content)
	textStr = strings.ReplaceAll(textStr, "\r\n", "\n")
	textStr = strings.ReplaceAll(textStr, "\r", "\n")
	lines := strings.Split(textStr, "\n")

	lineHeight := opts.CodeFontSize * 0.52
	for _, rawLine := range lines {
		// Expand tabs to 4 spaces for consistent column alignment
		line := strings.ReplaceAll(rawLine, "\t", "    ")
		if line == "" {
			pdf.Ln(lineHeight * 0.7)
			continue
		}
		pdf.MultiCell(0, lineHeight, line, "", "L", false)
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("failed to serialize text PDF: %w", err)
	}
	return buf.Bytes(), nil
}

// MarkdownToPDF converts a CommonMark/Markdown document into a styled PDF using active default options.
func MarkdownToPDF(source []byte, title string) ([]byte, error) {
	return MarkdownToPDFWithOptions(source, title, GetPDFOptions())
}

// MarkdownToPDFWithOptions converts a Markdown document into a styled PDF using custom PDF options.
func MarkdownToPDFWithOptions(source []byte, title string, opts PDFOptions) ([]byte, error) {
	opts = ResolvePDFOptions(opts)
	md := goldmark.New()
	doc := md.Parser().Parse(text.NewReader(source))

	// If no explicit title given, attempt to extract first H1 heading
	docTitle := title
	if docTitle == "" || strings.HasSuffix(strings.ToLower(docTitle), ".md") {
		if firstH1 := findFirstH1(doc, source); firstH1 != "" {
			docTitle = firstH1
		}
	}

	pdf, err := newBasePDF(docTitle, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize PDF generator: %w", err)
	}

	pdf.AddPage()

	renderer := &mdRenderer{
		pdf:       pdf,
		source:    source,
		opts:      opts,
		pageW:     opts.PageWidthMM,
		pageH:     opts.PageHeightMM,
		contentW:  opts.PageWidthMM - opts.MarginLeftMM - opts.MarginRightMM,
		leftBase:  opts.MarginLeftMM,
		rightBase: opts.MarginRightMM,
	}

	if err := renderer.renderNode(doc); err != nil {
		return nil, fmt.Errorf("failed to render markdown elements: %w", err)
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("failed to serialize markdown PDF: %w", err)
	}
	return buf.Bytes(), nil
}

func findFirstH1(doc ast.Node, source []byte) string {
	for child := doc.FirstChild(); child != nil; child = child.NextSibling() {
		if heading, ok := child.(*ast.Heading); ok && heading.Level == 1 {
			var sb strings.Builder
			for n := heading.FirstChild(); n != nil; n = n.NextSibling() {
				if t, ok := n.(*ast.Text); ok {
					sb.Write(t.Segment.Value(source))
				}
			}
			return strings.TrimSpace(sb.String())
		}
	}
	return ""
}

type mdRenderer struct {
	pdf       *fpdf.Fpdf
	source    []byte
	opts      PDFOptions
	pageW     float64
	pageH     float64
	contentW  float64
	leftBase  float64
	rightBase float64
	listLevel int
	quoteLvl  int
}

func (r *mdRenderer) checkPageBreak(neededHeight float64) {
	remaining := r.pageH - r.opts.MarginBottomMM - r.pdf.GetY()
	if remaining < neededHeight {
		r.pdf.AddPage()
	}
}

func (r *mdRenderer) renderNode(n ast.Node) error {
	switch node := n.(type) {
	case *ast.Document:
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			if err := r.renderNode(child); err != nil {
				return err
			}
		}

	case *ast.Heading:
		r.renderHeading(node)

	case *ast.Paragraph:
		r.renderParagraph(node)

	case *ast.FencedCodeBlock:
		r.renderFencedCodeBlock(node)

	case *ast.CodeBlock:
		r.renderCodeBlock(node)

	case *ast.List:
		r.renderList(node)

	case *ast.ListItem:
		r.renderListItem(node)

	case *ast.Blockquote:
		r.renderBlockquote(node)

	case *ast.ThematicBreak:
		r.renderThematicBreak()

	case *ast.TextBlock:
		r.renderInlines(node, r.opts.FontSize, r.opts.LineHeight, "")

	default:
		// Fallback: render any children
		for child := n.FirstChild(); child != nil; child = child.NextSibling() {
			if err := r.renderNode(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *mdRenderer) renderHeading(h *ast.Heading) {
	baseSize := r.opts.FontSize
	baseLineH := r.opts.LineHeight

	var fontSize float64
	var lineH float64
	var spaceBefore float64
	var spaceAfter float64

	switch h.Level {
	case 1:
		fontSize = baseSize * 1.7
		lineH = baseLineH * 1.44
		spaceBefore = 6.0
		spaceAfter = 3.5
	case 2:
		fontSize = baseSize * 1.4
		lineH = baseLineH * 1.25
		spaceBefore = 5.0
		spaceAfter = 3.0
	case 3:
		fontSize = baseSize * 1.2
		lineH = baseLineH * 1.06
		spaceBefore = 4.0
		spaceAfter = 2.5
	default:
		fontSize = baseSize * 1.05
		lineH = baseLineH * 0.96
		spaceBefore = 3.0
		spaceAfter = 2.0
	}

	// Prevent orphan headers near bottom of page
	r.checkPageBreak(lineH + spaceBefore + spaceAfter + 10.0)

	if r.pdf.GetY() > r.opts.MarginTopMM+2 {
		r.pdf.Ln(spaceBefore)
	}

	r.pdf.SetFont("GoSans", "B", fontSize)
	r.pdf.SetTextColor(20, 20, 20)

	r.renderInlines(h, fontSize, lineH, "B")
	r.pdf.Ln(lineH)

	// Add subtle horizontal underline rule under H1
	if h.Level == 1 {
		currY := r.pdf.GetY() + 0.5
		r.pdf.SetDrawColor(215, 215, 215)
		r.pdf.SetLineWidth(0.3)
		r.pdf.Line(r.leftBase, currY, r.leftBase+r.contentW, currY)
		r.pdf.SetY(currY + 1.0)
	}

	r.pdf.Ln(spaceAfter)
}

func (r *mdRenderer) renderParagraph(p *ast.Paragraph) {
	r.checkPageBreak(r.opts.LineHeight * 2.0)

	fontSize := r.opts.FontSize
	lineH := r.opts.LineHeight

	r.pdf.SetFont("GoSans", "", fontSize)
	r.pdf.SetTextColor(30, 30, 30)

	r.renderInlines(p, fontSize, lineH, "")
	r.pdf.Ln(lineH + 2.0)
}

func (r *mdRenderer) renderInlines(parent ast.Node, baseSize, lineH float64, baseStyle string) {
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		r.renderInline(child, baseSize, lineH, baseStyle)
	}
}

func (r *mdRenderer) renderInline(n ast.Node, baseSize, lineH float64, currentStyle string) {
	switch node := n.(type) {
	case *ast.Text:
		val := string(node.Segment.Value(r.source))
		r.pdf.Write(lineH, val)
		if node.HardLineBreak() {
			r.pdf.Ln(lineH)
		} else if node.SoftLineBreak() {
			r.pdf.Write(lineH, " ")
		}

	case *ast.String:
		r.pdf.Write(lineH, string(node.Value))

	case *ast.Emphasis:
		style := currentStyle
		if node.Level == 2 {
			// Bold
			if strings.Contains(style, "I") {
				style = "BI"
			} else {
				style = "B"
			}
		} else {
			// Italic
			if strings.Contains(style, "B") {
				style = "BI"
			} else {
				style = "I"
			}
		}
		r.pdf.SetFont("GoSans", style, baseSize)
		r.renderInlines(node, baseSize, lineH, style)
		// Restore previous style
		r.pdf.SetFont("GoSans", currentStyle, baseSize)

	case *ast.CodeSpan:
		// Inline code snippet
		codeVal := ""
		for c := node.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*ast.Text); ok {
				codeVal += string(t.Segment.Value(r.source))
			}
		}
		r.pdf.SetFont("GoMono", "", baseSize*0.92)
		r.pdf.SetTextColor(60, 60, 60)
		r.pdf.Write(lineH, codeVal)
		// Restore
		r.pdf.SetFont("GoSans", currentStyle, baseSize)
		r.pdf.SetTextColor(30, 30, 30)

	case *ast.Link:
		r.pdf.SetTextColor(0, 70, 160)
		r.renderInlines(node, baseSize, lineH, currentStyle)
		r.pdf.SetTextColor(30, 30, 30)

	case *ast.AutoLink:
		url := string(node.URL(r.source))
		r.pdf.SetTextColor(0, 70, 160)
		r.pdf.Write(lineH, url)
		r.pdf.SetTextColor(30, 30, 30)

	default:
		// Recurse children
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			r.renderInline(c, baseSize, lineH, currentStyle)
		}
	}
}

func (r *mdRenderer) renderFencedCodeBlock(fcb *ast.FencedCodeBlock) {
	lines := fcb.Lines()
	var codeLines []string
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		codeLines = append(codeLines, string(seg.Value(r.source)))
	}
	r.renderCodeLines(codeLines)
}

func (r *mdRenderer) renderCodeBlock(cb *ast.CodeBlock) {
	lines := cb.Lines()
	var codeLines []string
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		codeLines = append(codeLines, string(seg.Value(r.source)))
	}
	r.renderCodeLines(codeLines)
}

func (r *mdRenderer) renderCodeLines(codeLines []string) {
	if len(codeLines) == 0 {
		return
	}

	fontSize := r.opts.CodeFontSize
	lineH := fontSize * 0.51
	blockH := float64(len(codeLines))*lineH + 4.0

	// If block fits on remaining page, keep together. Otherwise, page break if near bottom.
	if blockH <= 120.0 {
		r.checkPageBreak(blockH)
	} else {
		r.checkPageBreak(20.0)
	}

	boxW := r.contentW - (float64(r.listLevel) * 6.0)
	indentX := r.leftBase + (float64(r.listLevel) * 6.0)

	r.pdf.SetFont("GoMono", "", fontSize)
	r.pdf.SetTextColor(35, 35, 35)

	// Top padding
	r.pdf.Ln(1.5)

	for _, rawLine := range codeLines {
		line := strings.TrimRight(rawLine, "\r\n")
		line = strings.ReplaceAll(line, "\t", "    ")

		// Draw shaded background box line
		r.pdf.SetX(indentX)
		r.pdf.SetFillColor(246, 248, 250)
		r.pdf.SetDrawColor(228, 232, 237)
		r.pdf.SetLineWidth(0.15)

		// Border left and right, fill background
		r.pdf.CellFormat(boxW, lineH, "  "+line, "LR", 1, "L", true, 0, "")
	}

	// Bottom border cap
	r.pdf.SetX(indentX)
	r.pdf.CellFormat(boxW, 1.5, "", "LRB", 1, "L", true, 0, "")
	r.pdf.Ln(3.0)
}

func (r *mdRenderer) renderList(list *ast.List) {
	r.listLevel++
	itemIdx := list.Start
	if itemIdx <= 0 {
		itemIdx = 1
	}

	for child := list.FirstChild(); child != nil; child = child.NextSibling() {
		if li, ok := child.(*ast.ListItem); ok {
			r.renderListItemWithPrefix(li, list.IsOrdered(), itemIdx)
			itemIdx++
		}
	}
	r.listLevel--
	r.pdf.Ln(2.0)
}

func (r *mdRenderer) renderListItem(li *ast.ListItem) {
	r.renderListItemWithPrefix(li, false, 1)
}

func (r *mdRenderer) renderListItemWithPrefix(li *ast.ListItem, isOrdered bool, index int) {
	fontSize := r.opts.FontSize
	lineH := r.opts.LineHeight

	r.checkPageBreak(lineH * 1.5)

	indentMM := float64(r.listLevel) * 5.0
	itemLeft := r.leftBase + indentMM

	var prefix string
	if isOrdered {
		prefix = fmt.Sprintf("%d. ", index)
	} else {
		prefix = "•  "
	}

	r.pdf.SetFont("GoSans", "B", fontSize)
	prefixW := r.pdf.GetStringWidth(prefix) + 1.0

	// Set left margin to the indented text position so wrapped lines align nicely
	r.pdf.SetLeftMargin(itemLeft + prefixW)
	r.pdf.SetX(itemLeft)

	// Draw marker
	r.pdf.SetTextColor(60, 60, 60)
	r.pdf.Write(lineH, prefix)

	// Draw content
	r.pdf.SetFont("GoSans", "", fontSize)
	r.pdf.SetTextColor(30, 30, 30)

	for child := li.FirstChild(); child != nil; child = child.NextSibling() {
		if err := r.renderNode(child); err != nil {
			break
		}
	}

	// Always ensure we end with a line break to the next line
	r.pdf.Ln(lineH)

	// Restore original margin
	r.pdf.SetLeftMargin(r.leftBase)
}

func (r *mdRenderer) renderBlockquote(bq *ast.Blockquote) {
	r.checkPageBreak(r.opts.LineHeight * 3.0)

	r.quoteLvl++
	quoteIndent := r.leftBase + float64(r.quoteLvl)*5.0
	r.pdf.SetLeftMargin(quoteIndent)

	startY := r.pdf.GetY()

	fontSize := r.opts.FontSize
	// Render inner blockquote children in Italic
	r.pdf.SetFont("GoSans", "I", fontSize)
	r.pdf.SetTextColor(55, 55, 55)

	for child := bq.FirstChild(); child != nil; child = child.NextSibling() {
		_ = r.renderNode(child)
	}

	endY := r.pdf.GetY()
	if endY > startY {
		// Draw vertical bar on left of quote
		r.pdf.SetDrawColor(180, 180, 180)
		r.pdf.SetLineWidth(0.8)
		r.pdf.Line(quoteIndent-2.5, startY, quoteIndent-2.5, endY-1.0)
	}

	r.quoteLvl--
	r.pdf.SetLeftMargin(r.leftBase)
	r.pdf.Ln(2.0)
}

func (r *mdRenderer) renderThematicBreak() {
	r.checkPageBreak(8.0)
	r.pdf.Ln(3.0)

	currY := r.pdf.GetY()
	r.pdf.SetDrawColor(210, 210, 210)
	r.pdf.SetLineWidth(0.3)
	r.pdf.Line(r.leftBase, currY, r.leftBase+r.contentW, currY)

	r.pdf.SetY(currY + 4.0)
}
