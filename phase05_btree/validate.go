package btree

import (
	"fmt"

	"github.com/natthphong/daTarbase/pkg/types"
)

// Problem 5.22 — Validate [Medium]
//
// Check every structural invariant of the tree and report the first violation (an amcheck for your index).
//
// Rules — return nil if all hold, otherwise an error wrapping ErrInvalidTree
// (fmt.Errorf("%w: ...", ErrInvalidTree, ...)); the message is up to you:
//  1. Every child ID reachable from t.Root exists in the store.
//  2. Balanced: the root is at depth 1, every leaf is at depth t.Height, and
//     every node above that depth is internal (Leaf=false).
//  3. Internal nodes: len(Children) == len(Items)+1 and len(Items) >= 1.
//  4. Sizes: len(Items) <= MaxItems for every node; every non-root node has
//     len(Items) >= MinItems. A root leaf may be empty.
//  5. Items inside a node are strictly increasing by CompareItems.
//  6. Bounds: every item x in a node satisfies low <= x < high, where
//     (low, high) are inherited from the parent separators around the path
//     (unbounded at the root). Child i of a node gets
//     low = Items[i-1] (if i > 0, else the node's own low) and
//     high = Items[i] (if i < len(Items), else the node's own high).
//     This applies to separators in internal nodes too.
//  7. Leaf chain: listing the leaves left to right by the tree walk,
//     the first has Prev == InvalidPageID, the last has Next == InvalidPageID,
//     and each adjacent pair (A, B) has A.Next == B.ID and B.Prev == A.ID.
//
// Example:
//
//	{d g | [a b c] [d e f] [g h i j]}                -> nil
//	same, leaf 1 = [d e c]                           -> ErrInvalidTree (unsorted)
//	same, separators [e g] (d escapes its low bound) -> ErrInvalidTree
//	same, t.Height = 3                               -> ErrInvalidTree
//
// Constraints: one pass over the tree; must not panic on a broken tree
// (missing nodes, wrong child counts, wrong Height).
// Postgres reference: contrib/amcheck (bt_index_check, verify_nbtree.c)
func (t *Tree) Validate() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrInvalidTree}, args...)...)
	}
	var leaves []*Node
	var walk func(id types.PageID, depth int, low, high *Item) error
	walk = func(id types.PageID, depth int, low, high *Item) error {
		n := t.Store.Get(id)
		if n == nil {
			return bad("node %d does not exist", id)
		}
		if depth > t.Height || (depth == t.Height) != n.Leaf {
			return bad("node %d (leaf=%v) at depth %d, height %d", id, n.Leaf, depth, t.Height)
		}
		if len(n.Items) > t.MaxItems {
			return bad("node %d has %d items > max %d", id, len(n.Items), t.MaxItems)
		}
		if depth > 1 && len(n.Items) < t.MinItems() {
			return bad("node %d has %d items < min %d", id, len(n.Items), t.MinItems())
		}
		for i, it := range n.Items {
			if i > 0 && CompareItems(n.Items[i-1], it) >= 0 {
				return bad("node %d items %d,%d out of order", id, i-1, i)
			}
			if low != nil && CompareItems(it, *low) < 0 {
				return bad("node %d item %d below low bound", id, i)
			}
			if high != nil && CompareItems(it, *high) >= 0 {
				return bad("node %d item %d not below high bound", id, i)
			}
		}
		if n.Leaf {
			leaves = append(leaves, n)
			return nil
		}
		if len(n.Items) == 0 || len(n.Children) != len(n.Items)+1 {
			return bad("internal node %d has %d items, %d children", id, len(n.Items), len(n.Children))
		}
		for i, c := range n.Children {
			lo, hi := low, high
			if i > 0 {
				lo = &n.Items[i-1]
			}
			if i < len(n.Items) {
				hi = &n.Items[i]
			}
			if err := walk(c, depth+1, lo, hi); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(t.Root, 1, nil, nil); err != nil {
		return err
	}
	for i, l := range leaves {
		if i == 0 && l.Prev != types.InvalidPageID {
			return bad("first leaf %d has Prev %d", l.ID, l.Prev)
		}
		if i == len(leaves)-1 && l.Next != types.InvalidPageID {
			return bad("last leaf %d has Next %d", l.ID, l.Next)
		}
		if i > 0 && (leaves[i-1].Next != l.ID || l.Prev != leaves[i-1].ID) {
			return bad("leaves %d and %d are not linked", leaves[i-1].ID, l.ID)
		}
	}
	return nil
}
