package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"fyreq/internal/catalog"
	"fyreq/internal/indexing"
)

type roots []string

func (r roots) DefaultRoots(context.Context) ([]string, error) { return r, nil }

type repository struct {
	snap  catalog.Snapshot
	calls int
}

func (r *repository) Clear(context.Context) (UpdateResult, error) { panic("unexpected clear") }

func (r *repository) Load(context.Context) (catalog.Snapshot, error) { return r.snap, nil }
func (r *repository) Update(ctx context.Context, f func(*catalog.Snapshot) error) (UpdateResult, error) {
	r.calls++
	next := catalog.New()
	for k, v := range r.snap.Files {
		next.Files[k] = v
	}
	for k, v := range r.snap.Coverage {
		next.Coverage[k] = v
	}
	next.Generation = r.snap.Generation
	if err := f(&next); err != nil {
		return UpdateResult{}, err
	}
	next.Generation++
	r.snap = next
	return UpdateResult{Published: true, Generation: next.Generation}, nil
}

type collector struct {
	fail       string
	incomplete string
}

func (c collector) Scan(ctx context.Context, root string, visit func(catalog.File) error) (catalog.Coverage, error) {
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
	repo := &repository{snap: catalog.New()}
	application := App{Repository: repo, Indexer: indexing.Indexer{Collector: collector{fail: b, incomplete: a}}, Roots: roots{c, a, b, a}}
	results, err := application.RefreshRoots(context.Background(), "")
	if err == nil || len(results) != 3 || !results[0].Publication.Published || results[0].Coverage.Complete || !results[1].Attempted || results[1].Publication.Published || results[2].Attempted || repo.snap.Generation != 1 {
		t.Fatalf("results %#v, error %v", results, err)
	}
}

type rootFunc func(context.Context) ([]string, error)

func (f rootFunc) DefaultRoots(ctx context.Context) ([]string, error) { return f(ctx) }

type repoFunc func(context.Context, func(*catalog.Snapshot) error) (UpdateResult, error)

func (f repoFunc) Clear(context.Context) (UpdateResult, error) { panic("unexpected clear") }

func (f repoFunc) Update(ctx context.Context, fn func(*catalog.Snapshot) error) (UpdateResult, error) {
	return f(ctx, fn)
}
func (f repoFunc) Load(context.Context) (catalog.Snapshot, error) { return catalog.New(), nil }

func TestRootResolutionAndExplicitPath(t *testing.T) {
	failure := errors.New("discovery")
	for _, provider := range []rootFunc{func(context.Context) ([]string, error) { return nil, failure }, func(context.Context) ([]string, error) { return nil, nil }} {
		app := App{Roots: provider}
		if _, err := app.RefreshRoots(context.Background(), ""); err == nil {
			t.Fatal("expected resolution error")
		}
	}
	dir := t.TempDir()
	repo := &repository{snap: catalog.New()}
	app := App{Roots: rootFunc(func(context.Context) ([]string, error) { t.Fatal("explicit path discovered roots"); return nil, nil }), Repository: repo, Indexer: indexing.Indexer{Collector: collector{}}}
	result, err := app.RefreshRoots(context.Background(), dir)
	if err != nil || len(result) != 1 || !result[0].Publication.Published {
		t.Fatalf("%#v %v", result, err)
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
		app := App{Roots: roots{a, b}, Repository: repoFunc(func(ctx context.Context, fn func(*catalog.Snapshot) error) (UpdateResult, error) {
			calls++
			if cancelAfter {
				cancel()
				return UpdateResult{Published: true, Generation: 1}, nil
			}
			return UpdateResult{Published: true, Generation: 1}, failure
		})}
		outcomes, err := app.RefreshRoots(ctx, "")
		if err == nil || calls != 1 || !outcomes[0].Publication.Published || outcomes[1].Attempted {
			t.Fatalf("%#v %v calls=%d", outcomes, err, calls)
		}
	}
}
