// Package app coordinates catalog operations independently of the user interface.
package app

import (
	"context"
	"fmt"
	"sort"

	"fyreq/internal/catalog"
	"fyreq/internal/indexing"
	"fyreq/internal/pathpolicy"
	"fyreq/internal/query"
)

// UpdateResult reports the commit boundary. Published remains true on a later
// confirmation error, with Generation identifying the visible replacement.
type UpdateResult struct {
	Published  bool   `json:"published"`
	Generation uint64 `json:"generation,omitempty"`
}

// Repository loads snapshots and serializes updates. Update gives the callback a
// private working snapshot that it must not retain. Callback errors or cancellation
// before publication leave the prior generation untouched. A published result and
// error means the replacement is visible but persistence confirmation failed.
// Clear replaces the catalog without loading it and starts generation history anew.
type Repository interface {
	Load(context.Context) (catalog.Snapshot, error)
	Clear(context.Context) (UpdateResult, error)
	Update(context.Context, func(*catalog.Snapshot) error) (UpdateResult, error)
}

type App struct {
	Repository Repository
	Indexer    indexing.Indexer
	Roots      RootResolver
}

// RootResolver selects default work; absence never authorizes deletion.
type RootResolver interface {
	DefaultRoots(context.Context) ([]string, error)
}

type RootOutcome struct {
	Root        string           `json:"root"`
	Attempted   bool             `json:"attempted"`
	Coverage    catalog.Coverage `json:"coverage"`
	Publication UpdateResult     `json:"publication"`
	Error       string           `json:"error,omitempty"`
}

// RefreshRoots resolves and refreshes roots in stable order, committing each separately.
func (a App) RefreshRoots(ctx context.Context, explicit string) ([]RootOutcome, error) {
	var roots []string
	if explicit != "" {
		roots = []string{explicit}
	} else {
		if a.Roots == nil {
			return nil, fmt.Errorf("default root resolver unavailable")
		}
		var err error
		roots, err = a.Roots.DefaultRoots(ctx)
		if err != nil {
			return nil, err
		}
		if len(roots) == 0 {
			return nil, fmt.Errorf("default root resolver returned no roots")
		}
	}
	unique := make(map[string]bool)
	for _, root := range roots {
		if root == "" {
			return nil, fmt.Errorf("empty root")
		}
		normalized, err := pathpolicy.Normalize(root)
		if err != nil {
			return nil, err
		}
		unique[normalized] = true
	}
	roots = make([]string, 0, len(unique))
	for root := range unique {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	outcomes := make([]RootOutcome, len(roots))
	for i, root := range roots {
		outcomes[i].Root = root
	}
	var incomplete bool
	for i, root := range roots {
		if err := ctx.Err(); err != nil {
			return outcomes, err
		}
		outcome := &outcomes[i]
		outcome.Attempted = true
		result, coverage, err := a.Refresh(ctx, root)
		outcome.Coverage = coverage
		outcome.Publication = result
		if err != nil {
			outcome.Error = err.Error()
			return outcomes, err
		}
		if !outcome.Coverage.Complete {
			incomplete = true
		}
	}
	if incomplete {
		return outcomes, fmt.Errorf("scan incomplete; observed records were saved and unobserved records retained")
	}
	return outcomes, nil
}

// Refresh updates one region in a private repository working snapshot.
func (a App) Refresh(ctx context.Context, root string) (UpdateResult, catalog.Coverage, error) {
	root, err := pathpolicy.Normalize(root)
	if err != nil {
		return UpdateResult{}, catalog.Coverage{}, err
	}
	var coverage catalog.Coverage
	result, err := a.Repository.Update(ctx, func(snapshot *catalog.Snapshot) error {
		var scanErr error
		coverage, scanErr = a.Indexer.Refresh(ctx, snapshot, root)
		return scanErr
	})
	return result, coverage, err
}

// QueryResult includes coverage so consumers can assess freshness and gaps.
type QueryResult struct {
	query.Result
	Generation uint64                      `json:"generation"`
	Coverage   map[string]catalog.Coverage `json:"coverage"`
}

func (a App) Query(ctx context.Context, input string) (QueryResult, error) {
	plan, err := query.Parse(input)
	if err != nil {
		return QueryResult{}, err
	}
	snapshot, err := a.Repository.Load(ctx)
	if err != nil {
		return QueryResult{}, err
	}
	result, err := query.Execute(ctx, plan, snapshot.Files)
	return QueryResult{Result: result, Generation: snapshot.Generation, Coverage: snapshot.Coverage}, err
}

type Status struct {
	Generation uint64                      `json:"generation"`
	Files      int                         `json:"files"`
	Coverage   map[string]catalog.Coverage `json:"coverage"`
}

// Clear replaces the catalog even when its previous format cannot be read.
func (a App) Clear(ctx context.Context) (UpdateResult, error) {
	return a.Repository.Clear(ctx)
}

func (a App) Status(ctx context.Context) (Status, error) {
	snapshot, err := a.Repository.Load(ctx)
	if err != nil {
		return Status{}, err
	}
	return Status{Generation: snapshot.Generation, Files: len(snapshot.Files), Coverage: snapshot.Coverage}, nil
}
