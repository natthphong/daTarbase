package btree

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
)

// nums returns entries "01".."n" with TIDs (1, i).
func nums(n int) []Item {
	out := make([]Item, n)
	for i := range out {
		out[i] = e(fmt.Sprintf("%02d", i+1), 1, i+1)
	}
	return out
}

func TestP5_19_BulkLoad_Shapes(t *testing.T) {
	tests := []struct {
		name       string
		maxItems   int
		n          int
		ff         int
		wantShape  string
		wantHeight int
		wantNodes  int
	}{
		{"10 entries, default fillfactor 90", 4, 10, 0,
			"{04 07 | [01 02 03] [04 05 06] [07 08 09 10]}", 2, 4},
		{"11 entries: remainder of 2 is enough", 4, 11, 90,
			"{04 07 10 | [01 02 03] [04 05 06] [07 08 09] [10 11]}", 2, 5},
		{"20 entries: two internal nodes and a root", 4, 20, 90,
			"{13 | {04 07 10 | [01 02 03] [04 05 06] [07 08 09] [10 11 12]} {16 19 | [13 14 15] [16 17 18] [19 20]}}", 3, 10},
		{"fillfactor 100, 5 entries: 4+1 is split evenly 3+2", 4, 5, 100,
			"{04 | [01 02 03] [04 05]}", 2, 3},
		{"fillfactor 50, 7 entries: 2+1 is merged", 4, 7, 50,
			"{03 05 | [01 02] [03 04] [05 06 07]}", 2, 4},
		{"fillfactor 100, 24 entries: internal 5+1 children split 3+3", 4, 24, 100,
			"{13 | {05 09 | [01 02 03 04] [05 06 07 08] [09 10 11 12]} {17 21 | [13 14 15 16] [17 18 19 20] [21 22 23 24]}}", 3, 9},
		{"4 entries: 3+1 merges into a single root leaf", 4, 4, 90, "[01 02 03 04]", 1, 1},
		{"3 entries fit one leaf", 4, 3, 90, "[01 02 03]", 1, 1},
		{"no entries: one empty leaf", 4, 0, 90, "[]", 1, 1},
		{"MaxItems 3: leaves of 2, min 1", 3, 5, 90, "{03 05 | [01 02] [03 04] [05]}", 2, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewNodeStore()
			items := nums(tc.n)
			tr, err := BulkLoad(s, tc.maxItems, items, tc.ff)
			if err != nil {
				t.Fatalf("BulkLoad: %v", err)
			}
			wantShape(t, tr, tc.wantShape)
			if tr.Height != tc.wantHeight || tr.MaxItems != tc.maxItems || tr.Store != s {
				t.Fatalf("height %d max %d, want %d %d (and the given store)", tr.Height, tr.MaxItems, tc.wantHeight, tc.maxItems)
			}
			if int(tr.Root) != tc.wantNodes-1 {
				t.Fatalf("root = %d, want %d (allocated last)", tr.Root, tc.wantNodes-1)
			}
			wantCounters(t, s, 0, tc.wantNodes, tc.wantNodes, 0)
			checkTree(t, tr)
			sameItems(t, leafWalk(tr), items)
		})
	}
}

func TestP5_19_BulkLoad_LevelOrderIDs(t *testing.T) {
	tr, err := BulkLoad(NewNodeStore(), 4, nums(20), 90)
	if err != nil {
		t.Fatal(err)
	}
	ids := leafIDs(tr)
	for i, id := range ids {
		if int(id) != i {
			t.Fatalf("leaf chain %v, want 0..6 (leaves allocated first, left to right)", ids)
		}
	}
	root := tr.Store.Peek(tr.Root)
	if root.Children[0] != 7 || root.Children[1] != 8 {
		t.Fatalf("root children %v, want [7 8]", root.Children)
	}
}

func TestP5_19_BulkLoad_Errors(t *testing.T) {
	tests := []struct {
		name    string
		items   []Item
		ff      int
		wantErr error
	}{
		{"descending keys", []Item{e("02", 1, 1), e("01", 1, 2)}, 90, ErrUnsorted},
		{"duplicate entry", []Item{e("01", 1, 1), e("01", 1, 1)}, 90, ErrUnsorted},
		{"same key, TIDs out of order", []Item{e("01", 2, 1), e("01", 1, 1)}, 90, ErrUnsorted},
		{"fillfactor below 50", nums(5), 40, ErrBadFillfactor},
		{"fillfactor above 100", nums(5), 101, ErrBadFillfactor},
		{"negative fillfactor", nums(5), -1, ErrBadFillfactor},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewNodeStore()
			tr, err := BulkLoad(s, 4, tc.items, tc.ff)
			if !errors.Is(err, tc.wantErr) || tr != nil {
				t.Fatalf("BulkLoad = (%v, %v), want (nil, %v)", tr, err, tc.wantErr)
			}
			if s.Allocs != 0 || s.Len() != 0 {
				t.Fatalf("store touched on error: %s, %d nodes", counters(s), s.Len())
			}
		})
	}
}

func TestP5_19_BulkLoad_DuplicateKeysAndPayloads(t *testing.T) {
	var items []Item
	for p := 1; p <= 12; p++ {
		items = append(items, Item{Key: b("k"), TID: tid(p, 1), Payload: b(fmt.Sprintf("row%d", p))})
	}
	tr, err := BulkLoad(NewNodeStore(), 4, items, 90)
	if err != nil {
		t.Fatal(err)
	}
	checkTree(t, tr) // also checks separators carry no payload
	sameItems(t, leafWalk(tr), items)
	if got := tr.Search(b("k")); len(got) != 12 {
		t.Fatalf("Search(k) = %d TIDs, want 12", len(got))
	}
}

func TestP5_19_BulkLoad_FillfactorLeavesRoomForInserts(t *testing.T) {
	for _, tc := range []struct {
		ff         int
		wantAllocs int
	}{{90, 0}, {100, 1}} {
		tr, err := BulkLoad(NewNodeStore(), 4, nums(12), tc.ff)
		if err != nil {
			t.Fatal(err)
		}
		tr.Store.ResetCounters()
		if err := tr.Insert(e("05a", 1, 1)); err != nil {
			t.Fatal(err)
		}
		if tr.Store.Allocs != tc.wantAllocs {
			t.Fatalf("fillfactor %d: insert allocated %d node(s), want %d", tc.ff, tr.Store.Allocs, tc.wantAllocs)
		}
		checkTree(t, tr)
	}
}

func TestP5_19_BulkLoad_CheaperThanInsertsAndStillWorks(t *testing.T) {
	items := nums(99)
	bulk, err := BulkLoad(NewNodeStore(), 4, items, 90)
	if err != nil {
		t.Fatal(err)
	}
	if bulk.Store.Writes != bulk.Store.Len() {
		t.Fatalf("bulk load wrote %d times for %d nodes, want each node once", bulk.Store.Writes, bulk.Store.Len())
	}
	one := New(NewNodeStore(), 4)
	for _, it := range items {
		if err := one.Insert(it); err != nil {
			t.Fatal(err)
		}
	}
	if bulk.Store.Writes >= one.Store.Writes {
		t.Fatalf("bulk load writes %d, one-by-one inserts %d: bulk should be cheaper", bulk.Store.Writes, one.Store.Writes)
	}

	// The loaded tree is a normal tree: random inserts and deletes keep it valid.
	rng := rand.New(rand.NewPCG(19, 19))
	want := oracle(append([]Item(nil), items...))
	for op := range 600 {
		if rng.IntN(2) == 0 {
			it := e(fmt.Sprintf("%02d", rng.IntN(120)), 2, rng.IntN(4))
			if (bulk.Insert(it) == nil) != want.insert(it) {
				t.Fatalf("op %d: Insert disagrees with the oracle", op)
			}
		} else if len(want) > 0 {
			it := want[rng.IntN(len(want))]
			if err := bulk.Delete(it.Key, it.TID); err != nil {
				t.Fatalf("op %d: Delete: %v", op, err)
			}
			want.remove(it)
		}
	}
	checkTree(t, bulk)
	sameItems(t, leafWalk(bulk), want)
}
