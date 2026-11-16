package converter

import (
	"bytes"
	"fmt"

	"github.com/alexgorbatchev/go-rmscene"
	"remarkable-fs/internal/rmv5"
)

const (
	// NotebookWidthPt is the standard reMarkable screen width in points (1404 * 72 / 226 ≈ 447.874 pt).
	NotebookWidthPt = rmscene.DefaultWidthPt
	// NotebookHeightPt is the standard reMarkable screen height in points (1872 * 72 / 226 ≈ 595.275 pt).
	NotebookHeightPt = rmscene.DefaultHeightPt
)

// RenderRMToSVG converts raw .rm binary bytes (either v5 or v6) into an SVG string.
// If widthPt and heightPt are provided (> 0), the stroke coordinates are scaled to match.
func RenderRMToSVG(data []byte, widthPt, heightPt float64) (string, error) {
	return renderRMToSVG(data, widthPt, heightPt, false)
}

// RenderNotebookRMToSVG converts raw .rm bytes for a notebook page, adjusting AnchorOriginX and vertical scroll.
func RenderNotebookRMToSVG(data []byte, widthPt, heightPt float64) (string, error) {
	return renderRMToSVG(data, widthPt, heightPt, true)
}

func renderRMToSVG(data []byte, widthPt, heightPt float64, isNotebook bool) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("empty stroke data")
	}

	if bytes.HasPrefix(data, []byte("reMarkable .lines file, version=6")) {
		sc, err := rmscene.Parse(data)
		if err != nil {
			return "", err
		}

		// 1. Build scene tree and anchor lookup
		groups := make(map[string]rmscene.Group)
		treeLinks := make(map[string]string)
		for _, block := range sc.Blocks {
			if tb, ok := block.(*rmscene.TreeNodeBlock); ok {
				groups[tb.Group.NodeID.String()] = tb.Group
			} else if stb, ok := block.(*rmscene.SceneTreeBlock); ok {
				treeLinks[stb.NodeID.String()] = stb.ParentID.String()
			}
		}

		getAnchorOriginX := func(nodeID string) float32 {
			curr := nodeID
			for curr != "" && curr != "CrdtId(0, 0)" {
				if g, ok := groups[curr]; ok && g.AnchorOriginX != nil {
					return *g.AnchorOriginX
				}
				curr = treeLinks[curr]
			}
			return 0
		}

		// 2. Transform stroke coordinates by group AnchorOriginX
		for _, block := range sc.Blocks {
			if lb, ok := block.(*rmscene.SceneLineItemBlock); ok && lb.Item.Value != nil {
				ox := getAnchorOriginX(lb.ParentID.String())
				if ox != 0 {
					for i := range lb.Item.Value.Points {
						lb.Item.Value.Points[i].X += ox
					}
				}
			}
		}

		// 3. For notebooks, adjust vertical scroll if strokes start above page origin (minY < -5)
		if isNotebook {
			minY := float32(999999.0)
			maxY := float32(-999999.0)
			hasPoints := false
			for _, block := range sc.Blocks {
				if lb, ok := block.(*rmscene.SceneLineItemBlock); ok && lb.Item.Value != nil {
					for _, pt := range lb.Item.Value.Points {
						hasPoints = true
						if pt.Y < minY {
							minY = pt.Y
						}
						if pt.Y > maxY {
							maxY = pt.Y
						}
					}
				}
			}

			if hasPoints && minY < -5.0 {
				offsetY := -minY + 10.0
				for _, block := range sc.Blocks {
					if lb, ok := block.(*rmscene.SceneLineItemBlock); ok && lb.Item.Value != nil {
						for i := range lb.Item.Value.Points {
							lb.Item.Value.Points[i].Y += offsetY
						}
					}
				}
				maxY += offsetY
				minY = 10.0
			}

			totalHeightPt := NotebookHeightPt
			if hasPoints {
				contentHeightPt := float64(maxY+20.0) * 72.0 / 226.0
				if contentHeightPt > totalHeightPt {
					totalHeightPt = contentHeightPt
				}
			}
			if heightPt < totalHeightPt {
				heightPt = totalHeightPt
			}
			if widthPt <= 0 {
				widthPt = NotebookWidthPt
			}
		}

		var opts []rmscene.SVGOption
		if widthPt > 0 && heightPt > 0 {
			opts = append(opts, rmscene.WithDimensions(widthPt, heightPt))
		}
		return rmscene.RenderToSVG(sc.Blocks, opts...), nil
	}

	if bytes.HasPrefix(data, []byte("reMarkable .lines file, version=5")) {
		return rmv5.ParseToSVG(data, widthPt, heightPt)
	}

	return "", fmt.Errorf("unrecognized .rm file header: %s", string(data[:min(30, len(data))]))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
