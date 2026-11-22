//go:build darwin || linux || windows

package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"fyreq/internal/app"
	"fyreq/internal/catalog"
	"fyreq/internal/query"
)

// TestReadersSeeConsistentGenerationsDuringWrites runs readers against a writer
// whose deltas and compactions continuously replace and delete segments. Each
// generation k stores every path with size k, so a reader mixing generations
// or missing rows is detected.
func TestReadersSeeConsistentGenerationsDuringWrites(t *testing.T) {
	const paths, rounds, readers = 40, 40, 4
	store := Store{Dir: t.TempDir(), layout: layout{blockRows: 4, segmentRows: 10, maxDeltas: 2, minDeltaRows: 3}}
	plan, err := query.Parse("SELECT path, size FROM files WHERE size >= 0")
	if err != nil {
		t.Fatal(err)
	}
	var done atomic.Bool
	var reads atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !done.Load() {
				if err := checkSnapshot(store, plan, paths); err != nil {
					errs <- err
					return
				}
				reads.Add(1)
			}
		}()
	}
	for k := 1; k <= rounds; k++ {
		files := make([]catalog.File, 0, paths)
		for i := 0; i < paths; i++ {
			files = append(files, file(fixturePath(fmt.Sprintf("/p/%03d", i)), int64(k)))
		}
		update(t, store, files)
	}
	done.Store(true)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if reads.Load() == 0 {
		t.Fatal("no reads completed")
	}
}

func checkSnapshot(store Store, plan query.Plan, paths int) error {
	snap, err := store.Open(context.Background())
	if err != nil {
		return err
	}
	defer snap.Close()
	header := snap.Header()
	result, err := query.Execute(context.Background(), plan, snap)
	if err != nil {
		return err
	}
	if header.Generation == 0 {
		if len(result.Rows) != 0 {
			return fmt.Errorf("generation 0 has rows")
		}
		return nil
	}
	if len(result.Rows) != paths || header.Entries != uint64(paths) {
		return fmt.Errorf("generation %d: %d rows, %d entries", header.Generation, len(result.Rows), header.Entries)
	}
	for _, row := range result.Rows {
		if row[1] != int64(header.Generation) {
			return fmt.Errorf("generation %d contains %v", header.Generation, row)
		}
	}
	return nil
}

// TestConcurrentWritersAreSerialized starts writers together: each either
// publishes a distinct generation or fails with ErrLocked, and no update is lost.
func TestConcurrentWritersAreSerialized(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	const writers = 16
	var wg sync.WaitGroup
	results := make(chan app.UpdateResult, writers)
	errs := make(chan error, writers)
	start := make(chan struct{})
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			result, err := store.Update(context.Background(), func(tx app.Transaction) error {
				return tx.Put(file(fixturePath(fmt.Sprintf("/w/%02d", w)), int64(w)))
			})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(w)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrLocked) {
			t.Fatalf("unexpected writer error: %v", err)
		}
	}
	generations := make(map[uint64]bool)
	for result := range results {
		if !result.Published || generations[result.Generation] {
			t.Fatalf("duplicate or unpublished generation: %#v", result)
		}
		generations[result.Generation] = true
	}
	header, files := records(t, store)
	if len(generations) == 0 || header.Generation != uint64(len(generations)) || len(files) != len(generations) {
		t.Fatalf("lost updates: generation %d, files %d, successes %d", header.Generation, len(files), len(generations))
	}
}

// TestSnapshotSurvivesSegmentRemoval keeps a snapshot open across a clear that
// unlinks every segment it references.
func TestSnapshotSurvivesSegmentRemoval(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	update(t, store, []catalog.File{file(fixturePath("/a"), 1), file(fixturePath("/b"), 2)})
	snap, err := store.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	if _, err := store.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	if files := segmentFiles(t, store.Dir); len(files) != 0 {
		t.Fatalf("segments not removed: %v", files)
	}
	count := 0
	if err := snap.Records(context.Background(), func(catalog.File) error { count++; return nil }); err != nil || count != 2 {
		t.Fatalf("open snapshot lost data: %d, %v", count, err)
	}
}
