package storage

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

// sliceLRU is a tiny, obviously-correct LRU used by the buffer-pool tests so
// they don't depend on the learner's replacer solutions.
type sliceLRU struct {
	order []FrameID // front = least recently used
	ev    map[FrameID]bool
}

func newSliceLRU() *sliceLRU { return &sliceLRU{ev: map[FrameID]bool{}} }

func (r *sliceLRU) index(f FrameID) int { return slices.Index(r.order, f) }

func (r *sliceLRU) RecordAccess(f FrameID) {
	if i := r.index(f); i >= 0 {
		r.order = append(slices.Delete(r.order, i, i+1), f)
		return
	}
	r.order = append(r.order, f)
	r.ev[f] = false
}

func (r *sliceLRU) SetEvictable(f FrameID, e bool) {
	if r.index(f) >= 0 {
		r.ev[f] = e
	}
}

func (r *sliceLRU) Evict() (FrameID, bool) {
	for i, f := range r.order {
		if r.ev[f] {
			r.order = slices.Delete(r.order, i, i+1)
			delete(r.ev, f)
			return f, true
		}
	}
	return 0, false
}

func (r *sliceLRU) Remove(f FrameID) {
	if i := r.index(f); i >= 0 {
		r.order = slices.Delete(r.order, i, i+1)
		delete(r.ev, f)
	}
}

func (r *sliceLRU) Size() int {
	n := 0
	for _, f := range r.order {
		if r.ev[f] {
			n++
		}
	}
	return n
}

func newPool(frames, diskPages int) (*BufferPool, *MemDisk, *sliceLRU) {
	disk := NewMemDisk(diskPages)
	r := newSliceLRU()
	return NewBufferPool(frames, disk, r), disk, r
}

// install puts page pid into frame fid directly (bypassing FetchPage), the
// way FetchPage would leave it: stamped Data, page table entry, tracked by
// the replacer, evictable iff pin == 0. Calls install in order define LRU order.
func install(bp *BufferPool, fid FrameID, pid types.PageID, pin int, dirty bool) *Frame {
	f := bp.frames[fid]
	clear(f.Data)
	StampPage(f.Data, pid)
	f.PageID, f.PinCount, f.Dirty = pid, pin, dirty
	bp.pageTable[pid] = fid
	if i := slices.Index(bp.freeList, fid); i >= 0 {
		bp.freeList = slices.Delete(bp.freeList, i, i+1)
	}
	bp.replacer.RecordAccess(fid)
	bp.replacer.SetEvictable(fid, pin == 0)
	return f
}

func wantIO(t *testing.T, d *MemDisk, reads, writes []types.PageID) {
	t.Helper()
	if !slices.Equal(d.ReadLog, reads) || d.Reads != len(reads) {
		t.Fatalf("disk reads = %v, want %v", d.ReadLog, reads)
	}
	if !slices.Equal(d.WriteLog, writes) || d.Writes != len(writes) {
		t.Fatalf("disk writes = %v, want %v", d.WriteLog, writes)
	}
}

func mustFetch(t *testing.T, bp *BufferPool, id types.PageID) *Frame {
	t.Helper()
	f, err := bp.FetchPage(id)
	if err != nil {
		t.Fatalf("FetchPage(%d): %v", id, err)
	}
	return f
}

func mustUnpin(t *testing.T, bp *BufferPool, id types.PageID, dirty bool) {
	t.Helper()
	if err := bp.UnpinPage(id, dirty); err != nil {
		t.Fatalf("UnpinPage(%d, %v): %v", id, dirty, err)
	}
}

// ---------------------------------------------------------------- 3.11 UnpinPage

func TestP3_11_UnpinPage(t *testing.T) {
	bp, disk, r := newPool(3, 10)
	f := install(bp, 1, 7, 2, false)

	steps := []struct {
		name      string
		id        types.PageID
		dirty     bool
		wantErr   error
		wantPins  int
		wantDirty bool
		wantEvict int // replacer.Size()
	}{
		{"first unpin marks dirty", 7, true, nil, 1, true, 0},
		{"last unpin makes evictable, dirty stays", 7, false, nil, 0, true, 1},
		{"unpin at zero", 7, false, ErrNotPinned, 0, true, 1},
		{"unpin at zero with dirty=true changes nothing", 7, true, ErrNotPinned, 0, true, 1},
		{"page not resident", 99, false, ErrPageNotResident, 0, true, 1},
	}
	for _, s := range steps {
		err := bp.UnpinPage(s.id, s.dirty)
		if !errors.Is(err, s.wantErr) {
			t.Fatalf("%s: err = %v, want %v", s.name, err, s.wantErr)
		}
		if f.PinCount != s.wantPins || f.Dirty != s.wantDirty || r.Size() != s.wantEvict {
			t.Fatalf("%s: pins=%d dirty=%v evictable=%d, want %d %v %d", s.name, f.PinCount, f.Dirty, r.Size(), s.wantPins, s.wantDirty, s.wantEvict)
		}
	}
	wantIO(t, disk, nil, nil)
}

func TestP3_11_UnpinPage_CleanStaysClean(t *testing.T) {
	bp, _, r := newPool(2, 10)
	f := install(bp, 0, 3, 1, false)
	install(bp, 1, 4, 1, false)
	mustUnpin(t, bp, 3, false)
	if f.Dirty || f.PinCount != 0 {
		t.Fatalf("pins=%d dirty=%v, want 0 false", f.PinCount, f.Dirty)
	}
	if !r.ev[0] || r.ev[1] {
		t.Fatalf("evictable flags f0=%v f1=%v, want true false", r.ev[0], r.ev[1])
	}
}

// ---------------------------------------------------------------- 3.12 acquireFrame

func TestP3_12_AcquireFrame_FreeListFirst(t *testing.T) {
	bp, disk, _ := newPool(3, 10)
	for want := FrameID(0); want < 3; want++ {
		got, err := bp.acquireFrame()
		if err != nil || got != want {
			t.Fatalf("acquireFrame = %d, %v; want %d", got, err, want)
		}
	}
	if len(bp.freeList) != 0 {
		t.Fatalf("free list = %v, want empty", bp.freeList)
	}
	if _, err := bp.acquireFrame(); !errors.Is(err, ErrNoFreeFrames) {
		t.Fatalf("err = %v, want ErrNoFreeFrames (nothing tracked by replacer)", err)
	}
	wantIO(t, disk, nil, nil)
}

func TestP3_12_AcquireFrame_FreeListBeatsEviction(t *testing.T) {
	bp, disk, r := newPool(3, 10)
	install(bp, 0, 5, 0, true) // evictable, dirty
	got, err := bp.acquireFrame()
	if err != nil || got != 1 {
		t.Fatalf("acquireFrame = %d, %v; want 1 (free frame before evicting)", got, err)
	}
	if r.Size() != 1 || bp.pageTable[5] != 0 {
		t.Fatal("page 5 must stay resident and evictable")
	}
	wantIO(t, disk, nil, nil)
}

func TestP3_12_AcquireFrame_Evicts(t *testing.T) {
	tests := []struct {
		name       string
		dirty      bool
		wantWrites []types.PageID
	}{
		{"clean victim: no I/O", false, nil},
		{"dirty victim written exactly once", true, []types.PageID{3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bp, disk, _ := newPool(2, 10)
			f3 := install(bp, 0, 3, 0, tc.dirty) // least recently used
			install(bp, 1, 7, 0, false)
			f3.Data[100] = 0x42 // a modification only in memory

			got, err := bp.acquireFrame()
			if err != nil || got != 0 {
				t.Fatalf("acquireFrame = %d, %v; want 0", got, err)
			}
			wantIO(t, disk, nil, tc.wantWrites)
			if tc.dirty && disk.Page(3)[100] != 0x42 {
				t.Fatal("written page 3 does not contain the in-memory modification")
			}
			if _, ok := bp.pageTable[3]; ok {
				t.Fatal("page 3 still in page table")
			}
			if f3.PageID != types.InvalidPageID || f3.Dirty || f3.PinCount != 0 {
				t.Fatalf("frame not reset: PageID=%d dirty=%v pins=%d", f3.PageID, f3.Dirty, f3.PinCount)
			}
			if bp.pageTable[7] != 1 {
				t.Fatal("page 7 must stay resident in frame 1")
			}
		})
	}
}

func TestP3_12_AcquireFrame_AllPinned(t *testing.T) {
	bp, disk, _ := newPool(2, 10)
	install(bp, 0, 3, 1, true)
	install(bp, 1, 4, 2, false)
	if _, err := bp.acquireFrame(); !errors.Is(err, ErrNoFreeFrames) {
		t.Fatalf("err = %v, want ErrNoFreeFrames", err)
	}
	if !slices.Equal(bp.Resident(), []types.PageID{3, 4}) {
		t.Fatalf("resident = %v, want [3 4]", bp.Resident())
	}
	wantIO(t, disk, nil, nil)
}

func TestP3_12_AcquireFrame_WriteBackFails(t *testing.T) {
	bp, disk, r := newPool(1, 10)
	f := install(bp, 0, 7, 0, true)
	disk.FailWrite[7] = errBoom
	if _, err := bp.acquireFrame(); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want errBoom", err)
	}
	if bp.pageTable[7] != 0 || f.PageID != 7 || !f.Dirty {
		t.Fatalf("victim must stay resident and dirty: PageID=%d dirty=%v", f.PageID, f.Dirty)
	}
	if r.Size() != 1 {
		t.Fatalf("replacer Size = %d, want 1 (victim evictable again)", r.Size())
	}
	delete(disk.FailWrite, 7)
	if got, err := bp.acquireFrame(); err != nil || got != 0 {
		t.Fatalf("retry acquireFrame = %d, %v; want 0", got, err)
	}
	wantIO(t, disk, nil, []types.PageID{7})
}

// ---------------------------------------------------------------- 3.13 FetchPage

func TestP3_13_FetchPage_DocExample(t *testing.T) {
	bp, disk, _ := newPool(2, 10)
	f1 := mustFetch(t, bp, 1)
	if f1.ID != 0 || PageStamp(f1.Data) != 1 || f1.PinCount != 1 {
		t.Fatalf("page 1: frame %d stamp %d pins %d, want frame 0 stamp 1 pins 1", f1.ID, PageStamp(f1.Data), f1.PinCount)
	}
	mustUnpin(t, bp, 1, false)
	f2 := mustFetch(t, bp, 2)
	if f2.ID != 1 {
		t.Fatalf("page 2 in frame %d, want 1", f2.ID)
	}
	mustUnpin(t, bp, 2, false)
	if again := mustFetch(t, bp, 1); again != f1 {
		t.Fatal("hit returned a different *Frame")
	}
	mustUnpin(t, bp, 1, false)
	wantIO(t, disk, []types.PageID{1, 2}, nil)

	f3 := mustFetch(t, bp, 3)
	if f3.ID != 1 || PageStamp(f3.Data) != 3 {
		t.Fatalf("page 3 in frame %d stamp %d, want frame 1 (page 2 evicted), stamp 3", f3.ID, PageStamp(f3.Data))
	}
	f4 := mustFetch(t, bp, 4)
	if f4.ID != 0 {
		t.Fatalf("page 4 in frame %d, want 0 (page 1 evicted)", f4.ID)
	}
	if _, err := bp.FetchPage(5); !errors.Is(err, ErrNoFreeFrames) {
		t.Fatalf("FetchPage(5) err = %v, want ErrNoFreeFrames", err)
	}
	wantIO(t, disk, []types.PageID{1, 2, 3, 4}, nil)
	if got := bp.Resident(); !slices.Equal(got, []types.PageID{3, 4}) {
		t.Fatalf("resident = %v, want [3 4]", got)
	}
}

func TestP3_13_FetchPage_HitDoesNotTouchDisk(t *testing.T) {
	bp, disk, r := newPool(3, 10)
	f := mustFetch(t, bp, 4)
	for i := 0; i < 5; i++ {
		if g := mustFetch(t, bp, 4); g != f {
			t.Fatal("hit returned a different frame")
		}
	}
	if f.PinCount != 6 {
		t.Fatalf("PinCount = %d, want 6", f.PinCount)
	}
	wantIO(t, disk, []types.PageID{4}, nil)
	// a hit on an unpinned page pins it again: no longer evictable
	for i := 0; i < 6; i++ {
		mustUnpin(t, bp, 4, false)
	}
	if r.Size() != 1 {
		t.Fatalf("evictable = %d, want 1", r.Size())
	}
	mustFetch(t, bp, 4)
	if r.Size() != 0 {
		t.Fatalf("evictable after hit = %d, want 0", r.Size())
	}
	wantIO(t, disk, []types.PageID{4}, nil)
}

func TestP3_13_FetchPage_DirtyVictimWrittenOnce(t *testing.T) {
	bp, disk, _ := newPool(1, 10)
	f := mustFetch(t, bp, 1)
	copy(f.Data[8:], "modified")
	mustUnpin(t, bp, 1, true)

	mustFetch(t, bp, 2) // evicts dirty page 1
	wantIO(t, disk, []types.PageID{1, 2}, []types.PageID{1})
	if got := disk.Page(1); !bytes.Equal(got[8:16], []byte("modified")) || PageStamp(got) != 1 {
		t.Fatalf("disk page 1 = %q stamp %d, want \"modified\" stamp 1", got[8:16], PageStamp(got))
	}
	mustUnpin(t, bp, 2, false)

	g := mustFetch(t, bp, 1) // clean page 2 evicted: no write
	if string(g.Data[8:16]) != "modified" || g.Dirty {
		t.Fatalf("re-read page 1 = %q dirty=%v, want \"modified\" clean", g.Data[8:16], g.Dirty)
	}
	wantIO(t, disk, []types.PageID{1, 2, 1}, []types.PageID{1})
}

func TestP3_13_FetchPage_Errors(t *testing.T) {
	t.Run("all frames pinned", func(t *testing.T) {
		bp, disk, _ := newPool(2, 10)
		mustFetch(t, bp, 1)
		mustFetch(t, bp, 2)
		if _, err := bp.FetchPage(3); !errors.Is(err, ErrNoFreeFrames) {
			t.Fatalf("err = %v, want ErrNoFreeFrames", err)
		}
		mustFetch(t, bp, 1) // hits still work on a full pool
		wantIO(t, disk, []types.PageID{1, 2}, nil)
	})
	t.Run("read error frees the frame", func(t *testing.T) {
		bp, disk, _ := newPool(2, 10)
		disk.FailRead[5] = errBoom
		if _, err := bp.FetchPage(5); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want errBoom", err)
		}
		if _, ok := bp.PinCount(5); ok || bp.FreeFrames() != 2 {
			t.Fatalf("resident=%v free=%d, want not resident, 2 free frames", ok, bp.FreeFrames())
		}
		if !slices.Equal(bp.freeList, []FrameID{1, 0}) {
			t.Fatalf("free list = %v, want [1 0] (failed frame appended)", bp.freeList)
		}
	})
	t.Run("page beyond end of disk", func(t *testing.T) {
		bp, _, _ := newPool(2, 10)
		if _, err := bp.FetchPage(10); !errors.Is(err, ErrPageNotFound) {
			t.Fatalf("err = %v, want ErrPageNotFound", err)
		}
		if bp.FreeFrames() != 2 {
			t.Fatalf("free frames = %d, want 2", bp.FreeFrames())
		}
	})
	t.Run("read error after evicting a dirty victim", func(t *testing.T) {
		bp, disk, _ := newPool(1, 10)
		mustFetch(t, bp, 1)
		mustUnpin(t, bp, 1, true)
		disk.FailRead[2] = errBoom
		if _, err := bp.FetchPage(2); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want errBoom", err)
		}
		wantIO(t, disk, []types.PageID{1}, []types.PageID{1})
		if len(bp.Resident()) != 0 || bp.FreeFrames() != 1 {
			t.Fatalf("resident=%v free=%d, want none resident, 1 free", bp.Resident(), bp.FreeFrames())
		}
	})
}

// 8 goroutines hammer a 4-frame pool over 16 pages. Run with -race.
func TestP3_13_FetchPage_Concurrent(t *testing.T) {
	const pages, workers, iters = 16, 8, 300
	bp, disk, _ := newPool(4, pages)
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(w), 42))
			for i := 0; i < iters; i++ {
				id := types.PageID(rng.IntN(pages))
				f, err := bp.FetchPage(id)
				if errors.Is(err, ErrNoFreeFrames) {
					continue // all 4 frames pinned by other workers right now
				}
				if err != nil {
					errs <- err.Error()
					return
				}
				if got := PageStamp(f.Data); got != id {
					errs <- "pinned frame holds the wrong page"
					return
				}
				if err := bp.UnpinPage(id, i%3 == 0); err != nil {
					errs <- err.Error()
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	for _, id := range bp.Resident() {
		if pins, _ := bp.PinCount(id); pins != 0 {
			t.Fatalf("page %d pin count = %d, want 0", id, pins)
		}
	}
	for id := types.PageID(0); id < pages; id++ {
		if got := PageStamp(disk.Page(id)); got != id {
			t.Fatalf("disk page %d stamp = %d: a write-back went to the wrong page", id, got)
		}
	}
	if disk.Reads == 0 || len(bp.Resident()) != 4 {
		t.Fatalf("reads=%d resident=%v, want some reads and 4 resident pages", disk.Reads, bp.Resident())
	}
}

// ---------------------------------------------------------------- 3.14 NewPage

func TestP3_14_NewPage(t *testing.T) {
	bp, disk, r := newPool(2, 3)
	f, err := bp.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	if f.PageID != 3 || f.PinCount != 1 || f.Dirty || f.ID != 0 {
		t.Fatalf("new page: id=%d pins=%d dirty=%v frame=%d, want 3 1 false 0", f.PageID, f.PinCount, f.Dirty, f.ID)
	}
	if !bytes.Equal(f.Data, make([]byte, PageSize)) {
		t.Fatal("new page data not all zero")
	}
	if disk.NumPages() != 4 || disk.Allocs != 1 {
		t.Fatalf("disk pages=%d allocs=%d, want 4, 1", disk.NumPages(), disk.Allocs)
	}
	wantIO(t, disk, nil, nil)
	if r.Size() != 0 {
		t.Fatal("new page must be pinned (not evictable)")
	}
	// It is a normal resident page now.
	if g := mustFetch(t, bp, 3); g != f || g.PinCount != 2 {
		t.Fatalf("FetchPage(3) after NewPage: same=%v pins=%d, want same frame, 2 pins", g == f, g.PinCount)
	}
	wantIO(t, disk, nil, nil)
}

func TestP3_14_NewPage_ReusesFrameAndZeroesIt(t *testing.T) {
	bp, disk, _ := newPool(1, 3)
	old := mustFetch(t, bp, 0)
	copy(old.Data[8:], "old bytes")
	mustUnpin(t, bp, 0, true)

	f, err := bp.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	if f != old || f.PageID != 3 {
		t.Fatalf("got frame %d page %d, want frame 0 reused for page 3", f.ID, f.PageID)
	}
	if !bytes.Equal(f.Data, make([]byte, PageSize)) {
		t.Fatalf("reused frame not zeroed: starts %q", f.Data[:17])
	}
	wantIO(t, disk, []types.PageID{0}, []types.PageID{0})
	if got := disk.Page(0); string(got[8:17]) != "old bytes" {
		t.Fatalf("dirty victim not written back: %q", got[8:17])
	}
	if got := bp.Resident(); !slices.Equal(got, []types.PageID{3}) {
		t.Fatalf("resident = %v, want [3]", got)
	}
}

func TestP3_14_NewPage_Errors(t *testing.T) {
	t.Run("no frame: nothing allocated on disk", func(t *testing.T) {
		bp, disk, _ := newPool(1, 3)
		if _, err := bp.NewPage(); err != nil {
			t.Fatal(err)
		}
		if _, err := bp.NewPage(); !errors.Is(err, ErrNoFreeFrames) {
			t.Fatalf("err = %v, want ErrNoFreeFrames", err)
		}
		if disk.NumPages() != 4 || disk.Allocs != 1 {
			t.Fatalf("disk pages=%d allocs=%d, want 4, 1 (no leaked page)", disk.NumPages(), disk.Allocs)
		}
	})
	t.Run("allocation fails: frame goes back to the free list", func(t *testing.T) {
		bp, disk, _ := newPool(2, 3)
		disk.FailAlloc = errBoom
		if _, err := bp.NewPage(); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want errBoom", err)
		}
		if bp.FreeFrames() != 2 || len(bp.Resident()) != 0 {
			t.Fatalf("free=%d resident=%v, want 2 free, none resident", bp.FreeFrames(), bp.Resident())
		}
		if !slices.Equal(bp.freeList, []FrameID{1, 0}) {
			t.Fatalf("free list = %v, want [1 0]", bp.freeList)
		}
	})
}

// ---------------------------------------------------------------- 3.15 FlushPage

func TestP3_15_FlushPage(t *testing.T) {
	bp, disk, r := newPool(3, 10)
	f := install(bp, 0, 4, 0, true) // dirty, unpinned
	copy(f.Data[8:], "v2")
	install(bp, 1, 5, 1, false) // clean, pinned

	steps := []struct {
		name       string
		id         types.PageID
		wantErr    error
		wantWrites []types.PageID
	}{
		{"dirty page is written", 4, nil, []types.PageID{4}},
		{"second flush: already clean", 4, nil, []types.PageID{4}},
		{"clean pinned page: no I/O", 5, nil, []types.PageID{4}},
		{"not resident", 42, ErrPageNotResident, []types.PageID{4}},
	}
	for _, s := range steps {
		if err := bp.FlushPage(s.id); !errors.Is(err, s.wantErr) {
			t.Fatalf("%s: err = %v, want %v", s.name, err, s.wantErr)
		}
		if !slices.Equal(disk.WriteLog, s.wantWrites) {
			t.Fatalf("%s: writes = %v, want %v", s.name, disk.WriteLog, s.wantWrites)
		}
	}
	if bp.IsDirty(4) || string(disk.Page(4)[8:10]) != "v2" {
		t.Fatal("page 4 should be clean and on disk")
	}
	if pins, _ := bp.PinCount(5); pins != 1 || r.Size() != 1 {
		t.Fatalf("pins(5)=%d evictable=%d, want 1, 1 (flush doesn't touch pins)", pins, r.Size())
	}
	if disk.Reads != 0 {
		t.Fatalf("reads = %d, want 0", disk.Reads)
	}
}

func TestP3_15_FlushPage_PinnedAndErrors(t *testing.T) {
	bp, disk, _ := newPool(2, 10)
	f := install(bp, 0, 6, 2, true)
	disk.FailWrite[6] = errBoom
	if err := bp.FlushPage(6); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want errBoom", err)
	}
	if !f.Dirty {
		t.Fatal("failed flush must keep the page dirty")
	}
	delete(disk.FailWrite, 6)
	if err := bp.FlushPage(6); err != nil {
		t.Fatal(err)
	}
	if f.Dirty || f.PinCount != 2 {
		t.Fatalf("dirty=%v pins=%d, want false, 2", f.Dirty, f.PinCount)
	}
	wantIO(t, disk, nil, []types.PageID{6})
}

// ---------------------------------------------------------------- 3.16 FlushAll

func TestP3_16_FlushAll(t *testing.T) {
	setup := func() (*BufferPool, *MemDisk) {
		bp, disk, _ := newPool(5, 10)
		install(bp, 0, 9, 0, true)
		install(bp, 1, 3, 1, false)
		install(bp, 2, 5, 2, true)
		install(bp, 3, 2, 0, true)
		return bp, disk
	}
	t.Run("increasing page order, dirty only", func(t *testing.T) {
		bp, disk := setup()
		if err := bp.FlushAll(); err != nil {
			t.Fatal(err)
		}
		wantIO(t, disk, nil, []types.PageID{2, 5, 9})
		for _, id := range []types.PageID{2, 3, 5, 9} {
			if bp.IsDirty(id) {
				t.Fatalf("page %d still dirty", id)
			}
		}
		if pins, _ := bp.PinCount(5); pins != 2 {
			t.Fatalf("pins(5) = %d, want 2", pins)
		}
		if err := bp.FlushAll(); err != nil {
			t.Fatal(err)
		}
		wantIO(t, disk, nil, []types.PageID{2, 5, 9})
	})
	t.Run("stops at first error", func(t *testing.T) {
		bp, disk := setup()
		disk.FailWrite[5] = errBoom
		if err := bp.FlushAll(); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want errBoom", err)
		}
		wantIO(t, disk, nil, []types.PageID{2})
		if bp.IsDirty(2) || !bp.IsDirty(5) || !bp.IsDirty(9) {
			t.Fatalf("dirty 2/5/9 = %v/%v/%v, want false/true/true", bp.IsDirty(2), bp.IsDirty(5), bp.IsDirty(9))
		}
	})
	t.Run("empty pool", func(t *testing.T) {
		bp, disk, _ := newPool(3, 10)
		if err := bp.FlushAll(); err != nil {
			t.Fatal(err)
		}
		wantIO(t, disk, nil, nil)
	})
}
