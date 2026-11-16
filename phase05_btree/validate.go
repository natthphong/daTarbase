package btree

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
	panic("TODO: Problem 5.22")
}
