package heap

import (
	"slices"

	"github.com/natthphong/daTarbase/pkg/types"
)

// SeqScan is a sequential scan over a PageStore (Postgres: HeapScanDesc used
// by heap_getnextslot). It walks pages 0..nblocks-1 and, on each page, slots
// 0..NumSlots()-1.
type SeqScan struct {
	store   *PageStore   // relation being scanned
	nblocks int          // pages to scan: NumPages() when the scan was created (Postgres: rs_nblocks)
	page    types.PageID // page currently being scanned (valid while cur != nil)
	cur     *MemPage     // current page, nil before the first page is read and between pages
	slot    int          // next slot of cur to examine
	next    int          // number of the next page to read (0..nblocks)
}

// NewSeqScan starts a scan of the pages that exist right now. Pages added
// later by Extend are not part of this scan.
func NewSeqScan(s *PageStore) *SeqScan { return &SeqScan{store: s, nblocks: s.NumPages()} }

// Problem 4.9 — SeqScan.Next [Medium]
//
// Return the next live tuple of a sequential scan: page by page, slot by slot, skipping dead slots.
//
//	page 0: [ (0,0) "a" | (0,1) dead | (0,2) "c" ]
//	page 1: [ ]                                      ← empty page, still read
//	page 2: [ (2,0) "d" ]
//
//	Next → (0,0) "a", (0,2) "c", (2,0) "d", then ok=false forever
//
// Rules:
//  1. Visit pages 0, 1, …, nblocks-1 in order (nblocks was fixed by
//     NewSeqScan). Read each page with exactly ONE ReadPage call, made when
//     the scan first needs that page — not up front. Empty pages and pages
//     whose slots are all dead are still read.
//  2. Inside a page return slots in increasing slot order, skipping slots
//     with Dead[i] == true. Return Tuple{TID: (page, slot), Data: Slots[i]}
//     and ok = true.
//  3. After the last tuple, return (Tuple{}, false, nil). Further calls
//     keep returning that and never read a page again.
//  4. If ReadPage fails, return (Tuple{}, false, err).
//
// Example (the pages above): 3 tuples, store.Reads == 3 after the scan.
//
// Constraints: O(1) extra memory; at most one ReadPage per page.
// Postgres reference: heapgettup_pagemode() in src/backend/access/heap/heapam.c
func (s *SeqScan) Next() (Tuple, bool, error) {
	for {
		if s.cur == nil {
			if s.next >= s.nblocks {
				return Tuple{}, false, nil
			}
			p, err := s.store.ReadPage(types.PageID(s.next))
			if err != nil {
				return Tuple{}, false, err
			}
			s.cur, s.page, s.slot = p, types.PageID(s.next), 0
			s.next++
		}
		for s.slot < s.cur.NumSlots() {
			i := s.slot
			s.slot++
			if !s.cur.Dead[i] {
				return Tuple{TID: types.TID{Page: s.page, Slot: types.SlotID(i)}, Data: s.cur.Slots[i]}, true, nil
			}
		}
		s.cur = nil
	}
}

// SyncLocation is the shared "where is the scan of this table right now"
// hint that synchronized scans publish and new scans start from (Postgres:
// the ss_scan_locations LRU in shared memory, see syncscan.c).
type SyncLocation struct {
	Page types.PageID // last page some scan reported reading
}

// SyncScan is a sequential scan that may start in the middle of the table
// and wraps around.
type SyncScan struct {
	store   *PageStore    // relation being scanned
	loc     *SyncLocation // shared hint to report progress to (may be nil)
	nblocks int           // pages to scan, fixed at creation
	start   types.PageID  // first page of the scan (Postgres: rs_startblock)
	read    int           // number of pages read so far (0..nblocks)
	page    types.PageID  // page currently being scanned (valid while cur != nil)
	cur     *MemPage      // current page, nil before the first page and between pages
	slot    int           // next slot of cur to examine
}

// NewSyncScan creates a scan of the pages that exist right now, starting at
// loc.Page — or at page 0 if loc is nil or loc.Page >= NumPages().
func NewSyncScan(s *PageStore, loc *SyncLocation) *SyncScan {
	sc := &SyncScan{store: s, loc: loc, nblocks: s.NumPages()}
	if loc != nil && uint64(loc.Page) < uint64(sc.nblocks) {
		sc.start = loc.Page
	}
	return sc
}

// Problem 4.10 — SyncScan [Medium]
//
// Return the next live tuple of a synchronized scan that starts at page `start`, wraps around, and publishes its position.
//
// When a second big sequential scan starts while another is running,
// Postgres makes it join the first one where it currently is, so both read
// the same pages at the same time (one physical read serves both). The late
// scan then wraps around to pick up the pages it missed:
//
//	nblocks = 4, start = 2:   page order 2, 3, 0, 1
//
// Rules:
//  1. Visit nblocks pages in the order start, start+1, …, nblocks-1, 0, 1,
//     …, start-1 — every page exactly once. Read each with exactly one
//     ReadPage, made when the scan first needs it.
//  2. Inside a page: slots in increasing order, skipping dead ones, exactly
//     like Problem 4.9. Return Tuple{TID, Data} and ok = true.
//  3. Each time you read a page, if loc != nil set loc.Page to that page's
//     id (report it BEFORE returning any tuple of it).
//  4. After all nblocks pages: (Tuple{}, false, nil), forever, with no more
//     reads. nblocks == 0 → no tuples, no reads.
//  5. If ReadPage fails, return (Tuple{}, false, err).
//
// Example (4 pages, tuples "p0", "p1", "p2", "p3" one per page, start 2):
//
//	Next → (2,0) "p2", (3,0) "p3", (0,0) "p0", (1,0) "p1", then ok=false
//	loc.Page after each read: 2, 3, 0, 1
//
// Constraints: at most one ReadPage per page.
// Postgres reference: ss_get_location() / ss_report_location() in
// src/backend/access/common/syncscan.c, used by heapgettup()
func (s *SyncScan) Next() (Tuple, bool, error) {
	for {
		if s.cur == nil {
			if s.read >= s.nblocks {
				return Tuple{}, false, nil
			}
			id := types.PageID((int(s.start) + s.read) % s.nblocks)
			p, err := s.store.ReadPage(id)
			if err != nil {
				return Tuple{}, false, err
			}
			s.read++
			s.cur, s.page, s.slot = p, id, 0
			if s.loc != nil {
				s.loc.Page = id
			}
		}
		for s.slot < s.cur.NumSlots() {
			i := s.slot
			s.slot++
			if !s.cur.Dead[i] {
				return Tuple{TID: types.TID{Page: s.page, Slot: types.SlotID(i)}, Data: s.cur.Slots[i]}, true, nil
			}
		}
		s.cur = nil
	}
}

// Problem 4.11 — TIDRangeScan [Easy]
//
// Return every live tuple whose TID lies in the inclusive range [lo, hi], in TID order, reading only the pages that can contain them.
//
// This is what `WHERE ctid >= '(1,2)' AND ctid <= '(3,0)'` does: since a TID
// is a physical address, the range maps directly to a run of pages.
//
// Rules:
//  1. A tuple at TID x is returned iff lo <= x <= hi in TID order (page,
//     then slot — see types.TID.Less) and it is live (not Dead).
//  2. If hi < lo, return an empty result without reading any page.
//  3. Read pages lo.Page … min(hi.Page, NumPages()-1) in order, each with
//     exactly one ReadPage. Never read a page outside that run; pages past
//     the end of the file simply do not exist (hi.Page may be
//     types.InvalidPageID — beware of overflow when looping).
//  4. Return Tuple{TID, Data} values; the result has length 0 when nothing
//     matches.
//
// Example (page 0: 3 tuples, page 1: 3 tuples with (1,1) dead, page 2: 2 tuples):
//
//	[(0,2), (2,0)] → (0,2), (1,0), (1,2), (2,0)        3 reads
//	[(1,5), (1,9)] → nothing                           1 read
//	[(2,1), (9,0)] → (2,1)                             1 read
//	[(2,0), (1,0)] → nothing                           0 reads
//
// Constraints: reads = number of pages in the clamped run.
// Postgres reference: heap_set_tidrange() / heap_getnextslot_tidrange() in
// src/backend/access/heap/heapam.c (TID Range Scan, PG 14+)
func TIDRangeScan(store *PageStore, lo, hi types.TID) []Tuple {
	out := []Tuple{}
	if hi.Less(lo) {
		return out
	}
	last := uint64(hi.Page)
	if n := uint64(store.NumPages()); n == 0 {
		return out
	} else if last > n-1 {
		last = n - 1
	}
	for pg := uint64(lo.Page); pg <= last; pg++ {
		p, err := store.ReadPage(types.PageID(pg))
		if err != nil {
			break
		}
		for i := 0; i < p.NumSlots(); i++ {
			tid := types.TID{Page: types.PageID(pg), Slot: types.SlotID(i)}
			if tid.Less(lo) || hi.Less(tid) || p.Dead[i] {
				continue
			}
			out = append(out, Tuple{TID: tid, Data: p.Slots[i]})
		}
	}
	return out
}

// Problem 4.12 — BitmapHeapScan [Medium]
//
// Fetch the tuples an index found — given as an unsorted TID list plus some lossy pages — visiting each heap page at most once, in page order.
//
// An index scan returns TIDs in index-key order, which jumps randomly around
// the heap: the same page may be read many times. A bitmap scan first
// collects all matching TIDs into a bitmap (sorted by page), then visits the
// heap in physical order. When the bitmap would grow too big, Postgres makes
// some pages "lossy": it only remembers the page, and every tuple on it
// must be returned (and the WHERE clause rechecked).
//
//	tids:  (3,1) (0,2) (3,1) (0,0) (7,0)    lossy: [2]
//	bitmap:  page 0 → {0,2}   page 2 → ALL   page 3 → {1}   page 7 → {0}
//
// Rules:
//  1. Duplicate TIDs count once. Order of `tids` and `lossy` is irrelevant;
//     `lossy` may contain duplicates too.
//  2. For a lossy page return every live tuple on it. If a page is lossy AND
//     has exact TIDs, the lossy entry wins (all live tuples, each once).
//  3. For an exact page return the listed slots that exist (slot <
//     NumSlots()) and are live. Missing slots and dead tuples are silently
//     skipped (the index may be stale) — no error.
//  4. Pages >= NumPages() are skipped WITHOUT calling ReadPage.
//  5. Read every remaining page exactly once, in increasing page order; the
//     output is in TID order. Empty input → empty result (length 0), 0 reads.
//
// Example (pages 0..3 exist; page 2 has 3 live tuples; (3,1) is live):
//
//	above → (0,0) (0,2) (2,0) (2,1) (2,2) (3,1)    3 reads (pages 0, 2, 3); page 7 ignored
//
// Constraints: O(k log k) for k input TIDs; reads = distinct existing pages.
// Postgres reference: tbm_add_tuples() / tbm_iterate() in
// src/backend/nodes/tidbitmap.c and heapam_scan_bitmap_next_block() in
// src/backend/access/heap/heapam_handler.c
func BitmapHeapScan(store *PageStore, tids []types.TID, lossy []types.PageID) []Tuple {
	type entry struct {
		lossy bool
		slots map[types.SlotID]bool
	}
	bm := map[types.PageID]*entry{}
	get := func(p types.PageID) *entry {
		e := bm[p]
		if e == nil {
			e = &entry{slots: map[types.SlotID]bool{}}
			bm[p] = e
		}
		return e
	}
	for _, t := range tids {
		get(t.Page).slots[t.Slot] = true
	}
	for _, p := range lossy {
		get(p).lossy = true
	}
	pages := make([]types.PageID, 0, len(bm))
	for p := range bm {
		if uint64(p) < uint64(store.NumPages()) {
			pages = append(pages, p)
		}
	}
	slices.Sort(pages)
	out := []Tuple{}
	for _, pg := range pages {
		p, err := store.ReadPage(pg)
		if err != nil {
			continue
		}
		e := bm[pg]
		for i := 0; i < p.NumSlots(); i++ {
			if p.Dead[i] || (!e.lossy && !e.slots[types.SlotID(i)]) {
				continue
			}
			out = append(out, Tuple{TID: types.TID{Page: pg, Slot: types.SlotID(i)}, Data: p.Slots[i]})
		}
	}
	return out
}
