// Package indexing reconciles observed filesystem entries with a catalog.
package indexing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"fyreq/internal/catalog"
)

type Collector interface {
	Scan(context.Context, string, func(catalog.File) error) (catalog.Coverage, error)
}

type Indexer struct{ Collector Collector }

// Refresh retains unobserved records whenever collection is incomplete. This
// conservative rule prevents inaccessible trees from looking like deletions.
func (i Indexer) Refresh(ctx context.Context, snapshot *catalog.Snapshot, root string) (catalog.Coverage, error) {
	seen := make(map[string]bool)
	report, err := i.Collector.Scan(ctx, root, func(file catalog.File) error {
		if !catalog.Contains(root, file.Path) {
			return fmt.Errorf("collector returned path outside root: %s", file.Path)
		}
		previous, exists := snapshot.Files[file.Path]
		if exists && (previous.Identity == "" || file.Identity == "" || previous.Identity == file.Identity) {
			file.ID = previous.ID
		} else {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return err
			}
			file.ID = hex.EncodeToString(id[:])
		}
		seen[file.Path] = true
		snapshot.Files[file.Path] = file
		return nil
	})
	if err != nil {
		return report, err
	}
	if report.Complete {
		for path := range snapshot.Files {
			if catalog.Contains(root, path) && !seen[path] {
				delete(snapshot.Files, path)
			}
		}
	}
	// Keep the last observation for each requested root, including overlapping
	// roots. Scanning a child does not refresh its parent's coverage timestamp.
	snapshot.Coverage[root] = report
	return report, nil
}
