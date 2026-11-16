package storage

import "github.com/natthphong/daTarbase/pkg/types"

// Ring is a buffer access strategy for a big sequential scan: the scan
// recycles its own small set of frames instead of pushing the whole cache
// out (Postgres: BufferAccessStrategy with BAS_BULKREAD, a 256 KB ring).
//
//	slots:  [ f4 | f5 | -1 | -1 ]     -1 = empty slot
//	          ^ next
//
// A Ring belongs to one scan; it is used while holding the pool's mutex.
type Ring struct {
	slots []FrameID // frame remembered in each slot, -1 when empty
	next  int       // slot used by the next miss
}

// NewRing returns a ring of size empty slots (size >= 1).
func NewRing(size int) *Ring {
	if size < 1 {
		panic("storage: ring size must be >= 1")
	}
	r := &Ring{slots: make([]FrameID, size)}
	for i := range r.slots {
		r.slots[i] = -1
	}
	return r
}

// Problem 3.17 — FetchPageWithRing [Hard]
//
// FetchPage for a bulk sequential scan: on a miss, recycle the frame in the ring's next slot so the scan cannot flush the whole cache.
//
// Without a ring, scanning a table bigger than the pool evicts every hot
// page; with a ring of N slots the scan only ever cycles through N frames.
//
// Rules:
//  1. Lock bp.mu for the whole call.
//  2. Hit: exactly like FetchPage (PinCount++, RecordAccess,
//     SetEvictable false, no I/O). The ring is NOT touched (next unchanged).
//  3. Miss: let s = ring.next, then advance ring.next = (s+1) % len(slots).
//     Let c = ring.slots[s].
//     a. If c != -1 and frame c holds a page (PageID != InvalidPageID)
//     with PinCount == 0: REUSE c. If it is dirty, write it back (one
//     WritePage; on failure return the error and leave c resident and
//     dirty). Then replacer.Remove(c), delete its page from the page
//     table and reset it (PageID InvalidPageID, Dirty false).
//     b. Otherwise (empty slot, frame pinned by someone, or frame free):
//     get a frame with acquireFrame (free list first, then the replacer)
//     and store it in ring.slots[s]. Errors are returned as is.
//  4. Read the page into the frame. On failure: set ring.slots[s] = -1,
//     append the frame to the END of the free list, return the error.
//  5. On success register it exactly like FetchPage: PageID, PinCount 1,
//     Dirty false, page table, RecordAccess, SetEvictable(false).
//
// Example (pool of 6 frames, hot pages 0..3 cached in frames 0..3 and
// unpinned, ring of 2; scan pages 10..29, unpinning each page after use):
//
//	page 10 -> slot 0 empty   -> acquireFrame -> free frame 4
//	page 11 -> slot 1 empty   -> free frame 5
//	page 12 -> slot 0 frame 4 unpinned -> reuse frame 4 (page 10 leaves)
//	...
//	after the scan: 20 reads, resident = [0 1 2 3 28 29], frames 0..3 untouched
//	(with plain FetchPage the scan would leave only pages 24..29 resident)
//
// Postgres reference: GetBufferFromRing() / AddBufferToRing() in src/backend/storage/buffer/freelist.c
func (bp *BufferPool) FetchPageWithRing(id types.PageID, ring *Ring) (*Frame, error) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	if fid, ok := bp.pageTable[id]; ok {
		f := bp.frames[fid]
		f.PinCount++
		bp.replacer.RecordAccess(fid)
		bp.replacer.SetEvictable(fid, false)
		return f, nil
	}
	s := ring.next
	ring.next = (s + 1) % len(ring.slots)
	var fid FrameID
	if c := ring.slots[s]; c >= 0 && bp.frames[c].PageID != types.InvalidPageID && bp.frames[c].PinCount == 0 {
		f := bp.frames[c]
		if f.Dirty {
			if err := bp.disk.WritePage(f.PageID, f.Data); err != nil {
				return nil, err
			}
		}
		bp.replacer.Remove(c)
		delete(bp.pageTable, f.PageID)
		f.PageID, f.Dirty = types.InvalidPageID, false
		fid = c
	} else {
		var err error
		if fid, err = bp.acquireFrame(); err != nil {
			return nil, err
		}
		ring.slots[s] = fid
	}
	f := bp.frames[fid]
	if err := bp.disk.ReadPage(id, f.Data); err != nil {
		ring.slots[s] = -1
		bp.freeList = append(bp.freeList, fid)
		return nil, err
	}
	f.PageID, f.PinCount, f.Dirty = id, 1, false
	bp.pageTable[id] = fid
	bp.replacer.RecordAccess(fid)
	bp.replacer.SetEvictable(fid, false)
	return f, nil
}
