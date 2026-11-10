package query

import (
	"strings"
	"testing"
)

func TestLikePatterns(t *testing.T) {
	for _, tc := range []struct {
		pattern, value, escape string
		want                   bool
	}{
		{"", "", "", true}, {"", "a", "", false},
		{"%", "", "", true}, {"%%%", "a\nb", "", true},
		{"_", "é", "", true}, {"_", "😀", "", true}, {"_", "\n", "", true},
		{"_", "", "", false}, {"_", "ab", "", false},
		{"a%b_c", "a\n😀bxc", "", true}, {"foo", "xfoo", "", false},
		{"foo", "foox", "", false}, {"foo", "FOO", "", false},
		{"[a].*+?(){}^$|", "[a].*+?(){}^$|", "", true},
		{`a\b`, `a\b`, "", true},
		{"100!%!_!!", "100%_!", "!", true},
		{"a§%b", "a%b", "§", true},
		{"a%%", "a%", "%", true}, {"a__", "a_", "_", true},
		{"a!b", "ab", "!", true},
		{"%" + strings.Repeat("a%", 100) + "b", strings.Repeat("a", 1000), "", false},
	} {
		t.Run(tc.pattern+"/"+tc.value, func(t *testing.T) {
			var escape rune
			if tc.escape != "" {
				escape = []rune(tc.escape)[0]
			}
			p, err := compileLike(tc.pattern, escape, tc.escape != "")
			if err != nil {
				t.Fatal(err)
			}
			if got := p.matches(tc.value); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
