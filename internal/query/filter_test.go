package query

import (
	"context"
	"reflect"
	"strings"
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

func TestNotAndParentheses(t *testing.T) {
	checkFilter(t, "(size = 10 OR size = 20) AND kind = 'directory'", []string{})
	checkFilter(t, "NOT kind = 'directory' AND size >= 20", []string{"/b"})
	checkFilter(t, "NOT (kind = 'directory' OR size = 20)", []string{"/a"})
	checkFilter(t, "NOT NOT (size = 10)", []string{"/a"})
	checkFilter(t, "NOT owner = 'other'", []string{"/a"})
	checkFilter(t, "NOT (owner = 'other' AND size = 10)", []string{"/a", "/b", "/c"})
	checkFilter(t, "NOT (owner = 'other' OR size = 10)", []string{})
	checkFilter(t, "not (size = 20 or (size = 30 and kind = 'directory'))", []string{"/a"})
	rejectFilters(t, "NOT", "()", "(size = 10", "size = 10)", "(size = 10 OR)", "NOT AND size = 10", strings.Repeat("NOT ", 129)+"size = 10", strings.Repeat("(", 129)+"size = 10"+strings.Repeat(")", 129))
}

func TestNullPredicates(t *testing.T) {
	checkFilter(t, "owner IS NULL", []string{"/b", "/c"})
	checkFilter(t, "owner IS NOT NULL", []string{"/a"})
	checkFilter(t, "uid IS NULL", []string{"/a", "/b", "/c"})
	checkFilter(t, "uid IS NOT NULL", []string{})
	checkFilter(t, "name IS NULL OR size IS NULL OR modified_at IS NULL", []string{})
	checkFilter(t, "path is not null AND NOT (owner IS NOT NULL)", []string{"/b", "/c"})
	checkFilter(t, "owner IS NULL AND size = 20 OR owner IS NOT NULL", []string{"/a", "/b"})
	rejectFilters(t, "owner IS", "owner IS NOT", "owner IS 'NULL'", "owner IS NOT 'NULL'", "owner IS TRUE", "owner = NULL", "nope IS NULL", "owner IS NULL NULL")
	zero, empty := int64(0), ""
	files := map[string]catalog.File{"/a": {Path: "/a", UID: &zero, Owner: &empty}}
	plan, err := Parse("SELECT path FROM files WHERE uid IS NOT NULL AND owner IS NOT NULL")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), plan, files)
	if err != nil || len(result.Rows) != 1 {
		t.Fatalf("zero and empty string are not null: %#v, %v", result, err)
	}
}
