package query

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"fyreq/internal/catalog"
)

func TestParseAndExecute(t *testing.T) {
	modified := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	files := map[string]catalog.File{
		"/b": {Path: "/b", Name: "O'Brien", Kind: "file", Size: 20, ModifiedAt: modified},
		"/a": {Path: "/a", Name: "a", Kind: "file", Size: 10, ModifiedAt: modified},
		"/c": {Path: "/c", Kind: "directory", Size: 100, ModifiedAt: modified},
	}
	for _, tc := range []struct {
		name, sql string
		want      [][]any
	}{
		{"projection and filter", "SELECT path, size FROM files WHERE kind = 'file' AND size >= 10 LIMIT 1", [][]any{{"/a", int64(10)}}},
		{"escaped quote", "select path from FILES where name = 'O''Brien';", [][]any{{"/b"}}},
		{"zero limit", "SELECT path FROM files LIMIT 0", [][]any{}},
		{"time comparison", "SELECT path FROM files WHERE modified_at > '2026-01-01T09:00:00-03:00'", [][]any{}},
		{"numeric inequality", "SELECT path FROM files WHERE size != 10 AND size < 100", [][]any{{"/b"}}},
		{"string comparison", "SELECT path FROM files WHERE path <= '/b'", [][]any{{"/a"}, {"/b"}}},
		{"strict numeric", "SELECT path FROM files WHERE size > 10 AND size <= 20", [][]any{{"/b"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := Parse(tc.sql)
			if err != nil {
				t.Fatal(err)
			}
			result, err := Execute(context.Background(), plan, files)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Rows, tc.want) {
				t.Fatalf("rows = %#v, want %#v", result.Rows, tc.want)
			}
		})
	}
}

func TestRejectInvalidQueries(t *testing.T) {
	for _, sql := range []string{
		"", "\xff", "SELECT FROM files", "SELECT nope FROM files", "SELECT * FROM processes",
		"SELECT * FROM files WHERE size = '10'", "SELECT * FROM files WHERE size = -1",
		"SELECT * FROM files WHERE size = 9223372036854775808",
		"SELECT * FROM files WHERE name = unquoted", "SELECT * FROM files WHERE name = 'unfinished",
		"SELECT * FROM files WHERE kind LIKE 'file'", "SELECT * FROM files LIMIT -1",
		"SELECT * FROM files LIMIT '1'", "SELECT * FROM files LIMIT",
		"SELECT * FROM files WHERE modified_at = 'yesterday'",
		"SELECT * FROM files WHERE size >", "SELECT * FROM files WHERE size > 1 AND",
		"SELECT * FROM files; SELECT * FROM files", "SELECT 'path' FROM files",
		"SELECT * FROM files WHERE kind = 'file' OR size = 1",
		"SELECT * FROM files WHERE uid = '0'", "SELECT * FROM files WHERE uid = -1",
		"SELECT * FROM files WHERE owner = root", "SELECT * FROM files WHERE uid = 9223372036854775808",
	} {
		t.Run(sql, func(t *testing.T) {
			if _, err := Parse(sql); err == nil {
				t.Fatal("expected parse error")
			}
		})
	}
}

func TestOwnershipQueriesAndUnavailableValues(t *testing.T) {
	uid, owner := int64(0), "root"
	files := map[string]catalog.File{
		"/known":      {Path: "/known", UID: &uid, Owner: &owner},
		"/unknown":    {Path: "/unknown"},
		"/unresolved": {Path: "/unresolved", UID: &uid},
	}
	for _, tc := range []struct {
		sql  string
		want [][]any
	}{
		{"SELECT path, uid, owner FROM files WHERE owner = 'root' AND uid = 0", [][]any{{"/known", int64(0), "root"}}},
		{"SELECT path FROM files WHERE owner != 'other'", [][]any{{"/known"}}},
		{"SELECT path FROM files WHERE uid >= 0", [][]any{{"/known"}, {"/unresolved"}}},
		{"SELECT uid, owner FROM files WHERE path = '/unknown'", [][]any{{nil, nil}}},
	} {
		plan, err := Parse(tc.sql)
		if err != nil {
			t.Fatal(err)
		}
		result, err := Execute(context.Background(), plan, files)
		if err != nil || !reflect.DeepEqual(result.Rows, tc.want) {
			t.Errorf("%s: got %#v, error %v; want %#v", tc.sql, result.Rows, err, tc.want)
		}
	}
}

func TestWildcardAndCancellation(t *testing.T) {
	plan, err := Parse("SELECT * FROM files")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), plan, map[string]catalog.File{"/a": {Path: "/a"}})
	if err != nil || len(result.Columns) != 9 || len(result.Rows) != 1 {
		t.Fatalf("unexpected result: %#v, %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Execute(ctx, plan, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if _, err := Execute(context.Background(), Plan{}, nil); err == nil {
		t.Fatal("expected invalid plan error")
	}
}
