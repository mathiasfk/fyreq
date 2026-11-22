package query

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// explain describes the execution of a plan. Without ANALYZE the source only
// estimates pruning from metadata; with ANALYZE the query runs and reports
// actual work, discarding its rows.
func explain(ctx context.Context, plan Plan, source Source) (Result, error) {
	request := plan.Request()
	var stats ScanStats
	var returned int
	var elapsed time.Duration
	if plan.analyze {
		start := time.Now()
		rows, actual, err := run(ctx, plan, source)
		if err != nil {
			return Result{Columns: []string{"plan"}, Rows: make([][]any, 0), Explain: true}, err
		}
		elapsed = time.Since(start)
		stats, returned = actual, len(rows.Rows)
	} else if plan.limit != 0 {
		estimate, err := source.Estimate(ctx, request)
		if err != nil {
			return Result{Columns: []string{"plan"}, Rows: make([][]any, 0), Explain: true}, err
		}
		stats = estimate
	}
	var lines []string
	add := func(depth int, format string, args ...any) {
		lines = append(lines, strings.Repeat("  ", depth)+fmt.Sprintf(format, args...))
	}
	depth := 0
	add(depth, "Project: %s", strings.Join(plan.columns, ", "))
	depth++
	if plan.limit >= 0 {
		add(depth, "Limit: %d", plan.limit)
		depth++
	}
	terms := make([]string, 0, len(plan.order)+1)
	for _, term := range plan.order {
		direction := "ASC"
		if term.descending {
			direction = "DESC"
		}
		terms = append(terms, term.expression.label()+" "+direction)
	}
	switch {
	case plan.grouped:
		add(depth, "Sort: %s", strings.Join(append(terms, "group keys ASC"), ", "))
		depth++
		var aggregates []string
		seen := make(map[string]bool)
		for _, expr := range plan.selections {
			if expr.aggregate != "" && !seen[expr.label()] {
				seen[expr.label()] = true
				aggregates = append(aggregates, expr.label())
			}
		}
		for _, term := range plan.order {
			if term.expression.aggregate != "" && !seen[term.expression.label()] {
				seen[term.expression.label()] = true
				aggregates = append(aggregates, term.expression.label())
			}
		}
		if len(plan.groupBy) > 0 {
			add(depth, "Aggregate: group by %s; aggregates: %s", strings.Join(plan.groupBy, ", "), listOrNone(aggregates))
		} else {
			add(depth, "Aggregate: single group; aggregates: %s", listOrNone(aggregates))
		}
	case plan.scanOrdered() && plan.limit > 0:
		add(depth, "Order: path ASC from scan; stops after %d rows", plan.limit)
	case plan.scanOrdered():
		add(depth, "Order: path ASC from scan")
	case plan.limit > 0:
		add(depth, "Top-N sort: %s", strings.Join(append(terms, "path ASC"), ", "))
	default:
		add(depth, "Sort: %s", strings.Join(append(terms, "path ASC"), ", "))
	}
	depth++
	if plan.filter != nil {
		add(depth, "Filter: %s", plan.filter.describe(true))
		depth++
	}
	if plan.limit == 0 {
		add(depth, "Scan: files skipped (LIMIT 0)")
	} else {
		add(depth, "Scan: files")
		output := make([]string, 0, len(request.Fields))
		filtered := make(map[string]bool)
		for _, field := range request.FilterFields {
			filtered[field] = true
		}
		for _, field := range request.Fields {
			if !filtered[field] {
				output = append(output, field)
			}
		}
		add(depth+1, "Columns: filter %s; output %s", listOrNone(request.FilterFields), listOrNone(output))
		add(depth+1, "Segments: %d, pruned %d", stats.Segments, stats.SegmentsPruned)
		add(depth+1, "Blocks: %d, pruned %d", stats.Blocks, stats.BlocksPruned)
		if plan.analyze {
			add(depth+1, "Rows: read %d, matched %d", stats.RowsRead, stats.RowsMatched)
			add(depth+1, "Bytes read: %d", stats.BytesRead)
		} else {
			add(depth+1, "Rows: at most %d candidates", stats.RowsRead)
		}
	}
	if plan.analyze {
		add(0, "Rows returned: %d", returned)
		add(0, "Execution time: %s", elapsed.Round(time.Microsecond))
	}
	result := Result{Columns: []string{"plan"}, Rows: make([][]any, 0, len(lines)), Explain: true}
	for _, line := range lines {
		result.Rows = append(result.Rows, []any{line})
	}
	return result, nil
}

func listOrNone(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}

// describe renders a condition in query syntax, parenthesizing nested
// AND and OR expressions.
func (c *condition) describe(top bool) string {
	var text string
	switch c.op {
	case "NOT":
		return "NOT " + c.left.describe(false)
	case "AND", "OR":
		text = c.left.describe(false) + " " + c.op + " " + c.right.describe(false)
		if !top {
			text = "(" + text + ")"
		}
		return text
	}
	p := c.predicate
	switch p.op {
	case "IS NULL", "IS NOT NULL":
		return p.field + " " + p.op
	case "LIKE":
		pattern := p.value.(*likePattern)
		text = p.field + " LIKE " + quoteLiteral(pattern.source)
		if pattern.hasEscape {
			text += " ESCAPE " + quoteLiteral(string(pattern.escape))
		}
		return text
	}
	var literal string
	switch v := p.value.(type) {
	case string:
		literal = quoteLiteral(v)
	case int64:
		literal = strconv.FormatInt(v, 10)
	case time.Time:
		literal = quoteLiteral(v.Format(time.RFC3339Nano))
	}
	return p.field + " " + p.op + " " + literal
}

func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
