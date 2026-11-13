package indexing

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"fyreq/internal/catalog"
)

type fakeCollector struct {
	files    []catalog.File
	complete bool
	err      error
}

func (f fakeCollector) Scan(ctx context.Context, root string, visit func(catalog.File) error) (catalog.Coverage, error) {
	for _, file := range f.files {
		if err := visit(file); err != nil {
			return catalog.Coverage{}, err
		}
	}
	return catalog.Coverage{Root: root, Complete: f.complete}, f.err
}

func TestRefreshReconcilesOnlyCoveredRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	snapshot := catalog.New()
	snapshot.Files[filepath.Join(root, "a")] = catalog.File{ID: "stable", Path: filepath.Join(root, "a"), Identity: "1:1"}
	snapshot.Files[filepath.Join(root, "deleted")] = catalog.File{ID: "deleted", Path: filepath.Join(root, "deleted")}
	snapshot.Files[filepath.Join(filepath.Dir(root), "root-other", "a")] = catalog.File{ID: "outside", Path: filepath.Join(filepath.Dir(root), "root-other", "a")}
	indexer := Indexer{Collector: fakeCollector{complete: true, files: []catalog.File{
		{Path: filepath.Join(root, "a"), Identity: "1:1", Size: 42}, {Path: filepath.Join(root, "new")},
	}}}
	if _, err := indexer.Refresh(context.Background(), &snapshot, root); err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Files[filepath.Join(root, "a")]; got.ID != "stable" || got.Size != 42 {
		t.Fatalf("record was not updated in place: %#v", got)
	}
	if snapshot.Files[filepath.Join(root, "new")].ID == "" {
		t.Fatal("new record has no ID")
	}
	if _, ok := snapshot.Files[filepath.Join(root, "deleted")]; ok {
		t.Fatal("deleted record retained after complete scan")
	}
	if _, ok := snapshot.Files[filepath.Join(filepath.Dir(root), "root-other", "a")]; !ok {
		t.Fatal("record outside root removed")
	}
}

func TestIncompleteRefreshRetainsUnobservedRecords(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	snapshot := catalog.New()
	snapshot.Files[filepath.Join(root, "unreadable")] = catalog.File{ID: "old", Path: filepath.Join(root, "unreadable")}
	indexer := Indexer{Collector: fakeCollector{files: []catalog.File{{Path: filepath.Join(root, "new")}}}}
	report, err := indexer.Refresh(context.Background(), &snapshot, root)
	if err != nil || report.Complete {
		t.Fatalf("unexpected report: %#v, %v", report, err)
	}
	if len(snapshot.Files) != 2 || snapshot.Coverage[root].Complete {
		t.Fatal("incomplete scan lost records or claimed complete coverage")
	}
}

func TestReplacementGetsNewID(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	snapshot := catalog.New()
	snapshot.Files[filepath.Join(root, "a")] = catalog.File{ID: "old", Path: filepath.Join(root, "a"), Identity: "1:1"}
	indexer := Indexer{Collector: fakeCollector{complete: true, files: []catalog.File{{Path: filepath.Join(root, "a"), Identity: "1:2"}}}}
	if _, err := indexer.Refresh(context.Background(), &snapshot, root); err != nil {
		t.Fatal(err)
	}
	if snapshot.Files[filepath.Join(root, "a")].ID == "old" {
		t.Fatal("replacement retained old ID")
	}
}

func TestCollectorFailureAndOutOfScopePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	for _, collector := range []fakeCollector{
		{err: context.Canceled},
		{files: []catalog.File{{Path: filepath.Join(filepath.Dir(root), "elsewhere", "a")}}},
	} {
		snapshot := catalog.New()
		_, err := (Indexer{Collector: collector}).Refresh(context.Background(), &snapshot, root)
		if err == nil {
			t.Fatal("expected error")
		}
		if collector.err != nil && !errors.Is(err, collector.err) {
			t.Fatalf("lost collector error: %v", err)
		}
	}
}

func TestParentAfterChildPreservesIdentityAndCoverage(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	file := filepath.Join(child, "file")
	snap := catalog.New()
	indexer := Indexer{Collector: fakeCollector{complete: true, files: []catalog.File{{Path: file, Identity: "native"}}}}
	if _, err := indexer.Refresh(context.Background(), &snap, child); err != nil {
		t.Fatal(err)
	}
	id := snap.Files[file].ID
	childCoverage := snap.Coverage[child]
	if _, err := indexer.Refresh(context.Background(), &snap, parent); err != nil {
		t.Fatal(err)
	}
	if snap.Files[file].ID != id || snap.Coverage[child].Root != childCoverage.Root || len(snap.Coverage) != 2 {
		t.Fatalf("identity or coverage changed: %#v", snap)
	}
}
