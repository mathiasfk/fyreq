// Package catalog defines the persistent filesystem catalog and its coverage.
package catalog

import (
	"time"
)

// File describes one observed directory entry. Paths are absolute; symbolic links
// describe the link itself, not its target. ID is an internal record identifier.
// IndexedAt is when the current metadata values were first recorded; reobserving
// unchanged metadata does not rewrite the record.
type File struct {
	ID         string    `json:"id"`
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
	IndexedAt  time.Time `json:"indexed_at"`
	Identity   string    `json:"identity,omitempty"`
	// Nil ownership fields mean unavailable. A zero UID is stored as "0".
	OwnerID *string `json:"owner_id"`
	Owner   *string `json:"owner"`
}

// SameMetadata reports whether two observations of a path carry identical
// recorded values, ignoring when they were recorded.
func SameMetadata(a, b File) bool {
	return a.ID == b.ID && a.Path == b.Path && a.Name == b.Name && a.Kind == b.Kind &&
		a.Size == b.Size && a.ModifiedAt.Equal(b.ModifiedAt) && a.Identity == b.Identity &&
		sameOptional(a.OwnerID, b.OwnerID) && sameOptional(a.Owner, b.Owner)
}

func sameOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Coverage describes the last attempt to scan a root. An incomplete attempt does
// not establish that previously recorded, unobserved files have disappeared.
// Coverage roots are the catalog's known roots, revisited by refresh.
type Coverage struct {
	Root        string    `json:"root"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Complete    bool      `json:"complete"`
	ErrorCount  int       `json:"error_count"`
	Errors      []string  `json:"errors,omitempty"`
}

// Header summarizes one published catalog generation without its records.
type Header struct {
	FormatVersion int                 `json:"format_version"`
	Platform      string              `json:"platform"`
	Generation    uint64              `json:"generation"`
	UpdatedAt     time.Time           `json:"updated_at"`
	Entries       uint64              `json:"entries"`
	Coverage      map[string]Coverage `json:"coverage"`
}

// Segment describes one persisted storage unit for diagnostics and plans.
// Base segments hold disjoint path ranges; delta segments overlay them, newest
// first, and may contain tombstones.
type Segment struct {
	Name       string `json:"name"`
	Level      string `json:"level"`
	Records    uint64 `json:"records"`
	Tombstones uint64 `json:"tombstones"`
	Blocks     int    `json:"blocks"`
	Bytes      int64  `json:"bytes"`
	MinPath    string `json:"min_path"`
	MaxPath    string `json:"max_path"`
}
