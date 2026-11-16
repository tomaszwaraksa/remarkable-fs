package rmv5

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

const (
	HeaderV5 = "reMarkable .lines file, version=5          "
	Width    = 1404.0
	Height   = 1872.0
)

// Point represents a stroke coordinate and pressure.
type Point struct {
	X         float32
	Y         float32
	Speed     float32
	Direction float32
	Width     float32
	Pressure  float32
}

// Line represents a single continuous pen stroke.
type Line struct {
	Tool      uint32
	Color     uint32
	Thickness float32
	Points    []Point
}

// Layer represents a drawing layer.
type Layer struct {
	Lines []Line
}

// Parse parses a v5 .rm binary file.
func Parse(data []byte) ([]Layer, error) {
	if len(data) < len(HeaderV5) {
		return nil, fmt.Errorf("data too short for v5 header")
	}

	header := string(data[:len(HeaderV5)])
	if !strings.HasPrefix(header, "reMarkable .lines file, version=5") {
		return nil, fmt.Errorf("invalid v5 header: %s", header)
	}

	r := bytes.NewReader(data[len(HeaderV5):])

	var numLayers uint32
	if err := binary.Read(r, binary.LittleEndian, &numLayers); err != nil {
		return nil, fmt.Errorf("failed to read numLayers: %w", err)
	}

	layers := make([]Layer, numLayers)
	for i := uint32(0); i < numLayers; i++ {
		var numLines uint32
		if err := binary.Read(r, binary.LittleEndian, &numLines); err != nil {
			return nil, fmt.Errorf("failed to read numLines for layer %d: %w", i, err)
		}

		layers[i].Lines = make([]Line, numLines)
		for j := uint32(0); j < numLines; j++ {
			var line Line
			var unk1, unk2, numPoints uint32

			if err := binary.Read(r, binary.LittleEndian, &line.Tool); err != nil {
				return nil, err
			}
			if err := binary.Read(r, binary.LittleEndian, &line.Color); err != nil {
				return nil, err
			}
			if err := binary.Read(r, binary.LittleEndian, &unk1); err != nil {
				return nil, err
			}
			if err := binary.Read(r, binary.LittleEndian, &line.Thickness); err != nil {
				return nil, err
			}
			if err := binary.Read(r, binary.LittleEndian, &unk2); err != nil {
				return nil, err
			}
			if err := binary.Read(r, binary.LittleEndian, &numPoints); err != nil {
				return nil, err
			}

			line.Points = make([]Point, numPoints)
			for p := uint32(0); p < numPoints; p++ {
				var pt Point
				if err := binary.Read(r, binary.LittleEndian, &pt); err != nil {
					return nil, err
				}
				line.Points[p] = pt
			}

			layers[i].Lines[j] = line
		}
	}

	return layers, nil
}

// getColorHex maps reMarkable color ID to RGB hex string and opacity.
func getColorHex(colorID, toolID uint32) (string, float64) {
	if toolID == 5 { // Highlighter
		return "#f2e468", 0.4
	}

	switch colorID {
	case 0: // Black
		return "#000000", 1.0
	case 1: // Grey
		return "#707070", 1.0
	case 2: // White
		return "#ffffff", 1.0
	case 3: // Yellow
		return "#ecd053", 1.0
	case 4: // Green
		return "#629e55", 1.0
	case 5: // Pink
		return "#e878a0", 1.0
	case 6: // Blue
		return "#4f82b8", 1.0
	case 7: // Red
		return "#cf4848", 1.0
	default:
		return "#000000", 1.0
	}
}

// ToSVG renders parsed v5 layers into an SVG string scaled to the target dimensions.
func ToSVG(layers []Layer, widthPt, heightPt float64) string {
	if widthPt <= 0 || heightPt <= 0 {
		widthPt = 447.874
		heightPt = 595.275
	}
	scaleX := float32(widthPt / Width)
	scaleY := float32(heightPt / Height)

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="%.3f" height="%.3f" viewBox="0 0 %.3f %.3f">
`, widthPt, heightPt, widthPt, heightPt))

	for layerIdx, layer := range layers {
		buf.WriteString(fmt.Sprintf(`  <g id="layer-%d">`+"\n", layerIdx))
		for _, line := range layer.Lines {
			if len(line.Points) == 0 {
				continue
			}

			colorHex, opacity := getColorHex(line.Color, line.Tool)
			strokeWidth := float64(line.Thickness) * 1.5 * float64(scaleX)
			if line.Tool == 5 {
				strokeWidth = math.Max(15.0*float64(scaleX), strokeWidth*3.0)
			}
			if strokeWidth < 1.0 {
				strokeWidth = 1.0
			}

			if len(line.Points) == 1 {
				pt := line.Points[0]
				buf.WriteString(fmt.Sprintf(`    <circle cx="%.2f" cy="%.2f" r="%.2f" fill="%s" fill-opacity="%.2f" />`+"\n",
					pt.X*scaleX, pt.Y*scaleY, strokeWidth/2, colorHex, opacity))
				continue
			}

			var d strings.Builder
			d.WriteString(fmt.Sprintf("M %.2f %.2f", line.Points[0].X*scaleX, line.Points[0].Y*scaleY))
			for _, pt := range line.Points[1:] {
				d.WriteString(fmt.Sprintf(" L %.2f %.2f", pt.X*scaleX, pt.Y*scaleY))
			}

			buf.WriteString(fmt.Sprintf(`    <path d="%s" fill="none" stroke="%s" stroke-width="%.2f" stroke-linecap="round" stroke-linejoin="round" stroke-opacity="%.2f" />`+"\n",
				d.String(), colorHex, strokeWidth, opacity))
		}
		buf.WriteString("  </g>\n")
	}

	buf.WriteString("</svg>\n")
	return buf.String()
}

// ParseToSVG parses v5 raw bytes and returns the rendered SVG string.
func ParseToSVG(data []byte, widthPt, heightPt float64) (string, error) {
	layers, err := Parse(data)
	if err != nil {
		return "", err
	}
	return ToSVG(layers, widthPt, heightPt), nil
}
