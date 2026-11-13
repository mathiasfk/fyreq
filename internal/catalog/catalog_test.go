package catalog

import (
	"strings"
	"testing"
)

func TestPlatformCheckedBeforePaths(t *testing.T) {
	s := New()
	s.Platform = "incompatible"
	s.Files["relative"] = File{Path: "relative"}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "platform") {
		t.Fatalf("expected platform error: %v", err)
	}
}

func TestInvalidPathsAndCoverage(t *testing.T) {
	s := New()
	s.Files["relative"] = File{ID: "x", Path: "relative"}
	if err := s.Validate(); err == nil {
		t.Fatal("accepted relative record")
	}
	s = New()
	s.Coverage["relative"] = Coverage{Root: "relative"}
	if err := s.Validate(); err == nil {
		t.Fatal("accepted relative coverage")
	}
}
