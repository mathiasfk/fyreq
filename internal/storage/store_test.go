//go:build darwin

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"fyreq/internal/catalog"
)

func TestClearReplacesUnreadableCatalogs(t *testing.T) {
	for _, input := range []string{
		`{"version":1,"generation":42,"files":{},"coverage":{}}`,
		`{"version":999,"files":{},"coverage":{}}`,
		`{"version":2,"platform":"other-platform","files":{},"coverage":{}}`,
		`{"version":2,"files":`,
	} {
		t.Run(input, func(t *testing.T) {
			store := Store{Dir: t.TempDir()}
			path := filepath.Join(store.Dir, "catalog.json")
			original := []byte(input)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			assertUnchanged := func() {
				t.Helper()
				if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, original) {
					t.Fatalf("failed clear changed catalog: %s, %v", after, err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if result, err := store.Clear(ctx); !errors.Is(err, context.Canceled) || result.Published {
				t.Fatalf("canceled clear: %#v, %v", result, err)
			}
			assertUnchanged()
			lock := filepath.Join(store.Dir, ".writer-lock")
			if err := os.Mkdir(lock, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Clear(context.Background()); !errors.Is(err, ErrLocked) {
				t.Fatalf("clear ignored writer lock: %v", err)
			}
			assertUnchanged()
			if err := os.Remove(lock); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("publication failed")
			store.publishForTest = func(string, string, string) (bool, error) { return false, failure }
			if result, err := store.Clear(context.Background()); !errors.Is(err, failure) || result.Published {
				t.Fatalf("failed clear: %#v, %v", result, err)
			}
			assertUnchanged()
			store.publishForTest = nil
			result, err := store.Clear(context.Background())
			if err != nil || !result.Published || result.Generation != 1 {
				t.Fatalf("clear: %#v, %v", result, err)
			}
			snapshot, err := store.Load(context.Background())
			if err != nil || snapshot.Generation != 1 || len(snapshot.Files) != 0 || len(snapshot.Coverage) != 0 {
				t.Fatalf("clear did not create a readable empty catalog: %#v, %v", snapshot, err)
			}
			store.publishForTest = func(src, dst, dir string) (bool, error) {
				if err := os.Rename(src, dst); err != nil {
					return false, err
				}
				return true, failure
			}
			if result, err := store.Clear(context.Background()); !errors.Is(err, failure) || !result.Published || result.Generation != 1 {
				t.Fatalf("published clear with confirmation failure: %#v, %v", result, err)
			}
		})
	}
}

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
