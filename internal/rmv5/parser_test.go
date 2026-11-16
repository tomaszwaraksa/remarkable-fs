package rmv5

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestParseV5(t *testing.T) {
	// Construct minimal valid v5 .rm file
	var buf bytes.Buffer
	buf.WriteString(HeaderV5)

	numLayers := uint32(1)
	binary.Write(&buf, binary.LittleEndian, numLayers)

	numLines := uint32(1)
	binary.Write(&buf, binary.LittleEndian, numLines)

	// Tool: 4 (Fineliner), Color: 0 (Black), unk1: 0, thickness: 2.0, unk2: 0, numPoints: 2
	binary.Write(&buf, binary.LittleEndian, uint32(4))
	binary.Write(&buf, binary.LittleEndian, uint32(0))
	binary.Write(&buf, binary.LittleEndian, uint32(0))
	binary.Write(&buf, binary.LittleEndian, float32(2.0))
	binary.Write(&buf, binary.LittleEndian, uint32(0))
	binary.Write(&buf, binary.LittleEndian, uint32(2))

	// Point 1: (100, 200, speed=1, dir=0, width=2, pressure=0.8)
	binary.Write(&buf, binary.LittleEndian, float32(100.0))
	binary.Write(&buf, binary.LittleEndian, float32(200.0))
	binary.Write(&buf, binary.LittleEndian, float32(1.0))
	binary.Write(&buf, binary.LittleEndian, float32(0.0))
	binary.Write(&buf, binary.LittleEndian, float32(2.0))
	binary.Write(&buf, binary.LittleEndian, float32(0.8))

	// Point 2: (150, 250, ...)
	binary.Write(&buf, binary.LittleEndian, float32(150.0))
	binary.Write(&buf, binary.LittleEndian, float32(250.0))
	binary.Write(&buf, binary.LittleEndian, float32(1.0))
	binary.Write(&buf, binary.LittleEndian, float32(0.0))
	binary.Write(&buf, binary.LittleEndian, float32(2.0))
	binary.Write(&buf, binary.LittleEndian, float32(0.8))

	svg, err := ParseToSVG(buf.Bytes(), 1404, 1872)
	if err != nil {
		t.Fatalf("ParseToSVG failed: %v", err)
	}

	if !strings.Contains(svg, `<svg`) || !strings.Contains(svg, `M 100.00 200.00 L 150.00 250.00`) {
		t.Errorf("Unexpected SVG content:\n%s", svg)
	}
}
