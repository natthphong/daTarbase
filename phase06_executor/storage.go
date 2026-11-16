package executor

import (
	"sort"

	"github.com/natthphong/daTarbase/pkg/types"
)

// This file is PROVIDED: tiny in-memory stand-ins for a heap table, a B-tree
// index and the visibility map, instrumented so tests can count I/O.

// Table is a fake heap relation: pages of slots, each slot a row or empty.
type Table struct {
	Name  string        // relation name (for EXPLAIN)
	pages [][]types.Row // pages[p][s] is the row at TID (p,s); nil = dead / unused slot

	PageReads int // number of ReadPage calls (sequential / bitmap page reads)
	Fetches   int // number of Fetch calls (random heap fetches by TID)
}

// NewTable lays rows out rowsPerPage per page, in order: row i gets
// TID (i / rowsPerPage, i % rowsPerPage).
func NewTable(name string, rowsPerPage int, rows ...types.Row) *Table {
	if rowsPerPage <= 0 {
		panic("executor.NewTable: rowsPerPage must be > 0")
	}
	t := &Table{Name: name}
	for i, r := range rows {
		if i%rowsPerPage == 0 {
			t.pages = append(t.pages, nil)
		}
		p := len(t.pages) - 1
		t.pages[p] = append(t.pages[p], r)
	}
	return t
}

// NumPages is the number of pages (Postgres: relpages / RelationGetNumberOfBlocks).
func (t *Table) NumPages() int { return len(t.pages) }

// ReadPage returns a copy of the slot array of page p (dead slots are nil) and
// increments PageReads. Out-of-range pages return nil (still counted).
func (t *Table) ReadPage(p types.PageID) []types.Row {
	t.PageReads++
	if int(p) >= len(t.pages) {
		return nil
	}
	return append([]types.Row(nil), t.pages[p]...)
}

// Fetch returns the row at tid and increments Fetches. ok=false when the TID
// is out of range or the slot is dead.
func (t *Table) Fetch(tid types.TID) (types.Row, bool) {
	t.Fetches++
	if int(tid.Page) >= len(t.pages) || int(tid.Slot) >= len(t.pages[tid.Page]) {
		return nil, false
	}
	r := t.pages[tid.Page][tid.Slot]
	return r, r != nil
}

// Delete marks the slot at tid dead (its TID is never reused). Indexes built
// earlier still point at it, like Postgres index entries before VACUUM.
func (t *Table) Delete(tid types.TID) {
	if int(tid.Page) < len(t.pages) && int(tid.Slot) < len(t.pages[tid.Page]) {
		t.pages[tid.Page][tid.Slot] = nil
	}
}

// IndexEntry is one index tuple: a key and the heap TID it points to.
type IndexEntry struct {
	Key types.Value // the indexed column value (never NULL here)
	TID types.TID   // heap location of the row
}

// Index is a fake single-column B-tree: a sorted array of entries.
type Index struct {
	Name    string       // index name (for EXPLAIN)
	Col     int          // which table column is indexed
	entries []IndexEntry // sorted by Key, then TID

	Reads int // number of At calls (index tuples examined)
}

// NewIndex indexes column col of every live row of t. Rows whose key is NULL
// are NOT indexed (a simplification: real btrees store NULLs). Entries are
// sorted by Key (types.Compare), ties by TID. Building does not touch t's counters.
func NewIndex(name string, t *Table, col int) *Index {
	ix := &Index{Name: name, Col: col}
	for p, page := range t.pages {
		for s, r := range page {
			if r == nil || r[col].IsNull() {
				continue
			}
			ix.entries = append(ix.entries, IndexEntry{Key: r[col], TID: types.TID{Page: types.PageID(p), Slot: types.SlotID(s)}})
		}
	}
	sort.SliceStable(ix.entries, func(i, j int) bool {
		if c := types.Compare(ix.entries[i].Key, ix.entries[j].Key); c != 0 {
			return c < 0
		}
		return ix.entries[i].TID.Less(ix.entries[j].TID)
	})
	return ix
}

// Len is the number of index entries.
func (ix *Index) Len() int { return len(ix.entries) }

// At returns entry i (0 ≤ i < Len()) and increments Reads.
func (ix *Index) At(i int) IndexEntry {
	ix.Reads++
	return ix.entries[i]
}

// Bound is one end of a key range.
type Bound struct {
	Val       types.Value // the boundary value (never NULL)
	Inclusive bool        // true: >= / <=, false: > / <
}

// KeyRange is an index condition on the indexed column. A nil end is unbounded.
//
//	id = 5            → KeyRange{Lo: &Bound{5, true},  Hi: &Bound{5, true}}
//	id > 3 AND id <= 9 → KeyRange{Lo: &Bound{3, false}, Hi: &Bound{9, true}}
//	(no condition)    → KeyRange{}
type KeyRange struct {
	Lo, Hi *Bound // lower / upper end; nil = unbounded
}

// Contains reports whether key lies inside the range.
func (r KeyRange) Contains(key types.Value) bool {
	if r.Lo != nil {
		c := types.Compare(key, r.Lo.Val)
		if c < 0 || (c == 0 && !r.Lo.Inclusive) {
			return false
		}
	}
	if r.Hi != nil {
		c := types.Compare(key, r.Hi.Val)
		if c > 0 || (c == 0 && !r.Hi.Inclusive) {
			return false
		}
	}
	return true
}

// VisibilityMap is a fake visibility map: one "all-visible" bit per heap page
// (Postgres: the _vm fork, see visibilitymap.c).
type VisibilityMap struct {
	allVisible map[types.PageID]bool // pages whose every tuple is visible to everyone

	Lookups int // number of AllVisible calls
}

// NewVisibilityMap returns a map with the given pages marked all-visible.
func NewVisibilityMap(allVisible ...types.PageID) *VisibilityMap {
	vm := &VisibilityMap{allVisible: map[types.PageID]bool{}}
	for _, p := range allVisible {
		vm.allVisible[p] = true
	}
	return vm
}

// Set sets or clears the all-visible bit of page p.
func (vm *VisibilityMap) Set(p types.PageID, allVisible bool) { vm.allVisible[p] = allVisible }

// AllVisible reports the bit for page p and increments Lookups.
func (vm *VisibilityMap) AllVisible(p types.PageID) bool {
	vm.Lookups++
	return vm.allVisible[p]
}
