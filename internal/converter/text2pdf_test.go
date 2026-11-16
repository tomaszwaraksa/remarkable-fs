package converter

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestTextToPDF(t *testing.T) {
	sampleText := `reMarkable 2 Plain Text Document
================================
This is a plain text file converted on the fly to vector PDF.

Features:
	- Tab indentation support (4 spaces)
	- UTF-8 characters: Zażółć gęślą jaźń, München, résumé, — em-dash
	- Fixed-width font (Go Mono) for clean tabular alignment

Column A    Column B    Column C
100         200         300
Alpha       Beta        Gamma
`
	pdfBytes, err := TextToPDF([]byte(sampleText), "Sample Document")
	if err != nil {
		t.Fatalf("TextToPDF failed: %v", err)
	}

	if len(pdfBytes) == 0 {
		t.Fatalf("TextToPDF returned empty byte slice")
	}

	dims, err := GetPDFPageDims(context.Background(), bytes.NewReader(pdfBytes))
	if err != nil {
		t.Fatalf("Failed to inspect generated PDF page dimensions: %v", err)
	}

	if len(dims) != 1 {
		t.Fatalf("Expected 1 page, got %d", len(dims))
	}

	if dims[0].Width <= 0 || dims[0].Height <= 0 {
		t.Fatalf("Invalid page dimensions: %+v", dims[0])
	}

	t.Logf("Generated Text PDF size: %d bytes, %d pages (dims: %.2fx%.2f pt)",
		len(pdfBytes), len(dims), dims[0].Width, dims[0].Height)
}

func TestTextToPDFMultiPage(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 200; i++ {
		sb.WriteString(strings.Repeat("Line of text for testing multi-page text rendering. ", 3))
		sb.WriteString("\n")
	}

	pdfBytes, err := TextToPDF([]byte(sb.String()), "Long Text File")
	if err != nil {
		t.Fatalf("TextToPDF multi-page failed: %v", err)
	}

	dims, err := GetPDFPageDims(context.Background(), bytes.NewReader(pdfBytes))
	if err != nil {
		t.Fatalf("Failed to inspect multi-page PDF dimensions: %v", err)
	}

	if len(dims) < 2 {
		t.Fatalf("Expected at least 2 pages for 200 lines, got %d", len(dims))
	}

	t.Logf("Generated Multi-page Text PDF: %d pages, %d bytes", len(dims), len(pdfBytes))
}

func TestMarkdownToPDF(t *testing.T) {
	mdContent := `# Project Architecture

Welcome to **remarkable-fs**! This document tests *Markdown* rendering.

Here is some ` + "`inline_code()`" + ` and a [link](https://github.com).

## Key Features

- Pure Go implementation: **zero** external CLI tools
- Clean typography using *Go Fonts*
- Supported Markdown elements:
  - Nested lists
  - Code blocks
  - Blockquotes

### Code Example

` + "```go\npackage main\n\nimport \"fmt\"\n\nfunc main() {\n    fmt.Println(\"Hello reMarkable!\")\n}\n```" + `

### Quotes and Rules

> Knowledge is power.
> Annotate cleanly with stylus.

---

That concludes our Markdown test document with Unicode: Zażółć gęślą jaźń & ä ö ü.
`

	pdfBytes, err := MarkdownToPDF([]byte(mdContent), "Project Architecture.md")
	if err != nil {
		t.Fatalf("MarkdownToPDF failed: %v", err)
	}

	if len(pdfBytes) == 0 {
		t.Fatalf("MarkdownToPDF returned empty byte slice")
	}

	dims, err := GetPDFPageDims(context.Background(), bytes.NewReader(pdfBytes))
	if err != nil {
		t.Fatalf("Failed to inspect generated Markdown PDF: %v", err)
	}

	if len(dims) != 1 {
		t.Fatalf("Expected 1 page, got %d", len(dims))
	}

	t.Logf("Generated Markdown PDF size: %d bytes, %d pages", len(pdfBytes), len(dims))
}

func TestMarkdownToPDFMultiPage(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("# Long Markdown Document\n\n")
	for i := 1; i <= 30; i++ {
		sb.WriteString("## Section ")
		sb.WriteString(strings.Repeat("Title ", 2))
		sb.WriteString("\n\n")
		sb.WriteString("Paragraph with **bold**, *italic*, and `code` describing feature section ")
		sb.WriteString("\n\n")
		sb.WriteString("```\nfunc block() {\n    // code\n}\n```\n\n")
		sb.WriteString("- Item 1\n- Item 2\n- Item 3\n\n")
		sb.WriteString("> Quote for section\n\n---\n\n")
	}

	pdfBytes, err := MarkdownToPDF([]byte(sb.String()), "Long Markdown")
	if err != nil {
		t.Fatalf("MarkdownToPDF multi-page failed: %v", err)
	}

	dims, err := GetPDFPageDims(context.Background(), bytes.NewReader(pdfBytes))
	if err != nil {
		t.Fatalf("Failed to inspect multi-page Markdown PDF: %v", err)
	}

	if len(dims) < 3 {
		t.Fatalf("Expected at least 3 pages, got %d", len(dims))
	}

	t.Logf("Generated Multi-page Markdown PDF: %d pages, %d bytes", len(dims), len(pdfBytes))
}

func TestTightOrderedListRendering(t *testing.T) {
	mdContent := `# Render Pipeline
1. create the Konva stage in the container element, at the size of the floor (floor.canvasDimensions),
2. create the base layer, then build the equipment hierarchy and the plan cache (refreshEquipmentHierarchy()),
3. create the other layers with createLayer() (src/renderer/layers/create-layer.js). Each layer takes the items of the floor which belong to it (floor.getItemsOn(layerId)), skips those which don't belong on this view (canRenderOnTheFloor()) and creates a shape for each,
4. settle the zoom (resolveZoom()) before anything is drawn, so that equipment which counter-scales with the zoom comes out at its final size at once,
5. render all element shapes, then the connectors (refreshConnectorLengths()). Connectors come last because they are drawn between the shapes they connect, and their lengths can only be measured once those shapes are in place. Changed lengths are saved a second later,
6. render each layer and attach it to the stage, bind the events, size the container to the zoom,
7. on a floor, redraw the scanned areas if anything which shapes them has changed since the plan was last opened (recreateScannedAreas()).
`
	pdfBytes, err := MarkdownToPDF([]byte(mdContent), "Pipeline Test")
	if err != nil {
		t.Fatalf("MarkdownToPDF failed on tight ordered list: %v", err)
	}

	dims, err := GetPDFPageDims(context.Background(), bytes.NewReader(pdfBytes))
	if err != nil {
		t.Fatalf("Failed to inspect generated PDF page dimensions: %v", err)
	}

	if len(dims) == 0 {
		t.Fatalf("Expected at least 1 page, got 0")
	}
}

func TestPDFOptionsConfigurability(t *testing.T) {
	// 1. Test Default Options
	def := DefaultPDFOptions()
	if def.PageSize != "remarkable" || def.Orientation != "portrait" {
		t.Fatalf("Unexpected default PDF options: %+v", def)
	}

	// 2. Test A4 Landscape Resolution
	a4Land := ResolvePDFOptions(PDFOptions{
		PageSize:    "a4",
		Orientation: "landscape",
	})
	if a4Land.PageSize != "a4" || a4Land.Orientation != "landscape" {
		t.Fatalf("Failed to resolve A4 landscape: %+v", a4Land)
	}
	if a4Land.PageWidthMM != 297.0 || a4Land.PageHeightMM != 210.0 {
		t.Fatalf("Expected A4 landscape (297x210 mm), got %.2fx%.2f mm", a4Land.PageWidthMM, a4Land.PageHeightMM)
	}

	// 3. Test Custom dimensions and margins
	custom := ResolvePDFOptions(PDFOptions{
		PageSize:       "custom",
		PageWidthMM:    120.0,
		PageHeightMM:   180.0,
		MarginTopMM:    10.0,
		MarginBottomMM: 10.0,
		MarginLeftMM:   12.0,
		MarginRightMM:  12.0,
		FontSize:       12.0,
	})
	if custom.PageWidthMM != 120.0 || custom.PageHeightMM != 180.0 {
		t.Fatalf("Failed custom dimensions: %+v", custom)
	}
	if custom.MarginLeftMM != 12.0 || custom.FontSize != 12.0 {
		t.Fatalf("Failed custom margins/font: %+v", custom)
	}

	// 4. Test Rendering with custom options
	mdContent := "# Custom Header\n\nTesting PDF generation with A4 landscape options."
	pdfBytes, err := MarkdownToPDFWithOptions([]byte(mdContent), "Custom Config", a4Land)
	if err != nil {
		t.Fatalf("MarkdownToPDFWithOptions failed: %v", err)
	}

	dims, err := GetPDFPageDims(context.Background(), bytes.NewReader(pdfBytes))
	if err != nil {
		t.Fatalf("Failed to get PDF dims: %v", err)
	}
	if len(dims) != 1 {
		t.Fatalf("Expected 1 page, got %d", len(dims))
	}

	// A4 in points is 841.89 x 595.28 pt
	if dims[0].Width <= dims[0].Height {
		t.Fatalf("Expected landscape dimensions (width > height), got %.2fx%.2f", dims[0].Width, dims[0].Height)
	}
}
