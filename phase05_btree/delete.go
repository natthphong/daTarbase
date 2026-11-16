package btree

import (
	"slices"

	"github.com/natthphong/daTarbase/pkg/types"
)

// Problem 5.15 — RemoveEntry [Medium]
//
// Remove one exact (key, tid) entry from its leaf — found by a single descent even among thousands of duplicates.
//
// When a heap tuple dies, VACUUM must remove exactly the index entry that
// points to it. Before heapkeyspace, Postgres had to scan through all
// duplicates of the key to find the right TID; with the TID as a tie-breaker
// one root-to-leaf descent finds it.
//
// Rules:
//  1. Descend with the probe Item{Key: key, TID: tid}.
//  2. If the leaf position does not hold an item equal to the probe (by
//     CompareItems) -> return (nil, ErrNotFound) with no writes.
//  3. Otherwise delete that item from the leaf, Put the leaf, and return the
//     descent path. The last Step's Node must be the updated leaf (the caller
//     uses the path to rebalance).
//  4. Do NOT rebalance: the leaf may be left with fewer than MinItems items,
//     or even empty.
//
// Example (40 entries with key "k", TIDs (1..8, 1..5), MaxItems 4, Height 3):
//
//	RemoveEntry("k", (5,3)) -> 3 reads, 1 write; Search("k") now has 39 TIDs
//	RemoveEntry("k", (9,9)) -> ErrNotFound, 0 writes
//
// Constraints: exactly Height reads, at most 1 write.
// Postgres reference: btbulkdelete() / _bt_delitems_vacuum() in src/backend/access/nbtree/nbtree.c, nbtpage.c
func (t *Tree) RemoveEntry(key []byte, tid types.TID) ([]Step, error) {
	probe := Item{Key: key, TID: tid}
	path := t.Descend(probe)
	s := path[len(path)-1]
	n := s.Node
	if s.Pos >= len(n.Items) || CompareItems(n.Items[s.Pos], probe) != 0 {
		return nil, ErrNotFound
	}
	n.Items = slices.Delete(n.Items, s.Pos, s.Pos+1)
	t.Store.Put(n)
	return path, nil
}

// Problem 5.16 — Borrow [Medium]
//
// Fix an underfull child by moving one item from a sibling that can spare it (rotation through the parent).
//
// parent.Children[idx] has fewer than MinItems items. Its siblings are the
// neighbouring children of the SAME parent.
//
//	leaf, from the left (MaxItems 4, MinItems 2):
//	  {d g | [a b c] [f] [g h]}    ->   {c g | [a b] [c f] [g h]}
//
//	internal, from the left: the parent separator moves DOWN into the child,
//	the left sibling's last separator moves UP, and its last child moves over:
//	  {m | X{c f i | A B C D}  Y{p | E F}}  ->  {i | X{c f | A B C}  Y{m p | D E F}}
//
// Rules:
//  1. Get the child. If idx > 0, Get the left sibling; if it has more than
//     MinItems items, borrow from it. Only if that did not work and
//     idx+1 < len(parent.Children), Get the right sibling and borrow from it
//     if it has more than MinItems items.
//  2. Leaf from left: move left's last item to the front of the child; set
//     parent.Items[idx-1] = child's new first item (Key+TID, no Payload).
//     Leaf from right: move right's first item to the end of the child; set
//     parent.Items[idx] = right's new first item (Key+TID, no Payload).
//  3. Internal from left: child gets parent.Items[idx-1] as its first item
//     and left's last child as its first child; parent.Items[idx-1] becomes
//     left's last item; left loses its last item and last child.
//     Internal from right: child gets parent.Items[idx] as its last item and
//     right's first child as its last child; parent.Items[idx] becomes
//     right's first item; right loses its first item and first child.
//  4. On success modify `parent` in place, Put the sibling, the child and the
//     parent (3 writes) and return true.
//  5. If neither sibling can lend, return false with no writes and nothing changed.
//
// Example (MaxItems 4):
//
//	{d g | [a b] [e] [g h i]}  Borrow(root, 1) -> {d h | [a b] [e g] [h i]}  reads 3
//	{d g | [a b] [e] [g h]}    Borrow(root, 1) -> false, 0 writes
//
// Constraints: reads 2 or 3, writes 0 or 3.
// Postgres reference: none — Postgres never borrows; it only deletes empty
// pages (_bt_pagedel in nbtpage.c). This is the textbook B+Tree rebalance.
func (t *Tree) Borrow(parent *Node, idx int) bool {
	min := t.MinItems()
	child := t.Store.Get(parent.Children[idx])
	if idx > 0 {
		left := t.Store.Get(parent.Children[idx-1])
		if len(left.Items) > min {
			last := len(left.Items) - 1
			if child.Leaf {
				child.Items = slices.Insert(child.Items, 0, left.Items[last])
				first := child.Items[0]
				parent.Items[idx-1] = Item{Key: first.Key, TID: first.TID}
			} else {
				child.Items = slices.Insert(child.Items, 0, parent.Items[idx-1])
				child.Children = slices.Insert(child.Children, 0, left.Children[last+1])
				parent.Items[idx-1] = left.Items[last]
				left.Children = left.Children[:last+1]
			}
			left.Items = left.Items[:last]
			t.Store.Put(left)
			t.Store.Put(child)
			t.Store.Put(parent)
			return true
		}
	}
	if idx+1 < len(parent.Children) {
		right := t.Store.Get(parent.Children[idx+1])
		if len(right.Items) > min {
			if child.Leaf {
				child.Items = append(child.Items, right.Items[0])
				right.Items = slices.Delete(right.Items, 0, 1)
				first := right.Items[0]
				parent.Items[idx] = Item{Key: first.Key, TID: first.TID}
			} else {
				child.Items = append(child.Items, parent.Items[idx])
				child.Children = append(child.Children, right.Children[0])
				parent.Items[idx] = right.Items[0]
				right.Items = slices.Delete(right.Items, 0, 1)
				right.Children = slices.Delete(right.Children, 0, 1)
			}
			t.Store.Put(right)
			t.Store.Put(child)
			t.Store.Put(parent)
			return true
		}
	}
	return false
}

// Problem 5.17 — Merge [Medium]
//
// Merge an underfull child with a sibling into one node and remove the separator between them from the parent.
//
//	leaf:      {d g | [a b] [e] [g h]}   Merge(root, 1) -> {g | [a b e] [g h]}
//	internal:  {i | X{c f | A B C} Y{l | D E}} Merge(root, 1)
//	           -> { | X{c f i l | A B C D E}}     (separator i is pulled down)
//
// Rules:
//  1. Pick the pair: if idx > 0 merge child idx INTO its left sibling
//     (left = idx-1, right = idx); if idx == 0 merge the right sibling into
//     the child (left = 0, right = 1). The right node of the pair is always
//     the one that disappears. Get left, then right.
//  2. Leaf pair: left.Items += right.Items; left.Next = right.Next; if that is
//     a real leaf, Get it, set its Prev = left.ID and Put it.
//  3. Internal pair: left.Items += parent.Items[left] + right.Items (the
//     separator comes down); left.Children += right.Children.
//  4. Remove parent.Items[left] and parent.Children[right] (modify `parent`
//     in place). Put left, Free right, Put parent.
//  5. The caller guarantees the merged node fits. The parent may become
//     underfull (or, for the root, empty) — that is the caller's problem.
//
// Example (MaxItems 4):
//
//	{d g | [a] [d e] [g h]}  Merge(root, 0) -> {g | [a d e] [g h]}
//	{d g | [a b] [d e] [g]}  Merge(root, 2) -> {d | [a b] [d e g]}   2 reads, 2 writes, 1 free
//
// Constraints: reads 2 (+1 for the leaf after right), writes 2 (+1), 1 free.
// Postgres reference: none — Postgres unlinks empty pages instead
// (_bt_unlink_halfdead_page in nbtpage.c).
func (t *Tree) Merge(parent *Node, idx int) {
	l, r := idx-1, idx
	if idx == 0 {
		l, r = 0, 1
	}
	left := t.Store.Get(parent.Children[l])
	right := t.Store.Get(parent.Children[r])
	if left.Leaf {
		left.Items = append(left.Items, right.Items...)
		left.Next = right.Next
		if right.Next != types.InvalidPageID {
			nx := t.Store.Get(right.Next)
			nx.Prev = left.ID
			t.Store.Put(nx)
		}
	} else {
		left.Items = append(append(left.Items, parent.Items[l]), right.Items...)
		left.Children = append(left.Children, right.Children...)
	}
	parent.Items = slices.Delete(parent.Items, l, l+1)
	parent.Children = slices.Delete(parent.Children, r, r+1)
	t.Store.Put(left)
	t.Store.Free(right.ID)
	t.Store.Put(parent)
}

// Problem 5.18 — Delete [Hard]
//
// Delete one exact (key, tid) entry and rebalance bottom-up: borrow, else merge, and shrink the root.
//
// MaxItems 4 (MinItems 2), starting from {d g | [a b c] [d e f] [g h i j]}:
//
//	Delete e -> {d g | [a b c] [d f] [g h i j]}        leaf still has 2: done
//	Delete d -> {c g | [a b] [c f] [g h i j]}          [f] borrows c from the left
//	Delete a -> {g | [b c f] [g h i j]}                [b] can't borrow: merge
//	...
//	Delete i on {h | [b g] [i j]} -> [b g j]           root emptied: Height 1
//
// Rules:
//  1. RemoveEntry(key, tid); return its error (ErrNotFound) unchanged.
//  2. Walk the path bottom-up, from the leaf up to (but excluding) the root.
//     If the node at this level has >= MinItems items, stop. Otherwise let
//     parent = the node one level up and idx = parent's Step.Pos:
//     Borrow(parent, idx); if it succeeded, stop; else Merge(parent, idx)
//     and continue one level up (the parent lost an item).
//  3. Borrow and Merge modify the path's parent node in place, so the path
//     stays current as you climb.
//  4. Finally, if the root is internal and has 0 items, its only child
//     becomes the root: Free the old root, decrement Height.
//  5. A root leaf is never rebalanced; it may become empty.
//
// Example (17 ascending keys a..q, Height 3, then Delete q, Delete p):
//
//	-> {d g j m | [a b c] [d e f] [g h i] [j k l] [m n o]}, Height 2
//
// Constraints: Height reads for the descent plus what Borrow/Merge read.
// Postgres reference: btbulkdelete() + _bt_pagedel() in src/backend/access/nbtree/nbtpage.c
// (Postgres only removes completely empty pages; it does not merge).
func (t *Tree) Delete(key []byte, tid types.TID) error {
	path, err := t.RemoveEntry(key, tid)
	if err != nil {
		return err
	}
	for lvl := len(path) - 1; lvl >= 1; lvl-- {
		if len(path[lvl].Node.Items) >= t.MinItems() {
			break
		}
		p := path[lvl-1]
		if t.Borrow(p.Node, p.Pos) {
			break
		}
		t.Merge(p.Node, p.Pos)
	}
	root := path[0].Node
	if !root.Leaf && len(root.Items) == 0 {
		t.Root = root.Children[0]
		t.Store.Free(root.ID)
		t.Height--
	}
	return nil
}
