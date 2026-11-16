package converter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/pdf"
)

const (
	// ptToMM converts PostScript points (1/72 inch) to millimeters (25.4 mm / inch).
	ptToMM = 25.4 / 72.0

	// Standard notebook page dimensions in millimeters.
	NotebookWidthMM  = NotebookWidthPt * ptToMM
	NotebookHeightMM = NotebookHeightPt * ptToMM
)

// GetPDFPageDims extracts page dimensions for every page in the given PDF reader.
func GetPDFPageDims(ctx context.Context, rs io.ReadSeeker) ([]types.Dim, error) {
	dims, err := api.PageDims(ctx, rs, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect PDF page dimensions: %w", err)
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("failed to rewind PDF reader: %w", err)
	}
	return dims, nil
}

// SVGsToPDF renders a slice of SVG strings (one per page) into a single multi-page notebook PDF.
func SVGsToPDF(svgPages []string, w io.Writer) error {
	if len(svgPages) == 0 {
		return fmt.Errorf("no pages to render")
	}

	var parsedCanvases []*canvas.Canvas
	for _, svgStr := range svgPages {
		if strings.TrimSpace(svgStr) == "" {
			parsedCanvases = append(parsedCanvases, nil)
			continue
		}
		c, err := canvas.ParseSVG(strings.NewReader(svgStr))
		if err != nil || c.Empty() {
			parsedCanvases = append(parsedCanvases, nil)
			continue
		}
		parsedCanvases = append(parsedCanvases, c)
	}

	firstW_MM := NotebookWidthMM
	firstH_MM := NotebookHeightMM
	if len(parsedCanvases) > 0 && parsedCanvases[0] != nil {
		if parsedCanvases[0].W > 0 {
			firstW_MM = parsedCanvases[0].W * ptToMM
		}
		if parsedCanvases[0].H > 0 {
			firstH_MM = parsedCanvases[0].H * ptToMM
		}
	}

	pdfRenderer := pdf.New(w, firstW_MM, firstH_MM, nil)
	closed := false
	defer func() {
		if !closed {
			_ = pdfRenderer.Close()
		}
	}()

	for i, c := range parsedCanvases {
		pageW_MM := NotebookWidthMM
		pageH_MM := NotebookHeightMM
		if c != nil {
			if c.W > 0 {
				pageW_MM = c.W * ptToMM
			}
			if c.H > 0 {
				pageH_MM = c.H * ptToMM
			}
		}

		if i > 0 {
			pdfRenderer.NewPage(pageW_MM, pageH_MM)
		}

		if c != nil && !c.Empty() {
			// Scale from points (SVG units) to millimeters (PDF renderer units)
			c.RenderViewTo(pdfRenderer, canvas.Identity.Scale(ptToMM, ptToMM))
		}
	}

	closed = true
	return pdfRenderer.Close()
}

// OverlayStrokesOnPDF merges transparent stroke SVGs on top of an existing base PDF.
// dims may be provided if already known, or nil to extract automatically from basePDF.
func OverlayStrokesOnPDF(ctx context.Context, basePDF io.ReadSeeker, strokeSVGs []string, dims []types.Dim, w io.Writer) error {
	// Check if any strokes actually exist
	hasStrokes := false
	for _, svg := range strokeSVGs {
		if strings.TrimSpace(svg) != "" {
			hasStrokes = true
			break
		}
	}

	if !hasStrokes {
		_, err := io.Copy(w, basePDF)
		return err
	}

	if len(dims) == 0 {
		var err error
		dims, err = GetPDFPageDims(ctx, basePDF)
		if err != nil {
			return err
		}
	}

	if len(dims) == 0 {
		return fmt.Errorf("no page dimensions found in base PDF")
	}

	// Generate multi-page transparent stroke overlay PDF matching base PDF page dimensions
	var strokesBuf bytes.Buffer
	firstW_MM := dims[0].Width * ptToMM
	firstH_MM := dims[0].Height * ptToMM
	pdfRenderer := pdf.New(&strokesBuf, firstW_MM, firstH_MM, nil)
	closed := false
	defer func() {
		if !closed {
			_ = pdfRenderer.Close()
		}
	}()

	numPages := len(dims)
	if len(strokeSVGs) > numPages {
		numPages = len(strokeSVGs)
	}

	for i := 0; i < numPages; i++ {
		wPt := dims[0].Width
		hPt := dims[0].Height
		if i < len(dims) {
			wPt = dims[i].Width
			hPt = dims[i].Height
		}
		wMM := wPt * ptToMM
		hMM := hPt * ptToMM

		if i > 0 {
			pdfRenderer.NewPage(wMM, hMM)
		}

		if i < len(strokeSVGs) && strings.TrimSpace(strokeSVGs[i]) != "" {
			c, err := canvas.ParseSVG(strings.NewReader(strokeSVGs[i]))
			if err == nil && !c.Empty() {
				// Scale from points (SVG units) to millimeters (PDF renderer units)
				c.RenderViewTo(pdfRenderer, canvas.Identity.Scale(ptToMM, ptToMM))
			}
		}
	}

	closed = true
	if err := pdfRenderer.Close(); err != nil {
		return fmt.Errorf("failed to generate stroke overlay PDF: %w", err)
	}

	strokesRS := bytes.NewReader(strokesBuf.Bytes())

	// Create multi-page watermark configuration:
	// rot:0 ensures no diagonal rotation is applied by pdfcpu
	// scale:1.0 abs preserves 1:1 coordinate alignment
	// onTop: true places annotations over the underlying PDF
	wm, err := api.PDFMultiWatermarkForReadSeeker(ctx, strokesRS, 1, 1, "pos:c, scale:1.0 abs, rot:0", true, false, types.POINTS)
	if err != nil {
		return fmt.Errorf("failed to configure PDF overlay: %w", err)
	}

	if _, err := basePDF.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("failed to rewind base PDF: %w", err)
	}

	// Apply watermark overlay
	if err := api.AddWatermarks(ctx, basePDF, w, nil, wm, nil); err != nil {
		return fmt.Errorf("failed to stamp vector strokes onto PDF: %w", err)
	}

	return nil
}
