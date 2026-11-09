// Package app coordinates catalog operations independently of the user interface.
package app

import (
	"context"
	"path/filepath"

	"fyreq/internal/catalog"
	"fyreq/internal/indexing"
	"fyreq/internal/query"
)

type Repository interface {
	Load(context.Context) (catalog.Snapshot, error)
	Update(context.Context, func(*catalog.Snapshot) error) error
}

type App struct {
	Repository Repository
	Indexer    indexing.Indexer
}

func (a App) Refresh(ctx context.Context, root string) (catalog.Coverage, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return catalog.Coverage{}, err
	}
	var report catalog.Coverage
	err = a.Repository.Update(ctx, func(snapshot *catalog.Snapshot) error {
		var scanErr error
		report, scanErr = a.Indexer.Refresh(ctx, snapshot, abs)
		return scanErr
	})
	return report, err
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

func (a App) Status(ctx context.Context) (Status, error) {
	snapshot, err := a.Repository.Load(ctx)
	if err != nil {
		return Status{}, err
	}
	return Status{Generation: snapshot.Generation, Files: len(snapshot.Files), Coverage: snapshot.Coverage}, nil
}
