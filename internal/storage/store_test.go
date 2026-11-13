//go:build darwin

package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"fyreq/internal/catalog"
)

func TestLegacyCatalogRejectedWithoutModification(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	path := filepath.Join(store.Dir, "catalog.json")
	data := []byte(`{"version":1,"files":{},"coverage":{}}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(context.Background(), func(*catalog.Snapshot) error { t.Fatal("mutation called"); return nil }); err == nil {
		t.Fatal("legacy catalog accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Fatalf("legacy catalog changed: %s %v", after, err)
	}
}

func TestPersistenceAndFailedUpdate(t *testing.T) {
	ctx := context.Background()
	store := Store{Dir: t.TempDir()}
	initial, err := store.Load(ctx)
	if err != nil || initial.Generation != 0 || len(initial.Files) != 0 {
		t.Fatalf("unexpected empty catalog: %#v, %v", initial, err)
	}
	path := filepath.Join(t.TempDir(), "a")
	if _, err := store.Update(ctx, func(s *catalog.Snapshot) error {
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
	if _, err := store.Update(ctx, func(s *catalog.Snapshot) error {
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
	if _, err := store.Update(ctx, func(s *catalog.Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(ctx, func(s *catalog.Snapshot) error {
		if _, err := store.Update(ctx, func(s *catalog.Snapshot) error { return nil }); !errors.Is(err, ErrLocked) {
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
	_, err := store.Update(ctx, func(s *catalog.Snapshot) error {
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
	if _, err := store.Update(context.Background(), func(s *catalog.Snapshot) error { return nil }); err != nil {
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

func TestPublicationOutcomes(t *testing.T) {
	ctx := context.Background()
	failure := errors.New("confirmation failed")
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			store := Store{Dir: t.TempDir()}
			store.publishForTest = func(src, dst, dir string) (bool, error) {
				if after {
					if err := os.Rename(src, dst); err != nil {
						t.Fatal(err)
					}
				}
				return after, failure
			}
			result, err := store.Update(ctx, func(*catalog.Snapshot) error { return nil })
			if !errors.Is(err, failure) || result.Published != after || (after && result.Generation != 1) {
				t.Fatalf("result: %#v %v", result, err)
			}
			snap, err := store.Load(ctx)
			if err != nil || (after && snap.Generation != 1) || (!after && snap.Generation != 0) {
				t.Fatalf("visibility: %#v %v", snap, err)
			}
		})
	}
}

func TestOwnershipRoundTrip(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	root := t.TempDir()
	zero := "0"
	_, err := store.Update(context.Background(), func(s *catalog.Snapshot) error {
		a, b := filepath.Join(root, "known"), filepath.Join(root, "unknown")
		s.Files[a] = catalog.File{ID: "a", Path: a, OwnerID: &zero}
		s.Files[b] = catalog.File{ID: "b", Path: b}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a, b := s.Files[filepath.Join(root, "known")], s.Files[filepath.Join(root, "unknown")]
	if a.OwnerID == nil || *a.OwnerID != "0" || b.OwnerID != nil {
		t.Fatalf("ownership: %#v %#v", a, b)
	}
}
