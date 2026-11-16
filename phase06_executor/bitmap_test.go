package executor

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

// bm parses the String() format: "0:1,2 1:L 3:4!" = page 0 slots {1,2},
// page 1 lossy, page 3 slot {4} with Recheck.
func bm(spec string) *TIDBitmap {
	b := NewTIDBitmap()
	for _, tok := range strings.Fields(spec) {
		ps, rest, _ := strings.Cut(tok, ":")
		p, _ := strconv.Atoi(ps)
		if rest == "L" {
			b.MarkLossy(types.PageID(p))
			continue
		}
		recheck := strings.HasSuffix(rest, "!")
		rest = strings.TrimSuffix(rest, "!")
		for _, ss := range strings.Split(rest, ",") {
			s, _ := strconv.Atoi(ss)
			b.Add(tid(p, s))
		}
		b.Pages[types.PageID(p)].Recheck = recheck
	}
	return b
}

// bitmapTable: 4 rows/page; keys 5..10 land on TIDs (0,1)(0,2)(1,0)(1,1)(1,2)(2,3).
func bitmapTable() *Table {
	return NewTable("t", 4, ints(
		100, 5, 6, 100,
		7, 8, 9, 100,
		100, 100, 100, 10,
	)...)
}

func TestP6_8_BitmapIndexScan(t *testing.T) {
	tests := []struct {
		name     string
		r        KeyRange
		maxExact int
		want     string
	}{
		{"unlimited", KeyRange{Lo: ge(5), Hi: ge(10)}, 0, "{0:[1 2] 1:[0 1 2] 2:[3]}"},
		{"budget 4 makes the fullest page lossy", KeyRange{Lo: ge(5), Hi: ge(10)}, 4, "{0:[1 2] 1:lossy 2:[3]}"},
		{"budget 1 makes two pages lossy", KeyRange{Lo: ge(5), Hi: ge(10)}, 1, "{0:lossy 1:lossy 2:[3]}"},
		{"budget exactly fits", KeyRange{Lo: ge(5), Hi: ge(10)}, 6, "{0:[1 2] 1:[0 1 2] 2:[3]}"},
		{"tie goes to the lowest page", KeyRange{Lo: ge(5), Hi: ge(8)}, 3, "{0:lossy 1:[0 1]}"},
		{"negative budget means unlimited", KeyRange{Lo: gt(8)}, -1, "{0:[0 3] 1:[2 3] 2:[0 1 2 3]}"},
		{"empty range", KeyRange{Lo: ge(11), Hi: ge(99)}, 0, "{}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tbl := bitmapTable()
			ix := NewIndex("t_idx", tbl, 0)
			got := BitmapIndexScan(ix, tc.r, tc.maxExact)
			if got == nil {
				t.Fatalf("BitmapIndexScan returned nil")
			}
			if got.String() != tc.want {
				t.Fatalf("bitmap = %s, want %s", got, tc.want)
			}
			if tbl.Fetches != 0 || tbl.PageReads != 0 {
				t.Fatalf("bitmap index scan touched the heap: Fetches=%d PageReads=%d", tbl.Fetches, tbl.PageReads)
			}
		})
	}
}

func TestP6_8_BitmapIndexScan_ReadBudget(t *testing.T) {
	tbl := seqTable("t", 10, 1, 2000)
	ix := NewIndex("t_idx", tbl, 0)
	got := BitmapIndexScan(ix, KeyRange{Lo: ge(1001), Hi: ge(1020)}, 0)
	if got.String() != "{100:[0 1 2 3 4 5 6 7 8 9] 101:[0 1 2 3 4 5 6 7 8 9]}" {
		t.Fatalf("bitmap = %s", got)
	}
	if max := maxIndexReads(2000, 20); ix.Reads > max {
		t.Fatalf("Index.Reads = %d, want ≤ %d", ix.Reads, max)
	}
}

func TestP6_9_BitmapCombine(t *testing.T) {
	tests := []struct {
		name string
		op   BitmapOp
		a, b string
		want string
	}{
		{"AND example", BitmapAnd, "0:1,2 1:L 3:4", "0:2,5 1:3 2:0 3:L", "{0:[2] 1:[3]! 3:[4]!}"},
		{"OR example", BitmapOr, "0:1,2 1:L 3:4", "0:2,5 1:3 2:0 3:L", "{0:[1 2 5] 1:lossy 2:[0] 3:lossy}"},
		{"AND lossy & lossy", BitmapAnd, "4:L", "4:L", "{4:lossy}"},
		{"AND drops empty intersections", BitmapAnd, "0:1 1:2", "0:2 1:2", "{1:[2]}"},
		{"AND keeps recheck", BitmapAnd, "0:1,2!", "0:2,3", "{0:[2]!}"},
		{"OR keeps recheck", BitmapOr, "0:1", "0:2!", "{0:[1 2]!}"},
		{"OR copies one-sided recheck", BitmapOr, "0:1!", "1:2", "{0:[1]! 1:[2]}"},
		{"AND with empty", BitmapAnd, "0:1 1:L", "", "{}"},
		{"OR with empty", BitmapOr, "", "0:1 1:L", "{0:[1] 1:lossy}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := bm(tc.a), bm(tc.b)
			aBefore, bBefore := a.String(), b.String()
			got := BitmapCombine(tc.op, a, b)
			if got.String() != tc.want {
				t.Fatalf("result = %s, want %s", got, tc.want)
			}
			if a.String() != aBefore || b.String() != bBefore {
				t.Fatalf("inputs modified: a %s→%s, b %s→%s", aBefore, a, bBefore, b)
			}
			for p, pb := range got.Pages {
				if pb.Lossy && (pb.Slots != nil || pb.Recheck) {
					t.Fatalf("lossy page %d must have Slots=nil and Recheck=false, got %+v", p, pb)
				}
			}
		})
	}
}

func TestP6_9_BitmapCombine_NoAliasing(t *testing.T) {
	a, b := bm("0:1 2:3"), bm("1:5 2:4")
	or := BitmapCombine(BitmapOr, a, b)
	or.Add(tid(0, 9))
	or.Add(tid(1, 9))
	or.Add(tid(2, 9))
	and := BitmapCombine(BitmapAnd, bm("0:1,2"), bm("0:1,2"))
	if and.String() != "{0:[1 2]}" {
		t.Fatalf("AND = %s, want {0:[1 2]}", and)
	}
	if a.String() != "{0:[1] 2:[3]}" || b.String() != "{1:[5] 2:[4]}" {
		t.Fatalf("adding to the OR result changed the inputs: a=%s b=%s", a, b)
	}
}

// WHERE a <= 3 AND b >= 30, answered with two bitmap index scans + BitmapAnd.
func TestP6_9_BitmapCombine_TwoIndexes(t *testing.T) {
	var rows []types.Row
	for i := 0; i < 8; i++ {
		rows = append(rows, testkit.R(i, i*10))
	}
	// pages of 3: (0,0)(1,10)(2,20) | (3,30)(4,40)(5,50) | (6,60)(7,70)
	tbl := NewTable("t", 3, rows...)
	ia, ib := NewIndex("t_a", tbl, 0), NewIndex("t_b", tbl, 1)
	ba := BitmapIndexScan(ia, KeyRange{Hi: ge(3)}, 0)
	bb := BitmapIndexScan(ib, KeyRange{Lo: ge(30)}, 0)
	if got := BitmapCombine(BitmapAnd, ba, bb).String(); got != "{1:[0]}" {
		t.Fatalf("AND = %s, want {1:[0]}", got)
	}
	if got := BitmapCombine(BitmapOr, ba, bb).String(); got != "{0:[0 1 2] 1:[0 1 2] 2:[0 1]}" {
		t.Fatalf("OR = %s", got)
	}
}

func TestP6_10_BitmapHeapScan(t *testing.T) {
	tbl := seqTable("t", 4, 0, 11)
	qual := In{X: ColRef{Index: 0, Name: "a"}, List: []Expr{k(1), k(3), k(5), k(9), k(99)}}
	s := &BitmapHeapScan{Table: tbl, Bitmap: bm("0:1,3 1:L 2:0!"), Qual: qual}
	testkit.AssertRows(t, collect(t, s), ints(1, 3, 5))
	if tbl.PageReads != 3 || tbl.Fetches != 0 {
		t.Fatalf("PageReads/Fetches = %d/%d, want 3/0", tbl.PageReads, tbl.Fetches)
	}
	if s.RowsRemovedByRecheck != 4 {
		t.Fatalf("RowsRemovedByRecheck = %d, want 4", s.RowsRemovedByRecheck)
	}
}

func TestP6_10_BitmapHeapScan_Cases(t *testing.T) {
	tests := []struct {
		name        string
		bitmap      string
		qual        Expr
		deleted     []types.TID
		want        []types.Row
		wantReads   int
		wantRemoved int
	}{
		{"exact pages are trusted without recheck", "0:0,1,2", tFalse, nil, ints(0, 1, 2), 1, 0},
		{"physical order regardless of insert order", "2:3,0 0:2", nil, nil, ints(2, 8, 11), 2, 0},
		{"lossy page with nil qual returns all live rows", "1:L", nil, []types.TID{tid(1, 1)}, ints(4, 6, 7), 1, 0},
		{"dead slots are skipped", "0:0,1,2,3", nil, []types.TID{tid(0, 0), tid(0, 3)}, ints(1, 2), 1, 0},
		{"pages past the end are not read", "2:1 7:0 9:L", nil, nil, ints(9), 1, 0},
		{"slots past the end of a page are skipped", "0:2,6", nil, nil, ints(2), 1, 0},
		{"recheck with NULL result removes the row", "0:1!", tNull, nil, nil, 1, 1},
		{"empty bitmap", "", nil, nil, nil, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tbl := seqTable("t", 4, 0, 11)
			for _, d := range tc.deleted {
				tbl.Delete(d)
			}
			s := &BitmapHeapScan{Table: tbl, Bitmap: bm(tc.bitmap), Qual: tc.qual}
			testkit.AssertRows(t, collect(t, s), tc.want)
			if tbl.PageReads != tc.wantReads {
				t.Fatalf("PageReads = %d, want %d", tbl.PageReads, tc.wantReads)
			}
			if s.RowsRemovedByRecheck != tc.wantRemoved {
				t.Fatalf("RowsRemovedByRecheck = %d, want %d", s.RowsRemovedByRecheck, tc.wantRemoved)
			}
		})
	}
}

// End to end: a lossy bitmap + recheck returns exactly what a filtered seq scan does,
// while reading only the pages that can match.
func TestP6_10_BitmapHeapScan_MatchesSeqScan(t *testing.T) {
	tbl := seqTable("t", 5, 1, 100)
	ix := NewIndex("t_idx", tbl, 0)
	cond := And{L: Cmp(OpGE, ColRef{Index: 0}, k(23)), R: Cmp(OpLE, ColRef{Index: 0}, k(41))}
	b := BitmapIndexScan(ix, KeyRange{Lo: ge(23), Hi: ge(41)}, 8) // forces lossy pages
	lossy := 0
	for _, pb := range b.Pages {
		if pb.Lossy {
			lossy++
		}
	}
	if lossy == 0 {
		t.Fatalf("expected some lossy pages in %s", b)
	}
	got := collect(t, &BitmapHeapScan{Table: tbl, Bitmap: b, Qual: cond})
	reads := tbl.PageReads
	want := collect(t, &SeqScan{Table: seqTable("t", 5, 1, 100), Qual: cond})
	testkit.AssertRows(t, got, want)
	if reads != 5 { // values 23..41 live on pages 4..8
		t.Fatalf("PageReads = %d, want 5", reads)
	}
}

func TestP6_10_BitmapHeapScan_QualError(t *testing.T) {
	tbl := seqTable("t", 4, 0, 3)
	_, err := exec.Collect(&BitmapHeapScan{Table: tbl, Bitmap: bm("0:L"), Qual: ColRef{Index: 0}})
	if !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("err = %v, want ErrTypeMismatch", err)
	}
}

// Duplicates on the same page collapse into one page entry; the bitmap index scan
// does not know about deleted heap tuples (that is the heap scan's job).
func TestP6_8_BitmapIndexScan_DuplicatesAndDead(t *testing.T) {
	tbl := NewTable("t", 4, ints(
		3, 1, 3, 2,
		3, 9, 9, 3,
	)...)
	ix := NewIndex("t_idx", tbl, 0)
	tbl.Delete(tid(1, 0))
	got := BitmapIndexScan(ix, KeyRange{Lo: ge(3), Hi: ge(3)}, 0)
	if got.String() != "{0:[0 2] 1:[0 3]}" {
		t.Fatalf("bitmap = %s, want {0:[0 2] 1:[0 3]}", got)
	}
	if got.ExactTuples() != 4 {
		t.Fatalf("ExactTuples = %d, want 4", got.ExactTuples())
	}
	got = BitmapIndexScan(ix, KeyRange{Lo: gt(3)}, 1)
	if got.String() != "{1:lossy}" {
		t.Fatalf("bitmap = %s, want {1:lossy}", got)
	}
	if tbl.Fetches != 0 || tbl.PageReads != 0 {
		t.Fatalf("heap touched: Fetches=%d PageReads=%d", tbl.Fetches, tbl.PageReads)
	}
}
