package platform

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"fyreq/internal/catalog"
)

func TestWindowsNativeMetadata(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	file := filepath.Join(root, "file")
	hardLink := filepath.Join(root, "hard-link")
	junction := filepath.Join(root, "junction")
	if err := os.WriteFile(file, []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, hardLink); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "outside"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, outside).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, output)
	}
	files := make(map[string]catalog.File)
	visit := func(file catalog.File) error {
		files[file.Path] = file
		return nil
	}
	for _, path := range []string{root, outside} {
		report, err := (Filesystem{}).Scan(context.Background(), path, visit)
		if err != nil || !report.Complete {
			t.Fatalf("scan %q: %#v, %v", path, report, err)
		}
	}
	if len(files) != 6 || files[junction].Kind == "directory" || files[junction].Identity == files[outside].Identity {
		t.Fatalf("unexpected junction observations: %#v", files)
	}
	if files[file].Identity != files[hardLink].Identity || files[file].Identity == files[root].Identity {
		t.Fatalf("unexpected hard-link identity: %#v", files)
	}
	for path, file := range files {
		if file.Identity == "" || file.OwnerID == nil {
			t.Fatalf("missing native metadata for %q: %#v", path, file)
		}
		if _, err := syscall.StringToSid(*file.OwnerID); err != nil {
			t.Errorf("invalid SID for %q: %v", path, err)
		}
	}
}

func TestWindowsDefaultRoots(t *testing.T) {
	roots, err := (Roots{}).DefaultRoots(context.Background())
	if err != nil || len(roots) == 0 {
		t.Fatalf("fixed drives: %v, %v", roots, err)
	}
	for i, root := range roots {
		if len(root) != 3 || root[0] < 'A' || root[0] > 'Z' || root[1:] != ":\\" || (i > 0 && roots[i-1] >= root) {
			t.Errorf("unexpected drive root %q in %v", root, roots)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Roots{}).DefaultRoots(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}
