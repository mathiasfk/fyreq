package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fyreq/internal/catalog"
	"fyreq/internal/pathpolicy"
	"fyreq/internal/query"
)

// openAttempts bounds retries when writers keep replacing segments while a
// reader opens them.
const openAttempts = 16

// snapshot holds open handles to every segment of one manifest, so later
// publications that unlink those files do not affect it.
type snapshot struct {
	manifest manifest
	base     []*segment
	deltas   []*segment
}

// openSnapshot reads the manifest and opens its segments. A segment removed or
// replaced after the manifest was read means a newer generation was published;
// the reader retries with the new manifest. Rereading an identical manifest
// that still references a missing segment indicates damage.
func openSnapshot(ctx context.Context, dir string) (*snapshot, error) {
	var previous []byte
	var previousErr error
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := readManifest(filepath.Join(dir, manifestName))
		if errors.Is(err, os.ErrNotExist) {
			if _, legacyErr := os.Stat(filepath.Join(dir, legacyName)); legacyErr == nil {
				return nil, fmt.Errorf("legacy JSON catalog is not supported; run clear or choose a new --data-dir")
			}
			return &snapshot{manifest: emptyManifest()}, nil
		}
		if err != nil {
			return nil, err
		}
		if previousErr != nil && bytes.Equal(previous, data) && segmentRetiring(previousErr) {
			return nil, previousErr
		}
		m, err := decodeManifest(data)
		if err != nil {
			return nil, err
		}
		s, err := openSegments(dir, m)
		if err == nil {
			return s, nil
		}
		if !errors.Is(err, errSegmentChanged) && !segmentRetiring(err) {
			return nil, err
		}
		if bytes.Equal(previous, data) {
			return nil, fmt.Errorf("catalog generation %d references a missing or replaced segment: %w", m.generation, errCorrupt)
		}
		if attempt == openAttempts {
			return nil, fmt.Errorf("catalog changed repeatedly while opening: %w", err)
		}
		previous, previousErr = data, err
	}
}

func readManifest(path string) ([]byte, error) {
	f, err := openCatalogFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func openSegments(dir string, m manifest) (*snapshot, error) {
	s := &snapshot{manifest: m}
	for _, list := range []struct {
		refs []segmentRef
		into *[]*segment
	}{{m.base, &s.base}, {m.deltas, &s.deltas}} {
		for _, ref := range list.refs {
			seg, err := openSegment(dir, ref)
			if err != nil {
				s.Close()
				return nil, err
			}
			*list.into = append(*list.into, seg)
		}
	}
	return s, nil
}

func (s *snapshot) Close() error {
	var first error
	for _, seg := range append(append([]*segment(nil), s.base...), s.deltas...) {
		if err := seg.file.Close(); err != nil && first == nil {
			first = err
		}
	}
	s.base, s.deltas = nil, nil
	return first
}

func (s *snapshot) Header() catalog.Header { return s.manifest.header() }

func (s *snapshot) Segments() []catalog.Segment {
	var result []catalog.Segment
	for _, list := range []struct {
		level string
		refs  []segmentRef
	}{{"base", s.manifest.base}, {"delta", s.manifest.deltas}} {
		for _, ref := range list.refs {
			result = append(result, catalog.Segment{Name: ref.name, Level: list.level, Records: ref.records, Tombstones: ref.tombstones, Blocks: ref.blocks, Bytes: ref.bytes, MinPath: ref.minPath, MaxPath: ref.maxPath})
		}
	}
	return result
}

// scanSpec selects columns and filters. Filter columns are decoded before
// match; output columns only for blocks with matching rows.
type scanSpec struct {
	filter   []column
	output   []column
	mayMatch func(query.Statistics) bool
	match    func(catalog.File) bool
}

func columnsFor(names []string) ([]column, error) {
	result := make([]column, 0, len(names))
	for _, name := range names {
		c, ok := columnsByName[name]
		if !ok || c == columnDeleted {
			return nil, fmt.Errorf("unknown catalog field %q", name)
		}
		result = append(result, c)
	}
	return result, nil
}

func (s *snapshot) Scan(ctx context.Context, request query.Request, visit func(catalog.File) error) (query.ScanStats, error) {
	filter, err := columnsFor(request.FilterFields)
	if err != nil {
		return query.ScanStats{}, err
	}
	output, err := columnsFor(request.Fields)
	if err != nil {
		return query.ScanStats{}, err
	}
	return s.scan(ctx, scanSpec{filter: filter, output: output, mayMatch: request.MayMatch, match: request.Match}, visit)
}

// Estimate applies segment and block pruning using footer statistics only.
func (s *snapshot) Estimate(ctx context.Context, request query.Request) (query.ScanStats, error) {
	var stats query.ScanStats
	for _, seg := range s.all() {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		stats.Segments++
		stats.Blocks += len(seg.blocks)
		if !request.MayMatch(seg.statistics()) {
			stats.SegmentsPruned++
			stats.BlocksPruned += len(seg.blocks)
			continue
		}
		for _, b := range seg.blocks {
			if request.MayMatch(b.statistics()) {
				stats.RowsRead += b.rows
			} else {
				stats.BlocksPruned++
			}
		}
	}
	return stats, nil
}

func (s *snapshot) Records(ctx context.Context, visit func(catalog.File) error) error {
	output, err := columnsFor(recordColumns)
	if err != nil {
		return err
	}
	_, err = s.scan(ctx, scanSpec{output: output}, visit)
	return err
}

// files returns the live records within root.
func (s *snapshot) files(ctx context.Context, root string) (map[string]catalog.File, error) {
	output, err := columnsFor(recordColumns)
	if err != nil {
		return nil, err
	}
	result := make(map[string]catalog.File)
	spec := scanSpec{output: output, mayMatch: subtree(root), match: func(f catalog.File) bool { return pathpolicy.Contains(root, f.Path) }}
	_, err = s.scan(ctx, spec, func(f catalog.File) error {
		result[f.Path] = f
		return nil
	})
	return result, err
}

// existing reports which of the sorted paths have live records.
func (s *snapshot) existing(ctx context.Context, paths []string) (map[string]bool, error) {
	wanted := make(map[string]bool, len(paths))
	for _, path := range paths {
		wanted[path] = true
	}
	found := make(map[string]bool)
	spec := scanSpec{
		mayMatch: func(stats query.Statistics) bool {
			b, ok := stats("path")
			if !ok || b.Min == nil {
				return true
			}
			low, high := b.Min.(string), b.Max.(string)
			i := sort.SearchStrings(paths, low)
			return i < len(paths) && paths[i] <= high
		},
		match: func(f catalog.File) bool { return wanted[f.Path] },
	}
	_, err := s.scan(ctx, spec, func(f catalog.File) error {
		found[f.Path] = true
		return nil
	})
	return found, err
}

// subtree prunes blocks whose path range cannot contain root or a descendant.
// Every descendant sorts below root followed by the byte after the separator.
func subtree(root string) func(query.Statistics) bool {
	if strings.HasSuffix(root, string(filepath.Separator)) {
		return nil
	}
	upper := root + string(rune(filepath.Separator+1))
	return func(stats query.Statistics) bool {
		b, ok := stats("path")
		if !ok || b.Min == nil {
			return true
		}
		return b.Max.(string) >= root && b.Min.(string) < upper
	}
}

func (s *snapshot) all() []*segment {
	return append(append([]*segment(nil), s.base...), s.deltas...)
}

func (seg *segment) statistics() query.Statistics {
	return func(field string) (query.Bounds, bool) {
		c, ok := columnsByName[field]
		if !ok || !seg.present[c.id] {
			return query.Bounds{}, false
		}
		st := seg.stats[c.id]
		return query.Bounds{Rows: seg.rows + seg.tombstones, Nulls: st.nulls, Min: st.min, Max: st.max}, true
	}
}

func (b block) statistics() query.Statistics {
	return func(field string) (query.Bounds, bool) {
		c, ok := columnsByName[field]
		if !ok {
			return query.Bounds{}, false
		}
		ch, ok := b.chunks[c.id]
		if !ok {
			return query.Bounds{}, false
		}
		return query.Bounds{Rows: b.rows, Nulls: ch.stats.nulls, Min: ch.stats.min, Max: ch.stats.max}, true
	}
}

// scan merges base and delta segments into one view in ascending path order.
// A delta row overrides any base row and any older delta row with the same
// path; tombstones hide the path. Shadowing is resolved from every delta path
// before filtering, so a filtered-out newer row still hides an older match.
func (s *snapshot) scan(ctx context.Context, spec scanSpec, visit func(catalog.File) error) (query.ScanStats, error) {
	var stats query.ScanStats
	for _, seg := range s.all() {
		stats.Segments++
		stats.Blocks += len(seg.blocks)
	}
	var newest map[string]int
	if len(s.deltas) > 0 {
		newest = make(map[string]int)
		for level, seg := range s.deltas {
			for _, b := range seg.blocks {
				if err := ctx.Err(); err != nil {
					return stats, err
				}
				paths, n, err := seg.read(b, columnPath)
				stats.BytesRead += n
				if err != nil {
					return stats, err
				}
				for _, p := range paths.strings {
					if p == nil {
						return stats, fmt.Errorf("segment %s has a row without a path: %w", seg.ref.name, errCorrupt)
					}
					newest[*p] = level
				}
			}
		}
	}
	cursors := []*cursor{{ctx: ctx, spec: spec, segments: s.base, level: -1, newest: newest, stats: &stats}}
	for level, seg := range s.deltas {
		cursors = append(cursors, &cursor{ctx: ctx, spec: spec, segments: []*segment{seg}, level: level, newest: newest, stats: &stats})
	}
	for {
		var best *cursor
		var bestFile *catalog.File
		for _, c := range cursors {
			f, err := c.peek()
			if err != nil {
				return stats, err
			}
			if f != nil && (bestFile == nil || f.Path < bestFile.Path) {
				best, bestFile = c, f
			}
		}
		if best == nil {
			return stats, nil
		}
		best.pos++
		if err := visit(*bestFile); err != nil {
			return stats, err
		}
	}
}

// cursor yields the visible matching rows of an ordered run of segments: the
// base level (-1) or one delta level.
type cursor struct {
	ctx      context.Context
	spec     scanSpec
	segments []*segment
	level    int
	newest   map[string]int
	stats    *query.ScanStats
	seg      int
	blk      int
	entered  bool
	buffer   []catalog.File
	pos      int
}

func (c *cursor) peek() (*catalog.File, error) {
	for c.pos >= len(c.buffer) {
		if c.seg >= len(c.segments) {
			return nil, nil
		}
		seg := c.segments[c.seg]
		if !c.entered {
			c.entered = true
			if c.spec.mayMatch != nil && !c.spec.mayMatch(seg.statistics()) {
				c.stats.SegmentsPruned++
				c.stats.BlocksPruned += len(seg.blocks)
				c.blk = len(seg.blocks)
			}
		}
		if c.blk >= len(seg.blocks) {
			c.seg, c.blk, c.entered = c.seg+1, 0, false
			continue
		}
		b := seg.blocks[c.blk]
		c.blk++
		if c.spec.mayMatch != nil && !c.spec.mayMatch(b.statistics()) {
			c.stats.BlocksPruned++
			continue
		}
		if err := c.ctx.Err(); err != nil {
			return nil, err
		}
		if err := c.load(seg, b); err != nil {
			return nil, err
		}
	}
	return &c.buffer[c.pos], nil
}

func (c *cursor) load(seg *segment, b block) error {
	c.buffer, c.pos = c.buffer[:0], 0
	paths, n, err := seg.read(b, columnPath)
	c.stats.BytesRead += n
	if err != nil {
		return err
	}
	var deleted []bool
	if c.level >= 0 {
		flags, n, err := seg.read(b, columnDeleted)
		c.stats.BytesRead += n
		if err != nil {
			return err
		}
		deleted = flags.bools
	}
	files := make([]catalog.File, b.rows)
	candidates := make([]int, 0, b.rows)
	for i, p := range paths.strings {
		if p == nil {
			return fmt.Errorf("segment %s has a row without a path: %w", seg.ref.name, errCorrupt)
		}
		if deleted != nil && deleted[i] {
			continue
		}
		if level, ok := c.newest[*p]; ok && level != c.level {
			continue
		}
		files[i].Path = *p
		candidates = append(candidates, i)
	}
	c.stats.RowsRead += len(candidates)
	decoded := map[uint64]bool{columnPath.id: true}
	decode := func(list []column, rows []int) error {
		for _, col := range list {
			if decoded[col.id] {
				continue
			}
			decoded[col.id] = true
			v, n, err := seg.read(b, col)
			c.stats.BytesRead += n
			if err != nil {
				return err
			}
			for _, i := range rows {
				v.apply(col, &files[i], i)
			}
		}
		return nil
	}
	if len(candidates) == 0 {
		return nil
	}
	if err := decode(c.spec.filter, candidates); err != nil {
		return err
	}
	matched := candidates
	if c.spec.match != nil {
		matched = candidates[:0]
		for _, i := range candidates {
			if c.spec.match(files[i]) {
				matched = append(matched, i)
			}
		}
	}
	c.stats.RowsMatched += len(matched)
	if len(matched) == 0 {
		return nil
	}
	if err := decode(c.spec.output, matched); err != nil {
		return err
	}
	for _, i := range matched {
		c.buffer = append(c.buffer, files[i])
	}
	return nil
}
