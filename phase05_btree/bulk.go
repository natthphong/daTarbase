package btree

import "github.com/natthphong/daTarbase/pkg/types"

// Problem 5.19 — BulkLoad [Hard]
//
// Build a tree from sorted entries the way CREATE INDEX does: fill leaves left to right, then build each upper level bottom-up.
//
// Inserting n sorted entries one by one splits every page on the way and
// leaves them half empty. CREATE INDEX instead sorts the heap's entries and
// writes each page exactly once, leaving a little free space (fillfactor)
// so that later inserts don't split immediately.
//
// MaxItems 4, fillfactor 90 -> 3 entries per leaf, 4 children per internal node:
//
//	01..10:   {04 07 | [01 02 03] [04 05 06] [07 08 09 10]}
//	          leaves 0,1,2 (written left to right), root 3
//
// Rules:
//  1. fillfactor 0 means DefaultFillfactor (90). Anything else outside
//     [50, 100] -> (nil, ErrBadFillfactor).
//  2. items must be strictly increasing by CompareItems, else
//     (nil, ErrUnsorted). Validate before allocating anything.
//  3. Capacities: cap = MaxItems*fillfactor/100 (integer division) items per
//     leaf; internal nodes take cap+1 children (cap separators).
//  4. Grouping a level of n things (entries or children) with capacity c,
//     minimum m and maximum M: cut into groups of c from the left; the last
//     group gets the remainder. If there are >= 2 groups and the last has
//     fewer than m: if last two together <= M, merge them into one group;
//     otherwise split their total evenly, the first getting the extra one
//     ((total+1)/2 and total/2).
//     Leaves:   c = cap,   m = MinItems,   M = MaxItems.
//     Internal: c = cap+1, m = MinItems+1, M = MaxItems+1   (counted in children).
//  5. Allocate all nodes of a level (left to right) before the next level up.
//     Leaves get Prev/Next links. An internal node's separators are the first
//     entry (Key+TID, no Payload) under each of its children except the first.
//     Put every node exactly once. Stop when a level has a single node: it is
//     the root. Height = number of levels.
//  6. Empty input -> one empty root leaf (1 alloc, 1 write), Height 1.
//  7. Leaf entries keep their Payload.
//
// Example (MaxItems 4):
//
//	ff 90, 20 entries -> leaves of 3,3,3,3,3,3,2; internal groups of 4 and 3 children:
//	  {13 | {04 07 10 | [01 02 03] [04 05 06] [07 08 09] [10 11 12]}
//	        {16 19 | [13 14 15] [16 17 18] [19 20]}}           10 nodes, 10 writes, Height 3
//	ff 100, 5 entries -> groups 4,1 -> total 5 > 4 -> 3,2: {04 | [01 02 03] [04 05]}
//	ff 50, 7 entries  -> groups 2,2,2,1 -> 2+1 <= 4 -> merge: {03 05 | [01 02] [03 04] [05 06 07]}
//
// Constraints: each node written exactly once; O(n).
// Postgres reference: _bt_load() / _bt_buildadd() / _bt_uppershutdown() in
// src/backend/access/nbtree/nbtsort.c
func BulkLoad(store *NodeStore, maxItems int, items []Item, fillfactor int) (*Tree, error) {
	if fillfactor == 0 {
		fillfactor = DefaultFillfactor
	}
	if fillfactor < 50 || fillfactor > 100 {
		return nil, ErrBadFillfactor
	}
	for i := 1; i < len(items); i++ {
		if CompareItems(items[i-1], items[i]) >= 0 {
			return nil, ErrUnsorted
		}
	}
	t := &Tree{Store: store, MaxItems: maxItems, Height: 1}
	capacity := maxItems * fillfactor / 100
	minItems := t.MinItems()
	groups := func(n, c, m, max int) []int {
		var g []int
		for n > 0 {
			s := min(c, n)
			g = append(g, s)
			n -= s
		}
		if k := len(g); k >= 2 && g[k-1] < m {
			total := g[k-2] + g[k-1]
			if total <= max {
				g = append(g[:k-2], total)
			} else {
				g[k-2], g[k-1] = (total+1)/2, total/2
			}
		}
		if len(g) == 0 {
			g = []int{0}
		}
		return g
	}

	sizes := groups(len(items), capacity, minItems, maxItems)
	leaves := make([]*Node, len(sizes))
	for i := range leaves {
		leaves[i] = store.Alloc(true)
	}
	ids := make([]types.PageID, len(leaves))
	lows := make([]Item, len(leaves))
	off := 0
	for i, n := range leaves {
		n.Items = append([]Item(nil), items[off:off+sizes[i]]...)
		off += sizes[i]
		if i > 0 {
			n.Prev = leaves[i-1].ID
		}
		if i+1 < len(leaves) {
			n.Next = leaves[i+1].ID
		}
		store.Put(n)
		ids[i] = n.ID
		if len(n.Items) > 0 {
			lows[i] = Item{Key: n.Items[0].Key, TID: n.Items[0].TID}
		}
	}

	for len(ids) > 1 {
		sizes := groups(len(ids), capacity+1, minItems+1, maxItems+1)
		parents := make([]*Node, len(sizes))
		for i := range parents {
			parents[i] = store.Alloc(false)
		}
		nextIDs := make([]types.PageID, len(parents))
		nextLows := make([]Item, len(parents))
		off := 0
		for i, p := range parents {
			p.Children = append([]types.PageID(nil), ids[off:off+sizes[i]]...)
			p.Items = append([]Item(nil), lows[off+1:off+sizes[i]]...)
			nextLows[i] = lows[off]
			off += sizes[i]
			store.Put(p)
			nextIDs[i] = p.ID
		}
		ids, lows = nextIDs, nextLows
		t.Height++
	}
	t.Root = ids[0]
	return t, nil
}
