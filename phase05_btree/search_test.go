package btree

import (
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP5_1_CompareItems(t *testing.T) {
	tests := []struct {
		name string
		a, b Item
		want int
	}{
		{"smaller key wins regardless of TID", e("a", 9, 9), e("b", 0, 0), -1},
		{"larger key", e("b", 0, 0), e("a", 9, 9), 1},
		{"same key: page decides", e("d", 2, 1), e("d", 1, 9), 1},
		{"same key and page: slot decides", e("d", 1, 1), e("d", 1, 2), -1},
		{"identical", e("d", 1, 2), e("d", 1, 2), 0},
		{"proper prefix sorts first", e("ab", 5, 5), e("abc", 0, 0), -1},
		{"bytes are unsigned", Item{Key: []byte{0x80}}, Item{Key: []byte{0x7f}}, 1},
		{"nil key equals empty key", Item{Key: nil, TID: tid(1, 1)}, Item{Key: []byte{}, TID: tid(1, 1)}, 0},
		{"payload is ignored", Item{Key: []byte("k"), TID: tid(1, 1), Payload: []byte("zzz")}, Item{Key: []byte("k"), TID: tid(1, 1), Payload: []byte("aaa")}, 0},
		{"MinTID before any TID", Item{Key: []byte("k"), TID: MinTID}, e("k", 0, 1), -1},
		{"MaxTID after any TID", Item{Key: []byte("k"), TID: MaxTID}, e("k", 1<<31, 65000), 1},
		{"large page numbers compare unsigned", e("k", 1<<31, 0), e("k", 1, 0), 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompareItems(tc.a, tc.b); got != tc.want {
				t.Fatalf("CompareItems = %d, want %d", got, tc.want)
			}
		})
	}
}

var boundNode = &Node{Leaf: true, Items: []Item{e("b", 1, 1), e("d", 1, 1), e("d", 1, 2), e("d", 2, 1), e("f", 1, 1)}}

var boundCases = []struct {
	name         string
	probe        Item
	lower, upper int
}{
	{"before everything", e("a", 0, 0), 0, 0},
	{"equal to first", e("b", 1, 1), 0, 1},
	{"key d with MinTID", Item{Key: []byte("d"), TID: MinTID}, 1, 1},
	{"exact duplicate in the middle", e("d", 1, 2), 2, 3},
	{"between duplicates by TID", e("d", 1, 5), 3, 3},
	{"after all d's", e("d", 9, 9), 4, 4},
	{"key d with MaxTID", Item{Key: []byte("d"), TID: MaxTID}, 4, 4},
	{"key between d and f", e("e", 0, 0), 4, 4},
	{"equal to last", e("f", 1, 1), 4, 5},
	{"after everything", e("z", 0, 0), 5, 5},
	{"smallest item", Item{}, 0, 0},
}

func TestP5_2_LowerBound(t *testing.T) {
	for _, tc := range boundCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := boundNode.LowerBound(tc.probe); got != tc.lower {
				t.Fatalf("LowerBound(%s%v) = %d, want %d", tc.probe.Key, tc.probe.TID, got, tc.lower)
			}
		})
	}
}

func TestP5_2_LowerBound_EmptyAndLarge(t *testing.T) {
	if got := (&Node{Leaf: true}).LowerBound(e("x", 1, 1)); got != 0 {
		t.Fatalf("empty node LowerBound = %d, want 0", got)
	}
	// 1000 entries with key "k" and TIDs (0..999, 7): each finds itself.
	n := &Node{}
	for i := range 1000 {
		n.Items = append(n.Items, e("k", i, 7))
	}
	for _, i := range []int{0, 1, 499, 500, 998, 999} {
		if got := n.LowerBound(e("k", i, 7)); got != i {
			t.Fatalf("LowerBound(k,(%d,7)) = %d, want %d", i, got, i)
		}
		if got := n.LowerBound(e("k", i, 8)); got != i+1 {
			t.Fatalf("LowerBound(k,(%d,8)) = %d, want %d", i, got, i+1)
		}
	}
}

func TestP5_3_UpperBound(t *testing.T) {
	for _, tc := range boundCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := boundNode.UpperBound(tc.probe); got != tc.upper {
				t.Fatalf("UpperBound(%s%v) = %d, want %d", tc.probe.Key, tc.probe.TID, got, tc.upper)
			}
		})
	}
}

func TestP5_3_UpperBound_PicksChild(t *testing.T) {
	// Internal node {d g}: child 0 < d <= child 1 < g <= child 2.
	n := &Node{Items: []Item{e("d", 1, 1), e("g", 1, 1)}, Children: []types.PageID{10, 11, 12}}
	tests := []struct {
		probe Item
		child int
	}{
		{e("a", 5, 5), 0},
		{e("d", 1, 0), 0}, // just before the separator
		{e("d", 1, 1), 1}, // equal to the separator goes right
		{e("f", 9, 9), 1},
		{e("g", 1, 1), 2},
		{e("zzz", 0, 0), 2},
	}
	for _, tc := range tests {
		if got := n.UpperBound(tc.probe); got != tc.child {
			t.Fatalf("UpperBound(%s%v) = %d, want child %d", tc.probe.Key, tc.probe.TID, got, tc.child)
		}
	}
	if got := (&Node{}).UpperBound(e("x", 0, 0)); got != 0 {
		t.Fatalf("empty node UpperBound = %d, want 0", got)
	}
}

// abcTree is {d g | [a b c] [d e f] [g h i j]}, every TID (1,1).
// IDs: leaves 0, 1, 2; root 3.
func abcTree(t testing.TB) *Tree {
	return build(t, 4, I(L("a", "b", "c"), L("d", "e", "f"), L("g", "h", "i", "j")))
}

// dupTree has the key "k" spread over three leaves:
//
//	root {k(2,5) k(4,4)}
//	[a(1,1) k(1,1) k(1,2) k(2,1)] [k(2,5) k(3,1) k(3,2)] [k(4,4) k(5,3) m(1,1)]
func dupTree(t testing.TB) *Tree {
	return build(t, 4, I(
		LI(e("a", 1, 1), e("k", 1, 1), e("k", 1, 2), e("k", 2, 1)),
		LI(e("k", 2, 5), e("k", 3, 1), e("k", 3, 2)),
		LI(e("k", 4, 4), e("k", 5, 3), e("m", 1, 1)),
	))
}

func TestP5_4_Descend(t *testing.T) {
	tests := []struct {
		name     string
		tree     func(testing.TB) *Tree
		probe    Item
		wantPos  []int
		wantLeaf string // keys of the leaf reached
	}{
		{"smallest item goes leftmost", abcTree, Item{}, []int{0, 0}, "a b c"},
		{"middle key", abcTree, e("e", 0, 0), []int{1, 1}, "d e f"},
		{"g with MinTID stops before the separator", abcTree, Item{Key: []byte("g"), TID: MinTID}, []int{1, 3}, "d e f"},
		{"g with its TID crosses the separator", abcTree, e("g", 1, 1), []int{2, 0}, "g h i j"},
		{"past the end", abcTree, e("zz", 0, 0), []int{2, 4}, "g h i j"},
		{"duplicate: exact TID in the middle leaf", dupTree, e("k", 3, 1), []int{1, 1}, "k k k"},
		{"duplicate: MinTID lands on the first leaf", dupTree, Item{Key: []byte("k"), TID: MinTID}, []int{0, 1}, "a k k k"},
		{"duplicate: exact TID in the last leaf", dupTree, e("k", 5, 3), []int{2, 1}, "k k m"},
		{"duplicate: missing TID between leaves", dupTree, e("k", 2, 3), []int{0, 4}, "a k k k"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := tc.tree(t)
			path := tr.Descend(tc.probe)
			if len(path) != tr.Height {
				t.Fatalf("path has %d steps, want %d", len(path), tr.Height)
			}
			var pos []int
			for _, s := range path {
				pos = append(pos, s.Pos)
			}
			if !slices.Equal(pos, tc.wantPos) {
				t.Fatalf("positions = %v, want %v", pos, tc.wantPos)
			}
			if path[0].Node.ID != tr.Root {
				t.Fatalf("first step is node %d, want root %d", path[0].Node.ID, tr.Root)
			}
			leaf := path[len(path)-1].Node
			if !leaf.Leaf || keysOf(leaf.Items) != tc.wantLeaf {
				t.Fatalf("reached leaf [%s], want [%s]", keysOf(leaf.Items), tc.wantLeaf)
			}
			wantCounters(t, tr.Store, tr.Height, 0, 0, 0)
		})
	}
}

func TestP5_4_Descend_ThreeLevels(t *testing.T) {
	// {j | {d g | [a b c] [d e f] [g h i]} {m p | [j k l] [m n o] [p q]}}
	tr := build(t, 4, I(
		I(L("a", "b", "c"), L("d", "e", "f"), L("g", "h", "i")),
		I(L("j", "k", "l"), L("m", "n", "o"), L("p", "q")),
	))
	path := tr.Descend(e("n", 1, 1))
	if len(path) != 3 || path[0].Pos != 1 || path[1].Pos != 1 || path[2].Pos != 1 {
		t.Fatalf("path positions wrong: %+v", path)
	}
	if got := keysOf(path[1].Node.Items); got != "m p" {
		t.Fatalf("level 2 node = {%s}, want {m p}", got)
	}
	wantCounters(t, tr.Store, 3, 0, 0, 0)
}

func TestP5_4_Descend_SingleLeaf(t *testing.T) {
	tr := build(t, 4, L("b", "d"))
	path := tr.Descend(e("c", 0, 0))
	if len(path) != 1 || path[0].Pos != 1 || !path[0].Node.Leaf {
		t.Fatalf("got %+v, want one leaf step with Pos 1", path)
	}
	wantCounters(t, tr.Store, 1, 0, 0, 0)
}

func TestP5_5_Search(t *testing.T) {
	// root {c(2,2) e(1,1)}: [a(1,1) c(1,1)] [c(2,2) c(3,3)] [e(1,1)]
	spanning := func(t testing.TB) *Tree {
		return build(t, 4, I(LI(e("a", 1, 1), e("c", 1, 1)), LI(e("c", 2, 2), e("c", 3, 3)), LI(e("e", 1, 1))))
	}
	// Stale separator c(1,1) with no c left in the tree: [a b] [d e]
	stale := func(t testing.TB) *Tree {
		return build(t, 4, IS([]Item{e("c", 1, 1)}, L("a", "b"), L("d", "e")))
	}
	tests := []struct {
		name      string
		tree      func(testing.TB) *Tree
		key       string
		want      []types.TID
		wantReads int
	}{
		{"duplicates over three leaves", dupTree, "k", []types.TID{tid(1, 1), tid(1, 2), tid(2, 1), tid(2, 5), tid(3, 1), tid(3, 2), tid(4, 4), tid(5, 3)}, 4},
		{"single entry at leaf start", dupTree, "a", []types.TID{tid(1, 1)}, 2},
		{"last entry of the last leaf", dupTree, "m", []types.TID{tid(1, 1)}, 2},
		{"missing key between entries", dupTree, "b", nil, 2},
		{"missing key after everything", dupTree, "z", nil, 2},
		{"missing key before everything", dupTree, "", nil, 2},
		{"descent lands at end of leaf, match continues right", spanning, "c", []types.TID{tid(1, 1), tid(2, 2), tid(3, 3)}, 4},
		{"stale separator: must peek at the next leaf", stale, "c", nil, 3},
		{"stale separator: key right of it", stale, "d", []types.TID{tid(1, 1)}, 2},
		{"prefix of an existing key is not a match", abcTree, "", nil, 2},
		{"key equal to a separator starts one leaf to the left", abcTree, "g", []types.TID{tid(1, 1)}, 3},
		{"exact match in the middle of a leaf", abcTree, "e", []types.TID{tid(1, 1)}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := tc.tree(t)
			got := tr.Search([]byte(tc.key))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Search(%q) = %v, want %v", tc.key, got, tc.want)
			}
			wantCounters(t, tr.Store, tc.wantReads, 0, 0, 0)
		})
	}
}

func TestP5_5_Search_EmptyTree(t *testing.T) {
	tr := New(NewNodeStore(), 4)
	if got := tr.Search([]byte("x")); len(got) != 0 {
		t.Fatalf("Search on empty tree = %v, want none", got)
	}
	if tr.Store.Reads != 1 {
		t.Fatalf("reads = %d, want 1", tr.Store.Reads)
	}
}

func TestP5_5_Search_PrefixKeysAreDistinct(t *testing.T) {
	tr := build(t, 4, I(LI(e("ab", 1, 1), e("ab", 1, 2)), LI(e("abc", 1, 3), e("abd", 1, 4))))
	if got := tr.Search([]byte("ab")); !slices.Equal(got, []types.TID{tid(1, 1), tid(1, 2)}) {
		t.Fatalf("Search(ab) = %v, want [(1,1) (1,2)]", got)
	}
	if got := tr.Search([]byte("abc")); !slices.Equal(got, []types.TID{tid(1, 3)}) {
		t.Fatalf("Search(abc) = %v, want [(1,3)]", got)
	}
}
