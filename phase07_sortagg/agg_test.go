package sortagg

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

// (dept, name, salary, bonus)
func empRows() []types.Row {
	return testkit.Rows(
		[]any{"eng", "ann", 100, 10},
		[]any{"ops", "bob", 80, nil},
		[]any{"eng", "cat", 120, nil},
		[]any{nil, "dan", 50, 5},
		[]any{"ops", "eve", 90, 20},
		[]any{nil, "fay", 70, nil},
		[]any{"eng", "gus", 110, 10},
	)
}

// empRows sorted by dept ASC NULLS LAST (stable).
func empRowsSorted() []types.Row {
	r := empRows()
	return []types.Row{r[0], r[2], r[6], r[1], r[4], r[3], r[5]}
}

// COUNT(*), COUNT(bonus), SUM(salary), AVG(salary), MIN(name), MAX(bonus)
var empAggs = []AggSpec{
	{Func: AggCountStar},
	{Func: AggCount, Col: 3},
	{Func: AggSum, Col: 2},
	{Func: AggAvg, Col: 2},
	{Func: AggMin, Col: 1},
	{Func: AggMax, Col: 3},
}

var empByDept = testkit.Rows(
	[]any{"eng", 3, 2, 330, 110.0, "ann", 10},
	[]any{"ops", 2, 1, 170, 85.0, "bob", 20},
	[]any{nil, 2, 1, 120, 60.0, "dan", 5},
)

func advanceAll(spec AggSpec, rows []types.Row) *Accum {
	a := NewAccum(spec)
	for _, r := range rows {
		a.Advance(r)
	}
	return a
}

func TestP7_8_Advance(t *testing.T) {
	col := func(xs ...any) []types.Row {
		out := make([]types.Row, len(xs))
		for i, x := range xs {
			out[i] = testkit.R(x)
		}
		return out
	}
	tests := []struct {
		name string
		spec AggSpec
		in   []types.Row
		want Accum // Spec is ignored
	}{
		{"count(*) counts null rows", AggSpec{Func: AggCountStar}, col(nil, nil, 1),
			Accum{Count: 3, Min: types.Null(), Max: types.Null()}},
		{"count(col) skips nulls", AggSpec{Func: AggCount}, col(nil, 4, nil, 5),
			Accum{Count: 2, Min: types.Null(), Max: types.Null()}},
		{"sum mixes int and float", AggSpec{Func: AggSum}, col(1, nil, 2.5, 3),
			Accum{Count: 3, IntSum: 4, FloatSum: 2.5, SawFloat: true, Min: types.Null(), Max: types.Null()}},
		{"avg ints only", AggSpec{Func: AggAvg}, col(1, 2, int32(3)),
			Accum{Count: 3, IntSum: 6, Min: types.Null(), Max: types.Null()}},
		{"min text", AggSpec{Func: AggMin}, col("b", "a", "c", nil),
			Accum{Count: 3, Min: types.Text("a"), Max: types.Null()}},
		{"max text", AggSpec{Func: AggMax}, col("b", "a", "c", nil),
			Accum{Count: 3, Min: types.Null(), Max: types.Text("c")}},
		{"max of only nulls stays null", AggSpec{Func: AggMax}, col(nil, nil),
			Accum{Count: 0, Min: types.Null(), Max: types.Null()}},
		{"count distinct: 1 and 1.0 are one value", AggSpec{Func: AggCount, Distinct: true}, col(1, 1, nil, 2, 1.0),
			Accum{Count: 2, Min: types.Null(), Max: types.Null()}},
		{"sum distinct", AggSpec{Func: AggSum, Distinct: true}, col(2, 2, 3),
			Accum{Count: 2, IntSum: 5, Min: types.Null(), Max: types.Null()}},
		{"count(*) ignores distinct and col", AggSpec{Func: AggCountStar, Col: 0, Distinct: true}, col(1, 1),
			Accum{Count: 2, Min: types.Null(), Max: types.Null()}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := advanceAll(tc.spec, tc.in)
			w := tc.want
			if a.Count != w.Count || a.IntSum != w.IntSum || a.FloatSum != w.FloatSum || a.SawFloat != w.SawFloat {
				t.Fatalf("Count/IntSum/FloatSum/SawFloat = %d/%d/%g/%v, want %d/%d/%g/%v",
					a.Count, a.IntSum, a.FloatSum, a.SawFloat, w.Count, w.IntSum, w.FloatSum, w.SawFloat)
			}
			if !types.Identical(a.Min, w.Min) || !types.Identical(a.Max, w.Max) {
				t.Fatalf("Min/Max = %v/%v, want %v/%v", a.Min, a.Max, w.Min, w.Max)
			}
		})
	}
}

func TestP7_8_Advance_DistinctSeen(t *testing.T) {
	a := NewAccum(AggSpec{Func: AggCount, Distinct: true})
	if a.Seen != nil {
		t.Fatal("NewAccum should leave Seen nil")
	}
	for _, x := range []any{1, 1, nil, 2, 1.0} {
		a.Advance(testkit.R(x))
	}
	if len(a.Seen) != 2 || !a.Seen[types.Int64(1).HashKey()] || !a.Seen[types.Int64(2).HashKey()] {
		t.Fatalf("Seen = %v, want the keys of 1 and 2", a.Seen)
	}
	b := advanceAll(AggSpec{Func: AggSum, Col: 0}, intRows(1, 1))
	if b.Seen != nil {
		t.Fatalf("non-distinct aggregate filled Seen: %v", b.Seen)
	}
}

func TestP7_8_Advance_MinMaxTiesKeepFirst(t *testing.T) {
	// 2 (int8) and 2.0 (float8) compare equal: the first one stays.
	a := advanceAll(AggSpec{Func: AggMin}, testkit.Rows([]any{2}, []any{2.0}))
	if a.Min.Kind != types.KindInt64 {
		t.Fatalf("Min kind = %s, want int8 (first of equal values)", a.Min.Kind)
	}
}

func TestP7_9_Finalize(t *testing.T) {
	tests := []struct {
		name     string
		spec     AggSpec
		in       []any
		want     types.Value
		wantKind types.Kind
	}{
		{"sum ints is int8", AggSpec{Func: AggSum}, []any{2, 3}, types.Int64(5), types.KindInt64},
		{"sum with float is float8", AggSpec{Func: AggSum}, []any{1, 2.5}, types.Float64(3.5), types.KindFloat64},
		{"sum of nothing is NULL", AggSpec{Func: AggSum}, nil, types.Null(), types.KindNull},
		{"sum of only nulls is NULL", AggSpec{Func: AggSum}, []any{nil}, types.Null(), types.KindNull},
		{"avg of ints is float8", AggSpec{Func: AggAvg}, []any{1, 2}, types.Float64(1.5), types.KindFloat64},
		{"avg exact int result still float8", AggSpec{Func: AggAvg}, []any{100, 120, 110}, types.Float64(110), types.KindFloat64},
		{"avg mixed", AggSpec{Func: AggAvg}, []any{1, nil, 2.5, 3}, types.Float64(6.5 / 3), types.KindFloat64},
		{"avg of nothing is NULL", AggSpec{Func: AggAvg}, nil, types.Null(), types.KindNull},
		{"count of nulls is 0", AggSpec{Func: AggCount}, []any{nil, nil}, types.Int64(0), types.KindInt64},
		{"count(*) of nothing is 0", AggSpec{Func: AggCountStar}, nil, types.Int64(0), types.KindInt64},
		{"count(*)", AggSpec{Func: AggCountStar}, []any{nil, 1}, types.Int64(2), types.KindInt64},
		{"sum distinct", AggSpec{Func: AggSum, Distinct: true}, []any{2, 2, 3}, types.Int64(5), types.KindInt64},
		{"avg distinct", AggSpec{Func: AggAvg, Distinct: true}, []any{1, 1, 1, 4}, types.Float64(2.5), types.KindFloat64},
		{"min", AggSpec{Func: AggMin}, []any{5, nil, -3, 7}, types.Int64(-3), types.KindInt64},
		{"max", AggSpec{Func: AggMax}, []any{"pear", "apple"}, types.Text("pear"), types.KindText},
		{"min of nothing is NULL", AggSpec{Func: AggMin}, nil, types.Null(), types.KindNull},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var rows []types.Row
			for _, x := range tc.in {
				rows = append(rows, testkit.R(x))
			}
			a := advanceAll(tc.spec, rows)
			before := *a
			got := a.Finalize()
			if got.Kind != tc.wantKind || !types.Identical(got, tc.want) {
				t.Fatalf("Finalize = %v (%s), want %v (%s)", got, got.Kind, tc.want, tc.wantKind)
			}
			if a.Count != before.Count || a.IntSum != before.IntSum || a.FloatSum != before.FloatSum {
				t.Fatal("Finalize modified the state")
			}
		})
	}
}

func TestP7_10_HashAggregate(t *testing.T) {
	tests := []struct {
		name      string
		rows      []types.Row
		groupCols []int
		aggs      []AggSpec
		having    func(types.Row) bool
		want      []types.Row
	}{
		{"group by dept, nulls form one group", empRows(), []int{0}, empAggs, nil, empByDept},
		{"having count(*) > 2", empRows(), []int{0}, empAggs,
			func(r types.Row) bool { return r[1].I > 2 }, empByDept[:1]},
		{"having sum(salary) < 200", empRows(), []int{0}, empAggs,
			func(r types.Row) bool { return r[3].I < 200 }, empByDept[1:]},
		{"no group by: distinct aggregates", empRows(), nil,
			[]AggSpec{{Func: AggCountStar}, {Func: AggCount, Col: 3, Distinct: true}, {Func: AggSum, Col: 3, Distinct: true}, {Func: AggSum, Col: 3}},
			nil, testkit.Rows([]any{7, 3, 35, 45})},
		{"group by dept: count(distinct bonus)", empRows(), []int{0},
			[]AggSpec{{Func: AggCount, Col: 3, Distinct: true}, {Func: AggCount, Col: 3}},
			nil, testkit.Rows([]any{"eng", 1, 2}, []any{"ops", 1, 1}, []any{nil, 1, 1})},
		{"group by two columns with nulls",
			testkit.Rows([]any{1, nil, 5}, []any{1, "x", 1}, []any{1, nil, 2}, []any{nil, nil, 3}, []any{nil, nil, 4}),
			[]int{0, 1}, []AggSpec{{Func: AggSum, Col: 2}},
			nil, testkit.Rows([]any{1, nil, 7}, []any{1, "x", 1}, []any{nil, nil, 7})},
		{"group cols in given order", empRows()[:2], []int{1, 0}, []AggSpec{{Func: AggCountStar}},
			nil, testkit.Rows([]any{"ann", "eng", 1}, []any{"bob", "ops", 1})},
		{"empty input, no group by → one row", nil, nil,
			[]AggSpec{{Func: AggCountStar}, {Func: AggSum, Col: 0}, {Func: AggMax, Col: 0}},
			nil, testkit.Rows([]any{0, nil, nil})},
		{"empty input, no group by, having rejects", nil, nil, []AggSpec{{Func: AggCountStar}},
			func(r types.Row) bool { return r[0].I > 0 }, nil},
		{"empty input with group by → no rows", nil, []int{0}, []AggSpec{{Func: AggCountStar}}, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			child := exec.Values(tc.rows...)
			got, err := HashAggregate(child, tc.groupCols, tc.aggs, tc.having)
			if err != nil {
				t.Fatal(err)
			}
			testkit.AssertRows(t, got, tc.want)
			if child.Opens != 1 || child.Closes != 1 || child.Pulled != len(tc.rows) {
				t.Fatalf("child opens/closes/pulled = %d/%d/%d, want 1/1/%d", child.Opens, child.Closes, child.Pulled, len(tc.rows))
			}
		})
	}
}

func TestP7_10_HashAggregate_ResultKinds(t *testing.T) {
	got, err := HashAggregate(exec.Values(empRows()...), []int{0}, empAggs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	r := got[0]
	wantKinds := []types.Kind{types.KindText, types.KindInt64, types.KindInt64, types.KindInt64, types.KindFloat64, types.KindText, types.KindInt64}
	for i, k := range wantKinds {
		if r[i].Kind != k {
			t.Fatalf("column %d kind = %s, want %s (row %v)", i, r[i].Kind, k, r)
		}
	}
}

func TestP7_10_HashAggregate_ChildError(t *testing.T) {
	child := &failingIter{rows: intRows(1)}
	if _, err := HashAggregate(child, []int{0}, []AggSpec{{Func: AggCountStar}}, nil); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want %v", err, errBoom)
	}
	if child.Closes != 1 {
		t.Fatalf("child closed %d times, want 1", child.Closes)
	}
}

func TestP7_11_GroupAggregate(t *testing.T) {
	tests := []struct {
		name      string
		rows      []types.Row
		groupCols []int
		aggs      []AggSpec
		having    func(types.Row) bool
		want      []types.Row
	}{
		{"sorted by dept", empRowsSorted(), []int{0}, empAggs, nil, empByDept},
		{"having keeps middle group", empRowsSorted(), []int{0}, empAggs,
			func(r types.Row) bool { return r[0].Kind == types.KindText && r[0].S == "ops" }, empByDept[1:2]},
		{"having rejects first and last", empRowsSorted(), []int{0}, empAggs,
			func(r types.Row) bool { return r[3].I == 170 }, empByDept[1:2]},
		{"having rejects all", empRowsSorted(), []int{0}, empAggs,
			func(types.Row) bool { return false }, nil},
		{"no group by → whole input", empRowsSorted(), nil,
			[]AggSpec{{Func: AggCountStar}, {Func: AggCount, Col: 3, Distinct: true}, {Func: AggSum, Col: 3}},
			nil, testkit.Rows([]any{7, 3, 45})},
		{"no group by, empty input → one row", nil, nil,
			[]AggSpec{{Func: AggCountStar}, {Func: AggAvg, Col: 0}}, nil, testkit.Rows([]any{0, nil})},
		{"group by, empty input → no rows", nil, []int{0}, []AggSpec{{Func: AggCountStar}}, nil, nil},
		{"single group", intRows(4, 4, 4), []int{0}, []AggSpec{{Func: AggSum, Col: 0}}, nil, testkit.Rows([]any{4, 12})},
		{"unsorted input: same key twice gives two groups", intRows(1, 2, 1), []int{0},
			[]AggSpec{{Func: AggCountStar}}, nil, testkit.Rows([]any{1, 1}, []any{2, 1}, []any{1, 1})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			op := NewGroupAggregate(exec.Values(tc.rows...), tc.groupCols, tc.aggs...)
			op.Having = tc.having
			got, err := exec.Collect(op)
			if err != nil {
				t.Fatal(err)
			}
			testkit.AssertRows(t, got, tc.want)
		})
	}
}

func TestP7_11_GroupAggregate_Streaming(t *testing.T) {
	child := exec.Values(empRowsSorted()...)
	op := NewGroupAggregate(child, []int{0}, empAggs...)
	if err := op.Open(); err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	wantPulled := []int{4, 6, 7} // each group + one look-ahead row (none after the last)
	for i, want := range wantPulled {
		got := pullN(t, op, 1)
		testkit.AssertRows(t, got, empByDept[i:i+1])
		if child.Pulled != want {
			t.Fatalf("after Next #%d child pulled %d rows, want %d", i+1, child.Pulled, want)
		}
	}
	for i := 0; i < 2; i++ {
		if _, ok, err := op.Next(); ok || err != nil {
			t.Fatalf("Next after end: ok=%v err=%v, want false/nil", ok, err)
		}
	}
}

func TestP7_11_GroupAggregate_ChildError(t *testing.T) {
	op := NewGroupAggregate(&failingIter{rows: intRows(1, 1)}, []int{0}, AggSpec{Func: AggCountStar})
	if err := op.Open(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := op.Next(); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want %v", err, errBoom)
	}
}
