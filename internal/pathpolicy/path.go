// Package pathpolicy defines exact lexical catalog path keys.
package pathpolicy

import (
	"path/filepath"
	"strings"
)

// Normalize returns an absolute, cleaned path without resolving links or case.
func Normalize(path string) (string, error) { return filepath.Abs(path) }

func Valid(path string) bool { return filepath.IsAbs(path) && filepath.Clean(path) == path }

// Contains compares exact path components, including the volume or UNC share.
func Contains(root, path string) bool {
	if !Valid(root) || !Valid(path) {
		return false
	}
	rv, pv := filepath.VolumeName(root), filepath.VolumeName(path)
	if rv != pv {
		return false
	}
	root = strings.TrimPrefix(root, rv)
	path = strings.TrimPrefix(path, pv)
	r := strings.Split(strings.Trim(root, string(filepath.Separator)), string(filepath.Separator))
	p := strings.Split(strings.Trim(path, string(filepath.Separator)), string(filepath.Separator))
	if len(r) == 1 && r[0] == "" {
		return true
	}
	if len(r) > len(p) {
		return false
	}
	for i := range r {
		if r[i] != p[i] {
			return false
		}
	}
	return true
}
