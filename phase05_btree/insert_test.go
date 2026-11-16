package btree

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP5_6_InsertItem(t *testing.T) {
	tests := []struct {
		name     string
		item     Item
		limit    int
		wantPos  int
		wantErr  error
		wantKeys string
	}{
		{"into the middle", e("b", 1, 1), 4, 1, nil, "a b c e"},
		{"at the end", e("f", 1, 1), 4, 3, nil, "a c e f"},
		{"at the front", e("0", 1, 1), 4, 0, nil, "0 a c e"},
		{"same key smaller TID goes first", e("a", 0, 5), 4, 0, nil, "a a c e"},
		{"same key larger TID goes after", e("c", 1, 2), 4, 2, nil, "a c c e"},
		{"exact duplicate rejected", e("c", 1, 1), 4, -1, ErrDuplicateEntry, "a c e"},
		{"full node rejected", e("b", 1, 1), 3, -1, ErrNodeFull, "a c e"},
		{"duplicate is reported before full", e("e", 1, 1), 3, -1, ErrDuplicateEntry, "a c e"},
		{"overflow slot (limit = max+1) accepted", e("d", 1, 1), 4, 2, nil, "a c d e"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := &Node{Leaf: true, Items: []Item{e("a", 1, 1), e("c", 1, 1), e("e", 1, 1)}}
			pos, err := n.InsertItem(tc.item, tc.limit)
			if !errors.Is(err, tc.wantErr) || pos != tc.wantPos {
				t.Fatalf("InsertItem = (%d, %v), want (%d, %v)", pos, err, tc.wantPos, tc.wantErr)
			}
			if got := keysOf(n.Items); got != tc.wantKeys {
				t.Fatalf("leaf = [%s], want [%s]", got, tc.wantKeys)
			}
			if err == nil && cmpItem(n.Items[pos], tc.item) != 0 {
				t.Fatalf("item at returned pos %d is %s%v", pos, n.Items[pos].Key, n.Items[pos].TID)
			}
		})
	}
}

func TestP5_6_InsertItem_EmptyAndPayload(t *testing.T) {
	n := &Node{Leaf: true}
	if pos, err := n.InsertItem(Item{Key: []byte("k"), TID: tid(3, 4), Payload: []byte("alice")}, 4); pos != 0 || err != nil {
		t.Fatalf("InsertItem into empty node = (%d, %v), want (0, nil)", pos, err)
	}
	if string(n.Items[0].Payload) != "alice" {
		t.Fatalf("payload = %q, want %q", n.Items[0].Payload, "alice")
	}
}

func TestP5_6_InsertItem_ManyDuplicatesStaySorted(t *testing.T) {
	n := &Node{Leaf: true}
	for _, p := range []int{5, 1, 9, 3, 7, 2} {
		if _, err := n.InsertItem(e("k", p, 0), 100); err != nil {
			t.Fatalf("insert (k,(%d,0)): %v", p, err)
		}
	}
	var pages []types.PageID
	for _, it := range n.Items {
		pages = append(pages, it.TID.Page)
	}
	if !slices.Equal(pages, []types.PageID{1, 2, 3, 5, 7, 9}) {
		t.Fatalf("TID pages = %v, want [1 2 3 5 7 9]", pages)
	}
}

func TestP5_7_SplitLeaf(t *testing.T) {
	tests := []struct {
		name      string
		keys      []string
		wantLeft  string
		wantRight string
		wantSep   string
	}{
		{"five items: 3 + 2", []string{"a", "b", "c", "d", "e"}, "a b c", "d e", "d"},
		{"four items: 2 + 2", []string{"a", "b", "c", "d"}, "a b", "c d", "c"},
		{"two items: 1 + 1", []string{"a", "b"}, "a", "b", "b"},
		{"six items: 3 + 3", []string{"a", "b", "c", "d", "e", "f"}, "a b c", "d e f", "d"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := build(t, 4, L(tc.keys...)) // single leaf, ID 0
			left := tr.Store.Peek(0)
			right, sep := tr.SplitLeaf(left)
			if right.ID != 1 {
				t.Fatalf("right.ID = %d, want 1 (next alloc)", right.ID)
			}
			if string(sep.Key) != tc.wantSep || sep.TID != tid(1, 1) || sep.Payload != nil {
				t.Fatalf("sep = %s%v/%q, want %s(1,1) without payload", sep.Key, sep.TID, sep.Payload, tc.wantSep)
			}
			l, r := tr.Store.Peek(0), tr.Store.Peek(1)
			if keysOf(l.Items) != tc.wantLeft || keysOf(r.Items) != tc.wantRight {
				t.Fatalf("stored [%s] [%s], want [%s] [%s]", keysOf(l.Items), keysOf(r.Items), tc.wantLeft, tc.wantRight)
			}
			if !r.Leaf || l.Next != 1 || r.Prev != 0 || r.Next != types.InvalidPageID || l.Prev != types.InvalidPageID {
				t.Fatalf("links: left prev/next %d/%d, right prev/next %d/%d", l.Prev, l.Next, r.Prev, r.Next)
			}
			wantCounters(t, tr.Store, 0, 2, 1, 0)
		})
	}
}

func TestP5_7_SplitLeaf_RelinksRightNeighbour(t *testing.T) {
	// leaves 0 [a b c d e], 1 [x y]; root 2. The split creates leaf 3.
	tr := build(t, 4, I(L("a", "b", "c", "d", "e"), L("x", "y")))
	right, _ := tr.SplitLeaf(tr.Store.Peek(0))
	if right.ID != 3 {
		t.Fatalf("right.ID = %d, want 3", right.ID)
	}
	l, r, x := tr.Store.Peek(0), tr.Store.Peek(3), tr.Store.Peek(1)
	if l.Next != 3 || r.Prev != 0 || r.Next != 1 || x.Prev != 3 {
		t.Fatalf("chain wrong: 0.next=%d 3.prev=%d 3.next=%d 1.prev=%d, want 3 0 1 3", l.Next, r.Prev, r.Next, x.Prev)
	}
	if got := keysOf(tr.Store.Peek(2).Items); got != "x" {
		t.Fatalf("parent changed to {%s}; SplitLeaf must not touch it", got)
	}
	wantCounters(t, tr.Store, 1, 3, 1, 0)
}

func TestP5_7_SplitLeaf_PayloadsAndNoAliasing(t *testing.T) {
	tr := build(t, 4, LI(
		Item{Key: []byte("a"), TID: tid(1, 1), Payload: []byte("pa")},
		Item{Key: []byte("b"), TID: tid(1, 2), Payload: []byte("pb")},
		Item{Key: []byte("c"), TID: tid(1, 3), Payload: []byte("pc")},
		Item{Key: []byte("d"), TID: tid(1, 4), Payload: []byte("pd")},
		Item{Key: []byte("e"), TID: tid(1, 5), Payload: []byte("pe")},
	))
	left := tr.Store.Peek(0)
	right, sep := tr.SplitLeaf(left)
	if sep.Payload != nil || sep.TID != tid(1, 4) {
		t.Fatalf("sep = %s%v/%q, want d(1,4) without payload", sep.Key, sep.TID, sep.Payload)
	}
	if string(right.Items[0].Payload) != "pd" || string(right.Items[1].Payload) != "pe" {
		t.Fatalf("right payloads lost: %q %q", right.Items[0].Payload, right.Items[1].Payload)
	}
	left.Items = append(left.Items, e("zz", 9, 9))
	if string(right.Items[0].Key) != "d" {
		t.Fatalf("appending to left overwrote right.Items[0] (= %s): slices alias", right.Items[0].Key)
	}
}

func TestP5_8_SplitInternal(t *testing.T) {
	tests := []struct {
		name                 string
		seps                 []string
		wantLeft, wantRight  string
		wantLKids, wantRKids []types.PageID
		wantSep              string
	}{
		{"five separators", []string{"b", "d", "f", "h", "j"}, "b d", "h j", []types.PageID{10, 11, 12}, []types.PageID{13, 14, 15}, "f"},
		{"four separators", []string{"b", "d", "f", "h"}, "b d", "h", []types.PageID{10, 11, 12}, []types.PageID{13, 14}, "f"},
		{"three separators", []string{"b", "d", "f"}, "b", "f", []types.PageID{10, 11}, []types.PageID{12, 13}, "d"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewNodeStore()
			n := s.Alloc(false) // ID 0
			for i, k := range tc.seps {
				n.Items = append(n.Items, e(k, 1, 1))
				n.Children = append(n.Children, types.PageID(10+i))
			}
			n.Children = append(n.Children, types.PageID(10+len(tc.seps)))
			s.Put(n)
			s.ResetCounters()
			tr := &Tree{Store: s, Root: 0, Height: 2, MaxItems: 4}

			right, sep := tr.SplitInternal(n)
			if right.ID != 1 || string(sep.Key) != tc.wantSep {
				t.Fatalf("got right %d sep %s, want right 1 sep %s", right.ID, sep.Key, tc.wantSep)
			}
			l, r := s.Peek(0), s.Peek(1)
			if r.Leaf || keysOf(l.Items) != tc.wantLeft || keysOf(r.Items) != tc.wantRight {
				t.Fatalf("stored {%s} {%s}, want {%s} {%s}", keysOf(l.Items), keysOf(r.Items), tc.wantLeft, tc.wantRight)
			}
			if !slices.Equal(l.Children, tc.wantLKids) || !slices.Equal(r.Children, tc.wantRKids) {
				t.Fatalf("children %v %v, want %v %v", l.Children, r.Children, tc.wantLKids, tc.wantRKids)
			}
			if r.Prev != types.InvalidPageID || r.Next != types.InvalidPageID {
				t.Fatalf("internal node got sibling links %d/%d", r.Prev, r.Next)
			}
			wantCounters(t, s, 0, 2, 1, 0)
		})
	}
}

func TestP5_9_GrowRoot(t *testing.T) {
	s := NewNodeStore()
	tr := New(s, 4) // root leaf 0
	right := s.Alloc(true)
	s.ResetCounters()
	tr.GrowRoot(e("d", 1, 1), right.ID)
	if tr.Root != 2 || tr.Height != 2 {
		t.Fatalf("root %d height %d, want 2 and 2", tr.Root, tr.Height)
	}
	root := s.Peek(2)
	if root.Leaf || keysOf(root.Items) != "d" || !slices.Equal(root.Children, []types.PageID{0, 1}) {
		t.Fatalf("root = leaf:%v {%s} %v, want internal {d} [0 1]", root.Leaf, keysOf(root.Items), root.Children)
	}
	wantCounters(t, s, 0, 1, 1, 0)

	// Growing again stacks another level on top.
	tr.GrowRoot(e("m", 1, 1), 1)
	if tr.Root != 3 || tr.Height != 3 || !slices.Equal(s.Peek(3).Children, []types.PageID{2, 1}) {
		t.Fatalf("second grow: root %d height %d children %v", tr.Root, tr.Height, s.Peek(3).Children)
	}
}

// insertKeys inserts each key with TID (1,1) and fails on error.
func insertKeys(t testing.TB, tr *Tree, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if err := tr.Insert(e(k, 1, 1)); err != nil {
			t.Fatalf("Insert(%s): %v", k, err)
		}
	}
}

func TestP5_10_Insert_Shapes(t *testing.T) {
	desc := letters(10)
	slices.Reverse(desc)
	tests := []struct {
		name       string
		keys       []string
		wantShape  string
		wantRoot   types.PageID
		wantHeight int
		wantLeaves []types.PageID
	}{
		{"fits in the root leaf", []string{"c", "a", "d", "b"}, "[a b c d]", 0, 1, []types.PageID{0}},
		{"fifth key splits the root leaf", letters(5), "{d | [a b c] [d e]}", 2, 2, []types.PageID{0, 1}},
		{"ten ascending keys", letters(10), "{d g | [a b c] [d e f] [g h i j]}", 2, 2, []types.PageID{0, 1, 3}},
		{"ten descending keys", desc, "{e g i | [a b c d] [e f] [g h] [i j]}", 2, 2, []types.PageID{0, 4, 3, 1}},
		{"seventeen ascending keys: internal split, height 3", letters(17),
			"{j | {d g | [a b c] [d e f] [g h i]} {m p | [j k l] [m n o] [p q]}}", 8, 3, []types.PageID{0, 1, 3, 4, 5, 6}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := New(NewNodeStore(), 4)
			insertKeys(t, tr, tc.keys...)
			wantShape(t, tr, tc.wantShape)
			if tr.Root != tc.wantRoot || tr.Height != tc.wantHeight {
				t.Fatalf("root %d height %d, want %d %d", tr.Root, tr.Height, tc.wantRoot, tc.wantHeight)
			}
			if got := leafIDs(tr); !slices.Equal(got, tc.wantLeaves) {
				t.Fatalf("leaf chain %v, want %v", got, tc.wantLeaves)
			}
			checkTree(t, tr)
		})
	}
}

func TestP5_10_Insert_IOCounts(t *testing.T) {
	tests := []struct {
		name      string
		before    []string // inserted before counters are reset
		key       string
		wantErr   error
		wantShape string
		reads     int
		writes    int
		allocs    int
	}{
		{"no split: 2 reads, 1 write", nil, "bb", nil, "{d g | [a b bb c] [d e f] [g h i j]}", 2, 1, 0},
		{"rightmost leaf split: left, right, parent", nil, "k", nil, "{d g j | [a b c] [d e f] [g h i] [j k]}", 2, 3, 1},
		{"split with a right neighbour also rewrites it", []string{"bb"}, "ab", nil, "{bb d g | [a ab b] [bb c] [d e f] [g h i j]}", 3, 4, 1},
		{"duplicate entry: no writes", nil, "c", ErrDuplicateEntry, "{d g | [a b c] [d e f] [g h i j]}", 2, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := New(NewNodeStore(), 4)
			insertKeys(t, tr, letters(10)...)
			insertKeys(t, tr, tc.before...)
			tr.Store.ResetCounters()
			if err := tr.Insert(e(tc.key, 1, 1)); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Insert(%s) = %v, want %v", tc.key, err, tc.wantErr)
			}
			wantShape(t, tr, tc.wantShape)
			wantCounters(t, tr.Store, tc.reads, tc.writes, tc.allocs, 0)
		})
	}
}

func TestP5_10_Insert_DuplicateKeysAndPayloads(t *testing.T) {
	tr := New(NewNodeStore(), 4)
	var want oracle
	// 30 entries of the same key, inserted in scrambled TID order, plus neighbours.
	for i := range 30 {
		p := (i * 7) % 30
		it := Item{Key: []byte("dup"), TID: tid(p, 1), Payload: []byte(fmt.Sprintf("row%d", p))}
		if err := tr.Insert(it); err != nil {
			t.Fatalf("Insert: %v", err)
		}
		want.insert(it)
	}
	for _, k := range []string{"aaa", "zzz"} {
		it := Item{Key: []byte(k), TID: tid(0, 0)}
		if err := tr.Insert(it); err != nil {
			t.Fatal(err)
		}
		want.insert(it)
	}
	checkTree(t, tr)
	if tr.Height < 3 {
		t.Fatalf("height = %d, want >= 3 for 32 entries at MaxItems 4", tr.Height)
	}
	sameItems(t, leafWalk(tr), want)
	if got := tr.Search([]byte("dup")); !slices.Equal(got, want.tids([]byte("dup"))) {
		t.Fatalf("Search(dup) = %v", got)
	}
}

func TestP5_10_Insert_RandomAgainstOracle(t *testing.T) {
	for _, maxItems := range []int{3, 4, 5, 8} {
		t.Run(fmt.Sprintf("max%d", maxItems), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(uint64(maxItems), 5))
			tr := New(NewNodeStore(), maxItems)
			var want oracle
			for op := range 2000 {
				it := e(fmt.Sprintf("k%03d", rng.IntN(300)), rng.IntN(20), rng.IntN(5))
				err := tr.Insert(it)
				if fresh := want.insert(it); fresh != (err == nil) {
					t.Fatalf("op %d Insert(%s%v) = %v, oracle fresh=%v", op, it.Key, it.TID, err, fresh)
				}
				if err != nil && !errors.Is(err, ErrDuplicateEntry) {
					t.Fatalf("op %d: unexpected error %v", op, err)
				}
				if op%250 == 0 {
					checkTree(t, tr)
				}
			}
			checkTree(t, tr)
			sameItems(t, leafWalk(tr), want)
			for _, k := range []string{"k000", "k123", "k299", "nope"} {
				if got, w := tr.Search([]byte(k)), want.tids([]byte(k)); !slices.Equal(got, w) {
					t.Fatalf("Search(%s) = %v, want %v", k, got, w)
				}
			}
		})
	}
}

func TestP5_11_InsertUnique(t *testing.T) {
	tr := New(NewNodeStore(), 4)
	steps := []struct {
		item    Item
		wantErr error
	}{
		{e("alice", 1, 1), nil},
		{e("bob", 1, 2), nil},
		{e("alice", 2, 2), ErrDuplicateKey}, // same key, other TID
		{e("alice", 1, 1), ErrDuplicateKey}, // same entry: still a key violation
		{e("alicia", 2, 2), nil},            // shares a prefix only
		{e("ali", 2, 3), nil},               // a prefix of an existing key
		{e("carol", 3, 1), nil},
		{e("dave", 3, 2), nil},
		{e("bob", 9, 9), ErrDuplicateKey}, // after splits too
	}
	for i, s := range steps {
		before := tr.Store.Writes
		err := tr.InsertUnique(s.item)
		if !errors.Is(err, s.wantErr) {
			t.Fatalf("step %d InsertUnique(%s%v) = %v, want %v", i, s.item.Key, s.item.TID, err, s.wantErr)
		}
		if err != nil && tr.Store.Writes != before {
			t.Fatalf("step %d: rejected insert wrote %d node(s)", i, tr.Store.Writes-before)
		}
	}
	if got := keysOf(leafWalk(tr)); got != "ali alice alicia bob carol dave" {
		t.Fatalf("entries = %s", got)
	}
	checkTree(t, tr)
}

func TestP5_11_InsertUnique_DuplicatesAcrossLeaves(t *testing.T) {
	// A non-unique tree where "k" spans three leaves: any new "k" is rejected.
	tr := dupTree(t)
	if err := tr.InsertUnique(e("k", 9, 9)); !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("InsertUnique(k) = %v, want ErrDuplicateKey", err)
	}
	if err := tr.InsertUnique(e("l", 9, 9)); err != nil {
		t.Fatalf("InsertUnique(l) = %v, want nil", err)
	}
	if got := tr.Search([]byte("l")); !slices.Equal(got, []types.TID{tid(9, 9)}) {
		t.Fatalf("Search(l) = %v", got)
	}
}

func TestP5_8_SplitInternal_InsideTree(t *testing.T) {
	// Overfull root {d g j m p} over six leaves 0..5; the root is node 6.
	tr := build(t, 4, I(L("a", "b"), L("d", "e"), L("g", "h"), L("j", "k"), L("m", "n"), L("p", "q")))
	chain := leafIDs(tr)
	right, sep := tr.SplitInternal(tr.Store.Peek(tr.Root))
	if right.ID != 7 || string(sep.Key) != "j" {
		t.Fatalf("right %d sep %s, want 7 and j", right.ID, sep.Key)
	}
	l, r := tr.Store.Peek(6), tr.Store.Peek(7)
	if keysOf(l.Items) != "d g" || !slices.Equal(l.Children, []types.PageID{0, 1, 2}) {
		t.Fatalf("left = {%s} %v, want {d g} [0 1 2]", keysOf(l.Items), l.Children)
	}
	if keysOf(r.Items) != "m p" || !slices.Equal(r.Children, []types.PageID{3, 4, 5}) {
		t.Fatalf("right = {%s} %v, want {m p} [3 4 5]", keysOf(r.Items), r.Children)
	}
	if !slices.Equal(leafIDs(tr), chain) {
		t.Fatalf("leaf chain changed: %v -> %v", chain, leafIDs(tr))
	}
	wantCounters(t, tr.Store, 0, 2, 1, 0)
}

func TestP5_8_SplitInternal_NoAliasing(t *testing.T) {
	s := NewNodeStore()
	n := s.Alloc(false)
	n.Items = dg("b", "d", "f", "h", "j")
	n.Children = []types.PageID{10, 11, 12, 13, 14, 15}
	s.Put(n)
	tr := &Tree{Store: s, Root: n.ID, Height: 2, MaxItems: 4}
	right, _ := tr.SplitInternal(n)
	n.Items = append(n.Items, e("x", 1, 1), e("y", 1, 1), e("z", 1, 1))
	n.Children = append(n.Children, 97, 98, 99)
	if keysOf(right.Items) != "h j" || !slices.Equal(right.Children, []types.PageID{13, 14, 15}) {
		t.Fatalf("appending to left changed right: {%s} %v", keysOf(right.Items), right.Children)
	}
}
