//go:build darwin

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"fyreq/internal/app"
	"fyreq/internal/indexing"
	"fyreq/internal/platform"
	"fyreq/internal/storage"
)

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
	run("refresh", root)
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
	snapshot, err := (storage.Store{Dir: data}).Load(context.Background())
	if err != nil || snapshot.Generation != 1 || len(snapshot.Coverage) != 1 {
		t.Fatalf("incomplete scan was not persisted: %#v, %v", snapshot, err)
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
