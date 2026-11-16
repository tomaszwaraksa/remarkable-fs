package model

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFlexibleTimestamp(t *testing.T) {
	// Format 1: string millisecond timestamp
	jsonStr := `{"lastModified": "1672531200000"}`
	var data1 struct {
		LastModified FlexibleTimestamp `json:"lastModified"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &data1); err != nil {
		t.Fatalf("Failed to unmarshal string timestamp: %v", err)
	}
	expectedTime := time.UnixMilli(1672531200000)
	if !data1.LastModified.Time().Equal(expectedTime) {
		t.Errorf("Expected %v, got %v", expectedTime, data1.LastModified.Time())
	}

	// Format 2: integer millisecond timestamp
	jsonInt := `{"lastModified": 1672531200000}`
	var data2 struct {
		LastModified FlexibleTimestamp `json:"lastModified"`
	}
	if err := json.Unmarshal([]byte(jsonInt), &data2); err != nil {
		t.Fatalf("Failed to unmarshal int timestamp: %v", err)
	}
	if !data2.LastModified.Time().Equal(expectedTime) {
		t.Errorf("Expected %v, got %v", expectedTime, data2.LastModified.Time())
	}
}

func TestFSNodeAddChildDisambiguation(t *testing.T) {
	parent := NewDirectoryNode("root-uuid", "root", time.Now(), nil)

	file1 := NewFileNode("f1", "Document.pdf", "/path/1.pdf", 1024, time.Now(), "pdf", nil, nil)
	file2 := NewFileNode("f2", "Document.pdf", "/path/2.pdf", 2048, time.Now(), "pdf", nil, nil)
	file3 := NewFileNode("f3", "Document.pdf", "/path/3.pdf", 4096, time.Now(), "pdf", nil, nil)

	parent.AddChild(file1)
	parent.AddChild(file2)
	parent.AddChild(file3)

	if file1.Name != "Document.pdf" {
		t.Errorf("Expected Document.pdf, got %s", file1.Name)
	}
	if file2.Name != "Document (1).pdf" {
		t.Errorf("Expected Document (1).pdf, got %s", file2.Name)
	}
	if file3.Name != "Document (2).pdf" {
		t.Errorf("Expected Document (2).pdf, got %s", file3.Name)
	}

	if len(parent.Children) != 3 {
		t.Errorf("Expected 3 children, got %d", len(parent.Children))
	}
	if len(parent.OrderedChildren) != 3 {
		t.Errorf("Expected 3 ordered children, got %d", len(parent.OrderedChildren))
	}
}
