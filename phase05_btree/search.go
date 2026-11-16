package btree

import (
	"bytes"

	"github.com/natthphong/daTarbase/pkg/types"
)

// Problem 5.1 — CompareItems [Easy]
//
// Order two index tuples by (Key, TID): the "heapkeyspace" rule that makes every entry unique.
//
// Since Postgres 12 a B-Tree treats the heap TID as the last key column.
// Two entries with the same key are ordered by where their heap tuples live,
// so duplicates are stored in physical order and every entry — even one of a
// million duplicates — has exactly one place in the tree.
//
//	("apple",(1,1)) < ("apple",(1,2)) < ("apple",(2,0)) < ("banana",(0,0))
//
// Rules:
//  1. Compare Key with bytes.Compare (unsigned byte-wise, a proper prefix
//     sorts first: "ab" < "abc"). A nil key equals an empty key.
//  2. If the keys are equal, compare TID.Page, then TID.Slot (both unsigned).
//  3. Payload is NOT part of the order and must be ignored.
//  4. Return -1 if a < b, 0 if equal, +1 if a > b.
//
// Example:
//
//	CompareItems(Item{Key: []byte("a"), TID: (1,1)}, Item{Key: []byte("b"), TID: (0,0)}) == -1
//	CompareItems(Item{Key: []byte("d"), TID: (2,1)}, Item{Key: []byte("d"), TID: (1,9)}) == +1
//	CompareItems(Item{Key: []byte{0x80}}, Item{Key: []byte{0x7f}})                        == +1
//
// Constraints: no allocation.
// Postgres reference: _bt_compare() in src/backend/access/nbtree/nbtsearch.c
// (the heap TID is compared last via BTreeTupleGetHeapTID).
func CompareItems(a, b Item) int {
	if c := bytes.Compare(a.Key, b.Key); c != 0 {
		return c
	}
	switch {
	case a.TID.Page < b.TID.Page:
		return -1
	case a.TID.Page > b.TID.Page:
		return 1
	case a.TID.Slot < b.TID.Slot:
		return -1
	case a.TID.Slot > b.TID.Slot:
		return 1
	}
	return 0
}

// Problem 5.2 — LowerBound [Easy]
//
// Binary-search a node for the first item that is >= probe.
//
// n.Items is sorted by CompareItems. Return the smallest index i such that
// CompareItems(n.Items[i], probe) >= 0, or len(n.Items) if every item is
// smaller. In a leaf this is "where the probe is, or would be inserted".
//
//	Items: b(1,1) d(1,1) d(1,2) d(2,1) f(1,1)
//	        0      1      2      3      4
//
// Rules:
//  1. Use CompareItems (Key, then TID). Works the same for leaf and internal nodes.
//  2. An empty node returns 0.
//
// Example (items above):
//
//	LowerBound(d, MinTID) = 1   LowerBound(d,(1,2)) = 2   LowerBound(d, MaxTID) = 4
//	LowerBound(a,(0,0))   = 0   LowerBound(z,(0,0)) = 5   LowerBound(f,(1,1))   = 4
//
// Constraints: O(log n) comparisons — a binary search, not a linear scan.
// Postgres reference: _bt_binsrch() in src/backend/access/nbtree/nbtsearch.c
func (n *Node) LowerBound(probe Item) int {
	lo, hi := 0, len(n.Items)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if CompareItems(n.Items[mid], probe) < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// Problem 5.3 — UpperBound [Easy]
//
// Binary-search a node for the first item that is > probe.
//
// Return the smallest index i such that CompareItems(n.Items[i], probe) > 0,
// or len(n.Items) if no item is greater. In an internal node this is the
// child to follow: UpperBound counts the separators that are <= probe, and
// child i holds entries x with Items[i-1] <= x < Items[i].
//
//	Items: b(1,1) d(1,1) d(1,2) d(2,1) f(1,1)
//	        0      1      2      3      4
//
// Rules:
//  1. Use CompareItems (Key, then TID).
//  2. An empty node returns 0.
//
// Example (items above):
//
//	UpperBound(b,(1,1)) = 1   UpperBound(d, MinTID) = 1   UpperBound(d,(1,2)) = 3
//	UpperBound(d, MaxTID) = 4 UpperBound(f,(1,1))   = 5   UpperBound(a,(0,0)) = 0
//
// Constraints: O(log n) comparisons.
// Postgres reference: _bt_binsrch() (internal pages) in src/backend/access/nbtree/nbtsearch.c
func (n *Node) UpperBound(probe Item) int {
	lo, hi := 0, len(n.Items)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if CompareItems(n.Items[mid], probe) <= 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// Problem 5.4 — Descend [Medium]
//
// Walk from the root to the leaf that owns probe, recording the path (a parent stack).
//
//	               root {d(1,1) g(1,1)}
//	          /            |             \
//	  [a b c]          [d e f]          [g h i j]
//
//	Descend(e,(0,0)) -> [ {root, Pos 1}, {leaf [d e f], Pos 1} ]
//
// Rules:
//  1. Start at t.Root and read every node with t.Store.Get (one read each).
//  2. At an internal node, Pos = n.UpperBound(probe); follow n.Children[Pos].
//  3. At the leaf, Pos = n.LowerBound(probe) and the descent stops.
//  4. Return one Step per level, root first, leaf last (len == t.Height).
//     Exactly t.Height nodes are read.
//  5. Because entries are unique by (Key, TID), descending with a full
//     (key, tid) probe lands on the ONE leaf that holds (or would hold) that
//     entry, no matter how many duplicates of key exist. Descending with
//     (key, MinTID) lands on the first leaf that may hold key.
//
// Example (tree above, every TID (1,1)):
//
//	Descend(Item{})          -> root Pos 0, leaf [a b c] Pos 0
//	Descend(g, MinTID)       -> root Pos 1, leaf [d e f] Pos 3  (g(1,1) is in the next leaf)
//	Descend(g, (1,1))        -> root Pos 2, leaf [g h i j] Pos 0
//
// Constraints: exactly Height reads, no writes.
// Postgres reference: _bt_search() in src/backend/access/nbtree/nbtsearch.c
func (t *Tree) Descend(probe Item) []Step {
	path := make([]Step, 0, t.Height)
	id := t.Root
	for {
		n := t.Store.Get(id)
		if n.Leaf {
			return append(path, Step{Node: n, Pos: n.LowerBound(probe)})
		}
		i := n.UpperBound(probe)
		path = append(path, Step{Node: n, Pos: i})
		id = n.Children[i]
	}
}

// Problem 5.5 — Search [Medium]
//
// Return the TIDs of every entry whose key equals key, following the leaf chain across duplicates.
//
// Duplicates of one key may span several leaves, and a separator can be
// stale (left behind by a delete), so the first leaf you land on may end
// before the matching entries start.
//
//	         root {c(2,2) e(1,1)}
//	      /          |          \
//	[a c(1,1)]  [c(2,2) c(3,3)]  [e]
//
//	Search("c") -> [(1,1) (2,2) (3,3)]   reads: root, leaf0, leaf1, leaf2 = 4
//
// Rules:
//  1. Descend with the probe Item{Key: key, TID: MinTID}; start at the
//     returned leaf position.
//  2. Collect TIDs while the entry's Key equals key (bytes.Equal). Stop at the
//     first entry with a different key.
//  3. When you reach the end of a leaf without having seen a different key,
//     read leaf.Next and keep going (stop if Next is InvalidPageID). Read a
//     sibling ONLY in that situation.
//  4. Result is in (Key, TID) order, i.e. ascending TID. No match -> empty
//     (nil) slice.
//
// Example (tree above):
//
//	Search("a") -> [(1,1)]              reads 2
//	Search("b") -> []                   reads 2   (leaf0 pos 1 holds "c")
//	Search("z") -> []                   reads 2   (end of last leaf)
//
// Constraints: reads = Height + number of extra leaves visited. No writes.
// Postgres reference: _bt_first() + _bt_next() in src/backend/access/nbtree/nbtsearch.c
func (t *Tree) Search(key []byte) []types.TID {
	path := t.Descend(Item{Key: key, TID: MinTID})
	last := path[len(path)-1]
	leaf, pos := last.Node, last.Pos
	var out []types.TID
	for {
		for ; pos < len(leaf.Items); pos++ {
			if !bytes.Equal(leaf.Items[pos].Key, key) {
				return out
			}
			out = append(out, leaf.Items[pos].TID)
		}
		if leaf.Next == types.InvalidPageID {
			return out
		}
		leaf, pos = t.Store.Get(leaf.Next), 0
	}
}
