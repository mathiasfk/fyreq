package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"fyreq/internal/catalog"
	"fyreq/internal/indexing"
	"fyreq/internal/pathpolicy"
	"fyreq/internal/query"
)

type roots []string

func (r roots) DefaultRoots(context.Context) ([]string, error) { return r, nil }

// memorySnapshot is an immutable copy of the repository state.
type memorySnapshot struct {
	records query.Records
	header  catalog.Header
}

func (s memorySnapshot) Header() catalog.Header      { return s.header }
func (s memorySnapshot) Segments() []catalog.Segment { return nil }
func (s memorySnapshot) Close() error                { return nil }
func (s memorySnapshot) Records(ctx context.Context, visit func(catalog.File) error) error {
	_, err := s.records.Scan(ctx, query.Request{}, visit)
	return err
}
func (s memorySnapshot) Scan(ctx context.Context, r query.Request, visit func(catalog.File) error) (query.ScanStats, error) {
	return s.records.Scan(ctx, r, visit)
}
func (s memorySnapshot) Estimate(ctx context.Context, r query.Request) (query.ScanStats, error) {
	return s.records.Estimate(ctx, r)
}

type memoryTx struct {
	files    map[string]catalog.File
	coverage map[string]catalog.Coverage
}

func (m *memoryTx) Files(_ context.Context, root string) (map[string]catalog.File, error) {
	result := make(map[string]catalog.File)
	for path, f := range m.files {
		if pathpolicy.Contains(root, path) {
			result[path] = f
		}
	}
	return result, nil
}
func (m *memoryTx) Put(f catalog.File) error             { m.files[f.Path] = f; return nil }
func (m *memoryTx) Delete(path string) error             { delete(m.files, path); return nil }
func (m *memoryTx) SetCoverage(c catalog.Coverage) error { m.coverage[c.Root] = c; return nil }
func (m *memoryTx) Coverage() map[string]catalog.Coverage {
	result := make(map[string]catalog.Coverage)
	for k, v := range m.coverage {
		result[k] = v
	}
	return result
}

type repository struct {
	files      map[string]catalog.File
	coverage   map[string]catalog.Coverage
	generation uint64
	calls      int
	// beforeUpdate runs before each update's callback, simulating another writer.
	beforeUpdate func(*repository)
}

func newRepository() *repository {
	return &repository{files: make(map[string]catalog.File), coverage: make(map[string]catalog.Coverage)}
}

func (r *repository) Clear(context.Context) (UpdateResult, error) {
	r.files, r.coverage, r.generation = make(map[string]catalog.File), make(map[string]catalog.Coverage), 1
	return UpdateResult{Published: true, Generation: 1}, nil
}

func (r *repository) Open(context.Context) (Snapshot, error) {
	tx := r.copy()
	return memorySnapshot{records: tx.files, header: catalog.Header{Generation: r.generation, Entries: uint64(len(tx.files)), Coverage: tx.coverage}}, nil
}

func (r *repository) copy() *memoryTx {
	tx := &memoryTx{files: make(map[string]catalog.File), coverage: make(map[string]catalog.Coverage)}
	for k, v := range r.files {
		tx.files[k] = v
	}
	for k, v := range r.coverage {
		tx.coverage[k] = v
	}
	return tx
}

func (r *repository) Update(ctx context.Context, f func(Transaction) error) (UpdateResult, error) {
	r.calls++
	if r.beforeUpdate != nil {
		r.beforeUpdate(r)
	}
	tx := r.copy()
	if err := f(tx); err != nil {
		return UpdateResult{}, err
	}
	r.files, r.coverage = tx.files, tx.coverage
	r.generation++
	return UpdateResult{Published: true, Generation: r.generation}, nil
}

type collector struct {
	fail       string
	incomplete string
	scanned    *[]string
}

func (c collector) Scan(ctx context.Context, root string, visit func(catalog.File) error) (catalog.Coverage, error) {
	if c.scanned != nil {
		*c.scanned = append(*c.scanned, root)
	}
	if root == c.fail {
		return catalog.Coverage{Root: root}, errors.New("failed")
	}
	if err := visit(catalog.File{Path: root}); err != nil {
		return catalog.Coverage{}, err
	}
	return catalog.Coverage{Root: root, Complete: root != c.incomplete}, nil
}

func TestMultipleRootsPartialSuccess(t *testing.T) {
	base := t.TempDir()
	a, b, c := filepath.Join(base, "a"), filepath.Join(base, "b"), filepath.Join(base, "c")
	repo := newRepository()
	application := App{Repository: repo, Indexer: indexing.Indexer{Collector: collector{fail: b, incomplete: a}}, Roots: roots{c, a, b, a}}
	results, err := application.Index(context.Background(), "")
	if err == nil || len(results) != 3 || !results[0].Publication.Published || results[0].Coverage.Complete || !results[1].Attempted || results[1].Publication.Published || results[2].Attempted || repo.generation != 1 {
		t.Fatalf("results %#v, error %v", results, err)
	}
}

type rootFunc func(context.Context) ([]string, error)

func (f rootFunc) DefaultRoots(ctx context.Context) ([]string, error) { return f(ctx) }

type repoFunc func(context.Context, func(Transaction) error) (UpdateResult, error)

func (f repoFunc) Clear(context.Context) (UpdateResult, error) { panic("unexpected clear") }

func (f repoFunc) Update(ctx context.Context, fn func(Transaction) error) (UpdateResult, error) {
	return f(ctx, fn)
}
func (f repoFunc) Open(context.Context) (Snapshot, error) {
	return memorySnapshot{header: catalog.Header{Coverage: map[string]catalog.Coverage{}}}, nil
}

func TestRootResolutionAndExplicitPath(t *testing.T) {
	failure := errors.New("discovery")
	for _, provider := range []rootFunc{func(context.Context) ([]string, error) { return nil, failure }, func(context.Context) ([]string, error) { return nil, nil }} {
		app := App{Roots: provider}
		if _, err := app.Index(context.Background(), ""); err == nil {
			t.Fatal("expected resolution error")
		}
	}
	dir := t.TempDir()
	repo := newRepository()
	app := App{Roots: rootFunc(func(context.Context) ([]string, error) { t.Fatal("explicit path discovered roots"); return nil, nil }), Repository: repo, Indexer: indexing.Indexer{Collector: collector{}}}
	result, err := app.Index(context.Background(), dir)
	if err != nil || len(result) != 1 || !result[0].Publication.Published {
		t.Fatalf("%#v %v", result, err)
	}
	if _, known := repo.coverage[dir]; !known {
		t.Fatal("index did not add its root")
	}
}

func TestStopAfterCancellationOrPublishedError(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	for _, cancelAfter := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		failure := errors.New("sync failure")
		app := App{Roots: roots{a, b}, Repository: repoFunc(func(ctx context.Context, fn func(Transaction) error) (UpdateResult, error) {
			calls++
			if cancelAfter {
				cancel()
				return UpdateResult{Published: true, Generation: 1}, nil
			}
			return UpdateResult{Published: true, Generation: 1}, failure
		})}
		outcomes, err := app.Index(ctx, "")
		if err == nil || calls != 1 || !outcomes[0].Publication.Published || outcomes[1].Attempted {
			t.Fatalf("%#v %v calls=%d", outcomes, err, calls)
		}
	}
}

func TestRefreshRevisitsKnownRoots(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	repo := newRepository()
	var scanned []string
	app := App{Repository: repo, Indexer: indexing.Indexer{Collector: collector{scanned: &scanned}}, Roots: rootFunc(func(context.Context) ([]string, error) {
		t.Fatal("refresh resolved default roots")
		return nil, nil
	})}
	if _, err := app.Refresh(context.Background()); err == nil {
		t.Fatal("refresh without known roots succeeded")
	}
	for _, root := range []string{b, a} {
		if _, err := app.Index(context.Background(), root); err != nil {
			t.Fatal(err)
		}
	}
	scanned = nil
	outcomes, err := app.Refresh(context.Background())
	if err != nil || len(outcomes) != 2 || outcomes[0].Root != a || outcomes[1].Root != b || len(scanned) != 2 || scanned[0] != a || repo.generation != 4 {
		t.Fatalf("outcomes %#v, scanned %v, error %v", outcomes, scanned, err)
	}
	if _, err := app.Index(context.Background(), a); err != nil || len(repo.coverage) != 2 {
		t.Fatalf("indexing a known root changed known roots: %v %v", repo.coverage, err)
	}
}

func TestRefreshSkipsRootsRemovedConcurrently(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	repo := newRepository()
	app := App{Repository: repo, Indexer: indexing.Indexer{Collector: collector{}}}
	for _, root := range []string{a, b} {
		if _, err := app.Index(context.Background(), root); err != nil {
			t.Fatal(err)
		}
	}
	// Another writer clears the catalog after refresh listed the roots.
	repo.beforeUpdate = func(r *repository) {
		if r.calls == 3 {
			r.Clear(context.Background())
		}
	}
	outcomes, err := app.Refresh(context.Background())
	if err != nil || len(outcomes) != 2 || !outcomes[0].Skipped || !outcomes[1].Skipped || outcomes[0].Attempted {
		t.Fatalf("outcomes %#v, error %v", outcomes, err)
	}
	if len(repo.coverage) != 0 || len(repo.files) != 0 {
		t.Fatalf("refresh resurrected cleared roots: %#v", repo.coverage)
	}
}

func TestQueryStatusAndExport(t *testing.T) {
	dir := t.TempDir()
	repo := newRepository()
	app := App{Repository: repo, Indexer: indexing.Indexer{Collector: collector{}}}
	if _, err := app.Index(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	result, err := app.Query(context.Background(), "SELECT path FROM files")
	if err != nil || result.Generation != 1 || len(result.Rows) != 1 || result.Rows[0][0] != dir || len(result.Coverage) != 1 {
		t.Fatalf("%#v %v", result, err)
	}
	status, err := app.Status(context.Background())
	if err != nil || status.Files != 1 || status.Generation != 1 {
		t.Fatalf("%#v %v", status, err)
	}
	var exported []string
	err = app.Export(context.Background(), func(h catalog.Header, _ []catalog.Segment) error {
		if h.Generation != 1 {
			t.Fatalf("header %#v", h)
		}
		return nil
	}, func(f catalog.File) error { exported = append(exported, f.Path); return nil })
	if err != nil || len(exported) != 1 {
		t.Fatalf("%v %v", exported, err)
	}
}
