package heap

import "github.com/natthphong/daTarbase/pkg/types"

const (
	// FSMCatStep is the number of bytes one free-space category stands for
	// (Postgres: FSM_CAT_STEP = BLCKSZ/256 = 32).
	FSMCatStep = types.PageSize / 256 // 32

	// MaxCategory is the highest category; it means "8160 bytes or more free".
	MaxCategory = 255
)

// FSM is a flat free space map: one uint8 category per heap page.
//
// Postgres does not remember the exact number of free bytes per page — it
// stores one byte, a "category", so the whole map stays tiny (one FSM page
// covers ~4000 heap pages). Category c means "at least c*32 bytes free".
//
//	page:      0     1     2     3
//	cats:   [ 255 |  3  |  0  |  9 ]   <- 255: >= 8160 B, 3: 96..127 B, 9: 288..319 B
type FSM struct {
	cats []uint8 // cats[p] = category of heap page p; pages >= len(cats) have category 0
}

// NewFSM returns an empty map (every page has category 0).
func NewFSM() *FSM { return &FSM{} }

// Category returns the recorded category of page p (0 if never recorded).
func (f *FSM) Category(p types.PageID) uint8 {
	if uint64(p) >= uint64(len(f.cats)) {
		return 0
	}
	return f.cats[p]
}

// Len is the number of pages the map has an entry for.
func (f *FSM) Len() int { return len(f.cats) }

// Problem 4.1 — FSMUpdate [Easy]
//
// Record that heap page `page` now has `free` bytes available, as a 1-byte category.
//
// Rules:
//  1. category = free / FSMCatStep (integer division, i.e. rounded DOWN),
//     capped at MaxCategory (255). A negative `free` counts as 0.
//     Rounding down keeps the map conservative: a page with category c is
//     guaranteed to have at least c*32 free bytes.
//  2. If `page` is beyond the end of the map, grow cats so that
//     len(cats) == page+1; the newly created entries in between get category 0.
//     Updating a page that already has an entry never shrinks the map.
//  3. `page` is always < types.InvalidPageID.
//
// Example:
//
//	f := NewFSM()
//	f.Update(0, 8184) // cats = [255]          (8184/32 = 255)
//	f.Update(4, 100)  // cats = [255 0 0 0 3]  (100/32 = 3)
//	f.Update(0, 8159) // cats = [254 0 0 0 3]  (8159/32 = 254)
//	f.Update(4, 31)   // cats = [254 0 0 0 0]
//
// Constraints: O(1) amortized.
// Postgres reference: fsm_space_avail_to_cat() and RecordPageWithFreeSpace()
// in src/backend/storage/freespace/freespace.c
func (f *FSM) Update(page types.PageID, free int) {
	panic("TODO: Problem 4.1")
}

// Problem 4.2 — FSMSearch [Easy]
//
// Return the lowest-numbered page whose category promises at least `need` free bytes.
//
// Rules:
//  1. The category needed is ceil(need / FSMCatStep) (rounded UP). A need
//     of 0 or less is treated as need = 1, i.e. category 1.
//  2. If the needed category is greater than MaxCategory (need > 8160), no
//     page can be proven to have room: return types.InvalidPageID.
//  3. Otherwise return the smallest page p with cats[p] >= needed category,
//     or types.InvalidPageID if there is none (including an empty map).
//  4. Because categories are rounded down and needs rounded up, an exact fit
//     can be missed: a page with exactly 300 free bytes (category 9) does
//     NOT satisfy need=300 (category 10). That is intended — the answer must
//     never be a page that might be too small.
//
// Example (cats = [1 3 9], i.e. pages recorded with 50, 100 and 300 free bytes):
//
//	Search(64)   -> 1        (needs cat 2)
//	Search(96)   -> 1        (needs cat 3)
//	Search(97)   -> 2        (needs cat 4)
//	Search(288)  -> 2        (needs cat 9)
//	Search(300)  -> InvalidPageID  (needs cat 10)
//	Search(0)    -> 0        (treated as need 1 → cat 1)
//	Search(8161) -> InvalidPageID  (needs cat 256)
//
// Constraints: O(number of pages). (Problem 4.4 makes this logarithmic.)
// Postgres reference: fsm_space_needed_to_cat() and GetPageWithFreeSpace()
// in src/backend/storage/freespace/freespace.c
func (f *FSM) Search(need int) types.PageID {
	panic("TODO: Problem 4.2")
}

// FSMTree is the shape of a real Postgres FSM page: a complete binary
// "max-tree" over the categories of a fixed number of heap pages. Leaves hold
// page categories; every inner node holds the max of its two children, so
// the root tells you instantly whether ANY page has enough room.
//
// Array layout (nleaves = 4, 7 nodes):
//
//	                nodes[0]
//	           /              \
//	      nodes[1]          nodes[2]
//	      /      \          /      \
//	 nodes[3] nodes[4] nodes[5] nodes[6]
//	  page 0   page 1   page 2   page 3
//
//	children of node i: 2i+1 and 2i+2;  parent of node i: (i-1)/2
//	leaf of page p:     nodes[nleaves-1+p]
type FSMTree struct {
	nleaves int     // number of leaves (= pages tracked); a power of two
	nodes   []uint8 // 2*nleaves-1 nodes in the array layout above
}

// NewFSMTree returns an all-zero tree whose number of leaves is the smallest
// power of two >= capacity (at least 1).
func NewFSMTree(capacity int) *FSMTree {
	n := 1
	for n < capacity {
		n *= 2
	}
	return &FSMTree{nleaves: n, nodes: make([]uint8, 2*n-1)}
}

// Capacity is the number of leaves (pages) the tree can track.
func (t *FSMTree) Capacity() int { return t.nleaves }

// Nodes returns the node array (read-only view, for tests).
func (t *FSMTree) Nodes() []uint8 { return t.nodes }

// Problem 4.3 — FSMTreeSet [Medium]
//
// Set the category of one page's leaf and repair the max-tree above it.
//
// Rules:
//  1. If page >= Capacity(), return ErrPageOutOfRange and leave the tree unchanged.
//  2. Write `cat` into the leaf nodes[nleaves-1+page].
//  3. Walk from that leaf up to the root, setting each ancestor to the max
//     of its TWO children. Recompute from the children — do not just take
//     max(old, cat) — because a category can also DECREASE (the page filled
//     up), and then the ancestors may have to decrease too.
//  4. A tree with a single leaf is just the root (nodes[0] is page 0).
//
// Example (capacity 4 → nodes[0..6], leaves at 3..6):
//
//	Set(0, 10); Set(2, 200); Set(3, 30)
//	nodes = [200 10 200 10 0 200 30]
//	Set(2, 5)                         // page 2 filled up
//	nodes = [30 10 30 10 0 5 30]
//	Set(4, 1)  -> ErrPageOutOfRange
//
// Constraints: O(log n) — touch only the leaf-to-root path.
// Postgres reference: fsm_set_avail() in src/backend/storage/freespace/fsmpage.c
func (t *FSMTree) Set(page types.PageID, cat uint8) error {
	panic("TODO: Problem 4.3")
}

// Problem 4.4 — FSMTreeSearch [Hard]
//
// Find, in O(log n), the first page at or after `start` (wrapping around) whose category is >= minCat.
//
// Postgres does not always hand out the lowest page with room: every FSM page
// keeps a "next slot" hint (fp_next_slot) and searches from there, so that
// concurrent inserters spread over different pages and a single inserter
// fills the table front-to-back instead of hammering page 0. You implement
// the exact version of that search:
//
// Rules:
//  1. If start >= Capacity(), use start = 0.
//  2. Consider pages in circular order: start, start+1, …, Capacity()-1, 0,
//     1, …, start-1. Return the FIRST page in that order whose leaf value is
//     >= minCat.
//  3. If no leaf qualifies (root < minCat) return types.InvalidPageID.
//  4. Leaves that were never Set have category 0, so minCat = 0 always
//     returns `start` (after rule 1).
//
// Example (capacity 8; Set(1,4), Set(3,9), Set(6,4)):
//
//	Search(4, 0)  -> 1
//	Search(4, 2)  -> 3
//	Search(5, 4)  -> 3    (pages 4..7 are too small, wrap around: 0,1,2,3)
//	Search(4, 7)  -> 1    (7 is too small, wrap: 0, 1)
//	Search(10, 0) -> InvalidPageID
//	Search(4, 99) -> 1    (start out of range → 0)
//
// Hint: a subtree whose max is < minCat can be skipped entirely. Split the
// circular range into [start, end] and [0, start-1] and, for each, find the
// leftmost qualifying leaf inside the range by descending the tree, pruning
// subtrees that are outside the range or too small.
//
// Constraints: O(log n) nodes visited per call — no linear scan over leaves.
// Postgres reference: fsm_search_avail() in src/backend/storage/freespace/fsmpage.c
// (Postgres uses an approximate "move right and climb up" walk instead.)
func (t *FSMTree) Search(minCat uint8, start types.PageID) types.PageID {
	panic("TODO: Problem 4.4")
}
