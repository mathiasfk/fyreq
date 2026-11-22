//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"fyreq/internal/app"
	"fyreq/internal/catalog"
	"fyreq/internal/indexing"
	"fyreq/internal/platform"
	"fyreq/internal/storage"
)

func TestCLIClear(t *testing.T) {
	ctx := context.Background()
	root, data := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(path, []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}
	build := func(dir string) app.App {
		return app.App{Repository: storage.Store{Dir: dir}, Indexer: indexing.Indexer{Collector: platform.Filesystem{}}}
	}
	var out, diagnostics bytes.Buffer
	if err := Run(ctx, []string{"index", root}, data, build, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	store := storage.Store{Dir: data}
	original, err := os.ReadFile(filepath.Join(data, "manifest"))
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	lock := filepath.Join(data, ".writer-lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, []string{"clear"}, data, build, &out, &diagnostics); !errors.Is(err, storage.ErrLocked) {
		t.Fatalf("expected writer lock error, got %v", err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := Run(canceled, []string{"clear"}, data, build, &out, &diagnostics); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if err := Run(ctx, []string{"clear", root}, data, build, &out, &diagnostics); err == nil {
		t.Fatal("clear accepted a path")
	}
	if after, err := os.ReadFile(filepath.Join(data, "manifest")); err != nil || !bytes.Equal(original, after) {
		t.Fatalf("failed clear changed catalog: %v", err)
	}
	out.Reset()
	if err := Run(ctx, []string{"--json", "clear"}, data, build, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var result app.UpdateResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || !result.Published || result.Generation != 1 {
		t.Fatalf("clear result %s: %v", out.String(), err)
	}
	snapshot, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if header := snapshot.Header(); header.Generation != 1 || header.Entries != 0 || len(header.Coverage) != 0 {
		t.Fatalf("catalog was not cleared: %#v", header)
	}
	if contents, err := os.ReadFile(path); err != nil || string(contents) != "contents" {
		t.Fatalf("clear affected indexed file: %v", err)
	}
	for _, dir := range []string{data, filepath.Join(t.TempDir(), "new-catalog")} {
		out.Reset()
		if err := Run(ctx, []string{"clear"}, dir, build, &out, &diagnostics); err != nil || !strings.Contains(out.String(), "Catalog cleared") {
			t.Fatalf("clearing empty catalog: %s, %v", out.String(), err)
		}
	}
}

func TestCLIIndexQueryRefresh(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "example.txt")
	if err := os.WriteFile(path, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	build := func(dir string) app.App {
		return app.App{Repository: storage.Store{Dir: dir}, Indexer: indexing.Indexer{Collector: platform.Filesystem{}}}
	}
	run := func(args ...string) []byte {
		t.Helper()
		var out, diagnostics bytes.Buffer
		if err := Run(context.Background(), args, data, build, &out, &diagnostics); err != nil {
			t.Fatalf("Run(%v): %v; %s", args, err, diagnostics.String())
		}
		return out.Bytes()
	}
	run("index", root)
	var result app.QueryResult
	if err := json.Unmarshal(run("--json", "query", "SELECT name, size FROM files WHERE kind = 'file'"), &result); err != nil {
		t.Fatal(err)
	}
	if result.Generation != 1 || len(result.Rows) != 1 || result.Rows[0][0] != "example.txt" || len(result.Coverage) != 1 {
		t.Fatalf("unexpected query result: %#v", result)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	var failure bytes.Buffer
	if err := Run(context.Background(), []string{"refresh", root}, data, build, &failure, &failure); err == nil {
		t.Fatal("refresh accepted a path")
	}
	run("refresh")
	if err := json.Unmarshal(run("--json", "query", "SELECT path FROM files WHERE kind = 'file'"), &result); err != nil {
		t.Fatal(err)
	}
	if result.Generation != 2 || len(result.Rows) != 0 {
		t.Fatalf("deleted file remained in catalog: %#v", result)
	}
	run("status")
	run("help")
}

func TestCLIOrderByBeforeLimit(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	for name, content := range map[string]string{"small": "a", "large": "abcde", "medium": "abc"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	build := func(dir string) app.App {
		return app.App{Repository: storage.Store{Dir: dir}, Indexer: indexing.Indexer{Collector: platform.Filesystem{}}}
	}
	var out, diagnostics bytes.Buffer
	ctx := context.Background()
	if err := Run(ctx, []string{"index", root}, data, build, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(ctx, []string{"--json", "query", "SELECT name FROM files WHERE kind = 'file' ORDER BY size DESC LIMIT 2"}, data, build, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var result app.QueryResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if want := [][]any{{"large"}, {"medium"}}; !reflect.DeepEqual(result.Rows, want) {
		t.Fatalf("got %#v, want %#v", result.Rows, want)
	}
}

func TestCLIReportsIncompleteScan(t *testing.T) {
	data := t.TempDir()
	build := func(dir string) app.App {
		return app.App{Repository: storage.Store{Dir: dir}, Indexer: indexing.Indexer{Collector: platform.Filesystem{}}}
	}
	var out, diagnostics bytes.Buffer
	err := Run(context.Background(), []string{"index", filepath.Join(t.TempDir(), "missing")}, data, build, &out, &diagnostics)
	if err == nil || diagnostics.Len() == 0 {
		t.Fatal("expected incomplete scan error and diagnostics")
	}
	snapshot, err := (storage.Store{Dir: data}).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if header := snapshot.Header(); header.Generation != 1 || len(header.Coverage) != 1 {
		t.Fatalf("incomplete scan was not persisted: %#v", header)
	}
}

func TestCLIGroupBy(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	for name, content := range map[string]string{"a": "x", "b": "y", "c": "zzzz"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	build := func(dir string) app.App {
		return app.App{Repository: storage.Store{Dir: dir}, Indexer: indexing.Indexer{Collector: platform.Filesystem{}}}
	}
	var out, diagnostics bytes.Buffer
	ctx := context.Background()
	if err := Run(ctx, []string{"index", root}, data, build, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	sql := "SELECT size, COUNT(*), SUM(size) FROM files WHERE kind = 'file' GROUP BY size ORDER BY COUNT(*) DESC LIMIT 1"
	if err := Run(ctx, []string{"--json", "query", sql}, data, build, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var result app.QueryResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if want := [][]any{{float64(1), float64(2), float64(2)}}; !reflect.DeepEqual(result.Rows, want) {
		t.Fatalf("got %#v, want %#v", result.Rows, want)
	}
	if !reflect.DeepEqual(result.Columns, []string{"size", "count(*)", "sum(size)"}) || result.Generation != 1 || len(result.Coverage) != 1 {
		t.Fatalf("unexpected grouped result metadata: %#v", result)
	}
}

func TestCLIBooleanPatternFilters(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	for _, name := range []string{"main.go", "main_test.go", "README.md", "notes.txt", "report_%.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	build := func(dir string) app.App {
		return app.App{Repository: storage.Store{Dir: dir}, Indexer: indexing.Indexer{Collector: platform.Filesystem{}}}
	}
	var out, diagnostics bytes.Buffer
	ctx := context.Background()
	if err := Run(ctx, []string{"index", root}, data, build, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		sql  string
		want [][]any
	}{
		{"SELECT name FROM files WHERE (name LIKE '%.go' OR name LIKE '%.md') AND NOT (name LIKE '%_test.go') AND path IS NOT NULL AND name NOT LIKE 'notes%' ORDER BY name", [][]any{{"README.md"}, {"main.go"}}},
		{"SELECT name FROM files WHERE name LIKE 'report!_!%.txt' ESCAPE '!'", [][]any{{"report_%.txt"}}},
		{"SELECT kind, COUNT(*) FROM files WHERE NOT (name NOT LIKE '%.go') GROUP BY kind", [][]any{{"file", float64(2)}}},
	} {
		out.Reset()
		if err := Run(ctx, []string{"--json", "query", tc.sql}, data, build, &out, &diagnostics); err != nil {
			t.Fatal(err)
		}
		var result app.QueryResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(result.Rows, tc.want) || result.Generation != 1 || len(result.Coverage) != 1 {
			t.Fatalf("%s: unexpected result %#v", tc.sql, result)
		}
	}
}

func TestCLIExportAndExplain(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	for _, name := range []string{"b.txt", "a.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	build := func(dir string) app.App {
		return app.App{Repository: storage.Store{Dir: dir}, Indexer: indexing.Indexer{Collector: platform.Filesystem{}}}
	}
	run := func(args ...string) (string, string) {
		t.Helper()
		var out, diagnostics bytes.Buffer
		if err := Run(context.Background(), args, data, build, &out, &diagnostics); err != nil {
			t.Fatalf("Run(%v): %v; %s", args, err, diagnostics.String())
		}
		return out.String(), diagnostics.String()
	}
	empty, _ := run("export")
	var export struct {
		FormatVersion int                         `json:"format_version"`
		Generation    uint64                      `json:"generation"`
		Entries       uint64                      `json:"entries"`
		Coverage      map[string]catalog.Coverage `json:"coverage"`
		Segments      []catalog.Segment           `json:"segments"`
		Files         []catalog.File              `json:"files"`
	}
	if err := json.Unmarshal([]byte(empty), &export); err != nil || export.Generation != 0 || export.Files == nil || export.Segments == nil {
		t.Fatalf("empty export %s: %v", empty, err)
	}
	run("index", root)
	out, _ := run("export")
	if err := json.Unmarshal([]byte(out), &export); err != nil {
		t.Fatalf("export %s: %v", out, err)
	}
	if export.FormatVersion != 1 || export.Generation != 1 || export.Entries != 3 || len(export.Files) != 3 || len(export.Segments) != 1 || len(export.Coverage) != 1 {
		t.Fatalf("unexpected export: %s", out)
	}
	if export.Files[0].Path != root || export.Files[1].Name != "a.txt" || export.Files[2].Name != "b.txt" || export.Files[1].Identity == "" {
		t.Fatalf("export records: %#v", export.Files)
	}
	var failure bytes.Buffer
	if err := Run(context.Background(), []string{"export", "extra"}, data, build, &failure, &failure); err == nil {
		t.Fatal("export accepted an argument")
	}
	plan, diagnostics := run("query", "EXPLAIN SELECT name FROM files WHERE kind = 'file' ORDER BY size DESC LIMIT 1")
	for _, want := range []string{"Project: name\n", "  Limit: 1\n", "Top-N sort: size DESC, path ASC", "Filter: kind = 'file'", "Scan: files", "Columns: filter kind; output path, name, size", "Rows: at most 3 candidates"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan missing %q:\n%s", want, plan)
		}
	}
	if !strings.Contains(diagnostics, "Catalog generation 1") {
		t.Errorf("diagnostics: %s", diagnostics)
	}
	analyzed, _ := run("query", "EXPLAIN ANALYZE SELECT kind, COUNT(*) FROM files GROUP BY kind")
	for _, want := range []string{"Aggregate: group by kind; aggregates: count(*)", "Rows: read 3, matched 3", "Rows returned: 2", "Execution time:"} {
		if !strings.Contains(analyzed, want) {
			t.Errorf("analyze missing %q:\n%s", want, analyzed)
		}
	}
	quoted, _ := run("query", "EXPLAIN SELECT path FROM files WHERE name = 'a\nb\x1b'")
	if !strings.Contains(quoted, `\n`) || strings.Contains(quoted, "\x1b") {
		t.Errorf("unsafe plan output: %q", quoted)
	}
	jsonPlan, _ := run("--json", "query", "EXPLAIN SELECT path FROM files")
	var result app.QueryResult
	if err := json.Unmarshal([]byte(jsonPlan), &result); err != nil || !result.Explain || !reflect.DeepEqual(result.Columns, []string{"plan"}) || result.Generation != 1 {
		t.Fatalf("json plan %s: %v", jsonPlan, err)
	}
}
