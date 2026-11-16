package btree

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP5_22_Validate_ValidTrees(t *testing.T) {
	tests := []struct {
		name string
		spec nd
	}{
		{"two-level tree", I(L("a", "b", "c"), L("d", "e", "f"), L("g", "h", "i", "j"))},
		{"empty root leaf", L()},
		{"root leaf with one entry", L("a")},
		{"root with a single separator", I(L("a", "b"), L("c", "d"))},
		{"three levels", I(I(L("a", "b"), L("c", "d"), L("e", "f")), I(L("m", "n"), L("p", "q"), L("s", "t")))},
		{"stale separator still bounds correctly", IS(dg("c"), L("a", "b"), L("d", "e"))},
		{"duplicates spanning leaves", I(LI(e("k", 1, 1), e("k", 1, 2)), LI(e("k", 2, 1), e("k", 2, 2)))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := build(t, 4, tc.spec).Validate(); err != nil {
				t.Fatalf("Validate = %v, want nil", err)
			}
		})
	}
}

func TestP5_22_Validate_Corruptions(t *testing.T) {
	edit := func(tr *Tree, id types.PageID, f func(n *Node)) {
		n := tr.Store.Peek(id)
		f(n)
		tr.Store.Put(n)
	}
	abc := func(t testing.TB) *Tree { return abcTree(t) } // leaves 0,1,2; root 3
	tests := []struct {
		name  string
		setup func(t testing.TB) *Tree
	}{
		{"unsorted leaf", func(t testing.TB) *Tree { return build(t, 4, I(L("a", "b", "c"), L("d", "f", "e"), L("g", "h"))) }},
		{"equal entries in a leaf", func(t testing.TB) *Tree {
			return build(t, 4, I(L("a", "b", "c"), LI(e("d", 1, 1), e("d", 1, 1)), L("g", "h")))
		}},
		{"entry below its low bound", func(t testing.TB) *Tree {
			return build(t, 4, IS(dg("e", "g"), L("a", "b", "c"), L("d", "e", "f"), L("g", "h")))
		}},
		{"entry equal to its high bound", func(t testing.TB) *Tree {
			return build(t, 4, IS(dg("d", "f"), L("a", "b", "c"), L("d", "e", "f"), L("g", "h")))
		}},
		{"overfull leaf", func(t testing.TB) *Tree { return build(t, 4, I(L("a", "b", "c", "d", "e"), L("f", "g"))) }},
		{"underfull non-root leaf", func(t testing.TB) *Tree { return build(t, 4, I(L("a", "b", "c"), L("d"))) }},
		{"overfull root", func(t testing.TB) *Tree { return build(t, 4, L("a", "b", "c", "d", "e")) }},
		{"separators out of order", func(t testing.TB) *Tree {
			return build(t, 4, IS(dg("g", "d"), L("a", "b"), L("d", "e"), L("g", "h")))
		}},
		{"height too large", func(t testing.TB) *Tree { tr := abc(t); tr.Height = 3; return tr }},
		{"height too small", func(t testing.TB) *Tree { tr := abc(t); tr.Height = 1; return tr }},
		{"height zero", func(t testing.TB) *Tree { tr := abc(t); tr.Height = 0; return tr }},
		{"unbalanced: leaves at different depths", func(t testing.TB) *Tree {
			return build(t, 4, I(L("a", "b"), I(L("c", "d"), L("e", "f"))))
		}},
		{"leaf Next skips a leaf", func(t testing.TB) *Tree {
			tr := abc(t)
			edit(tr, 0, func(n *Node) { n.Next = 2 })
			return tr
		}},
		{"leaf Prev broken", func(t testing.TB) *Tree {
			tr := abc(t)
			edit(tr, 1, func(n *Node) { n.Prev = types.InvalidPageID })
			return tr
		}},
		{"last leaf has a Next", func(t testing.TB) *Tree {
			tr := abc(t)
			edit(tr, 2, func(n *Node) { n.Next = 0 })
			return tr
		}},
		{"first leaf has a Prev", func(t testing.TB) *Tree {
			tr := abc(t)
			edit(tr, 0, func(n *Node) { n.Prev = 2 })
			return tr
		}},
		{"missing child", func(t testing.TB) *Tree { tr := abc(t); tr.Store.Free(2); return tr }},
		{"missing root", func(t testing.TB) *Tree { tr := abc(t); tr.Root = 99; return tr }},
		{"internal root without separators", func(t testing.TB) *Tree {
			tr := abc(t)
			edit(tr, 3, func(n *Node) { n.Items, n.Children = nil, n.Children[:1] })
			return tr
		}},
		{"children count does not match separators", func(t testing.TB) *Tree {
			tr := abc(t)
			edit(tr, 3, func(n *Node) { n.Items = n.Items[:1] })
			return tr
		}},
		{"internal node flagged as leaf", func(t testing.TB) *Tree {
			tr := abc(t)
			edit(tr, 3, func(n *Node) { n.Leaf = true })
			return tr
		}},
		{"leaf flagged as internal", func(t testing.TB) *Tree {
			tr := abc(t)
			edit(tr, 1, func(n *Node) { n.Leaf = false })
			return tr
		}},
		{"separator outside its parent's bounds", func(t testing.TB) *Tree {
			// {m | X{c | [a b] [c d]} Y{p | [m n] [p q]}}: X's separator becomes n >= m.
			tr := build(t, 4, I(I(L("a", "b"), L("c", "d")), I(L("m", "n"), L("p", "q"))))
			edit(tr, 2, func(n *Node) { n.Items[0] = e("n", 1, 1) })
			return tr
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := tc.setup(t)
			if err := tr.Validate(); !errors.Is(err, ErrInvalidTree) {
				t.Fatalf("Validate = %v, want ErrInvalidTree", err)
			}
		})
	}
}

// The capstone: a seeded workload of 5000 operations against a sorted-slice
// oracle, validating the tree as it goes.
func TestP5_22_Validate_RandomWorkload(t *testing.T) {
	for _, maxItems := range []int{3, 4, 6} {
		t.Run(fmt.Sprintf("max%d", maxItems), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(22, uint64(maxItems)))
			key := func() []byte { return []byte(fmt.Sprintf("u%03d", rng.IntN(400))) }

			// Start from a bulk-loaded index of 300 entries.
			var want oracle
			for i := range 300 {
				want.insert(Item{Key: []byte(fmt.Sprintf("u%03d", i)), TID: tid(i%17, i%5)})
			}
			tr, err := BulkLoad(NewNodeStore(), maxItems, want, 90)
			if err != nil {
				t.Fatal(err)
			}
			want = slices.Clone(want)

			for op := range 5000 {
				switch r := rng.IntN(20); {
				case r < 10: // insert
					it := Item{Key: key(), TID: tid(rng.IntN(30), rng.IntN(4)), Payload: []byte{byte(op)}}
					err := tr.Insert(it)
					if fresh := want.insert(it); fresh != (err == nil) {
						t.Fatalf("op %d Insert(%s%v) = %v, oracle fresh %v", op, it.Key, it.TID, err, fresh)
					}
				case r < 12: // unique insert
					it := Item{Key: key(), TID: tid(rng.IntN(30), rng.IntN(4))}
					err := tr.InsertUnique(it)
					if exists := len(want.tids(it.Key)) > 0; exists != errors.Is(err, ErrDuplicateKey) {
						t.Fatalf("op %d InsertUnique(%s) = %v, key exists %v", op, it.Key, err, exists)
					}
					if err == nil {
						want.insert(it)
					}
				default: // delete, mostly existing entries
					var it Item
					if len(want) > 0 && rng.IntN(4) > 0 {
						it = want[rng.IntN(len(want))]
					} else {
						it = Item{Key: key(), TID: tid(rng.IntN(30), rng.IntN(4))}
					}
					err := tr.Delete(it.Key, it.TID)
					if removed := want.remove(it); removed != (err == nil) {
						t.Fatalf("op %d Delete(%s%v) = %v, oracle had it %v", op, it.Key, it.TID, err, removed)
					}
				}
				if op%100 == 0 {
					if err := tr.Validate(); err != nil {
						t.Fatalf("op %d: %v", op, err)
					}
				}
			}
			if err := tr.Validate(); err != nil {
				t.Fatal(err)
			}
			sameItems(t, drain(tr.Scan(NoBound(), NoBound(), false)), want)
			rev := slices.Clone(want)
			slices.Reverse(rev)
			sameItems(t, drain(tr.Scan(NoBound(), NoBound(), true)), rev)
			for range 50 {
				k := key()
				if got, w := tr.Search(k), want.tids(k); !slices.Equal(got, w) {
					t.Fatalf("Search(%s) = %v, want %v", k, got, w)
				}
			}

			// Corrupt one leaf: Validate must notice.
			var n *Node
			for _, id := range leafIDs(tr) {
				if n = tr.Store.Peek(id); len(n.Items) >= 2 {
					break
				}
			}
			n.Items[0], n.Items[len(n.Items)-1] = n.Items[len(n.Items)-1], n.Items[0]
			tr.Store.Put(n)
			if err := tr.Validate(); !errors.Is(err, ErrInvalidTree) {
				t.Fatalf("Validate after swapping two entries = %v, want ErrInvalidTree", err)
			}
		})
	}
}
