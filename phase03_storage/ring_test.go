package storage

import (
	"errors"
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func pageRange(from, to types.PageID) []types.PageID {
	var out []types.PageID
	for id := from; id <= to; id++ {
		out = append(out, id)
	}
	return out
}

// hotPool: 6 frames, pages 0..3 cached in frames 0..3 and unpinned.
func hotPool(t *testing.T) (*BufferPool, *MemDisk) {
	t.Helper()
	bp, disk, _ := newPool(6, 30)
	for id := types.PageID(0); id < 4; id++ {
		install(bp, FrameID(id), id, 0, false)
	}
	return bp, disk
}

func TestP3_17_FetchPageWithRing_ScanKeepsHotPages(t *testing.T) {
	bp, disk := hotPool(t)
	ring := NewRing(2)
	usedFrames := map[FrameID]bool{}
	for id := types.PageID(10); id < 30; id++ {
		f, err := bp.FetchPageWithRing(id, ring)
		if err != nil {
			t.Fatalf("FetchPageWithRing(%d): %v", id, err)
		}
		if PageStamp(f.Data) != id || f.PinCount != 1 {
			t.Fatalf("page %d: stamp %d pins %d", id, PageStamp(f.Data), f.PinCount)
		}
		usedFrames[f.ID] = true
		mustUnpin(t, bp, id, false)
	}
	wantIO(t, disk, pageRange(10, 29), nil)
	if len(usedFrames) != 2 || !usedFrames[4] || !usedFrames[5] {
		t.Fatalf("scan used frames %v, want only {4, 5}", usedFrames)
	}
	if got := bp.Resident(); !slices.Equal(got, []types.PageID{0, 1, 2, 3, 28, 29}) {
		t.Fatalf("resident = %v, want [0 1 2 3 28 29]", got)
	}
	if !slices.Equal(ring.slots, []FrameID{4, 5}) || ring.next != 0 {
		t.Fatalf("ring slots=%v next=%d, want [4 5] next=0", ring.slots, ring.next)
	}
	// The hot pages are still cached: fetching them again is a hit.
	for id := types.PageID(0); id < 4; id++ {
		if f, err := bp.FetchPageWithRing(id, ring); err != nil || f.ID != FrameID(id) {
			t.Fatalf("hot page %d: frame %v err %v, want hit in frame %d", id, f, err, id)
		}
	}
	wantIO(t, disk, pageRange(10, 29), nil)
}

// Why the ring exists: the same scan with plain FetchPage wipes the cache.
func TestP3_17_FetchPageWithRing_PlainFetchFlushesCache(t *testing.T) {
	bp, disk := hotPool(t)
	for id := types.PageID(10); id < 30; id++ {
		mustFetch(t, bp, id)
		mustUnpin(t, bp, id, false)
	}
	if got := bp.Resident(); !slices.Equal(got, pageRange(24, 29)) {
		t.Fatalf("resident = %v, want [24..29]", got)
	}
	disk.ResetStats()
	for id := types.PageID(0); id < 4; id++ {
		mustFetch(t, bp, id)
	}
	if disk.Reads != 4 {
		t.Fatalf("re-reading hot pages took %d reads, want 4", disk.Reads)
	}
}

func TestP3_17_FetchPageWithRing_HitDoesNotTouchRing(t *testing.T) {
	bp, disk := hotPool(t)
	ring := NewRing(3)
	f, err := bp.FetchPageWithRing(2, ring)
	if err != nil || f.ID != 2 || f.PinCount != 1 {
		t.Fatalf("hit: frame %v err %v", f, err)
	}
	if ring.next != 0 || !slices.Equal(ring.slots, []FrameID{-1, -1, -1}) {
		t.Fatalf("ring slots=%v next=%d, want untouched", ring.slots, ring.next)
	}
	wantIO(t, disk, nil, nil)
	// hit pins: page 2 is no longer evictable
	if bp.replacer.Size() != 3 {
		t.Fatalf("evictable = %d, want 3", bp.replacer.Size())
	}
}

func TestP3_17_FetchPageWithRing_PinnedRingFrameIsReplaced(t *testing.T) {
	bp, disk, _ := newPool(4, 30)
	ring := NewRing(2)
	fetch := func(id types.PageID) *Frame {
		t.Helper()
		f, err := bp.FetchPageWithRing(id, ring)
		if err != nil {
			t.Fatalf("FetchPageWithRing(%d): %v", id, err)
		}
		return f
	}
	if f := fetch(10); f.ID != 0 { // slot 0 -> frame 0, kept pinned
		t.Fatalf("page 10 in frame %d, want 0", f.ID)
	}
	if f := fetch(11); f.ID != 1 { // slot 1 -> frame 1
		t.Fatalf("page 11 in frame %d, want 1", f.ID)
	}
	mustUnpin(t, bp, 11, false)
	if f := fetch(12); f.ID != 2 { // slot 0: frame 0 pinned -> new frame 2
		t.Fatalf("page 12 in frame %d, want 2 (frame 0 is pinned)", f.ID)
	}
	mustUnpin(t, bp, 12, false)
	if f := fetch(13); f.ID != 1 { // slot 1: frame 1 unpinned -> reuse
		t.Fatalf("page 13 in frame %d, want 1 (reused)", f.ID)
	}
	if !slices.Equal(ring.slots, []FrameID{2, 1}) {
		t.Fatalf("ring slots = %v, want [2 1]", ring.slots)
	}
	if got := bp.Resident(); !slices.Equal(got, []types.PageID{10, 12, 13}) {
		t.Fatalf("resident = %v, want [10 12 13]", got)
	}
	if bp.FreeFrames() != 1 {
		t.Fatalf("free frames = %d, want 1", bp.FreeFrames())
	}
	wantIO(t, disk, []types.PageID{10, 11, 12, 13}, nil)
}

func TestP3_17_FetchPageWithRing_DirtyRingFrameWrittenOnce(t *testing.T) {
	bp, disk, r := newPool(4, 30)
	ring := NewRing(1)
	f, err := bp.FetchPageWithRing(10, ring)
	if err != nil {
		t.Fatal(err)
	}
	copy(f.Data[8:], "updated")
	mustUnpin(t, bp, 10, true)
	g, err := bp.FetchPageWithRing(11, ring)
	if err != nil || g != f {
		t.Fatalf("page 11: frame %v err %v, want ring frame reused", g, err)
	}
	wantIO(t, disk, []types.PageID{10, 11}, []types.PageID{10})
	if string(disk.Page(10)[8:15]) != "updated" {
		t.Fatal("dirty page 10 not written back")
	}
	if g.Dirty || g.PinCount != 1 || r.Size() != 0 || len(r.order) != 1 {
		t.Fatalf("frame dirty=%v pins=%d, replacer evictable=%d tracked=%d; want false 1 0 1", g.Dirty, g.PinCount, r.Size(), len(r.order))
	}
}

func TestP3_17_FetchPageWithRing_Errors(t *testing.T) {
	t.Run("read error empties the slot", func(t *testing.T) {
		bp, disk, _ := newPool(2, 30)
		ring := NewRing(2)
		disk.FailRead[10] = errBoom
		if _, err := bp.FetchPageWithRing(10, ring); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want errBoom", err)
		}
		if !slices.Equal(ring.slots, []FrameID{-1, -1}) || ring.next != 1 {
			t.Fatalf("ring slots=%v next=%d, want [-1 -1] next=1", ring.slots, ring.next)
		}
		if bp.FreeFrames() != 2 || !slices.Equal(bp.freeList, []FrameID{1, 0}) {
			t.Fatalf("free list = %v, want [1 0]", bp.freeList)
		}
	})
	t.Run("ring frame freed by someone else is not reused", func(t *testing.T) {
		bp, disk, _ := newPool(2, 30)
		ring := NewRing(1)
		if _, err := bp.FetchPageWithRing(10, ring); err != nil { // frame 0
			t.Fatal(err)
		}
		mustUnpin(t, bp, 10, false)
		mustFetch(t, bp, 11) // takes free frame 1, stays pinned
		disk.FailRead[12] = errBoom
		if _, err := bp.FetchPage(12); !errors.Is(err, errBoom) { // evicts 10, frame 0 back to free list
			t.Fatalf("err = %v, want errBoom", err)
		}
		f, err := bp.FetchPageWithRing(13, ring)
		if err != nil || f.ID != 0 {
			t.Fatalf("page 13: frame %v err %v, want frame 0 taken from the free list", f, err)
		}
		if len(bp.freeList) != 0 || !slices.Equal(bp.Resident(), []types.PageID{11, 13}) {
			t.Fatalf("free list=%v resident=%v, want [] and [11 13]", bp.freeList, bp.Resident())
		}
	})
	t.Run("no frame available", func(t *testing.T) {
		bp, _, _ := newPool(1, 30)
		ring := NewRing(2)
		if _, err := bp.FetchPageWithRing(10, ring); err != nil {
			t.Fatal(err)
		}
		if _, err := bp.FetchPageWithRing(11, ring); !errors.Is(err, ErrNoFreeFrames) {
			t.Fatalf("err = %v, want ErrNoFreeFrames", err)
		}
	})
}
