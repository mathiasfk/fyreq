package catalog

import "testing"

func TestContains(t *testing.T) {
	for _, tc := range []struct {
		root, path string
		want       bool
	}{
		{"/work", "/work", true},
		{"/work", "/work/a", true},
		{"/work", "/workspace/a", false},
		{"/work", "/work/../elsewhere", false},
		{"/", "/work/a", true},
	} {
		if got := Contains(tc.root, tc.path); got != tc.want {
			t.Errorf("Contains(%q, %q) = %t, want %t", tc.root, tc.path, got, tc.want)
		}
	}
}
