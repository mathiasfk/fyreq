package platform

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"fyreq/internal/catalog"
)

func TestScanCollectsOwnership(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	link := filepath.Join(root, "link")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	count := 0
	report, err := (Filesystem{}).Scan(context.Background(), root, func(file catalog.File) error {
		count++
		if file.OwnerID == nil || *file.OwnerID != strconv.Itoa(os.Getuid()) {
			t.Errorf("incorrect UID for %q: %v", file.Path, file.OwnerID)
		}
		return nil
	})
	if err != nil || !report.Complete || count != 3 {
		t.Fatalf("unexpected scan: %#v, count=%d, error=%v", report, count, err)
	}
}
