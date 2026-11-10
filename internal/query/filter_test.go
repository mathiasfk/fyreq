package query

import (
	"context"
	"reflect"
	"testing"

	"fyreq/internal/catalog"
)

func checkFilter(t *testing.T, where string, want []string) {
	t.Helper()
	owner := "root"
	files := map[string]catalog.File{
		"/a": {Path: "/a", Name: "main.go", Kind: "file", Size: 10, Owner: &owner},
		"/b": {Path: "/b", Name: "readme.md", Kind: "file", Size: 20},
		"/c": {Path: "/c", Name: "c", Kind: "directory", Size: 30},
	}
	plan, err := Parse("SELECT path FROM files WHERE " + where)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), plan, files)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(result.Rows))
	for _, row := range result.Rows {
		got = append(got, row[0].(string))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got %v, want %v", where, got, want)
	}
	plan, err = Parse("SELECT COUNT(*) FROM files WHERE " + where)
	if err != nil {
		t.Fatal(err)
	}
	result, err = Execute(context.Background(), plan, files)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows[0][0] != int64(len(want)) {
		t.Fatalf("grouped filter: %#v", result)
	}
}

func rejectFilters(t *testing.T, filters ...string) {
	t.Helper()
	for _, filter := range filters {
		if _, err := Parse("SELECT path FROM files WHERE " + filter); err == nil {
			t.Errorf("accepted %q", filter)
		}
	}
}

func TestOr(t *testing.T) {
	checkFilter(t, "size = 10 OR size = 30", []string{"/a", "/c"})
	checkFilter(t, "size = 10 OR size = 20 AND kind = 'directory'", []string{"/a"})
	checkFilter(t, "kind = 'directory' AND size = 10 OR size = 20", []string{"/b"})
	checkFilter(t, "owner = 'root' OR size = 20", []string{"/a", "/b"})
	checkFilter(t, "owner = 'other' OR size = 99", []string{})
	checkFilter(t, "size = 10 or size = 10 OR size = 20", []string{"/a", "/b"})
	rejectFilters(t, "OR size = 1", "size = 1 OR", "size = 1 OR OR size = 2", "size = 1 OR nope = 2")
}
