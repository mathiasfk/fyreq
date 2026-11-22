package query

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"fyreq/internal/catalog"
)

func mayMatch(t *testing.T, where string, stats map[string]Bounds) bool {
	t.Helper()
	plan, err := Parse("SELECT path FROM files WHERE " + where)
	if err != nil {
		t.Fatal(err)
	}
	return plan.Request().MayMatch(func(field string) (Bounds, bool) {
		b, ok := stats[field]
		return b, ok
	})
}

func TestPruningDecisions(t *testing.T) {
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	stats := map[string]Bounds{
		"size":        {Rows: 5, Min: int64(10), Max: int64(20)},
		"owner":       {Rows: 5, Nulls: 5},
		"path":        {Rows: 5, Min: "/a/b", Max: "/a/c"},
		"kind":        {Rows: 5, Min: "file", Max: "file"},
		"modified_at": {Rows: 5, Min: early, Max: early.Add(time.Hour)},
	}
	for where, want := range map[string]bool{
		"size > 20":                                  false,
		"size >= 20":                                 true,
		"size = 5":                                   false,
		"size < 10":                                  false,
		"size <= 10":                                 true,
		"NOT size < 30":                              false,
		"kind != 'file'":                             false,
		"kind = 'file'":                              true,
		"owner = 'root'":                             false,
		"NOT owner = 'root'":                         false,
		"owner IS NULL":                              true,
		"owner IS NOT NULL":                          false,
		"owner = 'root' OR size > 15":                true,
		"owner = 'root' AND size > 15":               false,
		"NOT (owner = 'root' AND size > 15)":         true,
		"NOT (owner = 'root' OR size > 30)":          false,
		"path LIKE '/b%'":                            false,
		"path LIKE '/a/%'":                           true,
		"path NOT LIKE '/a/%'":                       false,
		"path LIKE '/a/b%'":                          true,
		"path LIKE '%x'":                             true,
		"path LIKE '/a/bz' OR path = '/a/0'":         true,
		"path = '/a/0'":                              false,
		"modified_at > '2026-01-01T02:00:00Z'":       false,
		"modified_at >= '2026-01-01T00:00:00+01:00'": true,
		"name = 'missing statistics'":                true,
		"name LIKE 'x%' AND NOT name LIKE 'x%' OR size>0": true,
	} {
		if got := mayMatch(t, where, stats); got != want {
			t.Errorf("%s: MayMatch = %v, want %v", where, got, want)
		}
	}
}

// TestPruningIsSound checks random groups: whenever a record matches, the
// group's bounds must not be pruned.
func TestPruningIsSound(t *testing.T) {
	random := rand.New(rand.NewSource(7))
	names := []string{"a", "ab", "abc", "b", "b%", "ba", "c", ""}
	owners := []string{"root", "alice", "", "\ufffd"}
	conditions := []string{
		"size > 5", "size <= 3", "size = 4", "size != 4", "NOT size >= 2",
		"name LIKE 'a%'", "name NOT LIKE 'a%'", "name LIKE 'ab_'", "name LIKE 'b!%%' ESCAPE '!'", "name = ''",
		"owner IS NULL", "owner IS NOT NULL", "owner = 'root'", "NOT owner = 'root'", "owner LIKE '\ufffd%'",
		"owner = 'root' OR size < 2", "NOT (owner = 'alice' AND size > 3)", "NOT (owner = 'alice' OR name < 'b')",
		"(name >= 'b' OR owner IS NULL) AND NOT size = 0",
	}
	for trial := 0; trial < 2000; trial++ {
		files := make([]catalog.File, 1+random.Intn(4))
		for i := range files {
			files[i] = catalog.File{Path: fmt.Sprintf("/%d", i), Name: names[random.Intn(len(names))], Size: int64(random.Intn(8))}
			if random.Intn(3) > 0 {
				owner := owners[random.Intn(len(owners))]
				files[i].Owner = &owner
			}
		}
		stats := map[string]Bounds{}
		for _, field := range []string{"name", "size", "owner"} {
			b := Bounds{Rows: len(files)}
			for _, f := range files {
				v := value(f, field)
				if v == nil {
					b.Nulls++
					continue
				}
				if b.Min == nil || compareValues(v, b.Min) < 0 {
					b.Min = v
				}
				if b.Max == nil || compareValues(v, b.Max) > 0 {
					b.Max = v
				}
			}
			stats[field] = b
		}
		for _, where := range conditions {
			plan, err := Parse("SELECT path FROM files WHERE " + where)
			if err != nil {
				t.Fatal(err)
			}
			request := plan.Request()
			matched := false
			for _, f := range files {
				matched = matched || request.Match(f)
			}
			if matched && !mayMatch(t, where, stats) {
				t.Fatalf("%s pruned a matching group %#v (stats %#v)", where, files, stats)
			}
		}
	}
}

func TestRequestFields(t *testing.T) {
	plan, err := Parse("SELECT name FROM files WHERE size > 1 AND owner IS NULL ORDER BY modified_at")
	if err != nil {
		t.Fatal(err)
	}
	request := plan.Request()
	if !reflect.DeepEqual(request.FilterFields, []string{"size", "owner"}) || !reflect.DeepEqual(request.Fields, []string{"path", "name", "size", "modified_at", "owner"}) {
		t.Fatalf("request %#v", request)
	}
	plan, err = Parse("SELECT COUNT(*) FROM files")
	if err != nil {
		t.Fatal(err)
	}
	if request := plan.Request(); len(request.Fields) != 0 || len(request.FilterFields) != 0 {
		t.Fatalf("aggregate request reads fields: %#v", request)
	}
}

func TestTopNMatchesFullSort(t *testing.T) {
	random := rand.New(rand.NewSource(3))
	files := Records{}
	for i := 0; i < 200; i++ {
		path := fmt.Sprintf("/f%03d", i)
		files[path] = catalog.File{Path: path, Size: int64(random.Intn(20))}
	}
	full, err := Parse("SELECT path, size FROM files ORDER BY size DESC")
	if err != nil {
		t.Fatal(err)
	}
	all, err := Execute(context.Background(), full, files)
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{1, 7, 200, 500} {
		plan, err := Parse(fmt.Sprintf("SELECT path, size FROM files ORDER BY size DESC LIMIT %d", limit))
		if err != nil {
			t.Fatal(err)
		}
		result, err := Execute(context.Background(), plan, files)
		if err != nil {
			t.Fatal(err)
		}
		if want := all.Rows[:min(limit, len(all.Rows))]; !reflect.DeepEqual(result.Rows, want) {
			t.Fatalf("limit %d: got %v, want %v", limit, result.Rows, want)
		}
	}
}

func TestExplain(t *testing.T) {
	files := Records{"/a": {Path: "/a", Name: "a", Kind: "file", Size: 1}, "/b": {Path: "/b", Name: "b", Kind: "directory"}}
	for sql, want := range map[string][]string{
		"EXPLAIN SELECT name FROM files WHERE (kind = 'file' OR size > 3) AND NOT name LIKE 'x!%%' ESCAPE '!' LIMIT 1": {
			"Project: name", "  Limit: 1", "    Order: path ASC from scan; stops after 1 rows",
			"      Filter: (kind = 'file' OR size > 3) AND NOT name LIKE 'x!%%' ESCAPE '!'",
			"        Scan: files", "          Columns: filter name, kind, size; output path",
			"          Segments: 1, pruned 0", "          Blocks: 1, pruned 0", "          Rows: at most 2 candidates",
		},
		"explain analyze SELECT kind, COUNT(*) FROM files WHERE modified_at < '2026-01-01T00:00:00Z' GROUP BY kind ORDER BY COUNT(*) DESC": {
			"Project: kind, count(*)", "  Sort: count(*) DESC, group keys ASC", "    Aggregate: group by kind; aggregates: count(*)",
			"      Filter: modified_at < '2026-01-01T00:00:00Z'", "        Scan: files", "          Columns: filter modified_at; output kind",
			"          Segments: 1, pruned 0", "          Blocks: 1, pruned 0", "          Rows: read 2, matched 2", "          Bytes read: 0", "Rows returned: 2",
		},
		"EXPLAIN SELECT path FROM files ORDER BY size LIMIT 0": {
			"Project: path", "  Limit: 0", "    Sort: size ASC, path ASC", "      Scan: files skipped (LIMIT 0)",
		},
		"EXPLAIN SELECT COUNT(*) FROM files": {
			"Project: count(*)", "  Sort: group keys ASC", "    Aggregate: single group; aggregates: count(*)", "      Scan: files",
			"        Columns: filter none; output none", "        Segments: 1, pruned 0", "        Blocks: 1, pruned 0", "        Rows: at most 2 candidates",
		},
	} {
		plan, err := Parse(sql)
		if err != nil {
			t.Fatal(err)
		}
		result, err := Execute(context.Background(), plan, files)
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		for _, row := range result.Rows {
			lines = append(lines, row[0].(string))
		}
		if strings.HasPrefix(lines[len(lines)-1], "Execution time: ") {
			lines = lines[:len(lines)-1]
		}
		if !result.Explain || !reflect.DeepEqual(result.Columns, []string{"plan"}) || !reflect.DeepEqual(lines, want) {
			t.Errorf("%s:\n%s", sql, strings.Join(lines, "\n"))
		}
	}
	for _, sql := range []string{"EXPLAIN", "ANALYZE SELECT path FROM files", "EXPLAIN EXPLAIN SELECT path FROM files", "SELECT path FROM files EXPLAIN"} {
		if _, err := Parse(sql); err == nil {
			t.Errorf("accepted %q", sql)
		}
	}
	plan, err := Parse("EXPLAIN ANALYZE SELECT path FROM files WHERE size > 1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Execute(ctx, plan, files); err == nil {
		t.Fatal("canceled analyze succeeded")
	}
}
