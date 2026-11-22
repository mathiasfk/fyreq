package query

import (
	"strings"
	"time"
)

// outcomes over-approximates the truth values a condition can produce for the
// records in a group. Pruning is sound because each set only ever grows.
type outcomes uint8

const (
	outcomeFalse outcomes = 1 << iota
	outcomeUnknown
	outcomeTrue
	outcomeAny = outcomeFalse | outcomeUnknown | outcomeTrue
)

func (o outcomes) canTrue() bool    { return o&outcomeTrue != 0 }
func (o outcomes) canFalse() bool   { return o&outcomeFalse != 0 }
func (o outcomes) canUnknown() bool { return o&outcomeUnknown != 0 }

func outcomeIf(ok bool, o outcomes) outcomes {
	if ok {
		return o
	}
	return 0
}

func (c *condition) possible(stats Statistics) outcomes {
	if c == nil {
		return outcomeTrue
	}
	switch c.op {
	case "NOT":
		child := c.left.possible(stats)
		return outcomeIf(child.canTrue(), outcomeFalse) | outcomeIf(child.canFalse(), outcomeTrue) | child&outcomeUnknown
	case "AND":
		l, r := c.left.possible(stats), c.right.possible(stats)
		unknown := (l.canUnknown() && (r.canTrue() || r.canUnknown())) || (r.canUnknown() && (l.canTrue() || l.canUnknown()))
		return outcomeIf(l.canTrue() && r.canTrue(), outcomeTrue) | outcomeIf(l.canFalse() || r.canFalse(), outcomeFalse) | outcomeIf(unknown, outcomeUnknown)
	case "OR":
		l, r := c.left.possible(stats), c.right.possible(stats)
		unknown := (l.canUnknown() && (r.canFalse() || r.canUnknown())) || (r.canUnknown() && (l.canFalse() || l.canUnknown()))
		return outcomeIf(l.canTrue() || r.canTrue(), outcomeTrue) | outcomeIf(l.canFalse() && r.canFalse(), outcomeFalse) | outcomeIf(unknown, outcomeUnknown)
	default:
		return c.predicate.possible(stats)
	}
}

func (p predicate) possible(stats Statistics) outcomes {
	if stats == nil {
		return outcomeAny
	}
	b, ok := stats(p.field)
	if !ok {
		return outcomeAny
	}
	present := b.Rows - b.Nulls
	switch p.op {
	case "IS NULL":
		return outcomeIf(b.Nulls > 0, outcomeTrue) | outcomeIf(present > 0, outcomeFalse)
	case "IS NOT NULL":
		return outcomeIf(b.Nulls > 0, outcomeFalse) | outcomeIf(present > 0, outcomeTrue)
	}
	result := outcomeIf(b.Nulls > 0, outcomeUnknown)
	if present <= 0 {
		return result
	}
	if p.op == "LIKE" {
		return result | p.value.(*likePattern).possible(b)
	}
	if !comparable(b.Min, p.value) || !comparable(b.Max, p.value) {
		return result | outcomeTrue | outcomeFalse
	}
	low, high := compareValues(b.Min, p.value), compareValues(b.Max, p.value)
	var canTrue, canFalse bool
	switch p.op {
	case "=":
		canTrue, canFalse = low <= 0 && high >= 0, low != 0 || high != 0
	case "!=":
		canTrue, canFalse = low != 0 || high != 0, low <= 0 && high >= 0
	case "<":
		canTrue, canFalse = low < 0, high >= 0
	case "<=":
		canTrue, canFalse = low <= 0, high > 0
	case ">":
		canTrue, canFalse = high > 0, low <= 0
	case ">=":
		canTrue, canFalse = high >= 0, low < 0
	default:
		canTrue, canFalse = true, true
	}
	return result | outcomeIf(canTrue, outcomeTrue) | outcomeIf(canFalse, outcomeFalse)
}

// comparable rejects statistics whose type differs from the literal, so a
// mismatched source degrades to no pruning rather than a wrong answer.
func comparable(bound, literal any) bool {
	switch literal.(type) {
	case string:
		_, ok := bound.(string)
		return ok
	case int64:
		_, ok := bound.(int64)
		return ok
	case time.Time:
		_, ok := bound.(time.Time)
		return ok
	}
	return false
}

// possible uses the pattern's literal prefix: matching strings form the lexical
// interval of strings starting with that prefix.
func (p *likePattern) possible(b Bounds) outcomes {
	low, lok := b.Min.(string)
	high, hok := b.Max.(string)
	if p.prefix == "" || !lok || !hok {
		return outcomeTrue | outcomeFalse
	}
	canTrue := high >= p.prefix && (low <= p.prefix || strings.HasPrefix(low, p.prefix))
	canFalse := !(p.prefixOnly && strings.HasPrefix(low, p.prefix) && strings.HasPrefix(high, p.prefix))
	return outcomeIf(canTrue, outcomeTrue) | outcomeIf(canFalse, outcomeFalse)
}
