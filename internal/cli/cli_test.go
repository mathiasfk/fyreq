package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
