package converter

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/alexgorbatchev/go-rmscene"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/pdf"
)

func TestStrokeOverlayPipeline(t *testing.T) {
	basePDFPath := testDataPath(t, "base.pdf")
	strokeRMPath := testDataPath(t, "stroke.rm")

	baseBytes, err := os.ReadFile(basePDFPath)
	if err != nil {
		t.Skipf("base.pdf not found: %v", err)
	}
	strokeBytes, err := os.ReadFile(strokeRMPath)
	if err != nil {
		t.Skipf("stroke.rm not found: %v", err)
	}

	ctx := context.Background()
	dims, err := api.PageDims(ctx, bytes.NewReader(baseBytes), nil)
	if err != nil {
		t.Fatalf("api.PageDims failed: %v", err)
	}

	t.Logf("Base PDF has %d pages. Page 1: width=%.2f pt, height=%.2f pt", len(dims), dims[0].Width, dims[0].Height)

	pageW_Pt := dims[0].Width
	pageH_Pt := dims[0].Height

	// Render .rm to SVG with exact page dimensions
	svgStr, err := rmscene.RenderRMToSVG(strokeBytes, rmscene.WithDimensions(pageW_Pt, pageH_Pt))
	if err != nil {
		t.Fatalf("RenderRMToSVG failed: %v", err)
	}

	// Test canvas parsing
	c, err := canvas.ParseSVG(strings.NewReader(svgStr))
	if err != nil {
		t.Fatalf("canvas.ParseSVG failed: %v", err)
	}
	t.Logf("Canvas parsed dimensions: c.W=%.2f, c.H=%.2f", c.W, c.H)
	t.Logf("Canvas Empty: %v, Bounds: %+v", c.Empty(), c.Bounds())

	pageW_MM := pageW_Pt * 25.4 / 72.0
	pageH_MM := pageH_Pt * 25.4 / 72.0
	t.Logf("Expected page in MM: %.2f x %.2f mm", pageW_MM, pageH_MM)

	var strokePDFBuf bytes.Buffer
	pdfRenderer := pdf.New(&strokePDFBuf, pageW_MM, pageH_MM, nil)
	// Scale from points (SVG units parsed by canvas) to millimeters (PDF renderer units)
	c.RenderViewTo(pdfRenderer, canvas.Identity.Scale(25.4/72.0, 25.4/72.0))
	if err := pdfRenderer.Close(); err != nil {
		t.Fatalf("pdfRenderer.Close failed: %v", err)
	}

	strokePDFBytes := strokePDFBuf.Bytes()
	strokeDims, err := api.PageDims(ctx, bytes.NewReader(strokePDFBytes), nil)
	if err != nil {
		t.Fatalf("Failed to get stroke PDF dims: %v", err)
	}
	t.Logf("Stroke PDF page 1 dims: width=%.2f pt, height=%.2f pt", strokeDims[0].Width, strokeDims[0].Height)

	// Save stroke PDF for inspection
	_ = os.WriteFile(testDataPath(t, "test_stroke_pipeline.pdf"), strokePDFBytes, 0644)

	// Now watermark onto base PDF with rot:0
	wm, err := api.PDFMultiWatermarkForReadSeeker(ctx, bytes.NewReader(strokePDFBytes), 1, 1, "pos:c, scale:1.0 abs, rot:0", true, false, types.POINTS)
	if err != nil {
		t.Fatalf("PDFMultiWatermarkForReadSeeker failed: %v", err)
	}

	var outBuf bytes.Buffer
	if err := api.AddWatermarks(ctx, bytes.NewReader(baseBytes), &outBuf, nil, wm, nil); err != nil {
		t.Fatalf("AddWatermarks failed: %v", err)
	}

	outPath := testDataPath(t, "test_pipeline_merged.pdf")
	if err := os.WriteFile(outPath, outBuf.Bytes(), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	t.Logf("Wrote merged PDF to %s (size %d bytes)", outPath, outBuf.Len())
}

func TestNotebookSVGsToPDF(t *testing.T) {
	strokeRMPath := testDataPath(t, "stroke.rm")
	strokeBytes, err := os.ReadFile(strokeRMPath)
	if err != nil {
		t.Skipf("stroke.rm not found: %v", err)
	}

	svgStr, err := RenderRMToSVG(strokeBytes, NotebookWidthPt, NotebookHeightPt)
	if err != nil {
		t.Fatalf("RenderRMToSVG failed: %v", err)
	}

	var pdfBuf bytes.Buffer
	if err := SVGsToPDF([]string{svgStr}, &pdfBuf); err != nil {
		t.Fatalf("SVGsToPDF failed: %v", err)
	}

	outPath := testDataPath(t, "test_notebook.pdf")
	if err := os.WriteFile(outPath, pdfBuf.Bytes(), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	t.Logf("Wrote notebook PDF to %s (size %d bytes)", outPath, pdfBuf.Len())
}

func TestOverlayStrokesOnPDF(t *testing.T) {
	basePDFPath := testDataPath(t, "base.pdf")
	strokeRMPath := testDataPath(t, "stroke.rm")

	baseBytes, err := os.ReadFile(basePDFPath)
	if err != nil {
		t.Skipf("base.pdf not found: %v", err)
	}
	strokeBytes, err := os.ReadFile(strokeRMPath)
	if err != nil {
		t.Skipf("stroke.rm not found: %v", err)
	}

	ctx := context.Background()
	dims, err := GetPDFPageDims(ctx, bytes.NewReader(baseBytes))
	if err != nil {
		t.Fatalf("GetPDFPageDims failed: %v", err)
	}

	svgStr, err := RenderRMToSVG(strokeBytes, dims[0].Width, dims[0].Height)
	if err != nil {
		t.Fatalf("RenderRMToSVG failed: %v", err)
	}

	var outBuf bytes.Buffer
	err = OverlayStrokesOnPDF(ctx, bytes.NewReader(baseBytes), []string{svgStr}, dims, &outBuf)
	if err != nil {
		t.Fatalf("OverlayStrokesOnPDF failed: %v", err)
	}

	if outBuf.Len() == 0 {
		t.Fatalf("Expected non-empty output")
	}
}

func TestInspectTodoPage1(t *testing.T) {
	strokeRMPath := testDataPath(t, "todo_page1.rm")
	rmBytes, err := os.ReadFile(strokeRMPath)
	if err != nil {
		t.Skipf("todo_page1.rm not found: %v", err)
	}

	svgStr, err := RenderRMToSVG(rmBytes, NotebookWidthPt, NotebookHeightPt)
	if err != nil {
		t.Fatalf("RenderRMToSVG failed: %v", err)
	}

	t.Logf("SVG first 300 chars: %s", svgStr[:300])

	inspectFile := func(name, path string) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Logf("Failed to read %s: %v", name, err)
			return
		}
		sc, err := rmscene.Parse(b)
		if err != nil {
			t.Logf("Failed to parse %s: %v", name, err)
			return
		}
		minX, maxX := 999999.0, -999999.0
		minY, maxY := 999999.0, -999999.0
		for _, block := range sc.Blocks {
			if lb, ok := block.(*rmscene.SceneLineItemBlock); ok && lb.Item.Value != nil {
				for _, pt := range lb.Item.Value.Points {
					x := float64(pt.X)
					y := float64(pt.Y)
					if x < minX {
						minX = x
					}
					if x > maxX {
						maxX = x
					}
					if y < minY {
						minY = y
					}
					if y > maxY {
						maxY = y
					}
				}
			}
		}
		t.Logf("[%s] Raw points: X=[%.2f, %.2f], Y=[%.2f, %.2f]", name, minX, maxX, minY, maxY)
		t.Logf("[%s] Scaled (* 72/226): X=[%.2f, %.2f], Y=[%.2f, %.2f]",
			name, minX*72/226, maxX*72/226, minY*72/226, maxY*72/226)

		anchorMap := make(map[string]int)
		for _, block := range sc.Blocks {
			if tb, ok := block.(*rmscene.TreeNodeBlock); ok {
				var ox, thresh float32
				if tb.Group.AnchorOriginX != nil {
					ox = *tb.Group.AnchorOriginX
				}
				if tb.Group.AnchorThreshold != nil {
					thresh = *tb.Group.AnchorThreshold
				}
				var at uint8
				if tb.Group.AnchorType != nil {
					at = *tb.Group.AnchorType
				}
				key := fmt.Sprintf("AnchorType=%d, OriginX=%.2f, Thresh=%.2f, AnchorID=%v",
					at, ox, thresh, tb.Group.AnchorID)
				anchorMap[key]++
			}
		}
		for k, v := range anchorMap {
			t.Logf("[%s] Anchor summary: %s => count=%d", name, k, v)
		}
		groups := make(map[string]rmscene.Group)
		for _, block := range sc.Blocks {
			if tb, ok := block.(*rmscene.TreeNodeBlock); ok {
				groups[tb.Group.NodeID.String()] = tb.Group
			}
		}

		// Also check SceneTreeBlock
		treeLinks := make(map[string]string)
		for _, block := range sc.Blocks {
			if stb, ok := block.(*rmscene.SceneTreeBlock); ok {
				treeLinks[stb.NodeID.String()] = stb.ParentID.String()
				t.Logf("[%s] SceneTree link: NodeID=%v -> ParentID=%v (TreeID=%v, IsUpdate=%v)",
					name, stb.NodeID, stb.ParentID, stb.TreeID, stb.IsUpdate)
			}
		}

		// Check line parent distribution
		parentLineCounts := make(map[string]int)
		for _, block := range sc.Blocks {
			if lb, ok := block.(*rmscene.SceneLineItemBlock); ok && lb.Item.Value != nil {
				parentLineCounts[lb.ParentID.String()]++
			}
		}
		for p, count := range parentLineCounts {
			g, ok := groups[p]
			var at uint8
			var ox, thresh float32
			if g.AnchorType != nil {
				at = *g.AnchorType
			}
			if g.AnchorOriginX != nil {
				ox = *g.AnchorOriginX
			}
			if g.AnchorThreshold != nil {
				thresh = *g.AnchorThreshold
			}
			// Find bounds of lines in this parent
			pMinX, pMaxX := 999999.0, -999999.0
			pMinY, pMaxY := 999999.0, -999999.0
			for _, block := range sc.Blocks {
				if lb, ok := block.(*rmscene.SceneLineItemBlock); ok && lb.Item.Value != nil && lb.ParentID.String() == p {
					for _, pt := range lb.Item.Value.Points {
						if float64(pt.X) < pMinX {
							pMinX = float64(pt.X)
						}
						if float64(pt.X) > pMaxX {
							pMaxX = float64(pt.X)
						}
						if float64(pt.Y) < pMinY {
							pMinY = float64(pt.Y)
						}
						if float64(pt.Y) > pMaxY {
							pMaxY = float64(pt.Y)
						}
					}
				}
			}
			t.Logf("[%s] Parent %s has %d lines (in groups=%v: Label=%q, AnchorType=%d, OriginX=%.2f, Thresh=%.2f, AnchorID=%v) Bounds: X=[%.2f, %.2f], Y=[%.2f, %.2f]",
				name, p, count, ok, g.Label, at, ox, thresh, g.AnchorID, pMinX, pMaxX, pMinY, pMaxY)
		}

		for _, block := range sc.Blocks {
			if lb, ok := block.(*rmscene.SceneLineItemBlock); ok && lb.Item.Value != nil {
				if lb.Item.Value.Tool.IsHighlighter() {
					hlMinX, hlMaxX := 999999.0, -999999.0
					hlMinY, hlMaxY := 999999.0, -999999.0
					for _, pt := range lb.Item.Value.Points {
						if float64(pt.X) < hlMinX {
							hlMinX = float64(pt.X)
						}
						if float64(pt.X) > hlMaxX {
							hlMaxX = float64(pt.X)
						}
						if float64(pt.Y) < hlMinY {
							hlMinY = float64(pt.Y)
						}
						if float64(pt.Y) > hlMaxY {
							hlMaxY = float64(pt.Y)
						}
					}
					t.Logf("[%s] Highlighter: %d pts, X=[%.2f, %.2f], Y=[%.2f, %.2f], Parent=%v",
						name, len(lb.Item.Value.Points), hlMinX, hlMaxX, hlMinY, hlMaxY, lb.ParentID)
				}
			}
		}
	}

	inspectFile("TODO p1 (67ae)", strokeRMPath)
	inspectFile("TODO p2 (38ab)", testDataPath(t, "38abcab4-0139-49b1-be3b-8488a7b29bd9.rm"))
	inspectFile("TODO p3 (bd5a)", testDataPath(t, "bd5ac891-3b23-46e9-9d14-f9a4696fbe70.rm"))
	inspectFile("TODO p4 (ae39)", testDataPath(t, "ae39bb7d-3bfd-42df-aee6-843207b99bc5.rm"))
	inspectFile("TODO p5 (e39f)", testDataPath(t, "e39f4d07-c44b-46af-9323-5077b2ea3f24.rm"))
	inspectFile("Notebook page1", testDataPath(t, "notebook_page1.rm"))
	inspectFile("Stroke sample", testDataPath(t, "stroke.rm"))

	var pdfBuf bytes.Buffer
	if err := SVGsToPDF([]string{svgStr}, &pdfBuf); err != nil {
		t.Fatalf("SVGsToPDF failed: %v", err)
	}

	outPath := testDataPath(t, "todo_rendered.pdf")
	_ = os.WriteFile(outPath, pdfBuf.Bytes(), 0644)
	t.Logf("Wrote todo_rendered.pdf (%d bytes)", pdfBuf.Len())
}

func TestRenderTodoWithTransforms(t *testing.T) {
	strokeRMPath := testDataPath(t, "todo_page1.rm")
	rmBytes, err := os.ReadFile(strokeRMPath)
	if err != nil {
		t.Skipf("todo_page1.rm not found: %v", err)
	}

	svgStr, err := RenderNotebookRMToSVG(rmBytes, NotebookWidthPt, NotebookHeightPt)
	if err != nil {
		t.Fatalf("RenderNotebookRMToSVG failed: %v", err)
	}

	var pdfBuf bytes.Buffer
	if err := SVGsToPDF([]string{svgStr}, &pdfBuf); err != nil {
		t.Fatalf("SVGsToPDF failed: %v", err)
	}

	outPath := testDataPath(t, "test_todo_fixed.pdf")
	if err := os.WriteFile(outPath, pdfBuf.Bytes(), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	t.Logf("Wrote test_todo_fixed.pdf (%d bytes) via RenderNotebookRMToSVG", pdfBuf.Len())
}
