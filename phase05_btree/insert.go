package btree

import (
	"slices"

	"github.com/natthphong/daTarbase/pkg/types"
)

// Problem 5.6 — InsertItem [Medium]
//
// Insert an entry into an in-memory leaf at its sorted position, without splitting.
//
//	leaf [a(1,1) c(1,1) e(1,1)]  InsertItem(b(1,1), 4) -> pos 1, leaf [a b c e]
//
// Rules:
//  1. The position is n.LowerBound(it); later items shift right.
//  2. If an item equal to it by CompareItems (same Key AND TID) is already
//     present -> return (-1, ErrDuplicateEntry). This check comes first.
//  3. If the node already holds `limit` items -> return (-1, ErrNodeFull).
//  4. On error the node is unchanged.
//  5. Store it as given, Payload included (no copying of the byte slices).
//  6. This is a pure in-memory operation on n: do not touch t.Store
//     (the caller decides when to Put the node).
//
// Example:
//
//	[a c e] insert a(0,5)  -> pos 0  (same key, smaller TID sorts first)
//	[a c e] insert f(1,1)  -> pos 3
//	[a c e] insert c(1,1)  -> -1, ErrDuplicateEntry
//	[a c e] insert b, limit 3 -> -1, ErrNodeFull
//
// Constraints: O(log n) search + O(n) shift.
// Postgres reference: _bt_insertonpg() / PageAddItem() in src/backend/access/nbtree/nbtinsert.c
func (n *Node) InsertItem(it Item, limit int) (int, error) {
	pos := n.LowerBound(it)
	if pos < len(n.Items) && CompareItems(n.Items[pos], it) == 0 {
		return -1, ErrDuplicateEntry
	}
	if len(n.Items) >= limit {
		return -1, ErrNodeFull
	}
	n.Items = slices.Insert(n.Items, pos, it)
	return pos, nil
}

// Problem 5.7 — SplitLeaf [Hard]
//
// Split an overflowing leaf in two, link the new right sibling into the leaf chain, and return the separator to copy up.
//
//	before:  ... <-> [a b c d e] <-> [x y] <-> ...
//	after:   ... <-> [a b c] <-> [d e] <-> [x y] <-> ...      separator d (copied up)
//	                  left       NEW right
//
// Rules:
//  1. With n = len(left.Items) (n >= 2): left keeps the first (n+1)/2
//     items, a NEW leaf from t.Store.Alloc(true) gets the rest. The right
//     node must own its own Items slice (no aliasing with left).
//  2. Sibling links: right.Prev = left.ID, right.Next = old left.Next,
//     left.Next = right.ID. If the old left.Next was a real leaf, Get it,
//     set its Prev = right.ID and Put it.
//  3. Put left and Put right.
//  4. Return the new right node and the separator: right's first item with
//     its Payload dropped — Item{Key, TID} only ("copy-up": the entry stays
//     in the leaf, a copy of its key goes to the parent).
//  5. Do not touch the parent; the caller inserts the separator.
//
// Example (left is leaf 0, no right neighbour, next alloc is node 1):
//
//	[a b c d e] -> left 0 [a b c], right 1 [d e], sep d
//	               0.Next = 1, 1.Prev = 0, 1.Next = Invalid
//	               counters: 1 alloc, 2 writes, 0 reads
//	[a b c d]   -> [a b] [c d]   (4 items: 2 + 2)
//
// Constraints: 1 alloc; 2 writes (+1 read and +1 write if left had a Next).
// Postgres reference: _bt_split() in src/backend/access/nbtree/nbtinsert.c
// (split point chosen by _bt_findsplitloc() in nbtsplitloc.c).
func (t *Tree) SplitLeaf(left *Node) (*Node, Item) {
	k := (len(left.Items) + 1) / 2
	right := t.Store.Alloc(true)
	right.Items = append([]Item(nil), left.Items[k:]...)
	left.Items = slices.Clip(left.Items[:k])

	right.Prev = left.ID
	right.Next = left.Next
	left.Next = right.ID
	if right.Next != types.InvalidPageID {
		nx := t.Store.Get(right.Next)
		nx.Prev = right.ID
		t.Store.Put(nx)
	}
	t.Store.Put(left)
	t.Store.Put(right)
	first := right.Items[0]
	return right, Item{Key: first.Key, TID: first.TID}
}

// Problem 5.8 — SplitInternal [Hard]
//
// Split an overflowing internal node in two and push its middle separator up to the parent.
//
// Unlike a leaf split, the middle separator MOVES up: it is removed from
// both halves, because an internal node only needs k separators for k+1
// children.
//
//	before: c0 b c1 d c2 f c3 h c4 j c5            (5 separators, 6 children)
//	after:  left  c0 b c1 d c2      push up f      right  c3 h c4 j c5
//
// Rules:
//  1. With n = len(left.Items) (n >= 3) and m = n/2:
//     the separator pushed up is left.Items[m];
//     left keeps Items[:m] and Children[:m+1];
//     a NEW internal node from t.Store.Alloc(false) gets Items[m+1:] and
//     Children[m+1:] (in its own slices).
//  2. Internal nodes have no sibling links: leave Prev/Next as allocated.
//  3. Put left and Put right. Return the right node and the pushed-up item.
//  4. Do not touch the parent.
//
// Example (child IDs 10..15, next alloc is node 1):
//
//	[b d f h j] kids [10 11 12 13 14 15] -> left [b d] kids [10 11 12]
//	                                        right 1 [h j] kids [13 14 15], sep f
//	[b d f h]   kids [10 11 12 13 14]    -> left [b d] [10 11 12], right [h] [13 14], sep f
//
// Constraints: 1 alloc, 2 writes, 0 reads.
// Postgres reference: _bt_split() in src/backend/access/nbtree/nbtinsert.c
// (on internal pages the "first data key" of the right page becomes its minus-infinity item).
func (t *Tree) SplitInternal(left *Node) (*Node, Item) {
	m := len(left.Items) / 2
	sep := left.Items[m]
	right := t.Store.Alloc(false)
	right.Items = append([]Item(nil), left.Items[m+1:]...)
	right.Children = append([]types.PageID(nil), left.Children[m+1:]...)
	left.Items = slices.Clip(left.Items[:m])
	left.Children = slices.Clip(left.Children[:m+1])
	t.Store.Put(left)
	t.Store.Put(right)
	return right, sep
}

// Problem 5.9 — GrowRoot [Easy]
//
// The root split: put a new root above the old one, growing the tree by one level.
//
//	before: root R (just split into R and right)
//	after:        NEW {sep}
//	             /        \
//	            R        right
//
// Rules:
//  1. Allocate an internal node, set Items = [sep], Children = [t.Root, right].
//  2. Put it, then set t.Root to its ID and increment t.Height.
//
// Example:
//
//	New tree (root leaf 0), leaf 1 allocated, GrowRoot(d, 1)
//	-> root 2 {d | 0 1}, Height 2; 1 alloc, 1 write
//
// Constraints: 1 alloc, 1 write.
// Postgres reference: _bt_newlevel() in src/backend/access/nbtree/nbtinsert.c
func (t *Tree) GrowRoot(sep Item, right types.PageID) {
	root := t.Store.Alloc(false)
	root.Items = []Item{sep}
	root.Children = []types.PageID{t.Root, right}
	t.Store.Put(root)
	t.Root = root.ID
	t.Height++
}

// Problem 5.10 — Insert [Hard]
//
// Insert an entry into the tree, splitting nodes bottom-up and growing the root when needed.
//
// Inserts with MaxItems = 4, keys a..e ascending (TIDs omitted):
//
//	[a b c d]   + e  ->  leaf overflows to [a b c d e], splits:
//
//	         {d}                 (new root, Height 2)
//	       /     \
//	  [a b c]   [d e]
//
// Rules:
//  1. Descend with the entry itself (Key + TID) and keep the path.
//  2. Insert into the leaf with InsertItem using limit MaxItems+1: a node may
//     hold MaxItems+1 items for a moment ("overflow") right before it is split.
//     If InsertItem fails (ErrDuplicateEntry) return the error: no writes.
//  3. If the leaf has <= MaxItems items, Put it and you are done.
//  4. Otherwise SplitLeaf; then walk up the path: insert the separator into
//     the parent at Items[Pos] and the new right child at Children[Pos+1]
//     (Pos = the child index followed during the descent). If the parent
//     still fits, Put it and stop; else SplitInternal and continue with the
//     pushed-up separator one level higher.
//  5. If the root itself was split, call GrowRoot.
//  6. Leaf entries keep their Payload; separators never carry one.
//
// Example (MaxItems 4, keys inserted a, b, ..., j ascending):
//
//	{d g | [a b c] [d e f] [g h i j]}      leaves 0 -> 1 -> 3, root 2
//	keep going to q: Height 3, root 8 {j | {d g | ...} {m p | ...}}
//
// Constraints: Height reads (+1 per leaf split whose left had a Next).
// Postgres reference: _bt_doinsert() / _bt_insert_parent() in src/backend/access/nbtree/nbtinsert.c
func (t *Tree) Insert(it Item) error {
	path := t.Descend(it)
	leaf := path[len(path)-1].Node
	if _, err := leaf.InsertItem(it, t.MaxItems+1); err != nil {
		return err
	}
	if len(leaf.Items) <= t.MaxItems {
		t.Store.Put(leaf)
		return nil
	}
	right, sep := t.SplitLeaf(leaf)
	rightID := right.ID
	for lvl := len(path) - 2; lvl >= 0; lvl-- {
		p, i := path[lvl].Node, path[lvl].Pos
		p.Items = slices.Insert(p.Items, i, sep)
		p.Children = slices.Insert(p.Children, i+1, rightID)
		if len(p.Items) <= t.MaxItems {
			t.Store.Put(p)
			return nil
		}
		r, s := t.SplitInternal(p)
		rightID, sep = r.ID, s
	}
	t.GrowRoot(sep, rightID)
	return nil
}

// Problem 5.11 — InsertUnique [Easy]
//
// Insert into a UNIQUE index: reject the entry if any entry with the same key exists.
//
// A unique index is an ordinary B-Tree whose insert first looks for an
// existing entry with the same key (ignoring the TID).
//
// Rules:
//  1. If at least one entry with Key equal to it.Key exists -> ErrDuplicateKey,
//     and the tree is unchanged (no writes).
//  2. Otherwise insert it like Insert.
//  3. Every existing entry counts as a conflict (there is no MVCC here; real
//     Postgres would check whether the conflicting heap tuple is still live).
//
// Example:
//
//	InsertUnique(alice,(1,1)) -> nil
//	InsertUnique(alice,(2,2)) -> ErrDuplicateKey
//	InsertUnique(alicia,(2,2)) -> nil
//
// Constraints: O(log n) reads for the check.
// Postgres reference: _bt_check_unique() in src/backend/access/nbtree/nbtinsert.c
func (t *Tree) InsertUnique(it Item) error {
	if len(t.Search(it.Key)) > 0 {
		return ErrDuplicateKey
	}
	return t.Insert(it)
}
