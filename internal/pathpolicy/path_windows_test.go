package pathpolicy

import "testing"

func TestWindowsContainment(t *testing.T) {
	for _, tc := range []struct {
		root string
		path string
		want bool
	}{
		{"C:\\", "C:\\Projects\\file.go", true},
		{"C:\\", "D:\\Projects\\file.go", false},
		{"C:\\Projects", "C:\\projects\\file.go", false},
		{"\\\\server\\share", "\\\\server\\share\\dir\\file.go", true},
		{"\\\\server\\share", "\\\\server\\share2\\dir\\file.go", false},
	} {
		if got := Contains(tc.root, tc.path); got != tc.want {
			t.Errorf("Contains(%q, %q) = %v, want %v", tc.root, tc.path, got, tc.want)
		}
	}
}
