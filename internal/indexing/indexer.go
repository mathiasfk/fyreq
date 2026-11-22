// Package indexing reconciles observed filesystem entries with a catalog.
package indexing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"fyreq/internal/catalog"
	"fyreq/internal/pathpolicy"
)

// Collector invokes callbacks synchronously and sequentially, never after Scan returns.
// Callback errors and cancellation are fatal. Paths must be absolute and normalized
// within root. Complete coverage means the whole region under the link policy was visited.
type Collector interface {
	Scan(context.Context, string, func(catalog.File) error) (catalog.Coverage, error)
}

// Catalog is a private working view of the catalog. Changes are buffered and
// become visible to readers only when the owner publishes them; Files reflects
// the view including buffered changes.
type Catalog interface {
	Files(ctx context.Context, root string) (map[string]catalog.File, error)
	Put(catalog.File) error
	Delete(path string) error
	SetCoverage(catalog.Coverage) error
}

type Indexer struct{ Collector Collector }

// Reconcile mutates its working catalog without rollback; the repository provides
// the publication boundary. Unchanged records are not rewritten, so their
// IndexedAt keeps the time their current values were first recorded. It retains
// unobserved records whenever collection is incomplete. This conservative rule
// prevents inaccessible trees from looking like deletions.
func (i Indexer) Reconcile(ctx context.Context, c Catalog, root string) (catalog.Coverage, error) {
	previous, err := c.Files(ctx, root)
	if err != nil {
		return catalog.Coverage{}, err
	}
	seen := make(map[string]bool)
	report, err := i.Collector.Scan(ctx, root, func(file catalog.File) error {
		if !pathpolicy.Contains(root, file.Path) {
			return fmt.Errorf("collector returned path outside root: %s", file.Path)
		}
		old, exists := previous[file.Path]
		if exists && (old.Identity == "" || file.Identity == "" || old.Identity == file.Identity) {
			file.ID = old.ID
		} else {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return err
			}
			file.ID = hex.EncodeToString(id[:])
		}
		seen[file.Path] = true
		if exists && catalog.SameMetadata(old, file) {
			return nil
		}
		return c.Put(file)
	})
	if err != nil {
		return report, err
	}
	if report.Complete {
		for path := range previous {
			if !seen[path] {
				if err := c.Delete(path); err != nil {
					return report, err
				}
			}
		}
	}
	// Keep the last observation for each requested root, including overlapping
	// roots. Scanning a child does not refresh its parent's coverage timestamp.
	return report, c.SetCoverage(report)
}
