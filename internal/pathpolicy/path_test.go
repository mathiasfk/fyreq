package pathpolicy

import (
	"path/filepath"
	"testing"
)

func TestContainment(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Root")
	for _, tc := range []struct {
		path string
		want bool
	}{{root, true}, {filepath.Join(root, "child"), true}, {root + "ed", false}, {filepath.Join(filepath.Dir(root), "root", "child"), false}, {"relative", false}} {
		if got := Contains(root, tc.path); got != tc.want {
			t.Errorf("Contains(%q,%q)=%v", root, tc.path, got)
		}
	}
}
