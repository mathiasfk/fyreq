package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"fyreq/internal/catalog"
)

func TestScanMetadataWithoutFollowingSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	files := make(map[string]catalog.File)
	report, err := (Filesystem{}).Scan(context.Background(), root, func(file catalog.File) error {
		files[file.Path] = file
		return nil
	})
	if err != nil || !report.Complete || report.ErrorCount != 0 {
		t.Fatalf("unexpected report: %#v, %v", report, err)
	}
	if len(files) != 3 || files[filepath.Join(root, "link")].Kind != "symlink" || files[filepath.Join(root, "hello.txt")].Size != 5 {
		t.Fatalf("unexpected collected metadata: %#v", files)
	}
}

func TestMissingRootAndCancellation(t *testing.T) {
	visit := func(catalog.File) error { return nil }
	report, err := (Filesystem{}).Scan(context.Background(), filepath.Join(t.TempDir(), "missing"), visit)
	if err != nil || report.Complete || report.ErrorCount != 1 {
		t.Fatalf("missing root was not recorded as incomplete: %#v, %v", report, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Filesystem{}).Scan(ctx, t.TempDir(), visit); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}
