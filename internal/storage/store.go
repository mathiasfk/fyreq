// Package storage implements versioned catalog persistence using the standard library.
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"fyreq/internal/app"
	"fyreq/internal/catalog"
)

var ErrLocked = errors.New("catalog is locked by another writer; if a writer crashed, remove the .writer-lock directory only after verifying that no writer is running")

type Store struct {
	Dir            string
	publishForTest func(string, string, string) (bool, error)
}

func (s Store) Load(ctx context.Context) (catalog.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return catalog.Snapshot{}, err
	}
	f, err := os.Open(filepath.Join(s.Dir, "catalog.json"))
	if errors.Is(err, os.ErrNotExist) {
		return catalog.New(), nil
	}
	if err != nil {
		return catalog.Snapshot{}, err
	}
	defer f.Close()
	var snapshot catalog.Snapshot
	decoder := json.NewDecoder(f)
	if err := decoder.Decode(&snapshot); err != nil {
		return snapshot, fmt.Errorf("decode catalog: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return snapshot, fmt.Errorf("catalog contains trailing data")
	}
	return snapshot, snapshot.Validate()
}

// Update holds the writer lock across loading, mutation, and publication. A failed
// callback leaves the previous snapshot untouched. Readers need no writer lock.
func (s Store) Update(ctx context.Context, mutate func(*catalog.Snapshot) error) (app.UpdateResult, error) {
	if err := ctx.Err(); err != nil {
		return app.UpdateResult{}, err
	}
	if err := publicationSupported(); err != nil {
		return app.UpdateResult{}, err
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return app.UpdateResult{}, err
	}
	lock := filepath.Join(s.Dir, ".writer-lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return app.UpdateResult{}, ErrLocked
		}
		return app.UpdateResult{}, err
	}
	defer os.Remove(lock)
	snapshot, err := s.Load(ctx)
	if err != nil {
		return app.UpdateResult{}, err
	}
	if err := mutate(&snapshot); err != nil {
		return app.UpdateResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return app.UpdateResult{}, err
	}
	snapshot.Generation++
	snapshot.UpdatedAt = time.Now().UTC()
	if err := snapshot.Validate(); err != nil {
		return app.UpdateResult{}, err
	}
	published, err := s.save(ctx, snapshot)
	if published {
		return app.UpdateResult{Published: true, Generation: snapshot.Generation}, err
	}
	return app.UpdateResult{}, err
}

func (s Store) save(ctx context.Context, snapshot catalog.Snapshot) (bool, error) {
	f, err := os.CreateTemp(s.Dir, ".catalog-*.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(snapshot); err != nil {
		return false, err
	}
	if err := f.Sync(); err != nil {
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	commit := publish
	if s.publishForTest != nil {
		commit = s.publishForTest
	}
	return commit(f.Name(), filepath.Join(s.Dir, "catalog.json"), s.Dir)
}
