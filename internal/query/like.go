package query

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Patterns are compiled once per plan. Go's regexp engine avoids exponential
// backtracking; quoting literals keeps LIKE independent of regular expression syntax.
// The literal prefix and source text support pruning and plan descriptions.
type likePattern struct {
	expression *regexp.Regexp
	source     string
	escape     rune
	hasEscape  bool
	prefix     string
	prefixOnly bool
}

func (p *likePattern) matches(value string) bool {
	return p.expression.MatchString(value)
}

func (p *parser) like(field string, negated bool) (*condition, error) {
	switch field {
	case "size", "modified_at", "indexed_at":
		return nil, fmt.Errorf("LIKE requires a string field")
	}
	literal, err := p.next()
	if err != nil {
		return nil, err
	}
	if !literal.quoted {
		return nil, fmt.Errorf("LIKE requires a single-quoted pattern")
	}
	var escape rune
	hasEscape := p.accept("ESCAPE")
	if hasEscape {
		token, err := p.next()
		if err != nil {
			return nil, err
		}
		if !token.quoted || !utf8.ValidString(token.text) || utf8.RuneCountInString(token.text) != 1 {
			return nil, fmt.Errorf("ESCAPE requires a single quoted Unicode character")
		}
		escape, _ = utf8.DecodeRuneInString(token.text)
	}
	pattern, err := compileLike(literal.text, escape, hasEscape)
	if err != nil {
		return nil, err
	}
	c := &condition{predicate: predicate{field: field, op: "LIKE", value: pattern}}
	if negated {
		c = &condition{op: "NOT", left: c}
	}
	return c, nil
}

func compileLike(pattern string, escape rune, hasEscape bool) (*likePattern, error) {
	if !utf8.ValidString(pattern) {
		return nil, fmt.Errorf("LIKE pattern must be valid UTF-8")
	}
	var expression, prefix strings.Builder
	expression.WriteString(`(?s)\A`)
	escaped := false
	percent := false
	literal := true
	wildcardsOnly := true
	for _, r := range pattern {
		if escaped {
			expression.WriteString(regexp.QuoteMeta(string(r)))
			escaped = false
			percent = false
			if literal {
				prefix.WriteRune(r)
			} else {
				wildcardsOnly = false
			}
		} else if hasEscape && r == escape {
			escaped = true
		} else if r == '%' {
			if !percent {
				expression.WriteString(".*")
			}
			percent = true
			literal = false
		} else {
			if r == '_' {
				expression.WriteByte('.')
				literal = false
				wildcardsOnly = false
			} else {
				expression.WriteString(regexp.QuoteMeta(string(r)))
				if literal {
					prefix.WriteRune(r)
				} else {
					wildcardsOnly = false
				}
			}
			percent = false
		}
	}
	if escaped {
		return nil, fmt.Errorf("LIKE pattern ends with an escape character")
	}
	expression.WriteString(`\z`)
	compiled, err := regexp.Compile(expression.String())
	if err != nil {
		return nil, fmt.Errorf("invalid LIKE pattern: %w", err)
	}
	result := &likePattern{expression: compiled, source: pattern, escape: escape, hasEscape: hasEscape}
	// The regexp engine decodes invalid UTF-8 in values as U+FFFD, so a literal
	// U+FFFD can match bytes that do not share the prefix.
	if !strings.ContainsRune(prefix.String(), utf8.RuneError) {
		result.prefix = prefix.String()
		result.prefixOnly = !literal && wildcardsOnly
	}
	return result, nil
}
