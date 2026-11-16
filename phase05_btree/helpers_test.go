package btree

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

// Test helpers. They use only PROVIDED code (NodeStore, Peek, ...), never a
// problem, unless the helper's comment says otherwise.

func tid(page, slot int) types.TID {
	return types.TID{Page: types.PageID(page), Slot: types.SlotID(slot)}
}

// e builds an entry: e("apple", 1, 2) = ("apple", (1,2)).
func e(key string, page, slot int) Item {
	return Item{Key: []byte(key), TID: tid(page, slot)}
}

// nd describes a hand-built node: a leaf (kids == nil) or an internal node.
type nd struct {
	items []Item // leaf entries
	seps  []Item // internal: explicit separators (nil = first entry under each child but the first)
	kids  []nd   // internal: children
}

// L is a leaf whose entries have the given keys, all with TID (1,1).
func L(keys ...string) nd {
	items := make([]Item, len(keys))
	for i, k := range keys {
		items[i] = e(k, 1, 1)
	}
	return nd{items: items}
}

// LI is a leaf with explicit entries.
func LI(items ...Item) nd { return nd{items: items} }

// I is an internal node with computed separators.
func I(kids ...nd) nd { return nd{kids: kids} }

// IS is an internal node with explicit separators.
func IS(seps []Item, kids ...nd) nd { return nd{seps: seps, kids: kids} }

// build allocates the described tree (children before parents, left to
// right), links the leaves, and resets the store counters. It does not
// check any invariant, so it can build broken trees too.
func build(t testing.TB, maxItems int, spec nd) *Tree {
	t.Helper()
	s := NewNodeStore()
	var leaves []types.PageID
	height := 0
	var mk func(x nd, depth int) (types.PageID, Item)
	mk = func(x nd, depth int) (types.PageID, Item) {
		if x.kids == nil {
			height = max(height, depth)
			n := s.Alloc(true)
			n.Items = append([]Item(nil), x.items...)
			s.Put(n)
			leaves = append(leaves, n.ID)
			var low Item
			if len(x.items) > 0 {
				low = Item{Key: x.items[0].Key, TID: x.items[0].TID}
			}
			return n.ID, low
		}
		var ids []types.PageID
		var lows []Item
		for _, k := range x.kids {
			id, lo := mk(k, depth+1)
			ids = append(ids, id)
			lows = append(lows, lo)
		}
		n := s.Alloc(false)
		n.Children = ids
		if x.seps != nil {
			n.Items = append([]Item(nil), x.seps...)
		} else {
			n.Items = append([]Item(nil), lows[1:]...)
		}
		s.Put(n)
		return n.ID, lows[0]
	}
	root, _ := mk(spec, 1)
	for i, id := range leaves {
		n := s.Peek(id)
		if i > 0 {
			n.Prev = leaves[i-1]
		}
		if i+1 < len(leaves) {
			n.Next = leaves[i+1]
		}
		s.Put(n)
	}
	s.ResetCounters()
	return &Tree{Store: s, Root: root, Height: height, MaxItems: maxItems}
}

// shape renders the tree by keys: leaf "[a b c]", internal "{d g | [..] [..]}".
func shape(tr *Tree) string {
	var f func(id types.PageID) string
	f = func(id types.PageID) string {
		n := tr.Store.Peek(id)
		if n == nil {
			return fmt.Sprintf("<missing %d>", id)
		}
		keys := make([]string, len(n.Items))
		for i, it := range n.Items {
			keys[i] = string(it.Key)
		}
		if n.Leaf {
			return "[" + strings.Join(keys, " ") + "]"
		}
		kids := make([]string, len(n.Children))
		for i, c := range n.Children {
			kids[i] = f(c)
		}
		return "{" + strings.Join(keys, " ") + " | " + strings.Join(kids, " ") + "}"
	}
	return f(tr.Root)
}

func wantShape(t testing.TB, tr *Tree, want string) {
	t.Helper()
	if got := shape(tr); got != want {
		t.Fatalf("tree shape\n  got:  %s\n  want: %s", got, want)
	}
}

// leafWalk returns every entry by walking the leaf chain from the leftmost leaf.
func leafWalk(tr *Tree) []Item {
	n := tr.Store.Peek(tr.Root)
	for !n.Leaf {
		n = tr.Store.Peek(n.Children[0])
	}
	var out []Item
	for {
		out = append(out, n.Items...)
		if n.Next == types.InvalidPageID {
			return out
		}
		n = tr.Store.Peek(n.Next)
	}
}

// leafIDs returns the leaf chain IDs from left to right.
func leafIDs(tr *Tree) []types.PageID {
	n := tr.Store.Peek(tr.Root)
	for !n.Leaf {
		n = tr.Store.Peek(n.Children[0])
	}
	var out []types.PageID
	for {
		out = append(out, n.ID)
		if n.Next == types.InvalidPageID {
			return out
		}
		n = tr.Store.Peek(n.Next)
	}
}

// cmpItem is the test's own (Key, TID) order, independent of CompareItems.
func cmpItem(a, b Item) int {
	if c := bytes.Compare(a.Key, b.Key); c != 0 {
		return c
	}
	if a.TID.Less(b.TID) {
		return -1
	}
	if b.TID.Less(a.TID) {
		return 1
	}
	return 0
}

// checkTree is the test's own invariant checker (same rules as Validate,
// plus: separators carry no payload). It fails the test on the first violation.
func checkTree(t testing.TB, tr *Tree) {
	t.Helper()
	var leaves []*Node
	var walk func(id types.PageID, depth int, low, high *Item)
	walk = func(id types.PageID, depth int, low, high *Item) {
		n := tr.Store.Peek(id)
		if n == nil {
			t.Fatalf("node %d missing\n%s", id, shape(tr))
		}
		if (depth == tr.Height) != n.Leaf || depth > tr.Height {
			t.Fatalf("node %d leaf=%v at depth %d, height %d\n%s", id, n.Leaf, depth, tr.Height, shape(tr))
		}
		if len(n.Items) > tr.MaxItems || (depth > 1 && len(n.Items) < tr.MaxItems/2) {
			t.Fatalf("node %d has %d items (max %d)\n%s", id, len(n.Items), tr.MaxItems, shape(tr))
		}
		for i, it := range n.Items {
			if i > 0 && cmpItem(n.Items[i-1], it) >= 0 {
				t.Fatalf("node %d not sorted at %d\n%s", id, i, shape(tr))
			}
			if (low != nil && cmpItem(it, *low) < 0) || (high != nil && cmpItem(it, *high) >= 0) {
				t.Fatalf("node %d item %d (%s) out of bounds\n%s", id, i, it.Key, shape(tr))
			}
			if !n.Leaf && it.Payload != nil {
				t.Fatalf("separator in node %d carries a payload", id)
			}
		}
		if n.Leaf {
			leaves = append(leaves, n)
			return
		}
		if len(n.Items) == 0 || len(n.Children) != len(n.Items)+1 {
			t.Fatalf("internal node %d: %d items, %d children", id, len(n.Items), len(n.Children))
		}
		for i, c := range n.Children {
			lo, hi := low, high
			if i > 0 {
				lo = &n.Items[i-1]
			}
			if i < len(n.Items) {
				hi = &n.Items[i]
			}
			walk(c, depth+1, lo, hi)
		}
	}
	walk(tr.Root, 1, nil, nil)
	for i, l := range leaves {
		var wantPrev, wantNext types.PageID = types.InvalidPageID, types.InvalidPageID
		if i > 0 {
			wantPrev = leaves[i-1].ID
		}
		if i+1 < len(leaves) {
			wantNext = leaves[i+1].ID
		}
		if l.Prev != wantPrev || l.Next != wantNext {
			t.Fatalf("leaf %d links prev=%d next=%d, want %d %d", l.ID, l.Prev, l.Next, wantPrev, wantNext)
		}
	}
}

// oracle is a sorted slice of entries.
type oracle []Item

func (o *oracle) find(it Item) (int, bool) { return slices.BinarySearchFunc(*o, it, cmpItem) }

func (o *oracle) insert(it Item) bool {
	i, found := o.find(it)
	if found {
		return false
	}
	*o = slices.Insert(*o, i, it)
	return true
}

func (o *oracle) remove(it Item) bool {
	i, found := o.find(it)
	if !found {
		return false
	}
	*o = slices.Delete(*o, i, i+1)
	return true
}

func (o oracle) tids(key []byte) []types.TID {
	var out []types.TID
	for _, it := range o {
		if bytes.Equal(it.Key, key) {
			out = append(out, it.TID)
		}
	}
	return out
}

func sameItems(t testing.TB, got, want []Item) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range got {
		if cmpItem(got[i], want[i]) != 0 || !bytes.Equal(got[i].Payload, want[i].Payload) {
			t.Fatalf("entry %d = %s%v/%q, want %s%v/%q", i, got[i].Key, got[i].TID, got[i].Payload, want[i].Key, want[i].TID, want[i].Payload)
		}
	}
}

func keysOf(items []Item) string {
	ks := make([]string, len(items))
	for i, it := range items {
		ks[i] = string(it.Key)
	}
	return strings.Join(ks, " ")
}

func counters(s *NodeStore) string {
	return fmt.Sprintf("reads=%d writes=%d allocs=%d frees=%d", s.Reads, s.Writes, s.Allocs, s.Frees)
}

func wantCounters(t testing.TB, s *NodeStore, reads, writes, allocs, frees int) {
	t.Helper()
	want := fmt.Sprintf("reads=%d writes=%d allocs=%d frees=%d", reads, writes, allocs, frees)
	if got := counters(s); got != want {
		t.Fatalf("store counters %s, want %s", got, want)
	}
}

// letters returns n one-letter keys starting at 'a'.
func letters(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = string(rune('a' + i))
	}
	return out
}
