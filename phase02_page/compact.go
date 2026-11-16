package page

// Problem 2.9 — Compact [Hard]
//
// Defragment the page: drop dead tuples, pack live tuples at the end, keep every slot number.
//
// After deletes (Problem 2.7) and shrinking updates (Problem 2.8) the tuple area
// is full of holes. Compact rebuilds it so all free space is one contiguous
// hole again. Line pointers are the indirection that makes this legal: a
// tuple's bytes move, its slot number (and therefore its TID) never changes.
//
//	before: | hdr | lp0 N | lp1 D | lp2 N | lp3 D |  free  |ddd|cc|bbbbbbbb|aaaa|
//	                                              lower    upper
//	after:  | hdr | lp0 N | lp1 U | lp2 N |      free      |cc|aaaa|
//	                                      lower            upper
//	(lp3 was the last slot and is no longer live, so the array is cut after lp2)
//
// Rules:
//  1. Every LPDead line pointer becomes LPUnused (all 4 bytes zero). LPUnused
//     stays LPUnused.
//  2. LPNormal tuples are packed against the end of the page in ASCENDING SLOT
//     ORDER: the lowest-numbered live slot ends at PageSize, the next live slot
//     ends where the previous one starts, and so on. Each one's Off is updated;
//     its Len, flags and slot number do not change. upper = PageSize - Σ len.
//  3. Trailing LPUnused line pointers are cut off (Postgres 14+ does this):
//     lower = HeaderSize + 4*(highest LPNormal slot + 1), or HeaderSize if no
//     live slot remains. Unused slots BELOW the highest live slot stay.
//  4. Every byte in [lower, upper) is zero afterwards (old line pointers and
//     old tuple bytes in the hole are wiped). Header bytes 4..8 (flags and
//     stored checksum) are not touched.
//  5. Return how many bytes of hole were gained:
//     (new upper - new lower) - (old upper - old lower).
//  6. Tuples may currently sit in any physical order (e.g. a reused slot's
//     tuple is below newer ones) — copy carefully so a move never overwrites a
//     tuple you have not moved yet. Hint: Postgres copies into a scratch buffer.
//
// Example (inserts "aaaa", "bbbbbbbb", "cc", "ddd" → slots 0..3, lower=24, upper=8175):
//
//	p.DeleteTuple(1); p.DeleteTuple(3)
//	p.Compact()   // → 15 (8 + 3 tuple bytes + one 4-byte line pointer)
//	// slot 0 {8188, NORMAL, 4}, slot 1 UNUSED, slot 2 {8186, NORMAL, 2}
//	// lower=20 (slot 3 truncated), upper=8186, FreeSpace()=8166
//
// Constraints: O(PageSize). Must not change any LPNormal tuple's slot number or bytes.
// Postgres reference: PageRepairFragmentation() + compactify_tuples() in src/backend/storage/page/bufpage.c,
// PageTruncateLinePointerArray() (same file), lazy_vacuum_heap_page() in vacuumlazy.c
func (p *Page) Compact() int {
	panic("TODO: Problem 2.9")
}

// Problem 2.10 — InsertWithFillFactor [Medium]
//
// Insert like InsertTuple, but refuse if the page would end up more than fillfactor% full.
//
// CREATE TABLE ... WITH (fillfactor = 90) tells Postgres to stop INSERTing
// into a page once it is ~90% full. The reserved 10% is left for future
// UPDATEs of the rows already on the page: a new row version that fits on the
// same page can be a HOT update (no index changes, cheap pruning).
//
//	reserve = PageSize * (100 - ff) / 100      (integer division)
//	ff=100 → 0     ff=90 → 819     ff=50 → 4096     ff=10 → 7372
//
// Rules (checked in this order):
//  1. ff < 10 or ff > 100 → ErrBadFillFactor.
//  2. len(data) == 0 → ErrEmptyTuple.
//  3. If the page has no line pointers at all (NumSlots() == 0) the fillfactor
//     is ignored: behave exactly like InsertTuple. (Postgres always accepts a
//     tuple on an empty page, otherwise a tuple bigger than the budget could
//     never be stored anywhere.)
//  4. Otherwise the insert is allowed only if the hole left AFTER it is still
//     ≥ reserve, i.e. FreeSpace() - len(data) ≥ reserve (FreeSpace from Problem
//     2.3 already accounts for a new line pointer). If not → ErrPageFull.
//  5. If allowed, insert with InsertTuple (Problem 2.5) and return its result.
//     On any error the page is unchanged.
//
// Example (100-byte tuples, each costing 104 bytes with its line pointer):
//
//	ff=100 → 78 tuples fit (same as InsertTuple)
//	ff=90  → 70 tuples fit: with 69 tuples FreeSpace=1004, 1004-100=904 ≥ 819 ok;
//	         with 70 tuples FreeSpace=900,  900-100=800 < 819 → ErrPageFull
//	ff=10  → 7 tuples fit
//	ff=10, empty page, 8000-byte tuple → accepted (rule 3)
//
// Constraints: O(len(data) + NumSlots()).
// Postgres reference: RelationGetBufferForTuple() in src/backend/access/heap/hio.c,
// RelationGetTargetPageFreeSpace in src/include/utils/rel.h
func (p *Page) InsertWithFillFactor(data []byte, ff int) (SlotID, error) {
	panic("TODO: Problem 2.10")
}
