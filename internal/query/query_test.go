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
		"SELECT * FROM files ORDER", "SELECT * FROM files ORDER size",
		"SELECT * FROM files ORDER BY", "SELECT * FROM files ORDER BY nope",
		"SELECT * FROM files ORDER BY 'size'", "SELECT * FROM files ORDER BY size,",
		"SELECT * FROM files ORDER BY size ASC DESC", "SELECT * FROM files ORDER BY size DOWN",
		"SELECT * FROM files ORDER BY size NULLS FIRST", "SELECT * FROM files ORDER BY 1",
		"SELECT * FROM files LIMIT 1 ORDER BY size", "SELECT * FROM files ORDER BY size WHERE size > 1",
	} {
		t.Run(sql, func(t *testing.T) {
			if _, err := Parse(sql); err == nil {
				t.Fatal("expected parse error")
			}
		})
	}
}

func TestOrderBy(t *testing.T) {
	zero, high, low := int64(0), int64(100), int64(20)
	alice, bob := "alice", "bob"
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	files := map[string]catalog.File{
		"/a": {Path: "/a", Name: "z", Kind: "file", Size: 2, UID: &high, Owner: &bob, ModifiedAt: late, IndexedAt: early},
		"/b": {Path: "/b", Name: "m", Kind: "file", Size: 10, UID: &zero, Owner: &alice, ModifiedAt: early, IndexedAt: late},
		"/c": {Path: "/c", Name: "m", Kind: "file", Size: 10, UID: &low, Owner: &alice, ModifiedAt: early.In(time.FixedZone("offset", 3600)), IndexedAt: late},
		"/d": {Path: "/d", Name: "a", Kind: "directory", Size: 100},
		"/e": {Path: "/e", Name: "b", Kind: "file", Size: 3},
	}
	for _, tc := range []struct {
		name, sql string
		want      []string
	}{
		{"default", "SELECT path FROM files", []string{"/a", "/b", "/c", "/d", "/e"}},
		{"implicit ascending", "SELECT path FROM files ORDER BY size", []string{"/a", "/e", "/b", "/c", "/d"}},
		{"descending before limit", "SELECT path FROM files WHERE kind = 'file' ORDER BY size DESC LIMIT 2", []string{"/b", "/c"}},
		{"multiple directions", "SELECT path FROM files ORDER BY size DESC, name ASC, path DESC", []string{"/d", "/c", "/b", "/e", "/a"}},
		{"case insensitive", "select path from files order by NAME desc, PATH desc;", []string{"/a", "/c", "/b", "/e", "/d"}},
		{"nullable ascending", "SELECT path FROM files ORDER BY uid ASC", []string{"/b", "/c", "/a", "/d", "/e"}},
		{"nullable descending", "SELECT path FROM files ORDER BY uid DESC", []string{"/a", "/c", "/b", "/d", "/e"}},
		{"null ties", "SELECT path FROM files ORDER BY owner, size DESC", []string{"/b", "/c", "/a", "/d", "/e"}},
		{"nullable string descending", "SELECT path FROM files ORDER BY owner DESC", []string{"/a", "/b", "/c", "/d", "/e"}},
		{"timestamp instants", "SELECT path FROM files WHERE kind = 'file' ORDER BY modified_at DESC", []string{"/a", "/b", "/c", "/e"}},
		{"observation time", "SELECT path FROM files WHERE kind = 'file' ORDER BY indexed_at", []string{"/e", "/a", "/b", "/c"}},
		{"zero limit", "SELECT path FROM files ORDER BY size DESC LIMIT 0", []string{}},
		{"empty matches", "SELECT path FROM files WHERE size > 100 ORDER BY size", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := Parse(tc.sql)
			if err != nil {
				t.Fatal(err)
			}
			// Repetition verifies that equal keys do not inherit map iteration order.
			for range 5 {
				result, err := Execute(context.Background(), plan, files)
				if err != nil {
					t.Fatal(err)
				}
				got := make([]string, 0, len(result.Rows))
				for _, row := range result.Rows {
					got = append(got, row[0].(string))
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
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
