package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"fyreq/internal/app"
	"fyreq/internal/catalog"
)

type failingRepository struct{}

func (failingRepository) Clear(context.Context) (app.UpdateResult, error) {
	return app.UpdateResult{Published: true, Generation: 7}, errors.New("sync failed")
}

func (failingRepository) Load(context.Context) (catalog.Snapshot, error) { return catalog.New(), nil }
func (failingRepository) Update(context.Context, func(*catalog.Snapshot) error) (app.UpdateResult, error) {
	return app.UpdateResult{Published: true, Generation: 7}, errors.New("sync failed")
}

func TestClearPublishedError(t *testing.T) {
	build := func(string) app.App { return app.App{Repository: failingRepository{}} }
	var out bytes.Buffer
	err := Run(context.Background(), []string{"--json", "clear"}, "", build, &out, &out)
	var result app.UpdateResult
	if err == nil || !strings.Contains(err.Error(), "generation 7 was published") || json.Unmarshal(out.Bytes(), &result) != nil || !result.Published || result.Generation != 7 {
		t.Fatalf("clear result %s, error %v", out.String(), err)
	}
}
func TestJSONPublishedErrorAndEmptyPath(t *testing.T) {
	build := func(string) app.App { return app.App{Repository: failingRepository{}} }
	var out, diagnostics bytes.Buffer
	err := Run(context.Background(), []string{"--json", "index", t.TempDir()}, "", build, &out, &diagnostics)
	var results []app.RootOutcome
	if err == nil || json.Unmarshal(out.Bytes(), &results) != nil || len(results) != 1 || !results[0].Publication.Published || results[0].Publication.Generation != 7 || results[0].Error == "" {
		t.Fatalf("%s %v", out.String(), err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"index", ""}, "", build, &out, &diagnostics); err == nil {
		t.Fatal("empty explicit path accepted")
	}
}
