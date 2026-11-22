//go:build darwin || linux

package storage

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"fyreq/internal/app"
	"fyreq/internal/catalog"
	"fyreq/internal/query"
)

func file(path string, size int64) catalog.File {
	return catalog.File{ID: "id-" + filepath.Base(path), Path: path, Name: filepath.Base(path), Kind: "file", Size: size}
}

func update(t *testing.T, store Store, puts []catalog.File, deletes ...string) app.UpdateResult {
	t.Helper()
	result, err := store.Update(context.Background(), func(tx app.Transaction) error {
		for _, f := range puts {
			if err := tx.Put(f); err != nil {
				return err
			}
		}
		for _, path := range deletes {
			if err := tx.Delete(path); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func records(t *testing.T, store Store) (catalog.Header, map[string]catalog.File) {
	t.Helper()
	snap, err := store.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	result := make(map[string]catalog.File)
	var previous string
	err = snap.Records(context.Background(), func(f catalog.File) error {
		if f.Path <= previous {
			t.Fatalf("records out of order: %q after %q", f.Path, previous)
		}
		previous = f.Path
		result[f.Path] = f
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap.Header(), result
}

func segmentFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*"+segmentExtension))
	if err != nil {
		t.Fatal(err)
	}
	for i := range matches {
		matches[i] = filepath.Base(matches[i])
	}
	sort.Strings(matches)
	return matches
}

func validManifest() manifest {
	m := emptyManifest()
	m.generation = 3
	return m
}

func TestClearReplacesUnreadableCatalogs(t *testing.T) {
	other := validManifest()
	other.platform = "other-platform"
	unsupported := validManifest().encode()
	binary.BigEndian.PutUint16(unsupported[len(manifestMagic):], 999)
	binary.BigEndian.PutUint32(unsupported[len(unsupported)-4:], checksum(unsupported[:len(unsupported)-4]))
	for name, tc := range map[string]struct {
		file string
		data []byte
	}{
		"legacy":      {legacyName, []byte(`{"version":2,"files":{},"coverage":{}}`)},
		"corrupt":     {manifestName, []byte("FYRQMAN\x00garbage")},
		"platform":    {manifestName, other.encode()},
		"unsupported": {manifestName, unsupported},
	} {
		t.Run(name, func(t *testing.T) {
			store := Store{Dir: t.TempDir()}
			path := filepath.Join(store.Dir, tc.file)
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Open(context.Background()); err == nil {
				t.Fatal("unreadable catalog opened")
			}
			assertUnchanged := func() {
				t.Helper()
				if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, tc.data) {
					t.Fatalf("failed clear changed catalog: %q, %v", after, err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if result, err := store.Clear(ctx); !errors.Is(err, context.Canceled) || result.Published {
				t.Fatalf("canceled clear: %#v, %v", result, err)
			}
			assertUnchanged()
			lock := filepath.Join(store.Dir, ".writer-lock")
			if err := os.Mkdir(lock, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Clear(context.Background()); !errors.Is(err, ErrLocked) {
				t.Fatalf("clear ignored writer lock: %v", err)
			}
			assertUnchanged()
			if err := os.Remove(lock); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("publication failed")
			store.publishForTest = func(string, string, string) (bool, error) { return false, failure }
			if result, err := store.Clear(context.Background()); !errors.Is(err, failure) || result.Published {
				t.Fatalf("failed clear: %#v, %v", result, err)
			}
			assertUnchanged()
			store.publishForTest = nil
			result, err := store.Clear(context.Background())
			if err != nil || !result.Published || result.Generation != 1 {
				t.Fatalf("clear: %#v, %v", result, err)
			}
			header, files := records(t, store)
			if header.Generation != 1 || len(files) != 0 || len(header.Coverage) != 0 || header.Entries != 0 {
				t.Fatalf("clear did not create a readable empty catalog: %#v", header)
			}
			if _, err := os.Stat(filepath.Join(store.Dir, legacyName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("legacy catalog retained: %v", err)
			}
			store.publishForTest = func(src, dst, dir string) (bool, error) {
				if err := os.Rename(src, dst); err != nil {
					return false, err
				}
				return true, failure
			}
			if result, err := store.Clear(context.Background()); !errors.Is(err, failure) || !result.Published || result.Generation != 1 {
				t.Fatalf("published clear with confirmation failure: %#v, %v", result, err)
			}
		})
	}
}

func TestClearRemovesSegments(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	update(t, store, []catalog.File{file("/a", 1)})
	if len(segmentFiles(t, store.Dir)) == 0 {
		t.Fatal("update wrote no segment")
	}
	if _, err := store.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	if files := segmentFiles(t, store.Dir); len(files) != 0 {
		t.Fatalf("segments retained after clear: %v", files)
	}
}

func TestLegacyCatalogRejectedWithoutModification(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	path := filepath.Join(store.Dir, legacyName)
	data := []byte(`{"version":2,"files":{},"coverage":{}}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(context.Background(), func(app.Transaction) error { t.Fatal("mutation called"); return nil }); err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("legacy catalog accepted: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Fatalf("legacy catalog changed: %s %v", after, err)
	}
}

func TestPersistenceAndFailedUpdate(t *testing.T) {
	ctx := context.Background()
	store := Store{Dir: t.TempDir()}
	header, files := records(t, store)
	if header.Generation != 0 || len(files) != 0 || header.Platform != runtime.GOOS {
		t.Fatalf("unexpected empty catalog: %#v", header)
	}
	path := filepath.Join(t.TempDir(), "a")
	update(t, store, []catalog.File{file(path, 12)})
	header, files = records(t, Store{Dir: store.Dir})
	if header.Generation != 1 || header.Entries != 1 || files[path].Size != 12 {
		t.Fatalf("unexpected persisted catalog: %#v, %#v", header, files)
	}
	segments := segmentFiles(t, store.Dir)
	failure := errors.New("collection failed")
	if _, err := store.Update(ctx, func(tx app.Transaction) error {
		if err := tx.Delete(path); err != nil {
			return err
		}
		if err := tx.Put(file(path+"2", 1)); err != nil {
			return err
		}
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("expected callback failure, got %v", err)
	}
	header, files = records(t, store)
	if header.Generation != 1 || len(files) != 1 {
		t.Fatalf("failed update changed persisted state: %#v", header)
	}
	if after := segmentFiles(t, store.Dir); !reflect.DeepEqual(after, segments) {
		t.Fatalf("failed update left segments: %v, want %v", after, segments)
	}
}

func TestInvalidRecordsRejected(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	for _, f := range []catalog.File{{ID: "x", Path: "relative"}, {Path: "/no-id"}} {
		if _, err := store.Update(context.Background(), func(tx app.Transaction) error { return tx.Put(f) }); err == nil {
			t.Fatalf("accepted invalid record %#v", f)
		}
	}
	if _, err := store.Update(context.Background(), func(tx app.Transaction) error { return tx.SetCoverage(catalog.Coverage{Root: "relative"}) }); err == nil {
		t.Fatal("accepted relative coverage")
	}
}

func TestWriterLockAndReaders(t *testing.T) {
	ctx := context.Background()
	store := Store{Dir: t.TempDir()}
	update(t, store, nil)
	if _, err := store.Update(ctx, func(app.Transaction) error {
		if _, err := store.Update(ctx, func(app.Transaction) error { return nil }); !errors.Is(err, ErrLocked) {
			t.Fatalf("expected writer lock, got %v", err)
		}
		if _, err := store.Clear(ctx); !errors.Is(err, ErrLocked) {
			t.Fatalf("expected writer lock for clear, got %v", err)
		}
		snap, err := store.Open(ctx)
		if err != nil || snap.Header().Generation != 1 {
			t.Fatalf("reader cannot access previous snapshot: %v", err)
		}
		return snap.Close()
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledUpdateDoesNotPublish(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := store.Update(ctx, func(tx app.Transaction) error {
		cancel()
		return tx.Put(file("/a", 1))
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if header, _ := records(t, store); header.Generation != 0 {
		t.Fatalf("canceled update published: %#v", header)
	}
	if files := segmentFiles(t, store.Dir); len(files) != 0 {
		t.Fatalf("canceled update left segments: %v", files)
	}
	update(t, store, nil)
}

func TestRejectCorruptAndUnsupportedCatalog(t *testing.T) {
	coverage := validManifest()
	coverage.coverage["relative"] = catalog.Coverage{Root: "relative"}
	traversal := validManifest()
	traversal.deltas = []segmentRef{{name: "../../outside" + segmentExtension}}
	missing := validManifest()
	missing.deltas = []segmentRef{{name: strings.Repeat("ab", 16) + segmentExtension}}
	flipped := validManifest().encode()
	flipped[len(flipped)-6] ^= 0xff
	for name, data := range map[string][]byte{
		"empty":     {},
		"truncated": validManifest().encode()[:12],
		"checksum":  flipped,
		"coverage":  coverage.encode(),
		"traversal": traversal.encode(),
		"missing":   missing.encode(),
	} {
		store := Store{Dir: t.TempDir()}
		if err := os.WriteFile(filepath.Join(store.Dir, manifestName), data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Open(context.Background()); err == nil {
			t.Fatalf("%s: accepted invalid catalog", name)
		}
	}
}

func TestCorruptSegmentDetected(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	update(t, store, []catalog.File{file("/a", 1), file("/b", 2)})
	name := segmentFiles(t, store.Dir)[0]
	path := filepath.Join(store.Dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[headerSize] ^= 0xff
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	snap, err := store.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	if err := snap.Records(context.Background(), func(catalog.File) error { return nil }); !errors.Is(err, errCorrupt) {
		t.Fatalf("corrupt chunk not detected: %v", err)
	}
}

func TestPublicationOutcomes(t *testing.T) {
	failure := errors.New("confirmation failed")
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			store := Store{Dir: t.TempDir()}
			store.publishForTest = func(src, dst, dir string) (bool, error) {
				if after {
					if err := os.Rename(src, dst); err != nil {
						t.Fatal(err)
					}
				}
				return after, failure
			}
			result, err := store.Update(context.Background(), func(tx app.Transaction) error { return tx.Put(file("/a", 1)) })
			if !errors.Is(err, failure) || result.Published != after || (after && result.Generation != 1) {
				t.Fatalf("result: %#v %v", result, err)
			}
			header, files := records(t, store)
			if (after && (header.Generation != 1 || len(files) != 1)) || (!after && header.Generation != 0) {
				t.Fatalf("visibility: %#v", header)
			}
			if !after && len(segmentFiles(t, store.Dir)) != 0 {
				t.Fatal("unpublished segments retained")
			}
		})
	}
}

func TestRecordRoundTrip(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	zero, name := "0", "root"
	modified := time.Date(2026, 2, 3, 4, 5, 6, 789, time.UTC)
	known := catalog.File{ID: "a", Path: "/dir/known", Name: "known", Kind: "file", Size: 1 << 40, ModifiedAt: modified, IndexedAt: modified.Add(time.Second), Identity: "1:2", OwnerID: &zero, Owner: &name}
	unknown := catalog.File{ID: "b", Path: "/dir/unknown", Name: "unknown", Kind: "directory", ModifiedAt: time.Unix(-5, 3).UTC()}
	empty := catalog.File{ID: "c", Path: "/dir/zero"}
	coverage := catalog.Coverage{Root: "/dir", StartedAt: modified, CompletedAt: modified.Add(time.Minute), ErrorCount: 101, Errors: []string{"x: denied", "y\nz"}}
	if _, err := store.Update(context.Background(), func(tx app.Transaction) error {
		for _, f := range []catalog.File{known, unknown, empty} {
			if err := tx.Put(f); err != nil {
				return err
			}
		}
		return tx.SetCoverage(coverage)
	}); err != nil {
		t.Fatal(err)
	}
	header, files := records(t, store)
	for _, want := range []catalog.File{known, unknown, empty} {
		if got := files[want.Path]; !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip:\n got %#v\nwant %#v", got, want)
		}
	}
	if !reflect.DeepEqual(header.Coverage["/dir"], coverage) || header.Entries != 3 {
		t.Fatalf("coverage: %#v", header)
	}
}

func TestSingleChangeWritesOneSmallDelta(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	var initial []catalog.File
	for i := 0; i < 5000; i++ {
		initial = append(initial, file(fmt.Sprintf("/root/%05d", i), int64(i)))
	}
	update(t, store, initial)
	before := segmentFiles(t, store.Dir)
	info := make(map[string]os.FileInfo)
	for _, name := range before {
		fi, err := os.Stat(filepath.Join(store.Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		info[name] = fi
	}
	changed := file("/root/02500", 99)
	update(t, store, []catalog.File{changed})
	after := segmentFiles(t, store.Dir)
	if len(after) != len(before)+1 {
		t.Fatalf("segments %v -> %v", before, after)
	}
	for _, name := range before {
		fi, err := os.Stat(filepath.Join(store.Dir, name))
		if err != nil || !os.SameFile(fi, info[name]) || fi.ModTime() != info[name].ModTime() || fi.Size() != info[name].Size() {
			t.Fatalf("base segment %s rewritten: %v", name, err)
		}
	}
	snap, err := store.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	segments := snap.Segments()
	delta := segments[len(segments)-1]
	if delta.Level != "delta" || delta.Records != 1 || delta.Tombstones != 0 || delta.Bytes > 1024 {
		t.Fatalf("unexpected delta: %#v", delta)
	}
	if header, files := records(t, store); header.Entries != 5000 || files[changed.Path].Size != 99 || len(files) != 5000 {
		t.Fatalf("unexpected view: %#v", header)
	}
	// Coverage-only updates write no segment.
	if _, err := store.Update(context.Background(), func(tx app.Transaction) error { return tx.SetCoverage(catalog.Coverage{Root: "/root"}) }); err != nil {
		t.Fatal(err)
	}
	if again := segmentFiles(t, store.Dir); !reflect.DeepEqual(again, after) {
		t.Fatalf("coverage update wrote segments: %v", again)
	}
}

// TestRandomizedUpdatesMatchModel exercises deltas, tombstones, compaction,
// and segment splitting with tiny layouts against an in-memory model.
func TestRandomizedUpdatesMatchModel(t *testing.T) {
	store := Store{Dir: t.TempDir(), layout: layout{blockRows: 3, segmentRows: 7, maxDeltas: 3, minDeltaRows: 5}}
	random := rand.New(rand.NewSource(1))
	model := make(map[string]catalog.File)
	for round := 0; round < 60; round++ {
		var puts []catalog.File
		var deletes []string
		for n := random.Intn(12); n >= 0; n-- {
			path := fmt.Sprintf("/r/%c/%d", 'a'+random.Intn(4), random.Intn(15))
			if random.Intn(3) == 0 {
				deletes = append(deletes, path)
				delete(model, path)
				for i := range puts {
					if puts[i].Path == path {
						puts = append(puts[:i], puts[i+1:]...)
						break
					}
				}
			} else {
				for i := range deletes {
					if deletes[i] == path {
						deletes = append(deletes[:i], deletes[i+1:]...)
						break
					}
				}
				f := file(path, int64(random.Intn(100)))
				puts = append(puts, f)
				model[path] = f
			}
		}
		update(t, store, puts, deletes...)
		header, files := records(t, store)
		if !reflect.DeepEqual(files, model) && !(len(files) == 0 && len(model) == 0) {
			t.Fatalf("round %d: view diverged\n got %v\nwant %v", round, files, model)
		}
		if header.Entries != uint64(len(model)) {
			t.Fatalf("round %d: entries %d, want %d", round, header.Entries, len(model))
		}
		snap, err := store.Open(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		live := make(map[string]bool)
		var previous string
		for _, seg := range snap.Segments() {
			live[seg.Name] = true
			if seg.Level == "base" {
				if seg.MinPath <= previous || seg.Records > 7 {
					t.Fatalf("round %d: invalid base layout %#v", round, snap.Segments())
				}
				previous = seg.MaxPath
			}
		}
		snap.Close()
		if len(segmentFiles(t, store.Dir)) != len(live) {
			t.Fatalf("round %d: unreferenced segments retained", round)
		}
	}
}

func scan(t *testing.T, store Store, sql string) (query.Result, query.ScanStats) {
	t.Helper()
	plan, err := query.Parse(sql)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := store.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	result, err := query.Execute(context.Background(), plan, snap)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := snap.Scan(context.Background(), plan.Request(), func(catalog.File) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return result, stats
}

func TestDeltasShadowFilteredRecords(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	update(t, store, []catalog.File{file("/a", 10), file("/b", 10), file("/c", 10)})
	update(t, store, []catalog.File{file("/a", 1)}, "/b")
	result, _ := scan(t, store, "SELECT path FROM files WHERE size >= 10")
	if want := [][]any{{"/c"}}; !reflect.DeepEqual(result.Rows, want) {
		t.Fatalf("rows %#v, want %#v", result.Rows, want)
	}
	result, _ = scan(t, store, "SELECT path, size FROM files")
	if want := [][]any{{"/a", int64(1)}, {"/c", int64(10)}}; !reflect.DeepEqual(result.Rows, want) {
		t.Fatalf("rows %#v, want %#v", result.Rows, want)
	}
}

func TestPruningSkipsBlocksAndColumns(t *testing.T) {
	store := Store{Dir: t.TempDir(), layout: layout{blockRows: 10, segmentRows: 100, maxDeltas: 8, minDeltaRows: 10}}
	var files []catalog.File
	for i := 0; i < 1000; i++ {
		files = append(files, file(fmt.Sprintf("/data/%04d", i), int64(i)))
	}
	update(t, store, files)
	result, stats := scan(t, store, "SELECT path FROM files WHERE size >= 995")
	if len(result.Rows) != 5 || stats.Segments != 10 || stats.SegmentsPruned != 9 || stats.BlocksPruned != 99 || stats.RowsRead != 10 || stats.RowsMatched != 5 {
		t.Fatalf("rows=%d stats=%#v", len(result.Rows), stats)
	}
	_, prefix := scan(t, store, "SELECT name FROM files WHERE path LIKE '/data/012%'")
	if prefix.RowsMatched != 10 || prefix.BlocksPruned != 99 {
		t.Fatalf("prefix stats %#v", prefix)
	}
	_, narrow := scan(t, store, "SELECT COUNT(*) FROM files WHERE kind = 'file'")
	_, wide := scan(t, store, "SELECT * FROM files WHERE kind = 'file'")
	if narrow.BytesRead*3 > wide.BytesRead {
		t.Fatalf("narrow query read %d bytes, wide %d", narrow.BytesRead, wide.BytesRead)
	}
	explained, _ := scan(t, store, "EXPLAIN SELECT path FROM files WHERE size >= 995")
	if !explained.Explain || !strings.Contains(fmt.Sprint(explained.Rows), "Blocks: 100, pruned 99") {
		t.Fatalf("explain %v", explained.Rows)
	}
	limited, limitedStats := scan(t, store, "EXPLAIN ANALYZE SELECT path FROM files LIMIT 3")
	if !strings.Contains(fmt.Sprint(limited.Rows), "Rows: read 10, matched 10") || limitedStats.RowsRead != 1000 {
		t.Fatalf("limit did not stop the scan early: %v", limited.Rows)
	}
}

func TestFilesWithinRoot(t *testing.T) {
	store := Store{Dir: t.TempDir(), layout: layout{blockRows: 2, segmentRows: 4, maxDeltas: 8, minDeltaRows: 1000}}
	update(t, store, []catalog.File{file("/a", 1), file("/a-b", 1), file("/a/x", 1), file("/a/y/z", 1), file("/b", 1)})
	update(t, store, nil, "/a/x")
	_, err := store.Update(context.Background(), func(tx app.Transaction) error {
		if err := tx.Put(file("/a/new", 1)); err != nil {
			return err
		}
		files, err := tx.Files(context.Background(), "/a")
		if err != nil {
			return err
		}
		var paths []string
		for path := range files {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		if want := []string{"/a", "/a/new", "/a/y/z"}; !reflect.DeepEqual(paths, want) {
			t.Fatalf("files %v, want %v", paths, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
