package indexing

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"fyreq/internal/catalog"
	"fyreq/internal/pathpolicy"
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

// memory records calls so tests can verify that unchanged records are not rewritten.
type memory struct {
	files    map[string]catalog.File
	coverage map[string]catalog.Coverage
	puts     []string
	deletes  []string
}

func newMemory() *memory {
	return &memory{files: make(map[string]catalog.File), coverage: make(map[string]catalog.Coverage)}
}

func (m *memory) Files(_ context.Context, root string) (map[string]catalog.File, error) {
	result := make(map[string]catalog.File)
	for path, file := range m.files {
		if pathpolicy.Contains(root, path) {
			result[path] = file
		}
	}
	return result, nil
}
func (m *memory) Put(file catalog.File) error {
	m.puts = append(m.puts, file.Path)
	m.files[file.Path] = file
	return nil
}
func (m *memory) Delete(path string) error {
	m.deletes = append(m.deletes, path)
	delete(m.files, path)
	return nil
}
func (m *memory) SetCoverage(c catalog.Coverage) error { m.coverage[c.Root] = c; return nil }

func TestReconcileOnlyCoveredRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	m := newMemory()
	m.files[filepath.Join(root, "a")] = catalog.File{ID: "stable", Path: filepath.Join(root, "a"), Identity: "1:1"}
	m.files[filepath.Join(root, "deleted")] = catalog.File{ID: "deleted", Path: filepath.Join(root, "deleted")}
	m.files[filepath.Join(filepath.Dir(root), "root-other", "a")] = catalog.File{ID: "outside", Path: filepath.Join(filepath.Dir(root), "root-other", "a")}
	indexer := Indexer{Collector: fakeCollector{complete: true, files: []catalog.File{
		{Path: filepath.Join(root, "a"), Identity: "1:1", Size: 42}, {Path: filepath.Join(root, "new")},
	}}}
	if _, err := indexer.Reconcile(context.Background(), m, root); err != nil {
		t.Fatal(err)
	}
	if got := m.files[filepath.Join(root, "a")]; got.ID != "stable" || got.Size != 42 {
		t.Fatalf("record was not updated in place: %#v", got)
	}
	if m.files[filepath.Join(root, "new")].ID == "" {
		t.Fatal("new record has no ID")
	}
	if _, ok := m.files[filepath.Join(root, "deleted")]; ok {
		t.Fatal("deleted record retained after complete scan")
	}
	if _, ok := m.files[filepath.Join(filepath.Dir(root), "root-other", "a")]; !ok {
		t.Fatal("record outside root removed")
	}
}

func TestUnchangedRecordsAreNotRewritten(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	modified := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recorded := modified.Add(time.Hour)
	same, changed := filepath.Join(root, "same"), filepath.Join(root, "changed")
	m := newMemory()
	m.files[same] = catalog.File{ID: "s", Path: same, Size: 1, ModifiedAt: modified, IndexedAt: recorded, Identity: "1:1"}
	m.files[changed] = catalog.File{ID: "c", Path: changed, Size: 1, ModifiedAt: modified, IndexedAt: recorded, Identity: "1:2"}
	later := recorded.Add(time.Hour)
	indexer := Indexer{Collector: fakeCollector{complete: true, files: []catalog.File{
		{Path: same, Size: 1, ModifiedAt: modified, IndexedAt: later, Identity: "1:1"},
		{Path: changed, Size: 2, ModifiedAt: modified, IndexedAt: later, Identity: "1:2"},
	}}}
	if _, err := indexer.Reconcile(context.Background(), m, root); err != nil {
		t.Fatal(err)
	}
	if len(m.puts) != 1 || m.puts[0] != changed || len(m.deletes) != 0 {
		t.Fatalf("puts=%v deletes=%v", m.puts, m.deletes)
	}
	if !m.files[same].IndexedAt.Equal(recorded) || !m.files[changed].IndexedAt.Equal(later) || m.files[changed].ID != "c" {
		t.Fatalf("unexpected records: %#v", m.files)
	}
}

func TestIncompleteReconcileRetainsUnobservedRecords(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	m := newMemory()
	m.files[filepath.Join(root, "unreadable")] = catalog.File{ID: "old", Path: filepath.Join(root, "unreadable")}
	indexer := Indexer{Collector: fakeCollector{files: []catalog.File{{Path: filepath.Join(root, "new")}}}}
	report, err := indexer.Reconcile(context.Background(), m, root)
	if err != nil || report.Complete {
		t.Fatalf("unexpected report: %#v, %v", report, err)
	}
	if len(m.files) != 2 || m.coverage[root].Complete {
		t.Fatal("incomplete scan lost records or claimed complete coverage")
	}
}

func TestReplacementGetsNewID(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	m := newMemory()
	m.files[filepath.Join(root, "a")] = catalog.File{ID: "old", Path: filepath.Join(root, "a"), Identity: "1:1"}
	indexer := Indexer{Collector: fakeCollector{complete: true, files: []catalog.File{{Path: filepath.Join(root, "a"), Identity: "1:2"}}}}
	if _, err := indexer.Reconcile(context.Background(), m, root); err != nil {
		t.Fatal(err)
	}
	if m.files[filepath.Join(root, "a")].ID == "old" {
		t.Fatal("replacement retained old ID")
	}
}

func TestCollectorFailureAndOutOfScopePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	for _, collector := range []fakeCollector{
		{err: context.Canceled},
		{files: []catalog.File{{Path: filepath.Join(filepath.Dir(root), "elsewhere", "a")}}},
	} {
		m := newMemory()
		_, err := (Indexer{Collector: collector}).Reconcile(context.Background(), m, root)
		if err == nil {
			t.Fatal("expected error")
		}
		if collector.err != nil && !errors.Is(err, collector.err) {
			t.Fatalf("lost collector error: %v", err)
		}
		if len(m.coverage) != 0 {
			t.Fatal("failed scan recorded coverage")
		}
	}
}

func TestParentAfterChildPreservesIdentityAndCoverage(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	file := filepath.Join(child, "file")
	m := newMemory()
	indexer := Indexer{Collector: fakeCollector{complete: true, files: []catalog.File{{Path: file, Identity: "native"}}}}
	if _, err := indexer.Reconcile(context.Background(), m, child); err != nil {
		t.Fatal(err)
	}
	id := m.files[file].ID
	childCoverage := m.coverage[child]
	if _, err := indexer.Reconcile(context.Background(), m, parent); err != nil {
		t.Fatal(err)
	}
	if m.files[file].ID != id || m.coverage[child].Root != childCoverage.Root || len(m.coverage) != 2 {
		t.Fatalf("identity or coverage changed: %#v", m)
	}
}
