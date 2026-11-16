package executor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/natthphong/daTarbase/pkg/types"
)

// PageBits is the bitmap entry for one heap page (Postgres: PagetableEntry).
//
//	exact page: Slots holds the matching slot numbers; Lossy = false.
//	lossy page: "some tuple on this page may match" — Slots is nil and every
//	            live tuple of the page must be checked against the original
//	            condition (Postgres: a lossy chunk in tidbitmap.c).
type PageBits struct {
	Lossy   bool                      // whole page, slots unknown
	Recheck bool                      // exact page whose tuples must still be rechecked (always implied by Lossy)
	Slots   map[types.SlotID]struct{} // matching slots of an exact page; nil when Lossy
}

// TIDBitmap is a set of heap TIDs grouped by page (Postgres: TIDBitmap).
type TIDBitmap struct {
	Pages map[types.PageID]*PageBits // one entry per page that has (or may have) matches
}

// NewTIDBitmap returns an empty bitmap.
func NewTIDBitmap() *TIDBitmap { return &TIDBitmap{Pages: map[types.PageID]*PageBits{}} }

// Add records an exact TID. If the page is already lossy this is a no-op.
func (b *TIDBitmap) Add(tid types.TID) {
	pb := b.Pages[tid.Page]
	if pb == nil {
		pb = &PageBits{Slots: map[types.SlotID]struct{}{}}
		b.Pages[tid.Page] = pb
	}
	if pb.Lossy {
		return
	}
	pb.Slots[tid.Slot] = struct{}{}
}

// MarkLossy turns page p into a lossy entry (creating it if needed),
// dropping its slot list.
func (b *TIDBitmap) MarkLossy(p types.PageID) {
	b.Pages[p] = &PageBits{Lossy: true}
}

// PageIDs returns the pages in ascending order.
func (b *TIDBitmap) PageIDs() []types.PageID {
	out := make([]types.PageID, 0, len(b.Pages))
	for p := range b.Pages {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// SlotsOf returns the slots of exact page p in ascending order (nil if absent or lossy).
func (b *TIDBitmap) SlotsOf(p types.PageID) []types.SlotID {
	pb := b.Pages[p]
	if pb == nil || pb.Lossy {
		return nil
	}
	out := make([]types.SlotID, 0, len(pb.Slots))
	for s := range pb.Slots {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ExactTuples is the total number of slots stored in exact pages.
func (b *TIDBitmap) ExactTuples() int {
	n := 0
	for _, pb := range b.Pages {
		if !pb.Lossy {
			n += len(pb.Slots)
		}
	}
	return n
}

// String renders the bitmap in page order, e.g. "{0:[1 3] 2:lossy 5:[0]!}"
// where a trailing "!" marks an exact page with Recheck set.
func (b *TIDBitmap) String() string {
	var parts []string
	for _, p := range b.PageIDs() {
		pb := b.Pages[p]
		if pb.Lossy {
			parts = append(parts, fmt.Sprintf("%d:lossy", p))
			continue
		}
		s := fmt.Sprintf("%d:%v", p, b.SlotsOf(p))
		if pb.Recheck {
			s += "!"
		}
		parts = append(parts, s)
	}
	return "{" + strings.Join(parts, " ") + "}"
}

// Problem 6.8 — BitmapIndexScan [Medium]
//
// Collect the TIDs of every index entry in a key range into a TID bitmap, going lossy when it exceeds a memory budget.
//
// A bitmap index scan does not touch the heap at all: it only gathers TIDs.
// The bitmap is later read in PHYSICAL order by Bitmap Heap Scan, turning
// random I/O into (mostly) sequential I/O. When the bitmap would use more than
// work_mem, Postgres degrades some pages to "lossy" (remember only the page,
// not the slots) — cheaper in memory, more work at heap time.
//
// Rules:
//  1. Locate the first in-range entry with a binary search on Range.Lo (as in
//     Problem 6.6) and Add the TID of every entry up to the end of the range.
//     Do not call Table.* (there is no table argument) — only Index.At.
//  2. Memory budget: if maxExact > 0, then while b.ExactTuples() > maxExact,
//     turn ONE exact page into a lossy page: the exact page holding the MOST slots,
//     ties broken by the LOWEST page id. Repeat until within budget.
//     maxExact ≤ 0 means unlimited.
//  3. An empty range returns an empty (non-nil) bitmap.
//
// Example: index over pages 0..2 with matching TIDs (0,1) (0,2) (1,0) (1,1) (1,2) (2,3)
//
//	maxExact 0 → {0:[1 2] 1:[0 1 2] 2:[3]}
//	maxExact 4 → {0:[1 2] 1:lossy 2:[3]}      (page 1 had the most slots)
//	maxExact 1 → {0:lossy 1:lossy 2:[3]}      (then page 0: 2 slots > page 2: 1 slot)
//
// Constraints: O(log n + k) index reads.
// Postgres reference: MultiExecBitmapIndexScan() in nodeBitmapIndexscan.c, tbm_lossify() in tidbitmap.c
func BitmapIndexScan(ix *Index, r KeyRange, maxExact int) *TIDBitmap {
	b := NewTIDBitmap()
	pos := 0
	if lo := r.Lo; lo != nil {
		pos = sort.Search(ix.Len(), func(i int) bool {
			c := types.Compare(ix.At(i).Key, lo.Val)
			return c > 0 || (c == 0 && lo.Inclusive)
		})
	}
	for ; pos < ix.Len(); pos++ {
		e := ix.At(pos)
		if hi := r.Hi; hi != nil {
			c := types.Compare(e.Key, hi.Val)
			if c > 0 || (c == 0 && !hi.Inclusive) {
				break
			}
		}
		b.Add(e.TID)
	}
	if maxExact <= 0 {
		return b
	}
	for b.ExactTuples() > maxExact {
		best, bestN := types.PageID(0), -1
		for _, p := range b.PageIDs() {
			pb := b.Pages[p]
			if !pb.Lossy && len(pb.Slots) > bestN {
				best, bestN = p, len(pb.Slots)
			}
		}
		b.MarkLossy(best)
	}
	return b
}

// BitmapOp selects how BitmapCombine merges two bitmaps.
type BitmapOp uint8

const (
	BitmapAnd BitmapOp = iota // intersection (Postgres: BitmapAnd node)
	BitmapOr                  // union (Postgres: BitmapOr node)
)

// Problem 6.9 — BitmapCombine [Medium]
//
// Intersect (BitmapAnd) or union (BitmapOr) two TID bitmaps, handling lossy pages and recheck flags.
//
// `WHERE a = 1 AND b = 2` with one index on a and one on b can be answered by
// two Bitmap Index Scans combined with BitmapAnd; `a = 1 OR b = 2` with BitmapOr.
// Postgres does it page by page (tbm_intersect / tbm_union).
//
// Rules — build a NEW bitmap; a and b must not be modified. Per page p:
//
//	OR:  page in only one input → copy of that entry.
//	     page in both: if either side is lossy → lossy;
//	     else exact with the union of the slots, Recheck = a.Recheck || b.Recheck.
//	AND: page in only one input → absent from the result.
//	     both lossy → lossy.
//	     one lossy, one exact → the exact side's slots with Recheck = true
//	       (we only know the lossy side "might" match, so the tuple must be rechecked).
//	     both exact → intersection of slots, Recheck = a.Recheck || b.Recheck;
//	       an empty intersection drops the page.
//
// Copies must not share Slots maps with the inputs. A lossy result entry has
// Lossy = true, Recheck = false, Slots = nil.
//
// Example:
//
//	a = {0:[1 2] 1:lossy 3:[4]}   b = {0:[2 5] 1:[3] 2:[0] 3:lossy}
//	AND → {0:[2] 1:[3]! 3:[4]!}
//	OR  → {0:[1 2 5] 1:lossy 2:[0] 3:lossy}
//
// Constraints: O(total entries).
// Postgres reference: tbm_intersect() / tbm_union() in src/backend/nodes/tidbitmap.c
func BitmapCombine(op BitmapOp, a, b *TIDBitmap) *TIDBitmap {
	out := NewTIDBitmap()
	copyEntry := func(pb *PageBits) *PageBits {
		if pb.Lossy {
			return &PageBits{Lossy: true}
		}
		c := &PageBits{Recheck: pb.Recheck, Slots: make(map[types.SlotID]struct{}, len(pb.Slots))}
		for s := range pb.Slots {
			c.Slots[s] = struct{}{}
		}
		return c
	}
	if op == BitmapOr {
		for p, pa := range a.Pages {
			out.Pages[p] = copyEntry(pa)
		}
		for p, pbb := range b.Pages {
			cur := out.Pages[p]
			switch {
			case cur == nil:
				out.Pages[p] = copyEntry(pbb)
			case cur.Lossy || pbb.Lossy:
				out.Pages[p] = &PageBits{Lossy: true}
			default:
				for s := range pbb.Slots {
					cur.Slots[s] = struct{}{}
				}
				cur.Recheck = cur.Recheck || pbb.Recheck
			}
		}
		return out
	}
	for p, pa := range a.Pages {
		pbb := b.Pages[p]
		if pbb == nil {
			continue
		}
		switch {
		case pa.Lossy && pbb.Lossy:
			out.Pages[p] = &PageBits{Lossy: true}
		case pa.Lossy:
			c := copyEntry(pbb)
			c.Recheck = true
			out.Pages[p] = c
		case pbb.Lossy:
			c := copyEntry(pa)
			c.Recheck = true
			out.Pages[p] = c
		default:
			c := &PageBits{Recheck: pa.Recheck || pbb.Recheck, Slots: map[types.SlotID]struct{}{}}
			for s := range pa.Slots {
				if _, ok := pbb.Slots[s]; ok {
					c.Slots[s] = struct{}{}
				}
			}
			if len(c.Slots) > 0 {
				out.Pages[p] = c
			}
		}
	}
	return out
}

// BitmapHeapScan visits the heap pages of a TID bitmap in physical order
// (Postgres: BitmapHeapScan node).
type BitmapHeapScan struct {
	Table  *Table     // heap to read
	Bitmap *TIDBitmap // TIDs to visit (from Problems 6.8 / 6.9)
	Qual   Expr       // the original index conditions ("Recheck Cond:"); nil = always true

	pages []types.PageID // pages still to visit, ascending (set by Open)
	cur   []types.Row    // slot array of the current page
	todo  []types.SlotID // slots of the current page still to visit, ascending
	check bool           // must rows of the current page be rechecked?

	RowsRemovedByRecheck int // rows rejected by the recheck so far ("Rows Removed by Index Recheck")
}

// Open snapshots the bitmap's page list (ascending) and resets the scan.
func (s *BitmapHeapScan) Open() error {
	s.pages = s.Bitmap.PageIDs()
	s.cur, s.todo, s.check, s.RowsRemovedByRecheck = nil, nil, false, 0
	return nil
}

// Close releases the current page.
func (s *BitmapHeapScan) Close() error { s.cur, s.todo, s.pages = nil, nil, nil; return nil }

// Problem 6.10 — BitmapHeapScan [Medium]
//
// Return the rows named by the bitmap in physical (page, slot) order, reading each heap page once and rechecking where needed.
//
// Rules:
//  1. Visit s.pages (already ascending) in order. For each page call
//     Table.ReadPage exactly once (never Table.Fetch), and only when the previous
//     page is used up. Pages ≥ Table.NumPages() are skipped without reading.
//  2. Exact page: candidate slots = Bitmap.SlotsOf(page) (ascending). Lossy page:
//     candidates = every slot of the page, in order.
//  3. Skip dead slots (nil rows) and slots beyond the page's slot array.
//  4. Recheck: evaluate Qual on the row when the page is lossy OR its Recheck flag
//     is set (and Qual is non-nil). Not TRUE → skip and increment
//     s.RowsRemovedByRecheck. Exact pages without Recheck are trusted blindly
//     (Qual is NOT evaluated there). Non-bool non-NULL → ErrTypeMismatch.
//  5. Exhausted → (nil, false, nil).
//
// Example: table a = 0..11 at 4 rows/page; bitmap {0:[1 3] 1:lossy 2:[0]!};
// Qual a IN (1, 3, 5, 9, 99)  (deliberately not matching the bitmap exactly)
//
//	page 0 exact      → rows 1, 3          (slots 1 and 3, not rechecked)
//	page 1 lossy      → row 5              (4, 6, 7 removed by recheck)
//	page 2 exact + !  → (row 8 fails)      (removed by recheck)
//	→ 1, 3, 5; PageReads = 3; Fetches = 0; RowsRemovedByRecheck = 4
//
// Constraints: one ReadPage per bitmap page.
// Postgres reference: BitmapHeapNext() in src/backend/executor/nodeBitmapHeapscan.c
func (s *BitmapHeapScan) Next() (types.Row, bool, error) {
	for {
		for len(s.todo) > 0 {
			slot := s.todo[0]
			s.todo = s.todo[1:]
			if int(slot) >= len(s.cur) || s.cur[slot] == nil {
				continue
			}
			row := s.cur[slot]
			if s.check && s.Qual != nil {
				v, err := Eval(s.Qual, row)
				if err != nil {
					return nil, false, err
				}
				if v.Kind != types.KindBool && v.Kind != types.KindNull {
					return nil, false, ErrTypeMismatch
				}
				if v.IsNull() || !v.B {
					s.RowsRemovedByRecheck++
					continue
				}
			}
			return row, true, nil
		}
		if len(s.pages) == 0 {
			return nil, false, nil
		}
		p := s.pages[0]
		s.pages = s.pages[1:]
		if int(p) >= s.Table.NumPages() {
			continue
		}
		pb := s.Bitmap.Pages[p]
		s.cur = s.Table.ReadPage(p)
		if pb.Lossy {
			s.todo = make([]types.SlotID, len(s.cur))
			for i := range s.cur {
				s.todo[i] = types.SlotID(i)
			}
			s.check = true
		} else {
			s.todo = s.Bitmap.SlotsOf(p)
			s.check = pb.Recheck
		}
	}
}
