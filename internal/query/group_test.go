package query

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"fyreq/internal/catalog"
)

func TestGroupedQueries(t *testing.T) {
	rootUID, aliceUID := int64(0), int64(501)
	root, alice, empty := "root", "alice", ""
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	files := map[string]catalog.File{
		"/a": {Path: "/a", Name: "a", Kind: "file", Size: 2, UID: &rootUID, Owner: &root, ModifiedAt: early},
		"/b": {Path: "/b", Name: "b", Kind: "file", Size: 10, UID: &rootUID, Owner: &root, ModifiedAt: late},
		"/c": {Path: "/c", Name: "c", Kind: "file", Size: 3, UID: &aliceUID, Owner: &alice, ModifiedAt: late},
		"/d": {Path: "/d", Name: "d", Kind: "directory", Size: 20, UID: &aliceUID, Owner: &alice, ModifiedAt: late},
		"/e": {Path: "/e", Name: "e", Kind: "file", Size: 1},
		"/f": {Path: "/f", Name: "f", Kind: "file", Size: 1},
		"/g": {Path: "/g", Name: "g", Kind: "file", Size: 1, Owner: &empty},
	}
	for _, tc := range []struct {
		name, sql string
		columns   []string
		rows      [][]any
	}{
		{"counts", "SELECT kind, COUNT(*) FROM files GROUP BY kind", []string{"kind", "count(*)"}, [][]any{{"directory", int64(1)}, {"file", int64(6)}}},
		{"numeric aggregates", "SELECT owner, COUNT(*), COUNT(uid), SUM(size), AVG(size), MIN(size), MAX(size) FROM files WHERE kind = 'file' GROUP BY owner", nil,
			[][]any{{"", int64(1), int64(0), int64(1), float64(1), int64(1), int64(1)}, {"alice", int64(1), int64(1), int64(3), float64(3), int64(3), int64(3)}, {"root", int64(2), int64(2), int64(12), float64(6), int64(2), int64(10)}, {nil, int64(2), int64(0), int64(2), float64(1), int64(1), int64(1)}}},
		{"multiple keys", "SELECT owner, kind, COUNT(*) FROM files GROUP BY owner, kind ORDER BY owner DESC, kind", nil,
			[][]any{{"root", "file", int64(2)}, {"alice", "directory", int64(1)}, {"alice", "file", int64(1)}, {"", "file", int64(1)}, {nil, "file", int64(2)}}},
		{"ordering by aggregate", "SELECT owner, SUM(size) FROM files GROUP BY owner ORDER BY SUM(size) DESC LIMIT 1", nil, [][]any{{"alice", int64(23)}}},
		{"hidden aggregate", "SELECT kind FROM files GROUP BY kind ORDER BY COUNT(*) DESC LIMIT 1", nil, [][]any{{"file"}}},
		{"hidden group key", "SELECT COUNT(*) FROM files GROUP BY kind ORDER BY kind DESC", nil, [][]any{{int64(6)}, {int64(1)}}},
		{"groups without aggregates", "SELECT kind FROM files GROUP BY kind", nil, [][]any{{"directory"}, {"file"}}},
		{"global aggregates", "select count(*), sum(size), avg(size), min(name), max(modified_at) from files", nil, [][]any{{int64(7), int64(38), float64(38) / 7, "a", late}}},
		{"all null aggregate input", "SELECT COUNT(uid), SUM(uid), AVG(uid), MIN(uid), MAX(uid) FROM files WHERE name > 'd'", nil, [][]any{{int64(0), nil, nil, nil, nil}}},
		{"empty global input", "SELECT COUNT(*), COUNT(owner), SUM(size), AVG(size), MIN(name), MAX(size) FROM files WHERE size > 100", nil, [][]any{{int64(0), int64(0), nil, nil, nil, nil}}},
		{"empty grouped input", "SELECT kind, COUNT(*) FROM files WHERE size > 100 GROUP BY kind", nil, [][]any{}},
		{"zero limit", "SELECT kind, COUNT(*) FROM files GROUP BY kind LIMIT 0", nil, [][]any{}},
		{"nullable order", "SELECT owner, MAX(uid) FROM files GROUP BY owner ORDER BY MAX(uid) DESC, owner DESC", nil, [][]any{{"alice", int64(501)}, {"root", int64(0)}, {"", nil}, {nil, nil}}},
		{"duplicate expressions", "SELECT COUNT(*), COUNT(*) FROM files", nil, [][]any{{int64(7), int64(7)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := Parse(tc.sql)
			if err != nil {
				t.Fatal(err)
			}
			for range 3 {
				result, err := Execute(context.Background(), plan, files)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(result.Rows, tc.rows) {
					t.Fatalf("got %#v, want %#v", result.Rows, tc.rows)
				}
				if tc.columns != nil && !reflect.DeepEqual(result.Columns, tc.columns) {
					t.Fatalf("columns = %v, want %v", result.Columns, tc.columns)
				}
			}
		})
	}
}

func TestInvalidGrouping(t *testing.T) {
	for _, sql := range []string{
		"SELECT path, COUNT(*) FROM files", "SELECT * FROM files GROUP BY kind",
		"SELECT path FROM files GROUP BY kind", "SELECT kind FROM files GROUP BY kind ORDER BY size",
		"SELECT COUNT(*) FROM files ORDER BY path", "SELECT COUNT() FROM files",
		"SELECT SUM(*) FROM files", "SELECT AVG(owner) FROM files", "SELECT SUM(path) FROM files",
		"SELECT COUNT(nope) FROM files", "SELECT MEDIAN(size) FROM files",
		"SELECT COUNT(SUM(size)) FROM files", "SELECT COUNT(DISTINCT owner) FROM files",
		"SELECT COUNT(size, uid) FROM files", "SELECT COUNT(size FROM files",
		"SELECT kind FROM files GROUP kind", "SELECT kind FROM files GROUP BY",
		"SELECT kind FROM files GROUP BY kind,", "SELECT kind FROM files GROUP BY COUNT(*)",
		"SELECT kind FROM files GROUP BY 'kind'", "SELECT kind FROM files GROUP BY 1",
		"SELECT kind FROM files ORDER BY kind GROUP BY kind", "SELECT kind FROM files LIMIT 1 GROUP BY kind",
		"SELECT kind FROM files GROUP BY kind HAVING COUNT(*) > 1",
		"SELECT COUNT(*) AS total FROM files", "SELECT kind FROM files WHERE COUNT(*) > 1 GROUP BY kind",
	} {
		t.Run(sql, func(t *testing.T) {
			if _, err := Parse(sql); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestGroupingKeys(t *testing.T) {
	instant := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	files := map[string]catalog.File{
		"/a": {Name: "a|b", Kind: "c", ModifiedAt: instant},
		"/b": {Name: "a", Kind: "b|c", ModifiedAt: instant.In(time.FixedZone("offset", 3600))},
	}
	plan, err := Parse("SELECT COUNT(*) FROM files GROUP BY name, kind")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), plan, files)
	if err != nil || !reflect.DeepEqual(result.Rows, [][]any{{int64(1)}, {int64(1)}}) {
		t.Fatalf("tuple keys collided: %#v, %v", result.Rows, err)
	}
	plan, err = Parse("SELECT modified_at, COUNT(*) FROM files GROUP BY modified_at")
	if err != nil {
		t.Fatal(err)
	}
	result, err = Execute(context.Background(), plan, files)
	if err != nil || !reflect.DeepEqual(result.Rows, [][]any{{instant, int64(2)}}) {
		t.Fatalf("equivalent timestamps were not grouped: %#v, %v", result.Rows, err)
	}
}

func TestAggregationOverflowAndCancellation(t *testing.T) {
	files := map[string]catalog.File{"/a": {Size: math.MaxInt64}, "/b": {Size: math.MaxInt64}}
	plan, err := Parse("SELECT SUM(size) FROM files")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), plan, files); err == nil || !strings.Contains(err.Error(), "64-bit") {
		t.Fatalf("expected sum overflow, got %v", err)
	}
	plan, err = Parse("SELECT AVG(size) FROM files")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), plan, files)
	if err != nil || !reflect.DeepEqual(result.Rows, [][]any{{float64(math.MaxInt64)}}) {
		t.Fatalf("average overflowed: %#v, %v", result.Rows, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Execute(ctx, plan, files); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	result, err = Execute(context.Background(), plan, nil)
	if err != nil || !reflect.DeepEqual(result.Rows, [][]any{{nil}}) {
		t.Fatalf("unexpected empty catalog aggregate: %#v, %v", result.Rows, err)
	}
}
