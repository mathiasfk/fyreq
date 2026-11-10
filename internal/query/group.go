package query

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"time"

	"fyreq/internal/catalog"
)

type accumulator struct {
	count int64
	sum   big.Int
	value any
}

func (a *accumulator) observe(expr expression, file catalog.File) {
	if expr.field == "*" {
		a.count++
		return
	}
	v := value(file, expr.field)
	if v == nil {
		return
	}
	a.count++
	switch expr.aggregate {
	case "sum", "avg":
		a.sum.Add(&a.sum, big.NewInt(v.(int64)))
	case "min":
		if a.value == nil || compareValues(v, a.value) < 0 {
			a.value = canonicalValue(v)
		}
	case "max":
		if a.value == nil || compareValues(v, a.value) > 0 {
			a.value = canonicalValue(v)
		}
	}
}

func (a *accumulator) finish(expr expression) (any, error) {
	if expr.aggregate == "count" {
		return a.count, nil
	}
	if a.count == 0 {
		return nil, nil
	}
	switch expr.aggregate {
	case "sum":
		if !a.sum.IsInt64() {
			return nil, fmt.Errorf("%s exceeds signed 64-bit integer range", expr.label())
		}
		return a.sum.Int64(), nil
	case "avg":
		average := new(big.Rat).SetFrac(&a.sum, big.NewInt(a.count))
		v, _ := average.Float64()
		return v, nil
	default:
		return a.value, nil
	}
}

// Normalize timestamps so equivalent instants have identical grouping keys and
// output, regardless of their original location or map iteration order.
func canonicalValue(v any) any {
	if t, ok := v.(time.Time); ok {
		return t.UTC()
	}
	return v
}

// compareOrdered places nulls last independently of direction.
func compareOrdered(a, b any, descending bool) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return 1
	}
	if b == nil {
		return -1
	}
	c := compareValues(a, b)
	if descending {
		return -c
	}
	return c
}

type group struct {
	keys       []any
	values     map[string]any
	aggregates map[expression]*accumulator
}

func executeGrouped(ctx context.Context, plan Plan, files map[string]catalog.File) (Result, error) {
	result := Result{Columns: append([]string(nil), plan.columns...), Rows: make([][]any, 0)}
	aggregates := make(map[expression]bool)
	for _, expr := range plan.selections {
		if expr.aggregate != "" {
			aggregates[expr] = true
		}
	}
	for _, term := range plan.order {
		if term.expression.aggregate != "" {
			aggregates[term.expression] = true
		}
	}
	newGroup := func(keys []any) *group {
		g := &group{keys: keys, values: make(map[string]any), aggregates: make(map[expression]*accumulator)}
		for i, field := range plan.groupBy {
			g.values[field] = keys[i]
		}
		for expr := range aggregates {
			g.aggregates[expr] = &accumulator{}
		}
		return g
	}
	groups := make(map[string]*group)
	// An aggregate without GROUP BY returns one row even for an empty input.
	if len(plan.groupBy) == 0 {
		groups["[]"] = newGroup(nil)
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if plan.filter.evaluate(file) != truthTrue {
			continue
		}

		keys := make([]any, len(plan.groupBy))
		for i, field := range plan.groupBy {
			keys[i] = canonicalValue(value(file, field))
		}
		// JSON encodes tuple boundaries, nulls, and string escaping unambiguously.
		encoded, err := json.Marshal(keys)
		if err != nil {
			return result, fmt.Errorf("encode group key: %w", err)
		}
		key := string(encoded)
		g := groups[key]
		if g == nil {
			g = newGroup(keys)
			groups[key] = g
		}
		for expr, a := range g.aggregates {
			a.observe(expr, file)
		}
	}
	ordered := make([]*group, 0, len(groups))
	for _, g := range groups {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		for expr, a := range g.aggregates {
			v, err := a.finish(expr)
			if err != nil {
				return result, err
			}
			g.values[expr.label()] = v
		}
		ordered = append(ordered, g)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		for _, term := range plan.order {
			label := term.expression.label()
			if c := compareOrdered(a.values[label], b.values[label], term.descending); c != 0 {
				return c < 0
			}
		}
		// Grouping keys provide deterministic default ordering and break ties.
		for k := range a.keys {
			if c := compareOrdered(a.keys[k], b.keys[k], false); c != 0 {
				return c < 0
			}
		}
		return false
	})
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if plan.limit >= 0 && len(ordered) > plan.limit {
		ordered = ordered[:plan.limit]
	}
	for _, g := range ordered {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		row := make([]any, len(plan.selections))
		for i, expr := range plan.selections {
			row[i] = g.values[expr.label()]
		}
		result.Rows = append(result.Rows, row)
	}
	return result, nil
}
