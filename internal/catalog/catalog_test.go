package catalog

import (
	"testing"
	"time"
)

func TestSameMetadataIgnoresObservationTime(t *testing.T) {
	owner, other := "0", "0"
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := File{ID: "x", Path: "/a", Size: 1, ModifiedAt: now, IndexedAt: now, OwnerID: &owner}
	b := a
	b.IndexedAt = now.Add(time.Hour)
	b.ModifiedAt = now.In(time.FixedZone("x", 3600))
	b.OwnerID = &other
	if !SameMetadata(a, b) {
		t.Fatal("equal metadata reported as changed")
	}
	b.OwnerID = nil
	if SameMetadata(a, b) {
		t.Fatal("ownership change not detected")
	}
	b = a
	b.Size = 2
	if SameMetadata(a, b) {
		t.Fatal("size change not detected")
	}
}
