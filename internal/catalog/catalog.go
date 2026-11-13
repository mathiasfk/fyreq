// Package catalog defines the persistent filesystem catalog and its coverage.
package catalog

import (
	"fmt"
	"runtime"
	"time"

	"fyreq/internal/pathpolicy"
)

const Version = 2

// File describes one observed directory entry. Paths are absolute; symbolic links
// describe the link itself, not its target. ID is an internal record identifier.
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

// Coverage describes the last attempt to scan a root. An incomplete attempt does
// not establish that previously recorded, unobserved files have disappeared.
type Coverage struct {
	Root        string    `json:"root"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Complete    bool      `json:"complete"`
	ErrorCount  int       `json:"error_count"`
	Errors      []string  `json:"errors,omitempty"`
}

type Snapshot struct {
	Platform   string              `json:"platform"`
	Version    int                 `json:"version"`
	Generation uint64              `json:"generation"`
	UpdatedAt  time.Time           `json:"updated_at"`
	Files      map[string]File     `json:"files"`
	Coverage   map[string]Coverage `json:"coverage"`
}

func New() Snapshot {
	return Snapshot{Version: Version, Platform: runtime.GOOS, Files: make(map[string]File), Coverage: make(map[string]Coverage)}
}

func (s Snapshot) Validate() error {
	if s.Version == 1 {
		return fmt.Errorf("catalog version 1 has no platform; collect into a new catalog directory")
	}
	if s.Version != Version {
		return fmt.Errorf("unsupported catalog version %d (expected %d)", s.Version, Version)
	}
	if s.Platform != runtime.GOOS {
		return fmt.Errorf("catalog platform %q is incompatible with runtime %q", s.Platform, runtime.GOOS)
	}
	if s.Files == nil || s.Coverage == nil {
		return fmt.Errorf("catalog is missing files or coverage")
	}
	for path, file := range s.Files {
		if !pathpolicy.Valid(path) || file.Path != path || file.ID == "" {
			return fmt.Errorf("invalid catalog record at %q", path)
		}
	}
	for root, coverage := range s.Coverage {
		if !pathpolicy.Valid(root) || coverage.Root != root {
			return fmt.Errorf("invalid coverage root %q", root)
		}
	}
	return nil
}
