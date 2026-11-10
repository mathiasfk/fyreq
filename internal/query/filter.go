package query

import "fyreq/internal/catalog"

// SQL boolean expressions preserve unknown values until WHERE selects true rows.
type truth uint8

const (
	truthFalse truth = iota
	truthUnknown
	truthTrue
)

func truthFromBool(v bool) truth {
	if v {
		return truthTrue
	}
	return truthFalse
}

type condition struct {
	op          string
	left, right *condition
	predicate   predicate
}

func (c *condition) evaluate(file catalog.File) truth {
	if c == nil {
		return truthTrue
	}
	switch c.op {
	case "NOT":
		switch c.left.evaluate(file) {
		case truthTrue:
			return truthFalse
		case truthFalse:
			return truthTrue
		default:
			return truthUnknown
		}
	case "AND":
		left := c.left.evaluate(file)
		if left == truthFalse {
			return truthFalse
		}
		right := c.right.evaluate(file)
		if right == truthFalse {
			return truthFalse
		}
		if left == truthUnknown || right == truthUnknown {
			return truthUnknown
		}
		return truthTrue
	case "OR":
		left := c.left.evaluate(file)
		if left == truthTrue {
			return truthTrue
		}
		right := c.right.evaluate(file)
		if right == truthTrue {
			return truthTrue
		}
		if left == truthUnknown || right == truthUnknown {
			return truthUnknown
		}
		return truthFalse
	default:
		return matches(file, c.predicate)
	}
}
