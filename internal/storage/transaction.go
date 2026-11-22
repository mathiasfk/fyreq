package storage

import (
	"context"
	"fmt"

	"fyreq/internal/catalog"
	"fyreq/internal/pathpolicy"
)

// transaction buffers changes over the snapshot read under the writer lock.
// Nothing is written until the update callback succeeds.
type transaction struct {
	snapshot *snapshot
	pending  map[string]*catalog.File // nil value: delete
	coverage map[string]catalog.Coverage
}

func newTransaction(s *snapshot) *transaction {
	return &transaction{snapshot: s, pending: make(map[string]*catalog.File), coverage: s.manifest.header().Coverage}
}

func (t *transaction) Files(ctx context.Context, root string) (map[string]catalog.File, error) {
	if !pathpolicy.Valid(root) {
		return nil, fmt.Errorf("invalid root %q", root)
	}
	files, err := t.snapshot.files(ctx, root)
	if err != nil {
		return nil, err
	}
	for path, file := range t.pending {
		if !pathpolicy.Contains(root, path) {
			continue
		}
		if file == nil {
			delete(files, path)
		} else {
			files[path] = *file
		}
	}
	return files, nil
}

func (t *transaction) Put(file catalog.File) error {
	if !pathpolicy.Valid(file.Path) || file.ID == "" {
		return fmt.Errorf("invalid catalog record at %q", file.Path)
	}
	t.pending[file.Path] = &file
	return nil
}

func (t *transaction) Delete(path string) error {
	if !pathpolicy.Valid(path) {
		return fmt.Errorf("invalid catalog path %q", path)
	}
	t.pending[path] = nil
	return nil
}

func (t *transaction) SetCoverage(c catalog.Coverage) error {
	if !pathpolicy.Valid(c.Root) {
		return fmt.Errorf("invalid coverage root %q", c.Root)
	}
	t.coverage[c.Root] = c
	return nil
}

func (t *transaction) Coverage() map[string]catalog.Coverage {
	result := make(map[string]catalog.Coverage, len(t.coverage))
	for root, c := range t.coverage {
		result[root] = c
	}
	return result
}
