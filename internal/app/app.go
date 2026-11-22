// Package app coordinates catalog operations independently of the user interface.
package app

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"fyreq/internal/catalog"
	"fyreq/internal/indexing"
	"fyreq/internal/pathpolicy"
	"fyreq/internal/query"
)

// ErrRootNotIndexed reports that a refresh found its root removed from the
// catalog, for example by a concurrent clear, after the roots were listed.
var ErrRootNotIndexed = errors.New("root is no longer indexed")

// UpdateResult reports the commit boundary. Published remains true on a later
// confirmation error, with Generation identifying the visible replacement.
type UpdateResult struct {
	Published  bool   `json:"published"`
	Generation uint64 `json:"generation,omitempty"`
}

// Snapshot is one consistent published generation. It remains readable after
// later publications until closed. Records visits complete records, including
// internal fields, in ascending path order.
type Snapshot interface {
	query.Source
	Header() catalog.Header
	Segments() []catalog.Segment
	Records(context.Context, func(catalog.File) error) error
	Close() error
}

// Transaction is a private working catalog for one update. It must not be
// retained after the update callback returns.
type Transaction interface {
	indexing.Catalog
	Coverage() map[string]catalog.Coverage
}

// Repository opens snapshots and serializes updates. Callback errors or cancellation
// before publication leave the prior generation untouched. A published result and
// error means the replacement is visible but persistence confirmation failed.
// Clear replaces the catalog without loading it and starts generation history anew.
type Repository interface {
	Open(context.Context) (Snapshot, error)
	Clear(context.Context) (UpdateResult, error)
	Update(context.Context, func(Transaction) error) (UpdateResult, error)
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

// RootOutcome reports one root. Skipped means a refresh found the root removed
// from the catalog before its update began, so nothing was collected.
type RootOutcome struct {
	Root        string           `json:"root"`
	Attempted   bool             `json:"attempted"`
	Skipped     bool             `json:"skipped,omitempty"`
	Coverage    catalog.Coverage `json:"coverage"`
	Publication UpdateResult     `json:"publication"`
	Error       string           `json:"error,omitempty"`
}

// Index collects an explicit root, or the platform's default roots, adding each
// root to the catalog's known roots when it is not already known.
func (a App) Index(ctx context.Context, explicit string) ([]RootOutcome, error) {
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
	return a.updateRoots(ctx, roots, false)
}

// Refresh revisits every known root in the current generation. Roots removed
// before their own update starts are skipped rather than re-added.
func (a App) Refresh(ctx context.Context) ([]RootOutcome, error) {
	snapshot, err := a.Repository.Open(ctx)
	if err != nil {
		return nil, err
	}
	coverage := snapshot.Header().Coverage
	if err := snapshot.Close(); err != nil {
		return nil, err
	}
	roots := make([]string, 0, len(coverage))
	for root := range coverage {
		roots = append(roots, root)
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("no indexed roots; run index first")
	}
	sort.Strings(roots)
	return a.updateRoots(ctx, roots, true)
}

// updateRoots updates roots in order, committing each separately.
func (a App) updateRoots(ctx context.Context, roots []string, known bool) ([]RootOutcome, error) {
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
		result, coverage, err := a.update(ctx, root, known)
		if errors.Is(err, ErrRootNotIndexed) && !result.Published {
			outcome.Attempted, outcome.Skipped = false, true
			continue
		}
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

// update reconciles one region in a private repository transaction. When known
// is set, the root must still be indexed under the writer lock, so a refresh
// cannot resurrect a root removed after the roots were listed.
func (a App) update(ctx context.Context, root string, known bool) (UpdateResult, catalog.Coverage, error) {
	root, err := pathpolicy.Normalize(root)
	if err != nil {
		return UpdateResult{}, catalog.Coverage{}, err
	}
	var coverage catalog.Coverage
	result, err := a.Repository.Update(ctx, func(tx Transaction) error {
		if _, ok := tx.Coverage()[root]; known && !ok {
			return ErrRootNotIndexed
		}
		var scanErr error
		coverage, scanErr = a.Indexer.Reconcile(ctx, tx, root)
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
	snapshot, err := a.Repository.Open(ctx)
	if err != nil {
		return QueryResult{}, err
	}
	defer snapshot.Close()
	header := snapshot.Header()
	result, err := query.Execute(ctx, plan, snapshot)
	return QueryResult{Result: result, Generation: header.Generation, Coverage: header.Coverage}, err
}

type Status struct {
	Generation uint64                      `json:"generation"`
	Files      uint64                      `json:"files"`
	Coverage   map[string]catalog.Coverage `json:"coverage"`
}

// Clear replaces the catalog even when its previous format cannot be read.
func (a App) Clear(ctx context.Context) (UpdateResult, error) {
	return a.Repository.Clear(ctx)
}

func (a App) Status(ctx context.Context) (Status, error) {
	snapshot, err := a.Repository.Open(ctx)
	if err != nil {
		return Status{}, err
	}
	defer snapshot.Close()
	header := snapshot.Header()
	return Status{Generation: header.Generation, Files: header.Entries, Coverage: header.Coverage}, nil
}

// Export reads one snapshot: begin receives its header and storage layout,
// then visit receives every record in ascending path order.
func (a App) Export(ctx context.Context, begin func(catalog.Header, []catalog.Segment) error, visit func(catalog.File) error) error {
	snapshot, err := a.Repository.Open(ctx)
	if err != nil {
		return err
	}
	defer snapshot.Close()
	if err := begin(snapshot.Header(), snapshot.Segments()); err != nil {
		return err
	}
	return snapshot.Records(ctx, visit)
}
