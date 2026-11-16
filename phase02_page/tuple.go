package page

import "iter"

// Problem 2.4 — GetTuple [Easy]
//
// Return the bytes of the live tuple stored in `slot`.
//
// A TID (page, slot) never points at tuple bytes directly — it points at a line
// pointer, which points at the bytes. That indirection is what lets the page
// move tuples around (Problem 2.9) without changing their slot numbers.
//
// Rules:
//  1. slot ≥ NumSlots() → nil, ErrInvalidSlot.
//  2. The line pointer must be LPNormal; LPUnused, LPDead (deleted) and
//     LPRedirect → nil, ErrInvalidSlot.
//  3. Return the page bytes [off, off+len). The slice aliases the page (zero
//     copy, like PageGetItem returns a pointer into the shared buffer) and must
//     have len == cap == lp_len, so a caller's append can never scribble over
//     the neighbouring tuple. Hint: full slice expression a[lo:hi:max].
//
// Example (page holding "hello" in slot 0 and "db" in slot 1):
//
//	p.GetTuple(0) // → "hello", nil
//	p.GetTuple(1) // → "db", nil
//	p.GetTuple(2) // → nil, ErrInvalidSlot
//
// Constraints: O(1), no allocation.
// Postgres reference: PageGetItemId + PageGetItem in src/include/storage/bufpage.h
func (p *Page) GetTuple(slot SlotID) ([]byte, error) {
	panic("TODO: Problem 2.4")
}

// Problem 2.5 — InsertTuple [Medium]
//
// Insert `data` into the page and return its slot, recycling an UNUSED slot before growing the array.
//
// Tuple bytes are carved from the top of the free hole (upper moves down);
// the line pointer either reuses a free slot or is appended (lower moves up):
//
//	before:  | hdr | lp0 | lp1 |        free        | t1 | t0 |
//	                         lower                 upper
//	after:   | hdr | lp0 | lp1 | lp2 |    free     | t2 | t1 | t0 |
//	                               lower         upper
//
// Rules:
//  1. len(data) == 0 → ErrEmptyTuple.
//  2. Pick the slot: the LOWEST-numbered LPUnused slot in [0, NumSlots()) if
//     there is one (lower does not change); otherwise a new slot NumSlots()
//     appended at [lower, lower+4) (lower += 4). LPDead slots are never reused.
//  3. If len(data) > FreeSpace() (Problem 2.3) → ErrPageFull. On any error the
//     page is completely unchanged.
//  4. Copy data to [upper-len(data), upper), set upper = upper-len(data), and
//     write the slot's line pointer as {Off: new upper, Flags: LPNormal, Len: len(data)}.
//
// Example:
//
//	p := NewPage()                  // lower=8,  upper=8192
//	p.InsertTuple([]byte("hello"))  // → slot 0, lower=12, upper=8187, lp0 bytes FB 1F 05 40
//	p.InsertTuple([]byte("db"))     // → slot 1, lower=16, upper=8185, lp1 bytes F9 1F 02 40
//	// bytes [8185,8192) = "dbhello"
//
//	// slots NORMAL("aaaa"), UNUSED, NORMAL("bb"); lower=20, upper=8186
//	p.InsertTuple([]byte("zzz"))    // → slot 1 (reused), lower=20, upper=8183
//	p.InsertTuple([]byte("q"))      // → slot 3 (appended), lower=24, upper=8182
//
//	NewPage().InsertTuple(make([]byte, 8180)) // exact fit: slot 0, lower=upper=12
//	NewPage().InsertTuple(make([]byte, 8181)) // ErrPageFull
//
// Constraints: O(len(data) + NumSlots()), no new page buffer allocation.
// Postgres reference: PageAddItemExtended() in src/backend/storage/page/bufpage.c
func (p *Page) InsertTuple(data []byte) (SlotID, error) {
	panic("TODO: Problem 2.5")
}

// Problem 2.6 — Tuples [Easy]
//
// Return an iterator over every live (LPNormal) tuple as (slot, bytes), in ascending slot order.
//
// This is the inner loop of a sequential scan over one page (heapgettup_pagemode
// walks the line pointers 1..PageGetMaxOffsetNumber and skips non-normal ones).
// Go 1.23+ range-over-func lets callers write:
//
//	for slot, data := range p.Tuples() { ... }
//
// Rules:
//  1. Visit slots 0, 1, …, NumSlots()-1; yield only LPNormal ones. UNUSED, DEAD
//     and REDIRECT slots are skipped silently.
//  2. The bytes yielded for a slot are exactly what GetTuple (Problem 2.4) returns.
//  3. If yield returns false, stop immediately (the caller did `break`).
//  4. An empty page yields nothing.
//
// Example (slots: 0 "hello" NORMAL, 1 "db" DEAD, 2 "xyz" NORMAL):
//
//	for s, d := range p.Tuples() → (0,"hello"), (2,"xyz")
//
// Constraints: O(NumSlots()) per full iteration, no allocation besides the closure.
// Postgres reference: heapgettup_pagemode() / page_collect_tuples() in src/backend/access/heap/heapam.c
func (p *Page) Tuples() iter.Seq2[SlotID, []byte] {
	panic("TODO: Problem 2.6")
}

// Problem 2.7 — DeleteTuple [Easy]
//
// Mark the tuple in `slot` as deleted by flipping its line pointer to LP_DEAD.
//
// Deleting does not move or free any bytes. The slot keeps its number (index
// entries and other TIDs may still point at it) and its off/len, so space is
// only reclaimed later by Compact (Problem 2.9), the way VACUUM does it.
//
//	before: lp1 = {off 8185, NORMAL, len 2}   bytes F9 1F 02 40
//	after:  lp1 = {off 8185, DEAD,   len 2}   bytes F9 1F 02 C0
//
// Rules:
//  1. slot ≥ NumSlots() or the slot is not LPNormal (already DEAD, UNUSED,
//     REDIRECT) → ErrInvalidSlot, page unchanged.
//  2. Only the 2 flag bits of that line pointer change: off, len, the tuple
//     bytes, lower, upper and every other slot stay exactly as they were.
//
// Example:
//
//	p.InsertTuple("hello"); p.InsertTuple("db")
//	p.DeleteTuple(1)  // nil; GetTuple(1) now → ErrInvalidSlot, FreeSpace unchanged
//	p.DeleteTuple(1)  // ErrInvalidSlot (already dead)
//
// Constraints: O(1).
// Postgres reference: ItemIdMarkDead() in src/include/storage/itemid.h (used by heap_page_prune)
func (p *Page) DeleteTuple(slot SlotID) error {
	panic("TODO: Problem 2.7")
}

// Problem 2.8 — UpdateTuple [Medium]
//
// Overwrite the tuple in `slot` in place when the new version is not longer than the old one.
//
// A Postgres UPDATE normally writes a brand-new tuple version (MVCC, later
// phase); this is the in-place path (heap_inplace_update, used for catalog
// rows like pg_class.relpages). Shrinking leaves a hole of garbage bytes that
// only Compact (Problem 2.9) reclaims:
//
//	old: off=8180 len=8  "bbbbbbbb"
//	new: off=8180 len=2  "BBbbbbbb"   ← bytes [8182,8188) are now a dead hole
//
// Rules (checked in this order):
//  1. slot ≥ NumSlots() or the slot is not LPNormal → ErrInvalidSlot.
//  2. len(data) == 0 → ErrEmptyTuple.
//  3. len(data) > current lp_len → ErrNoRoom (it would overwrite a neighbour).
//  4. On any error the page is unchanged.
//  5. On success copy data to [off, off+len(data)) — the offset does NOT
//     change — and set lp_len = len(data). The leftover old bytes
//     [off+len(data), off+old_len) are left as they were. lower, upper, flags
//     and every other slot are untouched.
//
// Example (slot 1 holds "bbbbbbbb" at offset 8180):
//
//	p.UpdateTuple(1, []byte("BB"))         // nil; lp1 = {8180, NORMAL, 2}; GetTuple(1) = "BB"
//	p.UpdateTuple(1, []byte("ccc"))        // ErrNoRoom (3 > 2)
//
// Constraints: O(len(data)).
// Postgres reference: heap_inplace_update_and_unlock() in src/backend/access/heap/heapam.c
func (p *Page) UpdateTuple(slot SlotID, data []byte) error {
	panic("TODO: Problem 2.8")
}
