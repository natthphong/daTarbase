package btree

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

// fortyK inserts 40 entries with key "k" and TIDs (1..8, 1..5).
func fortyK(t testing.TB) *Tree {
	tr := New(NewNodeStore(), 4)
	for p := 1; p <= 8; p++ {
		for s := 1; s <= 5; s++ {
			if err := tr.Insert(e("k", p, s)); err != nil {
				t.Fatal(err)
			}
		}
	}
	tr.Store.ResetCounters()
	return tr
}

func TestP5_15_RemoveEntry_OneDescentAmongDuplicates(t *testing.T) {
	tr := fortyK(t)
	if tr.Height != 3 {
		t.Fatalf("setup: height = %d, want 3", tr.Height)
	}
	path, err := tr.RemoveEntry(b("k"), tid(5, 3))
	if err != nil {
		t.Fatalf("RemoveEntry(k,(5,3)) = %v", err)
	}
	wantCounters(t, tr.Store, 3, 1, 0, 0)
	if len(path) != 3 {
		t.Fatalf("path length %d, want 3", len(path))
	}
	leaf := path[2].Node
	stored := tr.Store.Peek(leaf.ID)
	if keysOf(leaf.Items) != keysOf(stored.Items) || len(leaf.Items) != len(stored.Items) {
		t.Fatalf("path leaf (%d items) differs from the stored leaf (%d items)", len(leaf.Items), len(stored.Items))
	}
	got := tr.Search(b("k"))
	if len(got) != 39 || slices.Contains(got, tid(5, 3)) {
		t.Fatalf("Search(k) has %d TIDs (contains (5,3): %v), want 39 without it", len(got), slices.Contains(got, tid(5, 3)))
	}
}

func TestP5_15_RemoveEntry_NotFound(t *testing.T) {
	tests := []struct {
		name string
		key  string
		tid  types.TID
	}{
		{"same key, unknown TID", "k", tid(9, 9)},
		{"same key, TID between existing ones", "k", tid(3, 0)},
		{"unknown key", "j", tid(1, 1)},
		{"empty key", "", tid(1, 1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := fortyK(t)
			path, err := tr.RemoveEntry(b(tc.key), tc.tid)
			if !errors.Is(err, ErrNotFound) || path != nil {
				t.Fatalf("RemoveEntry = (%v, %v), want (nil, ErrNotFound)", path, err)
			}
			wantCounters(t, tr.Store, 3, 0, 0, 0)
		})
	}
}

func TestP5_15_RemoveEntry_NoRebalance(t *testing.T) {
	tr := abcTree(t)
	for _, k := range []string{"d", "e", "f"} {
		if _, err := tr.RemoveEntry(b(k), tid(1, 1)); err != nil {
			t.Fatalf("RemoveEntry(%s): %v", k, err)
		}
	}
	wantShape(t, tr, "{d g | [a b c] [] [g h i j]}")
	if _, err := tr.RemoveEntry(b("e"), tid(1, 1)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second RemoveEntry(e) = %v, want ErrNotFound", err)
	}
	if tr.Store.Allocs != 0 || tr.Store.Frees != 0 {
		t.Fatalf("RemoveEntry must not allocate or free: %s", counters(tr.Store))
	}
}

func dg(seps ...string) []Item {
	out := make([]Item, len(seps))
	for i, s := range seps {
		out[i] = e(s, 1, 1)
	}
	return out
}

func TestP5_16_Borrow_Leaves(t *testing.T) {
	tests := []struct {
		name          string
		spec          nd
		idx           int
		wantOK        bool
		wantShape     string
		reads, writes int
	}{
		{"from the left sibling", IS(dg("d", "g"), L("a", "b", "c"), L("f"), L("g", "h")), 1, true,
			"{c g | [a b] [c f] [g h]}", 2, 3},
		{"left can't spare: from the right", IS(dg("d", "g"), L("a", "b"), L("e"), L("g", "h", "i")), 1, true,
			"{d h | [a b] [e g] [h i]}", 3, 3},
		{"first child borrows from the right", IS(dg("d"), L("a"), L("d", "e", "f")), 0, true,
			"{e | [a d] [e f]}", 2, 3},
		{"no sibling can spare", IS(dg("d", "g"), L("a", "b"), L("e"), L("g", "h")), 1, false,
			"{d g | [a b] [e] [g h]}", 3, 0},
		{"last child, left can't spare", IS(dg("d", "g"), L("a", "b"), L("d", "e"), L("g")), 2, false,
			"{d g | [a b] [d e] [g]}", 2, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := build(t, 4, tc.spec)
			parent := tr.Store.Peek(tr.Root)
			if ok := tr.Borrow(parent, tc.idx); ok != tc.wantOK {
				t.Fatalf("Borrow = %v, want %v", ok, tc.wantOK)
			}
			wantShape(t, tr, tc.wantShape)
			if keysOf(parent.Items) != keysOf(tr.Store.Peek(tr.Root).Items) {
				t.Fatalf("parent passed in is {%s}, stored {%s}: modify it in place", keysOf(parent.Items), keysOf(tr.Store.Peek(tr.Root).Items))
			}
			wantCounters(t, tr.Store, tc.reads, tc.writes, 0, 0)
			if tc.wantOK {
				checkTree(t, tr)
			}
		})
	}
}

func TestP5_16_Borrow_MovesPayloadNotIntoSeparator(t *testing.T) {
	tr := build(t, 4, IS(dg("d"),
		LI(Item{Key: b("a"), TID: tid(1, 1), Payload: b("pa")}, Item{Key: b("b"), TID: tid(1, 2), Payload: b("pb")}, Item{Key: b("c"), TID: tid(1, 3), Payload: b("pc")}),
		LI(Item{Key: b("d"), TID: tid(1, 4), Payload: b("pd")})))
	parent := tr.Store.Peek(tr.Root)
	if !tr.Borrow(parent, 1) {
		t.Fatal("Borrow = false, want true")
	}
	child := tr.Store.Peek(parent.Children[1])
	if string(child.Items[0].Payload) != "pc" || child.Items[0].TID != tid(1, 3) {
		t.Fatalf("moved entry = %s%v/%q, want c(1,3)/pc", child.Items[0].Key, child.Items[0].TID, child.Items[0].Payload)
	}
	if sep := parent.Items[0]; string(sep.Key) != "c" || sep.TID != tid(1, 3) || sep.Payload != nil {
		t.Fatalf("separator = %s%v/%q, want c(1,3) without payload", sep.Key, sep.TID, sep.Payload)
	}
}

func TestP5_16_Borrow_InternalRotation(t *testing.T) {
	tests := []struct {
		name      string
		spec      nd
		idx       int
		wantShape string
	}{
		{"from the left: separator comes down, left's last child moves over",
			I(I(L("a", "b"), L("c", "d"), L("f", "g"), L("i", "j")), I(L("m", "n"), L("p", "q"))), 1,
			"{i | {c f | [a b] [c d] [f g]} {m p | [i j] [m n] [p q]}}"},
		{"from the right: separator comes down, right's first child moves over",
			I(I(L("a", "b"), L("c", "d")), I(L("f", "g"), L("i", "j"), L("l", "m"), L("o", "p"))), 0,
			"{i | {c f | [a b] [c d] [f g]} {l o | [i j] [l m] [o p]}}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := build(t, 4, tc.spec)
			leavesBefore := leafIDs(tr)
			if !tr.Borrow(tr.Store.Peek(tr.Root), tc.idx) {
				t.Fatal("Borrow = false, want true")
			}
			wantShape(t, tr, tc.wantShape)
			wantCounters(t, tr.Store, 2, 3, 0, 0)
			checkTree(t, tr)
			if !slices.Equal(leafIDs(tr), leavesBefore) {
				t.Fatalf("leaf chain changed: %v -> %v", leavesBefore, leafIDs(tr))
			}
		})
	}
}

func TestP5_17_Merge(t *testing.T) {
	tests := []struct {
		name          string
		spec          nd
		idx           int
		wantShape     string
		wantFreed     types.PageID
		reads, writes int
		wantLeaves    []types.PageID
	}{
		{"child merges into its left sibling", IS(dg("d", "g"), L("a", "b"), L("e"), L("g", "h")), 1,
			"{g | [a b e] [g h]}", 1, 3, 3, []types.PageID{0, 2}},
		{"first child absorbs its right sibling", IS(dg("d", "g"), L("a"), L("d", "e"), L("g", "h")), 0,
			"{g | [a d e] [g h]}", 1, 3, 3, []types.PageID{0, 2}},
		{"last child: no leaf to relink", IS(dg("d", "g"), L("a", "b"), L("d", "e"), L("g")), 2,
			"{d | [a b] [d e g]}", 2, 2, 2, []types.PageID{0, 1}},
		{"internal: the separator is pulled down", I(I(L("a", "b"), L("c", "d"), L("f", "g")), I(L("i", "j"), L("l", "m"))), 1,
			"{ | {c f i l | [a b] [c d] [f g] [i j] [l m]}}", 6, 2, 2, []types.PageID{0, 1, 2, 4, 5}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := build(t, 4, tc.spec)
			parent := tr.Store.Peek(tr.Root)
			tr.Merge(parent, tc.idx)
			wantShape(t, tr, tc.wantShape)
			if tr.Store.Peek(tc.wantFreed) != nil {
				t.Fatalf("node %d should have been freed", tc.wantFreed)
			}
			wantCounters(t, tr.Store, tc.reads, tc.writes, 0, 1)
			if keysOf(parent.Items) != keysOf(tr.Store.Peek(tr.Root).Items) || len(parent.Children) != len(tr.Store.Peek(tr.Root).Children) {
				t.Fatal("the parent passed in was not updated in place")
			}
			ids := leafIDs(tr)
			if !slices.Equal(ids, tc.wantLeaves) {
				t.Fatalf("leaf chain %v, want %v", ids, tc.wantLeaves)
			}
			for i, id := range ids {
				n := tr.Store.Peek(id)
				wantPrev := types.InvalidPageID
				if i > 0 {
					wantPrev = ids[i-1]
				}
				if n.Prev != wantPrev {
					t.Fatalf("leaf %d Prev = %d, want %d", id, n.Prev, wantPrev)
				}
			}
		})
	}
}

func TestP5_18_Delete_Sequence(t *testing.T) {
	tr := New(NewNodeStore(), 4)
	insertKeys(t, tr, letters(10)...)
	steps := []struct {
		key        string
		wantShape  string
		wantHeight int
	}{
		{"e", "{d g | [a b c] [d f] [g h i j]}", 2},
		{"d", "{c g | [a b] [c f] [g h i j]}", 2},
		{"a", "{g | [b c f] [g h i j]}", 2},
		{"f", "{g | [b c] [g h i j]}", 2},
		{"c", "{h | [b g] [h i j]}", 2},
		{"h", "{h | [b g] [i j]}", 2},
		{"i", "[b g j]", 1},
		{"j", "[b g]", 1},
		{"b", "[g]", 1},
		{"g", "[]", 1},
	}
	for _, s := range steps {
		if err := tr.Delete(b(s.key), tid(1, 1)); err != nil {
			t.Fatalf("Delete(%s) = %v", s.key, err)
		}
		wantShape(t, tr, s.wantShape)
		if tr.Height != s.wantHeight {
			t.Fatalf("after Delete(%s) height = %d, want %d", s.key, tr.Height, s.wantHeight)
		}
		checkTree(t, tr)
	}
	if tr.Root != 0 {
		t.Fatalf("root = %d, want leaf 0", tr.Root)
	}
	if err := tr.Delete(b("g"), tid(1, 1)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete on empty tree = %v, want ErrNotFound", err)
	}
}

func TestP5_18_Delete_ShrinksFromHeight3(t *testing.T) {
	tr := New(NewNodeStore(), 4)
	insertKeys(t, tr, letters(17)...)
	if err := tr.Delete(b("q"), tid(1, 1)); err != nil {
		t.Fatal(err)
	}
	wantShape(t, tr, "{j | {d g | [a b c] [d e f] [g h i]} {m o | [j k l] [m n] [o p]}}")
	tr.Store.ResetCounters()
	if err := tr.Delete(b("p"), tid(1, 1)); err != nil {
		t.Fatal(err)
	}
	wantShape(t, tr, "{d g j m | [a b c] [d e f] [g h i] [j k l] [m n o]}")
	if tr.Root != 2 || tr.Height != 2 {
		t.Fatalf("root %d height %d, want 2 and 2", tr.Root, tr.Height)
	}
	// descent 3 + leaf borrow attempt 2 + leaf merge 2 + internal borrow attempt 2 + internal merge 2
	wantCounters(t, tr.Store, 11, 5, 0, 3)
	for _, id := range []types.PageID{6, 7, 8} {
		if tr.Store.Peek(id) != nil {
			t.Fatalf("node %d should be freed", id)
		}
	}
	checkTree(t, tr)
}

func TestP5_18_Delete_ErrorsLeaveTreeUnchanged(t *testing.T) {
	tr := New(NewNodeStore(), 4)
	insertKeys(t, tr, letters(10)...)
	tr.Store.ResetCounters()
	for _, c := range []struct {
		key string
		tid types.TID
	}{{"z", tid(1, 1)}, {"c", tid(1, 2)}, {"", tid(0, 0)}} {
		if err := tr.Delete(b(c.key), c.tid); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Delete(%s%v) = %v, want ErrNotFound", c.key, c.tid, err)
		}
	}
	if tr.Store.Writes != 0 || tr.Store.Frees != 0 {
		t.Fatalf("failed deletes changed the store: %s", counters(tr.Store))
	}
	wantShape(t, tr, "{d g | [a b c] [d e f] [g h i j]}")
}

func TestP5_18_Delete_EverythingScrambled(t *testing.T) {
	tr := New(NewNodeStore(), 4)
	keys := letters(26)
	insertKeys(t, tr, keys...)
	rng := rand.New(rand.NewPCG(18, 1))
	rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	for i, k := range keys {
		if err := tr.Delete(b(k), tid(1, 1)); err != nil {
			t.Fatalf("Delete(%s): %v", k, err)
		}
		checkTree(t, tr)
		if got := len(leafWalk(tr)); got != len(keys)-i-1 {
			t.Fatalf("after %d deletes the tree holds %d entries", i+1, got)
		}
	}
	if tr.Height != 1 || tr.Store.Len() != 1 {
		t.Fatalf("empty tree: height %d, %d live nodes; want 1 and 1", tr.Height, tr.Store.Len())
	}
}

func TestP5_18_Delete_RandomAgainstOracle(t *testing.T) {
	for _, maxItems := range []int{3, 4, 5} {
		t.Run(fmt.Sprintf("max%d", maxItems), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(uint64(maxItems), 18))
			tr := New(NewNodeStore(), maxItems)
			var want oracle
			for op := range 3000 {
				if rng.IntN(10) < 6 || len(want) == 0 {
					it := e(fmt.Sprintf("k%02d", rng.IntN(60)), rng.IntN(8), rng.IntN(3))
					if (tr.Insert(it) == nil) != want.insert(it) {
						t.Fatalf("op %d: Insert disagrees with the oracle", op)
					}
				} else {
					var it Item
					if rng.IntN(10) < 7 {
						it = want[rng.IntN(len(want))]
					} else {
						it = e(fmt.Sprintf("k%02d", rng.IntN(60)), rng.IntN(8), rng.IntN(3))
					}
					err := tr.Delete(it.Key, it.TID)
					if removed := want.remove(it); removed != (err == nil) {
						t.Fatalf("op %d Delete(%s%v) = %v, oracle had it: %v", op, it.Key, it.TID, err, removed)
					}
				}
				if op%100 == 0 {
					checkTree(t, tr)
				}
			}
			checkTree(t, tr)
			sameItems(t, leafWalk(tr), want)
		})
	}
}

func TestP5_17_Merge_MiddleChildKeepsPayloadsAndLinks(t *testing.T) {
	// {c e g | [a b] [c] [e f] [g h]}: leaves 0..3, root 4. Merge child 1 into leaf 0.
	tr := build(t, 4, I(
		LI(Item{Key: b("a"), TID: tid(1, 1), Payload: b("pa")}, Item{Key: b("b"), TID: tid(1, 2), Payload: b("pb")}),
		LI(Item{Key: b("c"), TID: tid(1, 3), Payload: b("pc")}),
		L("e", "f"), L("g", "h")))
	parent := tr.Store.Peek(tr.Root)
	tr.Merge(parent, 1)
	wantShape(t, tr, "{e g | [a b c] [e f] [g h]}")
	merged := tr.Store.Peek(0)
	if string(merged.Items[2].Payload) != "pc" || merged.Next != 2 || tr.Store.Peek(2).Prev != 0 {
		t.Fatalf("merged leaf: payload %q next %d; leaf 2 prev %d", merged.Items[2].Payload, merged.Next, tr.Store.Peek(2).Prev)
	}
	if !slices.Equal(parent.Children, []types.PageID{0, 2, 3}) {
		t.Fatalf("parent children %v, want [0 2 3]", parent.Children)
	}
	checkTree(t, tr)
}

func TestP5_17_Merge_InternalKeepsLeafChain(t *testing.T) {
	// {i m | X{c f | [a b] [c d] [f g]} Y{k | [i j] [k l]} Z{o q | [m n] [o p] [q r]}}
	tr := build(t, 4, I(
		I(L("a", "b"), L("c", "d"), L("f", "g")),
		I(L("i", "j"), L("k", "l")),
		I(L("m", "n"), L("o", "p"), L("q", "r"))))
	chain := leafIDs(tr)
	tr.Merge(tr.Store.Peek(tr.Root), 1)
	wantShape(t, tr, "{m | {c f i k | [a b] [c d] [f g] [i j] [k l]} {o q | [m n] [o p] [q r]}}")
	if !slices.Equal(leafIDs(tr), chain) {
		t.Fatalf("leaf chain changed: %v -> %v", chain, leafIDs(tr))
	}
	checkTree(t, tr)
}
