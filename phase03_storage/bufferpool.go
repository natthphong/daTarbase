package storage

import (
	"slices"
	"sync"

	"github.com/natthphong/daTarbase/pkg/types"
)

// Frame is one slot of the buffer pool holding a copy of one disk page
// (Postgres: a BufferDesc + its 8 KB block in shared_buffers).
//
// Callers may read/modify Data while they hold a pin on the frame. All other
// fields belong to the BufferPool and are only touched while holding its mutex.
type Frame struct {
	ID       FrameID      // index of this frame in the pool
	PageID   types.PageID // page held, InvalidPageID when the frame is free
	Data     []byte       // PageSize bytes of page content
	PinCount int          // number of users currently holding the page
	Dirty    bool         // Data differs from the copy on disk
}

// BufferPool caches disk pages in a fixed number of frames.
//
//	pageTable: PageID -> FrameID    (Postgres: the buffer mapping hash table)
//	freeList:  frames holding no page, used before evicting anything
//	replacer:  picks an unpinned victim when the free list is empty
//
// A resident page's frame is tracked by the replacer: evictable iff its
// PinCount is 0. Frames on the free list are NOT tracked by the replacer.
// All methods are safe for concurrent use: each one holds mu for its whole
// duration (simple, but it serialises disk I/O — Postgres uses per-buffer
// locks and partitioned mapping locks instead).
type BufferPool struct {
	mu        sync.Mutex               // protects everything below and every Frame's metadata
	disk      DiskManager              // where pages come from / go to
	frames    []*Frame                 // all frames, frames[i].ID == i
	pageTable map[types.PageID]FrameID // resident pages
	freeList  []FrameID                // free frames; take from the FRONT
	replacer  Replacer                 // eviction policy for resident frames
}

// NewBufferPool creates a pool of n empty frames. The free list starts as
// [0, 1, ..., n-1], so free frames are handed out in increasing id order.
func NewBufferPool(n int, disk DiskManager, replacer Replacer) *BufferPool {
	bp := &BufferPool{
		disk:      disk,
		pageTable: make(map[types.PageID]FrameID, n),
		replacer:  replacer,
	}
	for i := 0; i < n; i++ {
		bp.frames = append(bp.frames, &Frame{ID: FrameID(i), PageID: types.InvalidPageID, Data: make([]byte, PageSize)})
		bp.freeList = append(bp.freeList, FrameID(i))
	}
	return bp
}

// PinCount returns the pin count of a resident page (ok=false if not resident).
func (bp *BufferPool) PinCount(id types.PageID) (pins int, ok bool) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	fid, ok := bp.pageTable[id]
	if !ok {
		return 0, false
	}
	return bp.frames[fid].PinCount, true
}

// IsDirty reports whether a resident page is dirty (false if not resident).
func (bp *BufferPool) IsDirty(id types.PageID) bool {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	fid, ok := bp.pageTable[id]
	return ok && bp.frames[fid].Dirty
}

// Resident lists the resident page ids in increasing order.
func (bp *BufferPool) Resident() []types.PageID {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	var out []types.PageID
	for id := range bp.pageTable {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// FreeFrames is the length of the free list.
func (bp *BufferPool) FreeFrames() int {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	return len(bp.freeList)
}

// Problem 3.11 — UnpinPage [Easy]
//
// Release one pin on a resident page, remembering whether the caller modified it.
//
// Rules:
//  1. Lock bp.mu for the whole call.
//  2. Page not resident -> ErrPageNotResident.
//  3. PinCount already 0 -> ErrNotPinned; nothing changes.
//  4. If dirty is true, mark the frame Dirty. dirty=false NEVER clears the
//     flag: another user may have modified the page (the flag is sticky
//     until the page is written to disk).
//  5. Decrement PinCount. When it reaches 0, tell the replacer the frame is
//     evictable (SetEvictable(frame, true)).
//  6. No disk I/O.
//
// Example:
//
//	// page 7 resident with PinCount 2
//	bp.UnpinPage(7, true)   // nil; PinCount 1, Dirty, still not evictable
//	bp.UnpinPage(7, false)  // nil; PinCount 0, still Dirty, now evictable
//	bp.UnpinPage(7, false)  // ErrNotPinned
//	bp.UnpinPage(99, false) // ErrPageNotResident
//
// Postgres reference: UnpinBuffer() + MarkBufferDirty() in src/backend/storage/buffer/bufmgr.c
func (bp *BufferPool) UnpinPage(id types.PageID, dirty bool) error {
	panic("TODO: Problem 3.11")
}

// Problem 3.12 — acquireFrame [Medium]
//
// Find a frame to load a new page into: a free frame if any, else evict a victim (writing it back if dirty).
//
// The caller already holds bp.mu (do NOT lock it here). The returned frame
// holds no page, is not in the page table, is not on the free list and is
// not tracked by the replacer; the caller fills it and registers it.
//
// Rules:
//  1. Free list not empty: remove and return its FIRST element. No I/O.
//  2. Otherwise ask the replacer for a victim. None -> ErrNoFreeFrames
//     and nothing changes.
//  3. Victim dirty: write its Data to disk under its PageID — exactly one
//     WritePage. If that write fails, return the error and put the victim
//     back as it was: still resident, still dirty, and tracked again by the
//     replacer as evictable (RecordAccess(victim) then SetEvictable(victim, true)).
//  4. Clean victim: no I/O at all.
//  5. Remove the victim's old page from the page table and reset the frame:
//     PageID = InvalidPageID, Dirty = false, PinCount = 0. Data may keep
//     its old bytes (the caller overwrites it).
//
// Example (2 frames, LRU replacer):
//
//	fresh pool:  acquireFrame() -> 0, then -> 1     (free list, no I/O)
//	frames 0,1 hold pages 3,7 unpinned, 3 used least recently, 3 dirty
//	acquireFrame() -> 0; disk WriteLog == [3]; page 3 no longer resident
//
// Postgres reference: BufferAlloc() + StrategyGetBuffer() + FlushBuffer() in src/backend/storage/buffer/
func (bp *BufferPool) acquireFrame() (FrameID, error) {
	panic("TODO: Problem 3.12")
}

// Problem 3.13 — FetchPage [Hard]
//
// Return the pinned frame holding page id, reading it from disk on a cache miss; safe for concurrent callers.
//
// Rules:
//  1. Lock bp.mu for the whole call: 8 goroutines calling FetchPage and
//     UnpinPage at the same time must not race (tests run with -race).
//  2. Hit (page in the page table): PinCount++, replacer.RecordAccess(frame),
//     replacer.SetEvictable(frame, false). NO disk I/O. Return the same
//     *Frame as before.
//  3. Miss: get a frame with acquireFrame (its error, e.g. ErrNoFreeFrames,
//     is returned as is). Read the page from disk into frame.Data.
//     - Read fails: append the frame to the END of the free list, leave
//     the page non-resident, return the read error.
//     - Read succeeds: PageID = id, PinCount = 1, Dirty = false, add to the
//     page table, RecordAccess(frame), SetEvictable(frame, false).
//  4. Each miss does exactly one ReadPage (plus at most one WritePage of a
//     dirty victim, done by acquireFrame).
//
// Example (pool of 2 frames, LRU replacer, empty pool):
//
//	FetchPage(1); UnpinPage(1, false)  // miss: read page 1 into frame 0
//	FetchPage(2); UnpinPage(2, false)  // miss: read page 2 into frame 1
//	FetchPage(1); UnpinPage(1, false)  // hit: no I/O; page 1 is now most recently used
//	FetchPage(3)                       // miss: evicts page 2, reads 3 into frame 1
//	FetchPage(4)                       // miss: evicts page 1, reads 4 into frame 0
//	FetchPage(5)                       // ErrNoFreeFrames (3 and 4 pinned), no I/O
//	// disk ReadLog == [1 2 3 4], no writes
//
// Postgres reference: ReadBuffer() -> ReadBuffer_common() -> BufferAlloc() in src/backend/storage/buffer/bufmgr.c
func (bp *BufferPool) FetchPage(id types.PageID) (*Frame, error) {
	panic("TODO: Problem 3.13")
}

// Problem 3.14 — NewPage [Medium]
//
// Allocate a brand-new page on disk and return it pinned in a zeroed frame.
//
// Rules:
//  1. Lock bp.mu for the whole call.
//  2. Get a frame FIRST with acquireFrame. If that fails, return the error
//     WITHOUT allocating on disk (otherwise a full pool would leak pages).
//  3. Then disk.AllocatePage(). If it fails, append the frame to the END of
//     the free list and return the error.
//  4. Zero the whole frame.Data (the frame may hold an old page's bytes).
//     No ReadPage: the new page is known to be all zeros.
//  5. PageID = new id, PinCount = 1, Dirty = false (the disk already holds
//     the zeroed page), add to the page table, RecordAccess(frame),
//     SetEvictable(frame, false).
//
// Example (disk has pages 0..2, pool of 1 frame):
//
//	f, _ := bp.NewPage()   // f.PageID == 3, Data all zero, PinCount 1; disk NumPages 4, 0 reads
//	bp.NewPage()           // ErrNoFreeFrames (page 3 pinned); disk NumPages still 4
//
// Postgres reference: ExtendBufferedRel() in src/backend/storage/buffer/bufmgr.c
func (bp *BufferPool) NewPage() (*Frame, error) {
	panic("TODO: Problem 3.14")
}

// Problem 3.15 — FlushPage [Easy]
//
// Write a resident page to disk if it is dirty, and mark it clean.
//
// Rules:
//  1. Lock bp.mu for the whole call.
//  2. Page not resident -> ErrPageNotResident.
//  3. Dirty: exactly one WritePage(id, Data), then Dirty = false.
//     If the write fails, return the error and keep the page dirty.
//  4. Clean: no I/O, return nil.
//  5. Pinning is irrelevant: a pinned page can be flushed and stays pinned
//     with the same PinCount; the replacer is not touched.
//
// Example:
//
//	// page 4 resident and dirty
//	bp.FlushPage(4)   // nil, Writes == 1, IsDirty(4) == false
//	bp.FlushPage(4)   // nil, Writes still 1
//	bp.FlushPage(42)  // ErrPageNotResident
//
// Postgres reference: FlushOneBuffer() / FlushBuffer() in src/backend/storage/buffer/bufmgr.c
func (bp *BufferPool) FlushPage(id types.PageID) error {
	panic("TODO: Problem 3.15")
}

// Problem 3.16 — FlushAll [Easy]
//
// Write every dirty resident page to disk in increasing page-id order (what a checkpoint does).
//
// Rules:
//  1. Lock bp.mu for the whole call.
//  2. Visit resident pages in INCREASING PageID order (sequential I/O, and
//     a deterministic order the tests check through the disk's WriteLog).
//  3. Dirty page: one WritePage, then mark clean. Clean page: no I/O.
//  4. On the first write error, return it immediately: pages already written
//     stay clean, the failed page and all later ones stay dirty.
//  5. Pin counts and the replacer are not touched.
//
// Example:
//
//	// resident: 2 (dirty), 3 (clean), 5 (dirty), 9 (dirty)
//	bp.FlushAll()   // nil, WriteLog == [2 5 9]
//	// same, but writing page 5 fails with errBoom:
//	bp.FlushAll()   // errBoom, WriteLog == [2]; 5 and 9 still dirty
//
// Postgres reference: BufferSync() in src/backend/storage/buffer/bufmgr.c (it also sorts by file+block)
func (bp *BufferPool) FlushAll() error {
	panic("TODO: Problem 3.16")
}
