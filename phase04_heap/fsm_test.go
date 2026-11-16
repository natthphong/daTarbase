package heap

import (
	"errors"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP4_1_FSMUpdate(t *testing.T) {
	type upd struct {
		page types.PageID
		free int
	}
	tests := []struct {
		name string
		upds []upd
		want []uint8
	}{
		{"empty page rounds 8184 down to 255", []upd{{0, 8184}}, []uint8{255}},
		{"growing the map fills the gap with 0", []upd{{0, 8184}, {4, 100}}, []uint8{255, 0, 0, 0, 3}},
		{"doc example: categories can decrease", []upd{{0, 8184}, {4, 100}, {0, 8159}, {4, 31}}, []uint8{254, 0, 0, 0, 0}},
		{"31 bytes is category 0, 32 is category 1", []upd{{0, 31}, {1, 32}, {2, 63}, {3, 64}}, []uint8{0, 1, 1, 2}},
		{"8160 is exactly category 255", []upd{{0, 8159}, {1, 8160}}, []uint8{254, 255}},
		{"huge values cap at 255", []upd{{0, 1 << 20}}, []uint8{255}},
		{"negative free counts as 0", []upd{{2, -50}}, []uint8{0, 0, 0}},
		{"updating a low page never shrinks the map", []upd{{3, 64}, {1, 96}}, []uint8{0, 3, 0, 2}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFSM()
			for _, u := range tc.upds {
				f.Update(u.page, u.free)
			}
			if f.Len() != len(tc.want) {
				t.Fatalf("Len() = %d, want %d", f.Len(), len(tc.want))
			}
			for p, w := range tc.want {
				if got := f.Category(types.PageID(p)); got != w {
					t.Fatalf("Category(%d) = %d, want %d", p, got, w)
				}
			}
		})
	}
}

func TestP4_2_FSMSearch(t *testing.T) {
	f := NewFSM()
	f.Update(0, 50)  // cat 1
	f.Update(1, 100) // cat 3
	f.Update(2, 300) // cat 9
	tests := []struct {
		name string
		need int
		want types.PageID
	}{
		{"need 64 → cat 2 → page 1", 64, 1},
		{"need 96 → cat 3 → page 1", 96, 1},
		{"need 97 rounds up to cat 4 → page 2", 97, 2},
		{"need 288 → cat 9 → page 2", 288, 2},
		{"exact 300 is not provable (cat 10)", 300, types.InvalidPageID},
		{"need 0 treated as 1 → page 0", 0, 0},
		{"negative need treated as 1 → page 0", -5, 0},
		{"need 32 is cat 1 → lowest page 0", 32, 0},
		{"need 33 is cat 2 → page 1", 33, 1},
		{"need beyond 8160 can never be satisfied", 8161, types.InvalidPageID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.Search(tc.need); got != tc.want {
				t.Fatalf("Search(%d) = %d, want %d", tc.need, got, tc.want)
			}
		})
	}
}

func TestP4_2_FSMSearch_EdgeMaps(t *testing.T) {
	if got := NewFSM().Search(1); got != types.InvalidPageID {
		t.Fatalf("empty map: Search(1) = %d, want InvalidPageID", got)
	}
	f := NewFSM()
	f.Update(3, 8184) // pages 0..2 are 0
	if got := f.Search(8160); got != 3 {
		t.Fatalf("Search(8160) = %d, want 3", got)
	}
	if got := f.Search(1); got != 3 {
		t.Fatalf("Search(1) = %d, want 3 (pages 0..2 have category 0)", got)
	}
}

func TestP4_3_FSMTreeSet(t *testing.T) {
	tr := NewFSMTree(4)
	for _, s := range []struct {
		p types.PageID
		c uint8
	}{{0, 10}, {2, 200}, {3, 30}} {
		if err := tr.Set(s.p, s.c); err != nil {
			t.Fatalf("Set(%d,%d): %v", s.p, s.c, err)
		}
	}
	if want := []uint8{200, 10, 200, 10, 0, 200, 30}; !slices.Equal(tr.Nodes(), want) {
		t.Fatalf("nodes = %v, want %v", tr.Nodes(), want)
	}
	if err := tr.Set(2, 5); err != nil {
		t.Fatal(err)
	}
	if want := []uint8{30, 10, 30, 10, 0, 5, 30}; !slices.Equal(tr.Nodes(), want) {
		t.Fatalf("after decrease: nodes = %v, want %v", tr.Nodes(), want)
	}
	before := slices.Clone(tr.Nodes())
	if err := tr.Set(4, 1); !errors.Is(err, ErrPageOutOfRange) {
		t.Fatalf("Set(4,1) err = %v, want ErrPageOutOfRange", err)
	}
	if !slices.Equal(tr.Nodes(), before) {
		t.Fatalf("failed Set changed the tree: %v, want %v", tr.Nodes(), before)
	}
}

func TestP4_3_FSMTreeSet_SingleLeafAndRounding(t *testing.T) {
	one := NewFSMTree(1)
	if err := one.Set(0, 77); err != nil {
		t.Fatal(err)
	}
	if want := []uint8{77}; !slices.Equal(one.Nodes(), want) {
		t.Fatalf("single leaf nodes = %v, want %v", one.Nodes(), want)
	}
	five := NewFSMTree(5) // rounds up to 8 leaves, 15 nodes
	if five.Capacity() != 8 {
		t.Fatalf("Capacity = %d, want 8", five.Capacity())
	}
	if err := five.Set(7, 9); err != nil {
		t.Fatalf("Set(7) on capacity 8: %v", err)
	}
	if err := five.Set(5, 3); err != nil {
		t.Fatal(err)
	}
	// leaves at 7..14: page 5 → node 12, page 7 → node 14
	want := []uint8{9, 0, 9, 0, 0, 3, 9, 0, 0, 0, 0, 0, 3, 0, 9}
	if !slices.Equal(five.Nodes(), want) {
		t.Fatalf("nodes = %v, want %v", five.Nodes(), want)
	}
}

func TestP4_3_FSMTreeSet_RandomInvariant(t *testing.T) {
	rng := rand.New(rand.NewPCG(4, 3))
	tr := NewFSMTree(64)
	leaves := make([]uint8, 64)
	for i := 0; i < 2000; i++ {
		p, c := rng.IntN(64), uint8(rng.IntN(256))
		if err := tr.Set(types.PageID(p), c); err != nil {
			t.Fatal(err)
		}
		leaves[p] = c
	}
	n := tr.Nodes()
	for i := 0; i < 63; i++ {
		if n[i] != max(n[2*i+1], n[2*i+2]) {
			t.Fatalf("node %d = %d, want max of children %d,%d", i, n[i], n[2*i+1], n[2*i+2])
		}
	}
	if !slices.Equal(n[63:], leaves) {
		t.Fatalf("leaves = %v, want %v", n[63:], leaves)
	}
}

func TestP4_4_FSMTreeSearch(t *testing.T) {
	tr := NewFSMTree(8)
	tr.Set(1, 4)
	tr.Set(3, 9)
	tr.Set(6, 4)
	tests := []struct {
		name   string
		minCat uint8
		start  types.PageID
		want   types.PageID
	}{
		{"from 0, first page with ≥4", 4, 0, 1},
		{"from 2 skips page 1", 4, 2, 3},
		{"start exactly on a match", 9, 3, 3},
		{"from 4 finds page 6", 4, 4, 6},
		{"wraps: pages 5..7 too small", 5, 4, 3},
		{"wraps: from 7 back to page 1", 4, 7, 1},
		{"nothing qualifies", 10, 0, types.InvalidPageID},
		{"start out of range → 0", 4, 99, 1},
		{"minCat 0 returns start", 0, 5, 5},
		{"minCat 0 with bad start returns 0", 0, 8, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tr.Search(tc.minCat, tc.start); got != tc.want {
				t.Fatalf("Search(%d, %d) = %d, want %d", tc.minCat, tc.start, got, tc.want)
			}
		})
	}
}

func TestP4_4_FSMTreeSearch_SingleLeaf(t *testing.T) {
	tr := NewFSMTree(1)
	if got := tr.Search(1, 0); got != types.InvalidPageID {
		t.Fatalf("empty single leaf: got %d, want InvalidPageID", got)
	}
	tr.Set(0, 3)
	if got := tr.Search(3, 0); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestP4_4_FSMTreeSearch_MatchesLinear(t *testing.T) {
	rng := rand.New(rand.NewPCG(44, 1))
	const n = 128
	tr := NewFSMTree(n)
	leaves := make([]uint8, n)
	for i := range leaves {
		if rng.IntN(4) == 0 {
			leaves[i] = uint8(rng.IntN(200))
			tr.Set(types.PageID(i), leaves[i])
		}
	}
	for q := 0; q < 3000; q++ {
		minCat, start := uint8(rng.IntN(210)), rng.IntN(n)
		want := types.InvalidPageID
		for k := 0; k < n; k++ {
			if p := (start + k) % n; leaves[p] >= minCat {
				want = types.PageID(p)
				break
			}
		}
		if got := tr.Search(minCat, types.PageID(start)); got != want {
			t.Fatalf("Search(%d, %d) = %d, want %d", minCat, start, got, want)
		}
	}
}
