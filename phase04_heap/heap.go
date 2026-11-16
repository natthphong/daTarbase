package heap

import "github.com/natthphong/daTarbase/pkg/types"

// Heap is a heap file: a PageStore of pages holding tuples in no particular
// order, plus a free space map that remembers roughly how much room each page
// has so Insert does not have to read every page to find space.
type Heap struct {
	Store *PageStore // the relation's pages
	FSM   *FSM       // free space map for Store (Problems 4.1–4.2)
}

// NewHeap returns an empty heap (0 pages, empty FSM).
func NewHeap() *Heap { return &Heap{Store: NewPageStore(), FSM: NewFSM()} }

// Problem 4.5 — Heap.Insert [Medium]
//
// Store a tuple somewhere in the heap — on a page the FSM says has room, otherwise on a brand-new page — and return its TID.
//
// A tuple of n bytes needs n+ItemOverhead bytes of page space ("need").
//
// Rules:
//  1. len(data) == 0 → ErrEmptyTuple. len(data) > MaxTupleSize (8180) →
//     ErrTupleTooBig. Nothing changes on error.
//  2. Ask the FSM: id := h.FSM.Search(need).
//  3. If id is a page: ReadPage(id). The FSM is only a hint and may be
//     stale, so re-check the real page.FreeSpace():
//     - enough room → Add the tuple there;
//     - not enough → record the page's true free space in the FSM
//     (h.FSM.Update(id, page.FreeSpace())) and go back to step 2;
//     - ReadPage fails (the FSM points past the end of the file) → record
//     0 free bytes for id and go back to step 2.
//  4. If the FSM has no candidate (InvalidPageID): Store.Extend() a new
//     empty page and Add the tuple there.
//  5. After adding, record the target page's new FreeSpace() in the FSM.
//  6. Return TID{page, slot} where slot is what MemPage.Add returned.
//
// Every iteration of step 3 lowers one FSM entry below what is needed, so
// the loop always terminates.
//
// Example (fresh heap):
//
//	Insert(4000 bytes) -> (0,0)  extend: page 0 free 8184-4004 = 4180, FSM cat 130
//	Insert(4000 bytes) -> (0,1)  FSM says page 0 (cat 130 ≥ 126): free 176, cat 5
//	Insert(4000 bytes) -> (1,0)  no page has cat ≥ 126: extend page 1
//	Insert(100 bytes)  -> (0,2)  need 104 = cat 4, page 0 has cat 5: free 72, cat 2
//
// I/O: reusing an existing page costs exactly one ReadPage (plus one per
// stale FSM entry that had to be corrected); extending costs none.
//
// Constraints: never scan the pages yourself — the FSM decides.
// Postgres reference: heap_insert() → RelationGetBufferForTuple() in
// src/backend/access/heap/hio.c (RecordAndGetPageWithFreeSpace on a stale hint)
func (h *Heap) Insert(data []byte) (types.TID, error) {
	panic("TODO: Problem 4.5")
}

// Problem 4.6 — Heap.Fetch [Easy]
//
// Return the newest version of the tuple at `tid`, following forward (ctid) pointers left by updates.
//
// When a tuple is updated (Problem 4.8) the old version is marked dead and
// its Forward[slot] points to the newer version — possibly on the same page,
// possibly on another one. Versions form a chain:
//
//	(0,0) dead → (0,1) dead → (3,0) live      Fetch((0,0)) = data of (3,0)
//
// Rules:
//  1. tid.Page must exist and tid.Slot < page.NumSlots(), otherwise
//     return ErrInvalidTID. This applies to every hop of the chain.
//  2. Live slot → return (its bytes, the TID where it was found, nil).
//  3. Dead slot whose Forward is InvalidTID (it was deleted) →
//     ErrTupleDead. Dead slot with a Forward TID → continue from that TID.
//  4. On error return (nil, types.TID{}, err).
//
// I/O: one ReadPage per distinct page visited in a row — when the next
// hop is on the page you already hold, do not read it again. A chain
// (0,0)→(0,1)→(3,0) costs 2 reads.
//
// Constraints: O(chain length).
// Postgres reference: heap_fetch() and heap_get_latest_tid() in
// src/backend/access/heap/heapam.c (heap_hot_search_buffer for same-page chains)
func (h *Heap) Fetch(tid types.TID) ([]byte, types.TID, error) {
	panic("TODO: Problem 4.6")
}

// Problem 4.7 — Heap.Delete [Easy]
//
// Mark the tuple version at `tid` dead.
//
// In Postgres a DELETE never removes bytes: it stamps the tuple's xmax and
// the space stays used until VACUUM. Here "dead" is simply Dead[slot] = true.
//
// Rules:
//  1. tid.Page must exist and tid.Slot < page.NumSlots(), otherwise ErrInvalidTID.
//  2. If the slot is already dead (deleted, or superseded by an update) →
//     ErrTupleDead. Delete does NOT follow forward pointers: it targets
//     exactly the version it is given.
//  3. Otherwise set Dead[slot] = true. Do not touch Slots, Forward, Used or
//     the FSM — the bytes still occupy the page.
//
// Example:
//
//	tid, _ := h.Insert([]byte("alice"))  // (0,0)
//	h.Delete(tid)                         // nil
//	h.Delete(tid)                         // ErrTupleDead
//	h.Fetch(tid)                          // ErrTupleDead
//	h.Delete(TID{0, 7})                   // ErrInvalidTID
//
// Constraints: exactly one ReadPage.
// Postgres reference: heap_delete() in src/backend/access/heap/heapam.c
func (h *Heap) Delete(tid types.TID) error {
	panic("TODO: Problem 4.7")
}

// Problem 4.8 — Heap.Update [Medium]
//
// Write a new version of the tuple at `tid`: on the same page if it fits (HOT-like), elsewhere otherwise, and chain the old version to it.
//
// Postgres never overwrites a tuple in place (MVCC readers may still need
// the old version). It writes a new version and sets the old one's t_ctid to
// point at it. If the new version fits on the SAME page, the update is a
// "heap-only tuple" (HOT) update: no new index entries are needed because
// indexes still point at the old TID and the chain is followed within the page.
//
//	same page (HOT):      page 0: [ (0,0) dead →(0,1) | (0,1) new ]
//	no room on page 0:    page 0: [ (0,0) dead →(2,0) ]   page 2: [ (2,0) new ]
//
// Rules:
//  1. Validate data first: len(data) == 0 → ErrEmptyTuple; len(data) >
//     MaxTupleSize → ErrTupleTooBig.
//  2. Then the old version: missing page or slot → ErrInvalidTID; dead
//     slot → ErrTupleDead (only the newest version can be updated; no
//     chain following). Nothing changes on any error.
//  3. If the old version's page has FreeSpace() >= len(data)+ItemOverhead:
//     Add the new version to that page and record the page's new free space
//     in the FSM.
//  4. Otherwise insert the new version exactly like Insert (Problem 4.5)
//     does — FSM search, stale-hint correction, or extend.
//  5. Only after the new version exists: mark the old slot Dead and set its
//     Forward to the new TID. Return the new TID.
//
// Example (fresh heap; Insert "v1" → (0,0), Insert 5000 bytes → (0,1);
// page 0 now has 8184-6-5004 = 3174 bytes free):
//
//	Update((0,0), "v2") -> (0,2)   HOT: same page, free 3168; Fetch((0,0)) = "v2" at (0,2)
//	Update((0,0), "v3") -> ErrTupleDead   (0,0) is no longer the newest version
//	Update((0,2), 4000 bytes) -> (1,0)    page 0 cannot fit 4004 bytes: FSM has
//	                                      nothing big enough, extend page 1
//	Fetch((0,0)) = the 4000 bytes, found at (1,0)   (chain (0,0)→(0,2)→(1,0))
//
// Constraints: the old page is read once by Update itself (the Insert path
// may read other pages).
// Postgres reference: heap_update() in src/backend/access/heap/heapam.c
func (h *Heap) Update(tid types.TID, data []byte) (types.TID, error) {
	panic("TODO: Problem 4.8")
}
