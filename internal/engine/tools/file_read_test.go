package tools

import "testing"

func TestFileReadLabel(t *testing.T) {
	if got := fileReadLabel(fileReadArgs{File: "a.go"}); got != "a.go" {
		t.Errorf("no-range label: want a.go, got %q", got)
	}
	if got := fileReadLabel(fileReadArgs{File: "a.go", Start: 10, End: 20}); got != "a.go:10-20" {
		t.Errorf("range label: want a.go:10-20, got %q", got)
	}
}
