package executor

import (
	"errors"
	"math/bits"
	"testing"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

func tid(p, s int) types.TID { return types.TID{Page: types.PageID(p), Slot: types.SlotID(s)} }

func TestP6_5_SeqScan(t *testing.T) {
	tbl := seqTable("t", 3, 1, 7) // [1 2 3] [4 5 6] [7]
	tbl.Delete(tid(0, 1))         // row 2
	s := &SeqScan{Table: tbl, Qual: Cmp(OpGE, ColRef{Index: 0}, k(3))}
	testkit.AssertRows(t, collect(t, s), ints(3, 4, 5, 6, 7))
	if tbl.PageReads != 3 {
		t.Fatalf("PageReads = %d, want 3", tbl.PageReads)
	}
	if tbl.Fetches != 0 {
		t.Fatalf("Fetches = %d, want 0 (a seq scan never fetches by TID)", tbl.Fetches)
	}
	if s.RowsRemoved != 1 {
		t.Fatalf("RowsRemoved = %d, want 1", s.RowsRemoved)
	}
}

func TestP6_5_SeqScan_Cases(t *testing.T) {
	tests := []struct {
		name      string
		table     func() *Table
		qual      Expr
		want      []types.Row
		wantReads int
	}{
		{"no qual returns everything in physical order", func() *Table {
			return NewTable("t", 2, ints(5, 3, 9, 1, 4)...)
		}, nil, ints(5, 3, 9, 1, 4), 3},
		{"empty table", func() *Table { return NewTable("t", 4) }, nil, nil, 0},
		{"a fully deleted page is still read", func() *Table {
			tb := seqTable("t", 2, 1, 6)
			tb.Delete(tid(1, 0))
			tb.Delete(tid(1, 1))
			return tb
		}, nil, ints(1, 2, 5, 6), 3},
		{"NULL qual results are rejected", func() *Table {
			return NewTable("t", 2, testkit.R(1), testkit.R(nil), testkit.R(3))
		}, Cmp(OpNE, ColRef{Index: 0}, k(1)), ints(3), 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tbl := tc.table()
			testkit.AssertRows(t, collect(t, &SeqScan{Table: tbl, Qual: tc.qual}), tc.want)
			if tbl.PageReads != tc.wantReads {
				t.Fatalf("PageReads = %d, want %d", tbl.PageReads, tc.wantReads)
			}
		})
	}
}

// LIMIT on top of a seq scan must stop the scan from reading further pages.
func TestP6_5_SeqScan_Lazy(t *testing.T) {
	tbl := seqTable("t", 3, 1, 9)
	got := collect(t, &Limit{Child: &SeqScan{Table: tbl}, Count: 1})
	testkit.AssertRows(t, got, ints(1))
	if tbl.PageReads != 1 {
		t.Fatalf("LIMIT 1: PageReads = %d, want 1", tbl.PageReads)
	}
	tbl.PageReads = 0
	got = collect(t, &Limit{Child: &SeqScan{Table: tbl}, Count: 4})
	testkit.AssertRows(t, got, ints(1, 2, 3, 4))
	if tbl.PageReads != 2 {
		t.Fatalf("LIMIT 4: PageReads = %d, want 2", tbl.PageReads)
	}

	// After exhaustion further Next calls read nothing.
	tbl.PageReads = 0
	s := &SeqScan{Table: tbl}
	collect(t, s)
	s.Open()
	for i := 0; i < 12; i++ {
		s.Next()
	}
	if tbl.PageReads != 6 {
		t.Fatalf("two full scans + extra Next calls: PageReads = %d, want 6", tbl.PageReads)
	}
}

func TestP6_5_SeqScan_QualError(t *testing.T) {
	tbl := seqTable("t", 3, 1, 3)
	if _, err := exec.Collect(&SeqScan{Table: tbl, Qual: ColRef{Index: 0}}); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("err = %v, want ErrTypeMismatch", err)
	}
}

// maxIndexReads is the read budget for an index range scan: a binary search
// plus the k matches plus the entry that ends the range (and one spare).
func maxIndexReads(n, k int) int { return bits.Len(uint(n)) + k + 2 }

func TestP6_6_IndexScan(t *testing.T) {
	tbl := seqTable("t", 10, 1, 1000)
	ix := NewIndex("t_id_idx", tbl, 0)
	s := &IndexScan{Index: ix, Table: tbl, Range: KeyRange{Lo: ge(500), Hi: &Bound{Val: v(503), Inclusive: false}}}
	testkit.AssertRows(t, collect(t, s), ints(500, 501, 502))
	if tbl.Fetches != 3 || tbl.PageReads != 0 {
		t.Fatalf("Fetches/PageReads = %d/%d, want 3/0", tbl.Fetches, tbl.PageReads)
	}
	if max := maxIndexReads(1000, 3); ix.Reads > max {
		t.Fatalf("Index.Reads = %d, want ≤ %d (binary search, not a full index walk)", ix.Reads, max)
	}
}

func TestP6_6_IndexScan_Ranges(t *testing.T) {
	// value column 0, label column 1; laid out in NON-key order with duplicates.
	rows := testkit.Rows(
		[]any{30, "a"}, []any{10, "b"}, []any{20, "c"},
		[]any{10, "d"}, []any{nil, "e"}, []any{40, "f"},
		[]any{20, "g"}, []any{50, "h"},
	)
	tests := []struct {
		name        string
		r           KeyRange
		qual        Expr
		wantLabels  []string
		wantFetches int
	}{
		{"equality with duplicates, TID order", KeyRange{Lo: ge(20), Hi: ge(20)}, nil, []string{"c", "g"}, 2},
		{"exclusive lower", KeyRange{Lo: gt(20), Hi: ge(40)}, nil, []string{"a", "f"}, 2},
		{"unbounded below", KeyRange{Hi: &Bound{Val: v(20), Inclusive: false}}, nil, []string{"b", "d"}, 2},
		{"unbounded above", KeyRange{Lo: ge(40)}, nil, []string{"f", "h"}, 2},
		{"full index skips NULL keys", KeyRange{}, nil, []string{"b", "d", "c", "g", "a", "f", "h"}, 7},
		{"empty range", KeyRange{Lo: ge(21), Hi: ge(29)}, nil, nil, 0},
		{"inverted range", KeyRange{Lo: ge(40), Hi: ge(10)}, nil, nil, 0},
		{"above everything", KeyRange{Lo: ge(60)}, nil, nil, 0},
		{"filter after fetch", KeyRange{Lo: ge(10), Hi: ge(20)}, Cmp(OpGT, ColRef{Index: 1}, k("c")), []string{"d", "g"}, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tbl := NewTable("t", 3, rows...)
			ix := NewIndex("t_v_idx", tbl, 0)
			got := collect(t, &IndexScan{Index: ix, Table: tbl, Range: tc.r, Qual: tc.qual})
			var labels []string
			for _, r := range got {
				labels = append(labels, r[1].S)
			}
			if len(labels) != len(tc.wantLabels) {
				t.Fatalf("labels = %v, want %v", labels, tc.wantLabels)
			}
			for i := range labels {
				if labels[i] != tc.wantLabels[i] {
					t.Fatalf("labels = %v, want %v", labels, tc.wantLabels)
				}
			}
			if tbl.Fetches != tc.wantFetches {
				t.Fatalf("Fetches = %d, want %d", tbl.Fetches, tc.wantFetches)
			}
		})
	}
}

func TestP6_6_IndexScan_DeadTuplesAndDone(t *testing.T) {
	tbl := seqTable("t", 4, 1, 12)
	ix := NewIndex("t_idx", tbl, 0)
	tbl.Delete(tid(1, 1)) // value 6, index still points at it
	s := &IndexScan{Index: ix, Table: tbl, Range: KeyRange{Lo: ge(5), Hi: ge(8)}}
	testkit.AssertRows(t, collect(t, s), ints(5, 7, 8))
	if tbl.Fetches != 4 {
		t.Fatalf("Fetches = %d, want 4 (the dead tuple is fetched too, then skipped)", tbl.Fetches)
	}
	// Once done, more Next calls touch nothing.
	s.Open()
	for {
		_, ok, _ := s.Next()
		if !ok {
			break
		}
	}
	reads, fetches := ix.Reads, tbl.Fetches
	for i := 0; i < 3; i++ {
		if _, ok, _ := s.Next(); ok {
			t.Fatalf("Next after end returned a row")
		}
	}
	if ix.Reads != reads || tbl.Fetches != fetches {
		t.Fatalf("Next after end did I/O: reads %d→%d fetches %d→%d", reads, ix.Reads, fetches, tbl.Fetches)
	}
}

func TestP6_6_IndexScan_LargeRangeBudget(t *testing.T) {
	tbl := seqTable("t", 50, 1, 5000)
	ix := NewIndex("t_idx", tbl, 0)
	got := collect(t, &IndexScan{Index: ix, Table: tbl, Range: KeyRange{Lo: gt(4900)}})
	if len(got) != 100 {
		t.Fatalf("got %d rows, want 100", len(got))
	}
	if max := maxIndexReads(5000, 100); ix.Reads > max {
		t.Fatalf("Index.Reads = %d, want ≤ %d", ix.Reads, max)
	}
}

func TestP6_7_IndexOnlyScan(t *testing.T) {
	tbl := seqTable("t", 4, 1, 12) // pages: [1..4] [5..8] [9..12]
	ix := NewIndex("t_idx", tbl, 0)
	tbl.Delete(tid(1, 2)) // id 7
	vm := NewVisibilityMap(0, 2)
	s := &IndexOnlyScan{Index: ix, Table: tbl, VM: vm, Range: KeyRange{Lo: ge(3), Hi: ge(10)}}
	testkit.AssertRows(t, collect(t, s), ints(3, 4, 5, 6, 8, 9, 10))
	if s.HeapFetches != 4 || tbl.Fetches != 4 {
		t.Fatalf("HeapFetches/Table.Fetches = %d/%d, want 4/4", s.HeapFetches, tbl.Fetches)
	}
	if vm.Lookups != 8 {
		t.Fatalf("VM lookups = %d, want 8 (one per in-range entry)", vm.Lookups)
	}
	if tbl.PageReads != 0 {
		t.Fatalf("PageReads = %d, want 0", tbl.PageReads)
	}
}

func TestP6_7_IndexOnlyScan_VisibilityCases(t *testing.T) {
	tests := []struct {
		name        string
		allVisible  []types.PageID
		r           KeyRange
		want        []types.Row
		wantFetches int
	}{
		{"freshly VACUUMed: no heap fetches", []types.PageID{0, 1, 2}, KeyRange{}, ints(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12), 0},
		{"never VACUUMed: every entry fetched", nil, KeyRange{Lo: ge(2), Hi: ge(4)}, ints(2, 3, 4), 3},
		{"one dirty page", []types.PageID{0, 2}, KeyRange{Lo: ge(8), Hi: ge(9)}, ints(8, 9), 1},
		{"empty range", nil, KeyRange{Lo: ge(20)}, nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tbl := seqTable("t", 4, 1, 12)
			ix := NewIndex("t_idx", tbl, 0)
			s := &IndexOnlyScan{Index: ix, Table: tbl, VM: NewVisibilityMap(tc.allVisible...), Range: tc.r}
			testkit.AssertRows(t, collect(t, s), tc.want)
			if s.HeapFetches != tc.wantFetches || tbl.Fetches != tc.wantFetches {
				t.Fatalf("HeapFetches/Table.Fetches = %d/%d, want %d", s.HeapFetches, tbl.Fetches, tc.wantFetches)
			}
		})
	}
}

// The output comes from the index, not the heap: a two-column table still
// yields one-column rows.
func TestP6_7_IndexOnlyScan_OutputFromIndex(t *testing.T) {
	tbl := NewTable("t", 2, testkit.R(20, "x"), testkit.R(10, "y"), testkit.R(30, "z"))
	ix := NewIndex("t_idx", tbl, 0)
	s := &IndexOnlyScan{Index: ix, Table: tbl, VM: NewVisibilityMap(1), Range: KeyRange{}}
	testkit.AssertRows(t, collect(t, s), ints(10, 20, 30))
	if s.HeapFetches != 2 {
		t.Fatalf("HeapFetches = %d, want 2 (page 0 is not all-visible)", s.HeapFetches)
	}
	ix.Reads = 0
	big := seqTable("big", 100, 1, 10000)
	bix := NewIndex("big_idx", big, 0)
	s = &IndexOnlyScan{Index: bix, Table: big, VM: NewVisibilityMap(), Range: KeyRange{Lo: ge(5000), Hi: ge(5004)}}
	testkit.AssertRows(t, collect(t, s), ints(5000, 5001, 5002, 5003, 5004))
	if max := maxIndexReads(10000, 5); bix.Reads > max {
		t.Fatalf("Index.Reads = %d, want ≤ %d", bix.Reads, max)
	}
}
