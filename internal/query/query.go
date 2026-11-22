// Package query parses and executes a small SQL-like language over catalog data.
// It has no dependency on storage formats, terminal output, or OS APIs.
package query

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"fyreq/internal/catalog"
)

var fields = []string{"id", "path", "name", "kind", "size", "modified_at", "indexed_at", "owner_id", "owner"}

type predicate struct {
	field string
	op    string
	value any
}

type ordering struct {
	expression expression
	descending bool
}

type expression struct {
	field     string
	aggregate string
}

func (e expression) label() string {
	if e.aggregate != "" {
		return e.aggregate + "(" + e.field + ")"
	}
	return e.field
}

// Plan is a validated query. Its private predicates cannot bypass type checking.
type Plan struct {
	columns    []string
	selections []expression
	filter     *condition
	groupBy    []string
	grouped    bool
	order      []ordering
	limit      int
	explain    bool
	analyze    bool
}

// Result rows are projected values. Explain results have one plan column
// whose rows are human-readable plan lines.
type Result struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
	Explain bool     `json:"explain,omitempty"`
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
		if strings.ContainsRune(",*;=<>!()", r) {
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
			if unicode.IsSpace(ch) || strings.ContainsRune(",*;=<>!'()", ch) {
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
	depth  int
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

func (p *parser) expression() (expression, error) {
	if p.pos+1 < len(p.tokens) && !p.tokens[p.pos].quoted &&
		!p.tokens[p.pos+1].quoted && p.tokens[p.pos+1].text == "(" {
		function := strings.ToLower(p.tokens[p.pos].text)
		switch function {
		case "count", "sum", "avg", "min", "max":
		default:
			return expression{}, fmt.Errorf("unsupported aggregate %q", function)
		}
		p.pos += 2
		field := "*"
		if !p.accept("*") {
			var err error
			field, err = p.field()
			if err != nil {
				return expression{}, err
			}
		}
		if err := p.expect(")"); err != nil {
			return expression{}, err
		}
		if field == "*" && function != "count" {
			return expression{}, fmt.Errorf("only COUNT accepts *")
		}
		if (function == "sum" || function == "avg") && field != "size" {
			return expression{}, fmt.Errorf("%s requires a numeric field", strings.ToUpper(function))
		}
		return expression{field: field, aggregate: function}, nil
	}
	field, err := p.field()
	return expression{field: field}, err
}

func (p *parser) disjunction() (*condition, error) {
	left, err := p.conjunction()
	for err == nil && p.accept("OR") {
		var right *condition
		right, err = p.conjunction()
		left = &condition{op: "OR", left: left, right: right}
	}
	return left, err
}

func (p *parser) conjunction() (*condition, error) {
	left, err := p.unary()
	for err == nil && p.accept("AND") {
		var right *condition
		right, err = p.unary()
		left = &condition{op: "AND", left: left, right: right}
	}
	return left, err
}

// Bound recursive syntax to reject pathological nesting before exhausting the stack.
func (p *parser) unary() (*condition, error) {
	if p.depth >= 128 {
		return nil, fmt.Errorf("condition nesting exceeds 128 levels")
	}
	p.depth++
	defer func() { p.depth-- }()
	if p.accept("NOT") {
		child, err := p.unary()
		return &condition{op: "NOT", left: child}, err
	}
	if p.accept("(") {
		child, err := p.disjunction()
		if err != nil {
			return nil, err
		}
		if err := p.expect(")"); err != nil {
			return nil, err
		}
		return child, nil
	}
	return p.comparison()
}

func (p *parser) comparison() (*condition, error) {
	field, err := p.field()
	if err != nil {
		return nil, err
	}
	if p.accept("IS") {
		op := "IS NULL"
		if p.accept("NOT") {
			op = "IS NOT NULL"
		}
		if err := p.expect("NULL"); err != nil {
			return nil, err
		}
		return &condition{predicate: predicate{field: field, op: op}}, nil
	}

	if p.accept("LIKE") {
		return p.like(field, false)
	}
	if p.accept("NOT") {
		if err := p.expect("LIKE"); err != nil {
			return nil, err
		}
		return p.like(field, true)
	}

	op, err := p.next()
	if err != nil {
		return nil, err
	}
	if op.quoted || !validOperator(op.text) {
		return nil, fmt.Errorf("unsupported comparison %q", op.text)
	}
	literal, err := p.next()
	if err != nil {
		return nil, err
	}
	var value any
	switch field {
	case "size":
		v, err := strconv.ParseInt(literal.text, 10, 64)
		if err != nil || literal.quoted || v < 0 {
			return nil, fmt.Errorf("%s requires a non-negative integer", field)
		}
		value = v
	case "modified_at", "indexed_at":
		v, err := time.Parse(time.RFC3339Nano, literal.text)
		if err != nil || !literal.quoted {
			return nil, fmt.Errorf("%s requires a quoted RFC 3339 timestamp", field)
		}
		value = v
	default:
		if !literal.quoted {
			return nil, fmt.Errorf("%s requires a single-quoted string", field)
		}
		value = literal.text
	}
	return &condition{predicate: predicate{field: field, op: op.text, value: value}}, nil
}

// Parse accepts an optional EXPLAIN [ANALYZE] prefix, then SELECT fields or
// aggregates FROM files, optional WHERE, GROUP BY, ORDER BY, and LIMIT clauses
// in that order. Keywords and fields are case-insensitive; string values are exact.
func Parse(input string) (Plan, error) {
	tokens, err := lex(input)
	if err != nil {
		return Plan{}, err
	}
	p := parser{tokens: tokens}
	plan := Plan{limit: -1}
	if p.accept("EXPLAIN") {
		plan.explain = true
		plan.analyze = p.accept("ANALYZE")
	}
	if err := p.expect("SELECT"); err != nil {
		return plan, err
	}
	if p.accept("*") {
		for _, field := range fields {
			plan.selections = append(plan.selections, expression{field: field})
		}
	} else {
		for {
			expr, err := p.expression()
			if err != nil {
				return plan, err
			}
			plan.selections = append(plan.selections, expr)
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
		plan.filter, err = p.disjunction()
		if err != nil {
			return plan, err
		}
	}

	if p.accept("GROUP") {
		if err := p.expect("BY"); err != nil {
			return plan, err
		}
		for {
			field, err := p.field()
			if err != nil {
				return plan, err
			}
			plan.groupBy = append(plan.groupBy, field)
			if !p.accept(",") {
				break
			}
		}
	}
	if p.accept("ORDER") {
		if err := p.expect("BY"); err != nil {
			return plan, err
		}
		for {
			expr, err := p.expression()
			if err != nil {
				return plan, err
			}
			term := ordering{expression: expr}
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
	for _, selection := range plan.selections {
		plan.columns = append(plan.columns, selection.label())
		plan.grouped = plan.grouped || selection.aggregate != ""
	}
	for _, term := range plan.order {
		plan.grouped = plan.grouped || term.expression.aggregate != ""
	}
	plan.grouped = plan.grouped || len(plan.groupBy) > 0
	if plan.grouped {
		groupFields := make(map[string]bool)
		for _, field := range plan.groupBy {
			groupFields[field] = true
		}
		validate := func(expr expression) error {
			if expr.aggregate == "" && !groupFields[expr.field] {
				return fmt.Errorf("field %q must appear in GROUP BY or an aggregate", expr.field)
			}
			return nil
		}
		for _, selection := range plan.selections {
			if err := validate(selection); err != nil {
				return plan, err
			}
		}
		for _, term := range plan.order {
			if err := validate(term.expression); err != nil {
				return plan, err
			}
		}
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
	case "owner_id":
		if file.OwnerID != nil {
			return *file.OwnerID
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

func matches(file catalog.File, p predicate) truth {
	got := value(file, p.field)
	if p.op == "IS NULL" {
		return truthFromBool(got == nil)
	}
	if p.op == "IS NOT NULL" {
		return truthFromBool(got != nil)
	}

	// Comparisons with unavailable metadata are unknown.
	if got == nil {
		return truthUnknown
	}
	if p.op == "LIKE" {
		return truthFromBool(p.value.(*likePattern).matches(got.(string)))
	}

	comparison := compareValues(got, p.value)
	switch p.op {
	case "=":
		return truthFromBool(comparison == 0)
	case "!=":
		return truthFromBool(comparison != 0)
	case "<":
		return truthFromBool(comparison < 0)
	case "<=":
		return truthFromBool(comparison <= 0)
	case ">":
		return truthFromBool(comparison > 0)
	case ">=":
		return truthFromBool(comparison >= 0)
	}
	return truthFalse
}

// compareValues compares non-null values of the same validated field type.
func compareValues(a, b any) int {
	switch a := a.(type) {
	case string:
		return strings.Compare(a, b.(string))
	case int64:
		return cmp.Compare(a, b.(int64))
	case float64:
		return cmp.Compare(a, b.(float64))
	case time.Time:
		return a.Compare(b.(time.Time))
	default:
		panic("unvalidated query value type")
	}
}

// Execute filters records, sorts matches, and applies the limit before projection.
// Nulls sort last in both directions, with ascending path as the final tie-breaker.
// EXPLAIN plans describe the execution instead of returning its rows.
func Execute(ctx context.Context, plan Plan, source Source) (Result, error) {
	if len(plan.columns) == 0 {
		return Result{Columns: []string{}, Rows: make([][]any, 0)}, fmt.Errorf("query plan must be created by Parse")
	}
	if plan.explain {
		return explain(ctx, plan, source)
	}
	result, _, err := run(ctx, plan, source)
	return result, err
}

// errStop ends a scan early once a streaming execution has enough rows.
var errStop = errors.New("scan stopped")

func run(ctx context.Context, plan Plan, source Source) (Result, ScanStats, error) {
	result := Result{Columns: append([]string(nil), plan.columns...), Rows: make([][]any, 0)}
	if err := ctx.Err(); err != nil {
		return result, ScanStats{}, err
	}
	if plan.limit == 0 {
		return result, ScanStats{}, nil
	}
	if plan.grouped {
		return executeGrouped(ctx, plan, source)
	}
	request := plan.Request()
	project := func(file catalog.File) []any {
		row := make([]any, len(plan.columns))
		for j, column := range plan.columns {
			row[j] = value(file, column)
		}
		return row
	}
	var files []catalog.File
	var top *topN
	visit := func(file catalog.File) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch {
		case plan.scanOrdered():
			result.Rows = append(result.Rows, project(file))
			if plan.limit > 0 && len(result.Rows) == plan.limit {
				return errStop
			}
		case top != nil:
			top.offer(file)
		default:
			files = append(files, file)
		}
		return nil
	}
	if !plan.scanOrdered() && plan.limit > 0 {
		top = &topN{limit: plan.limit, less: plan.less}
	}
	stats, err := source.Scan(ctx, request, visit)
	if err != nil && !errors.Is(err, errStop) {
		return result, stats, err
	}
	if plan.scanOrdered() {
		return result, stats, nil
	}
	if top != nil {
		files = top.sorted()
	} else {
		sort.Slice(files, func(i, j int) bool { return plan.less(files[i], files[j]) })
	}
	if err := ctx.Err(); err != nil {
		return result, stats, err
	}
	for _, file := range files {
		result.Rows = append(result.Rows, project(file))
	}
	return result, stats, nil
}

// less orders records by the ORDER BY terms, then by ascending path.
func (plan Plan) less(a, b catalog.File) bool {
	for _, term := range plan.order {
		av, bv := value(a, term.expression.field), value(b, term.expression.field)
		if comparison := compareOrdered(av, bv, term.descending); comparison != 0 {
			return comparison < 0
		}
	}
	return a.Path < b.Path
}

// scanOrdered reports whether the source's ascending path order already is the
// result order, which allows streaming and stopping at the limit. Paths are
// unique, so terms after an ascending path term never apply.
func (plan Plan) scanOrdered() bool {
	if plan.grouped {
		return false
	}
	return len(plan.order) == 0 || (plan.order[0].expression.field == "path" && !plan.order[0].descending)
}

// Request lists the fields execution reads, so sources can skip the others.
func (plan Plan) Request() Request {
	filter := make(map[string]bool)
	plan.filter.fields(filter)
	needed := make(map[string]bool)
	for field := range filter {
		needed[field] = true
	}
	for _, selection := range plan.selections {
		needed[selection.field] = true
	}
	for _, term := range plan.order {
		needed[term.expression.field] = true
	}
	for _, field := range plan.groupBy {
		needed[field] = true
	}
	if !plan.grouped {
		needed["path"] = true
	}
	request := Request{filter: plan.filter}
	for _, field := range fields {
		if filter[field] {
			request.FilterFields = append(request.FilterFields, field)
		}
		if needed[field] {
			request.Fields = append(request.Fields, field)
		}
	}
	return request
}
