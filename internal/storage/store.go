// Package storage implements versioned binary catalog persistence using the
// standard library. A small manifest names immutable, columnar segment files;
// updates write new segments for changed records and atomically replace the
// manifest.
package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fyreq/internal/app"
	"fyreq/internal/catalog"
)

var ErrLocked = errors.New("catalog is locked by another writer; if a writer crashed, remove the .writer-lock directory only after verifying that no writer is running")

// layout bounds block and segment sizes and when deltas are compacted.
type layout struct {
	blockRows    int
	segmentRows  int
	maxDeltas    int
	minDeltaRows int
}

var defaultLayout = layout{blockRows: 1024, segmentRows: 65_536, maxDeltas: 8, minDeltaRows: 4096}

type Store struct {
	Dir            string
	publishForTest func(string, string, string) (bool, error)
	layout         layout
}

func (s Store) config() layout {
	if s.layout.blockRows == 0 {
		return defaultLayout
	}
	return s.layout
}

// Open returns the current published generation. Readers take no lock; the
// snapshot stays consistent while writers publish newer generations.
func (s Store) Open(ctx context.Context) (app.Snapshot, error) {
	snap, err := openSnapshot(ctx, s.Dir)
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// Update holds the writer lock across reading, mutation, and publication. A failed
// callback leaves the previous generation untouched and writes nothing.
func (s Store) Update(ctx context.Context, mutate func(app.Transaction) error) (app.UpdateResult, error) {
	return s.write(ctx, func() (manifest, []string, error) {
		snap, err := openSnapshot(ctx, s.Dir)
		if err != nil {
			return manifest{}, nil, err
		}
		defer snap.Close()
		tx := newTransaction(snap)
		if err := mutate(tx); err != nil {
			return manifest{}, nil, err
		}
		if err := ctx.Err(); err != nil {
			return manifest{}, nil, err
		}
		return s.commit(ctx, snap, tx)
	})
}

// Clear replaces any previous catalog without reading its format or contents.
// The empty catalog starts a new generation history at one.
func (s Store) Clear(ctx context.Context) (app.UpdateResult, error) {
	return s.write(ctx, func() (manifest, []string, error) { return emptyManifest(), nil, nil })
}

// write holds the writer lock while preparing and publishing a manifest.
// prepare returns the next manifest and the segment files it created, which
// are removed if publication does not happen.
func (s Store) write(ctx context.Context, prepare func() (manifest, []string, error)) (app.UpdateResult, error) {
	if err := ctx.Err(); err != nil {
		return app.UpdateResult{}, err
	}
	if err := publicationSupported(); err != nil {
		return app.UpdateResult{}, err
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return app.UpdateResult{}, err
	}
	lock := filepath.Join(s.Dir, ".writer-lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return app.UpdateResult{}, ErrLocked
		}
		return app.UpdateResult{}, err
	}
	defer os.Remove(lock)
	next, created, err := prepare()
	discard := func() {
		for _, name := range created {
			os.Remove(filepath.Join(s.Dir, name))
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && len(created) > 0 {
		// Segment directory entries must be durable before a manifest names them.
		err = syncDirectory(s.Dir)
	}
	if err != nil {
		discard()
		return app.UpdateResult{}, err
	}
	next.generation++
	next.updatedAt = time.Now().UTC()
	published, err := s.save(ctx, next)
	if !published {
		discard()
		return app.UpdateResult{}, err
	}
	s.collect(next)
	return app.UpdateResult{Published: true, Generation: next.generation}, err
}

func (s Store) save(ctx context.Context, m manifest) (bool, error) {
	f, err := os.CreateTemp(s.Dir, ".manifest-*.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(m.encode()); err != nil {
		return false, err
	}
	if err := f.Sync(); err != nil {
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	commit := publish
	if s.publishForTest != nil {
		commit = s.publishForTest
	}
	return commit(f.Name(), filepath.Join(s.Dir, manifestName), s.Dir)
}

// collect removes files the published manifest no longer needs: replaced
// segments, leftovers from interrupted writers, and the legacy JSON catalog.
// It runs under the writer lock, so no writer is creating files concurrently.
// Readers already holding a segment open keep reading it; readers that have
// not opened it yet retry with the new manifest. Failures are harmless and
// retried by the next write.
func (s Store) collect(m manifest) {
	live := make(map[string]bool)
	for _, ref := range append(append([]segmentRef(nil), m.base...), m.deltas...) {
		live[ref.name] = true
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		stale := (validSegmentName(name) && !live[name]) || name == legacyName ||
			((strings.HasPrefix(name, ".manifest-") || strings.HasPrefix(name, ".catalog-")) && strings.HasSuffix(name, ".tmp"))
		if stale && entry.Type().IsRegular() {
			os.Remove(filepath.Join(s.Dir, name))
		}
	}
}

// commit turns buffered changes into the next manifest. Changes become one new
// delta segment, or, when deltas grow too large or numerous, are compacted
// with existing deltas into the base segments whose path ranges they touch.
// Unchanged base segments are kept as they are.
func (s Store) commit(ctx context.Context, snap *snapshot, tx *transaction) (manifest, []string, error) {
	next := snap.manifest
	next.coverage = tx.coverage
	next.base = append([]segmentRef(nil), snap.manifest.base...)
	next.deltas = append([]segmentRef(nil), snap.manifest.deltas...)
	paths := make([]string, 0, len(tx.pending))
	for path := range tx.pending {
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return next, nil, nil
	}
	sort.Strings(paths)
	existing, err := snap.existing(ctx, paths)
	if err != nil {
		return manifest{}, nil, err
	}
	changes := make([]row, 0, len(paths))
	var inserted, removed uint64
	for _, path := range paths {
		file := tx.pending[path]
		switch {
		case file == nil && existing[path]:
			changes = append(changes, row{file: catalog.File{Path: path}, deleted: true})
			removed++
		case file != nil:
			if !existing[path] {
				inserted++
			}
			changes = append(changes, row{file: *file})
		}
	}
	if len(changes) == 0 {
		return next, nil, nil
	}
	next.entries = next.entries + inserted - min(removed, next.entries+inserted)
	config := s.config()
	pendingRows := uint64(len(changes))
	for _, ref := range next.deltas {
		pendingRows += ref.records + ref.tombstones
	}
	if len(next.deltas)+1 > config.maxDeltas || pendingRows > uint64(config.minDeltaRows)+next.entries/64 {
		return s.compact(ctx, snap, next, changes)
	}
	ref, err := writeSegment(s.Dir, changes, config.blockRows)
	if err != nil {
		return manifest{}, nil, err
	}
	next.deltas = append(next.deltas, ref)
	return next, []string{ref.name}, nil
}

// compact folds all deltas and the new changes into the base. Each change is
// assigned to the base segment whose range starts at or before its path (or
// the first segment), which keeps base ranges disjoint and ordered. Only
// segments receiving changes are rewritten, split evenly when oversized.
func (s Store) compact(ctx context.Context, snap *snapshot, next manifest, changes []row) (manifest, []string, error) {
	latest := make(map[string]row)
	for _, seg := range snap.deltas {
		rows, err := seg.readAll(ctx)
		if err != nil {
			return manifest{}, nil, err
		}
		for _, r := range rows {
			latest[r.file.Path] = r
		}
	}
	for _, r := range changes {
		latest[r.file.Path] = r
	}
	keys := make([]string, 0, len(latest))
	for path := range latest {
		keys = append(keys, path)
	}
	sort.Strings(keys)
	groups := make([][]row, max(len(snap.base), 1))
	for _, path := range keys {
		i := sort.Search(len(snap.base), func(i int) bool { return snap.base[i].ref.minPath > path }) - 1
		groups[max(i, 0)] = append(groups[max(i, 0)], latest[path])
	}
	var created []string
	next.base, next.deltas, next.entries = nil, nil, 0
	for i := range groups {
		if err := ctx.Err(); err != nil {
			return manifest{}, created, err
		}
		var old []row
		if i < len(snap.base) {
			if len(groups[i]) == 0 {
				next.base = append(next.base, snap.base[i].ref)
				next.entries += snap.base[i].ref.records
				continue
			}
			var err error
			if old, err = snap.base[i].readAll(ctx); err != nil {
				return manifest{}, created, err
			}
		}
		merged := mergeRows(old, groups[i])
		config := s.config()
		parts := (len(merged) + config.segmentRows - 1) / config.segmentRows
		for k := 0; k < parts; k++ {
			ref, err := writeSegment(s.Dir, merged[k*len(merged)/parts:(k+1)*len(merged)/parts], config.blockRows)
			if err != nil {
				return manifest{}, created, err
			}
			created = append(created, ref.name)
			next.base = append(next.base, ref)
			next.entries += ref.records
		}
	}
	return next, created, nil
}

// mergeRows overlays sorted changes on sorted live rows, dropping tombstones.
func mergeRows(old, changes []row) []row {
	merged := make([]row, 0, len(old)+len(changes))
	i, j := 0, 0
	for i < len(old) || j < len(changes) {
		var next row
		switch {
		case j == len(changes) || (i < len(old) && old[i].file.Path < changes[j].file.Path):
			next = old[i]
			i++
		case i == len(old) || changes[j].file.Path < old[i].file.Path:
			next = changes[j]
			j++
		default:
			next = changes[j]
			i++
			j++
		}
		if !next.deleted {
			merged = append(merged, next)
		}
	}
	return merged
}

// readAll decodes every row of a segment, including tombstones.
func (seg *segment) readAll(ctx context.Context) ([]row, error) {
	rows := make([]row, 0, seg.rows+seg.tombstones)
	for _, b := range seg.blocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		part := make([]row, b.rows)
		for _, c := range columns {
			v, _, err := seg.read(b, c)
			if err != nil {
				return nil, err
			}
			for i := range part {
				if c == columnDeleted {
					part[i].deleted = v.bools[i]
				} else {
					v.apply(c, &part[i].file, i)
				}
			}
		}
		rows = append(rows, part...)
	}
	return rows, nil
}
