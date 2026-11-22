package query

import (
	"context"
	"sort"

	"fyreq/internal/catalog"
)

// Bounds summarizes one field across a group of records. Min and Max are nil
// when the group has no non-null values. Values use the field's query type:
// string, int64, or time.Time.
type Bounds struct {
	Rows  int
	Nulls int
	Min   any
	Max   any
}

// Statistics returns bounds for a field, or false when none are available.
type Statistics func(field string) (Bounds, bool)

// Request describes the data one execution needs. Sources decode FilterFields,
// call Match, and only then need to provide the remaining Fields for matches.
// Fields not listed may be left as zero values. MayMatch prunes groups of
// records by their statistics; it never rejects a group containing a match.
type Request struct {
	FilterFields []string
	Fields       []string
	filter       *condition
}

// MayMatch reports whether a group summarized by stats can contain a match.
func (r Request) MayMatch(stats Statistics) bool {
	return r.filter.possible(stats).canTrue()
}

// Match reports whether a record satisfies the filter.
func (r Request) Match(file catalog.File) bool {
	return r.filter.evaluate(file) == truthTrue
}

// ScanStats reports the physical work of a scan or, from Estimate, the work
// predicted from metadata alone.
type ScanStats struct {
	Segments       int   `json:"segments"`
	SegmentsPruned int   `json:"segments_pruned"`
	Blocks         int   `json:"blocks"`
	BlocksPruned   int   `json:"blocks_pruned"`
	RowsRead       int   `json:"rows_read"`
	RowsMatched    int   `json:"rows_matched"`
	BytesRead      int64 `json:"bytes_read"`
}

// Source supplies records for one consistent catalog generation. Scan visits
// matching records once each in ascending path order and returns errors from
// visit unchanged, stopping immediately. Estimate applies pruning without
// reading record data.
type Source interface {
	Scan(context.Context, Request, func(catalog.File) error) (ScanStats, error)
	Estimate(context.Context, Request) (ScanStats, error)
}

// Records is an in-memory Source keyed by path, without pruning statistics.
type Records map[string]catalog.File

func (r Records) Scan(ctx context.Context, request Request, visit func(catalog.File) error) (ScanStats, error) {
	stats := ScanStats{Segments: 1, Blocks: 1}
	paths := make([]string, 0, len(r))
	for path := range r {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		stats.RowsRead++
		if !request.Match(r[path]) {
			continue
		}
		stats.RowsMatched++
		if err := visit(r[path]); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func (r Records) Estimate(ctx context.Context, _ Request) (ScanStats, error) {
	return ScanStats{Segments: 1, Blocks: 1, RowsRead: len(r)}, ctx.Err()
}
