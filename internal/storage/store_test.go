package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"fyreq/internal/catalog"
)

func TestPersistenceAndFailedUpdate(t *testing.T) {
	ctx := context.Background()
	store := Store{Dir: t.TempDir()}
	initial, err := store.Load(ctx)
	if err != nil || initial.Generation != 0 || len(initial.Files) != 0 {
		t.Fatalf("unexpected empty catalog: %#v, %v", initial, err)
	}
	path := filepath.Join(t.TempDir(), "a")
	if err := store.Update(ctx, func(s *catalog.Snapshot) error {
		s.Files[path] = catalog.File{ID: "one", Path: path, Size: 12}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reopened := Store{Dir: store.Dir}
	snapshot, err := reopened.Load(ctx)
	if err != nil || snapshot.Generation != 1 || snapshot.Files[path].Size != 12 {
		t.Fatalf("unexpected persisted catalog: %#v, %v", snapshot, err)
	}
	failure := errors.New("collection failed")
	if err := store.Update(ctx, func(s *catalog.Snapshot) error {
		delete(s.Files, path)
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("expected callback failure, got %v", err)
	}
	after, err := reopened.Load(ctx)
	if err != nil || after.Generation != 1 || len(after.Files) != 1 {
		t.Fatalf("failed update changed persisted state: %#v, %v", after, err)
	}
}

func TestWriterLockAndReaders(t *testing.T) {
	ctx := context.Background()
	store := Store{Dir: t.TempDir()}
	if err := store.Update(ctx, func(s *catalog.Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, func(s *catalog.Snapshot) error {
		if err := store.Update(ctx, func(s *catalog.Snapshot) error { return nil }); !errors.Is(err, ErrLocked) {
			t.Fatalf("expected writer lock, got %v", err)
		}
		read, err := store.Load(ctx)
		if err != nil || read.Generation != 1 {
			t.Fatalf("reader cannot access previous snapshot: %#v, %v", read, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledUpdateDoesNotPublish(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	err := store.Update(ctx, func(s *catalog.Snapshot) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	snapshot, err := store.Load(context.Background())
	if err != nil || snapshot.Generation != 0 {
		t.Fatalf("canceled update published: %#v, %v", snapshot, err)
	}
	if err := store.Update(context.Background(), func(s *catalog.Snapshot) error { return nil }); err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
}

func TestRejectCorruptAndUnsupportedCatalog(t *testing.T) {
	for _, input := range []string{
		"{", `{"version":999,"files":{},"coverage":{}}`,
		`{"version":1,"files":{},"coverage":{}} {}`,
		`{"version":1,"files":null,"coverage":{}}`,
		`{"version":1,"files":{"relative":{"id":"x","path":"relative"}},"coverage":{}}`,
	} {
		store := Store{Dir: t.TempDir()}
		if err := os.WriteFile(filepath.Join(store.Dir, "catalog.json"), []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(context.Background()); err == nil {
			t.Fatalf("accepted invalid catalog: %s", input)
		}
	}
}
