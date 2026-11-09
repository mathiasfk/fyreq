// Package query parses and executes a small SQL-like language over catalog data.
// It has no dependency on storage formats, terminal output, or OS APIs.
package query

import (
	"cmp"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"fyreq/internal/catalog"
)

var fields = []string{"id", "path", "name", "kind", "size", "modified_at", "indexed_at", "uid", "owner"}

type predicate struct {
	field string
	op    string
	value any
}

type ordering struct {
	field      string
	descending bool
}

// Plan is a validated query. Its private predicates cannot bypass type checking.
type Plan struct {
	columns    []string
	predicates []predicate
	order      []ordering
	limit      int
}

type Result struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

type token struct {
	text   string
	quoted bool
}

func lex(input string) ([]token, error) {
	var tokens []token
	for len(input) > 0 {
		r, n := utf8.DecodeRuneInString(input)
		if unicode.IsSpace(r) {
			input = input[n:]
			continue
		}
		if input[0] == '\'' {
			input = input[1:]
			var value strings.Builder
			closed := false
			for len(input) > 0 {
				if input[0] == '\'' {
					if len(input) > 1 && input[1] == '\'' {
						value.WriteByte('\'')
						input = input[2:]
						continue
					}
					input = input[1:]
					closed = true
					break
				}
				value.WriteByte(input[0])
				input = input[1:]
			}
			if !closed {
				return nil, fmt.Errorf("unterminated string literal")
			}
			tokens = append(tokens, token{text: value.String(), quoted: true})
			continue
		}
		if strings.ContainsRune(",*;=<>!", r) {
			end := n
			if len(input) > n && input[n] == '=' && strings.ContainsRune("<>!", r) {
				end++
			}
			tokens = append(tokens, token{text: input[:end]})
			input = input[end:]
			continue
		}
		end := 0
		for end < len(input) {
			ch, width := utf8.DecodeRuneInString(input[end:])
			if unicode.IsSpace(ch) || strings.ContainsRune(",*;=<>!'", ch) {
				break
			}
			end += width
		}
		if end == 0 {
			return nil, fmt.Errorf("invalid query token")
		}
		tokens = append(tokens, token{text: input[:end]})
		input = input[end:]
	}
	return tokens, nil
}

type parser struct {
	tokens []token
	pos    int
}

func (p *parser) accept(s string) bool {
	if p.pos < len(p.tokens) && !p.tokens[p.pos].quoted && strings.EqualFold(p.tokens[p.pos].text, s) {
		p.pos++
		return true
	}
	return false
}

func (p *parser) expect(s string) error {
	if !p.accept(s) {
		return fmt.Errorf("expected %s at token %d", s, p.pos+1)
	}
	return nil
}

func (p *parser) next() (token, error) {
	if p.pos == len(p.tokens) {
		return token{}, fmt.Errorf("unexpected end of query")
	}
	t := p.tokens[p.pos]
	p.pos++
	return t, nil
}

func (p *parser) field() (string, error) {
	t, err := p.next()
	if err != nil {
		return "", err
	}
	name := strings.ToLower(t.text)
	for _, field := range fields {
		if !t.quoted && name == field {
			return name, nil
		}
	}
	return "", fmt.Errorf("unknown field %q", t.text)
}

// Parse accepts SELECT fields FROM files [WHERE comparisons joined by AND]
// [ORDER BY field [ASC|DESC], ...] [LIMIT n]. Keywords and fields are
// case-insensitive; string values are exact.
func Parse(input string) (Plan, error) {
	tokens, err := lex(input)
	if err != nil {
		return Plan{}, err
	}
	p := parser{tokens: tokens}
	plan := Plan{limit: -1}
	if err := p.expect("SELECT"); err != nil {
		return plan, err
	}
	if p.accept("*") {
		plan.columns = append([]string(nil), fields...)
	} else {
		for {
			field, err := p.field()
			if err != nil {
				return plan, err
			}
			plan.columns = append(plan.columns, field)
			if !p.accept(",") {
				break
			}
		}
	}
	if err := p.expect("FROM"); err != nil {
		return plan, err
	}
	if err := p.expect("files"); err != nil {
		return plan, err
	}
	if p.accept("WHERE") {
		for {
			field, err := p.field()
			if err != nil {
				return plan, err
			}
			op, err := p.next()
			if err != nil {
				return plan, err
			}
			if op.quoted || !validOperator(op.text) {
				return plan, fmt.Errorf("unsupported comparison %q", op.text)
			}
			literal, err := p.next()
			if err != nil {
				return plan, err
			}
			var value any
			switch field {
			case "size", "uid":
				v, err := strconv.ParseInt(literal.text, 10, 64)
				if err != nil || literal.quoted || v < 0 {
					return plan, fmt.Errorf("%s requires a non-negative integer", field)
				}
				value = v
			case "modified_at", "indexed_at":
				v, err := time.Parse(time.RFC3339Nano, literal.text)
				if err != nil || !literal.quoted {
					return plan, fmt.Errorf("%s requires a quoted RFC 3339 timestamp", field)
				}
				value = v
			default:
				if !literal.quoted {
					return plan, fmt.Errorf("%s requires a single-quoted string", field)
				}
				value = literal.text
			}
			plan.predicates = append(plan.predicates, predicate{field: field, op: op.text, value: value})
			if !p.accept("AND") {
				break
			}
		}
	}
	if p.accept("ORDER") {
		if err := p.expect("BY"); err != nil {
			return plan, err
		}
		for {
			field, err := p.field()
			if err != nil {
				return plan, err
			}
			term := ordering{field: field}
			if p.accept("DESC") {
				term.descending = true
			} else {
				p.accept("ASC")
			}
			plan.order = append(plan.order, term)
			if !p.accept(",") {
				break
			}
		}
	}
	if p.accept("LIMIT") {
		t, err := p.next()
		if err != nil {
			return plan, err
		}
		n, err := strconv.Atoi(t.text)
		if err != nil || t.quoted || n < 0 {
			return plan, fmt.Errorf("LIMIT requires a non-negative integer")
		}
		plan.limit = n
	}
	p.accept(";")
	if p.pos != len(p.tokens) {
		return plan, fmt.Errorf("unexpected token %q", p.tokens[p.pos].text)
	}
	return plan, nil
}

func validOperator(op string) bool {
	switch op {
	case "=", "!=", "<", "<=", ">", ">=":
		return true
	}
	return false
}

func value(file catalog.File, field string) any {
	switch field {
	case "id":
		return file.ID
	case "path":
		return file.Path
	case "name":
		return file.Name
	case "kind":
		return file.Kind
	case "size":
		return file.Size
	case "modified_at":
		return file.ModifiedAt
	case "indexed_at":
		return file.IndexedAt
	case "uid":
		if file.UID != nil {
			return *file.UID
		}
		return nil
	case "owner":
		if file.Owner != nil {
			return *file.Owner
		}
		return nil
	}
	panic("unvalidated query field")
}

func matches(file catalog.File, p predicate) bool {
	got := value(file, p.field)
	// Unavailable metadata never satisfies a comparison, including inequality.
	if got == nil {
		return false
	}
	comparison := compareValues(got, p.value)
	switch p.op {
	case "=":
		return comparison == 0
	case "!=":
		return comparison != 0
	case "<":
		return comparison < 0
	case "<=":
		return comparison <= 0
	case ">":
		return comparison > 0
	case ">=":
		return comparison >= 0
	}
	return false
}

// compareValues compares non-null values of the same validated field type.
func compareValues(a, b any) int {
	switch a := a.(type) {
	case string:
		return strings.Compare(a, b.(string))
	case int64:
		return cmp.Compare(a, b.(int64))
	case time.Time:
		return a.Compare(b.(time.Time))
	default:
		panic("unvalidated query value type")
	}
}

// Execute filters records, sorts matches, and applies the limit before projection.
// Nulls sort last in both directions, with ascending path as the final tie-breaker.
// Secondary indexes can replace this in-memory full scan later.
func Execute(ctx context.Context, plan Plan, files map[string]catalog.File) (Result, error) {
	result := Result{Columns: append([]string(nil), plan.columns...), Rows: make([][]any, 0)}
	if len(plan.columns) == 0 {
		return result, fmt.Errorf("query plan must be created by Parse")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if plan.limit == 0 {
		return result, nil
	}
	paths := make([]string, 0, len(files))
	for path, file := range files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		match := true
		for _, predicate := range plan.predicates {
			if !matches(file, predicate) {
				match = false
				break
			}
		}
		if match {
			paths = append(paths, path)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		a, b := files[paths[i]], files[paths[j]]
		for _, term := range plan.order {
			av, bv := value(a, term.field), value(b, term.field)
			if av == nil && bv == nil {
				continue
			}
			if av == nil {
				return false
			}
			if bv == nil {
				return true
			}
			comparison := compareValues(av, bv)
			if comparison != 0 {
				if term.descending {
					return comparison > 0
				}
				return comparison < 0
			}
		}
		return paths[i] < paths[j]
	})
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if plan.limit >= 0 && len(paths) > plan.limit {
		paths = paths[:plan.limit]
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		file := files[path]
		row := make([]any, len(plan.columns))
		for j, column := range plan.columns {
			row[j] = value(file, column)
		}
		result.Rows = append(result.Rows, row)
	}
	return result, nil
}
