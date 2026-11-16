package sortagg

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

// (dept, name, salary)
func winRows() []types.Row {
	return testkit.Rows(
		[]any{"eng", "ann", 100},
		[]any{"eng", "bob", 120},
		[]any{"eng", "cat", 100},
		[]any{"ops", "dan", 80},
		[]any{"ops", "eve", nil},
		[]any{"eng", "fay", 90},
		[]any{"ops", "gus", 80},
	)
}

var allWin = []WinFunc{{Kind: WinRowNumber}, {Kind: WinRank}, {Kind: WinDenseRank}, {Kind: WinSum, Col: 2}}

func TestP7_15_Window(t *testing.T) {
	tests := []struct {
		name string
		rows []types.Row
		spec WindowSpec
		fns  []WinFunc
		want []types.Row
	}{
		{"partition by dept order by salary desc", winRows(),
			WindowSpec{PartitionBy: []int{0}, OrderBy: []SortKey{Desc(2)}}, allWin,
			testkit.Rows(
				[]any{"eng", "bob", 120, 1, 1, 1, 120},
				[]any{"eng", "ann", 100, 2, 2, 2, 320},
				[]any{"eng", "cat", 100, 3, 2, 2, 320},
				[]any{"eng", "fay", 90, 4, 4, 3, 410},
				[]any{"ops", "eve", nil, 1, 1, 1, nil},
				[]any{"ops", "dan", 80, 2, 2, 2, 160},
				[]any{"ops", "gus", 80, 3, 2, 2, 160},
			)},
		{"partition only: whole partition is one peer group", winRows(),
			WindowSpec{PartitionBy: []int{0}}, allWin,
			testkit.Rows(
				[]any{"eng", "ann", 100, 1, 1, 1, 410},
				[]any{"eng", "bob", 120, 2, 1, 1, 410},
				[]any{"eng", "cat", 100, 3, 1, 1, 410},
				[]any{"eng", "fay", 90, 4, 1, 1, 410},
				[]any{"ops", "dan", 80, 1, 1, 1, 160},
				[]any{"ops", "eve", nil, 2, 1, 1, 160},
				[]any{"ops", "gus", 80, 3, 1, 1, 160},
			)},
		{"order only: one partition, nulls last", winRows(),
			WindowSpec{OrderBy: []SortKey{Asc(2)}}, allWin,
			testkit.Rows(
				[]any{"ops", "dan", 80, 1, 1, 1, 160},
				[]any{"ops", "gus", 80, 2, 1, 1, 160},
				[]any{"eng", "fay", 90, 3, 3, 2, 250},
				[]any{"eng", "ann", 100, 4, 4, 3, 450},
				[]any{"eng", "cat", 100, 5, 4, 3, 450},
				[]any{"eng", "bob", 120, 6, 6, 4, 570},
				[]any{"ops", "eve", nil, 7, 7, 5, 570},
			)},
		{"functions in given order, subset", winRows(),
			WindowSpec{PartitionBy: []int{0}, OrderBy: []SortKey{Asc(1)}}, []WinFunc{{Kind: WinSum, Col: 2}, {Kind: WinRowNumber}},
			testkit.Rows(
				[]any{"eng", "ann", 100, 100, 1},
				[]any{"eng", "bob", 120, 220, 2},
				[]any{"eng", "cat", 100, 320, 3},
				[]any{"eng", "fay", 90, 410, 4},
				[]any{"ops", "dan", 80, 80, 1},
				[]any{"ops", "eve", nil, 80, 2},
				[]any{"ops", "gus", 80, 160, 3},
			)},
		{"null partition key forms its own partition, last",
			testkit.Rows([]any{nil, 1}, []any{"a", 2}, []any{nil, 3}),
			WindowSpec{PartitionBy: []int{0}, OrderBy: []SortKey{Desc(1)}}, []WinFunc{{Kind: WinRowNumber}},
			testkit.Rows([]any{"a", 2, 1}, []any{nil, 3, 1}, []any{nil, 1, 2})},
		{"empty input", nil, WindowSpec{PartitionBy: []int{0}}, allWin, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			child := exec.Values(tc.rows...)
			got, err := Window(child, tc.spec, tc.fns)
			if err != nil {
				t.Fatal(err)
			}
			testkit.AssertRows(t, got, tc.want)
			if child.Opens != 1 || child.Closes != 1 {
				t.Fatalf("child opens/closes = %d/%d, want 1/1", child.Opens, child.Closes)
			}
		})
	}
}

func TestP7_15_Window_SumKinds(t *testing.T) {
	rows := testkit.Rows([]any{1, nil}, []any{2, 1}, []any{3, 2.5}, []any{4, 1})
	got, err := Window(exec.Values(rows...), WindowSpec{OrderBy: []SortKey{Asc(0)}}, []WinFunc{{Kind: WinSum, Col: 1}})
	if err != nil {
		t.Fatal(err)
	}
	want := []types.Value{types.Null(), types.Int64(1), types.Float64(3.5), types.Float64(4.5)}
	for i, w := range want {
		v := got[i][2]
		if v.Kind != w.Kind || !types.Identical(v, w) {
			t.Fatalf("row %d sum = %v (%s), want %v (%s)", i, v, v.Kind, w, w.Kind)
		}
	}
}

func TestP7_15_Window_RankKinds(t *testing.T) {
	got, err := Window(exec.Values(winRows()...), WindowSpec{PartitionBy: []int{0}, OrderBy: []SortKey{Desc(2)}}, allWin[:3])
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		for c := 3; c < 6; c++ {
			if r[c].Kind != types.KindInt64 {
				t.Fatalf("column %d kind = %s, want int8 (row %v)", c, r[c].Kind, r)
			}
		}
	}
}

func TestP7_15_Window_DoesNotModifyInput(t *testing.T) {
	// Give every input row spare capacity so an in-place append would be caught.
	var rows []types.Row
	for _, r := range winRows() {
		rows = append(rows, append(make(types.Row, 0, 16), r...))
	}
	got, err := Window(exec.Values(rows...), WindowSpec{PartitionBy: []int{0}, OrderBy: []SortKey{Asc(2)}}, allWin)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		for c := range r {
			r[c] = types.Text("clobbered")
		}
	}
	testkit.AssertRows(t, rows, winRows())
	for i, r := range rows {
		if len(r) != 3 {
			t.Fatalf("input row %d has %d columns, want 3", i, len(r))
		}
	}
}

func TestP7_15_Window_ChildError(t *testing.T) {
	child := &failingIter{rows: intRows(1)}
	if _, err := Window(child, WindowSpec{}, []WinFunc{{Kind: WinRowNumber}}); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want %v", err, errBoom)
	}
	if child.Closes != 1 {
		t.Fatalf("child closed %d times, want 1", child.Closes)
	}
}
