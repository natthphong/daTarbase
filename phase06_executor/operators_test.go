package executor

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP6_2_Filter(t *testing.T) {
	a := ColRef{Index: 0}
	tests := []struct {
		name        string
		rows        []types.Row
		pred        Expr
		want        []types.Row
		wantRemoved int
	}{
		{"keeps TRUE, drops FALSE and NULL", testkit.Rows([]any{1}, []any{nil}, []any{7}, []any{3}), Cmp(OpGT, a, k(2)), ints(7, 3), 2},
		{"<> does not return NULL rows", testkit.Rows([]any{5}, []any{nil}, []any{6}), Cmp(OpNE, a, k(5)), ints(6), 2},
		{"IS NULL finds them", testkit.Rows([]any{5}, []any{nil}, []any{6}), IsNull{X: a}, testkit.Rows([]any{nil}), 2},
		{"empty input", nil, tTrue, nil, 0},
		{"constant NULL predicate keeps nothing", ints(1, 2), tNull, nil, 2},
		{"constant TRUE keeps everything", ints(1, 2), tTrue, ints(1, 2), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := exec.Values(tc.rows...)
			f := &Filter{Child: src, Pred: tc.pred}
			testkit.AssertRows(t, collect(t, f), tc.want)
			if f.RowsRemoved != tc.wantRemoved {
				t.Fatalf("RowsRemoved = %d, want %d", f.RowsRemoved, tc.wantRemoved)
			}
			if src.Opens != 1 || src.Closes != 1 {
				t.Fatalf("child opens/closes = %d/%d, want 1/1", src.Opens, src.Closes)
			}
		})
	}
}

func TestP6_2_Filter_Streams(t *testing.T) {
	src := exec.Values(ints(1, 9, 2, 8, 3)...)
	f := &Filter{Child: src, Pred: Cmp(OpGT, ColRef{Index: 0}, k(5))}
	if err := f.Open(); err != nil {
		t.Fatal(err)
	}
	r, ok, err := f.Next()
	if err != nil || !ok || !types.Identical(r[0], v(9)) {
		t.Fatalf("first Next = %v %v %v, want (9) true nil", r, ok, err)
	}
	if src.Pulled != 2 {
		t.Fatalf("child pulled %d rows for the first output, want 2", src.Pulled)
	}
}

func TestP6_2_Filter_Errors(t *testing.T) {
	t.Run("non-boolean predicate", func(t *testing.T) {
		f := &Filter{Child: exec.Values(ints(1)...), Pred: ColRef{Index: 0}}
		if _, err := exec.Collect(f); !errors.Is(err, ErrTypeMismatch) {
			t.Fatalf("err = %v, want ErrTypeMismatch", err)
		}
	})
	t.Run("eval error", func(t *testing.T) {
		f := &Filter{Child: exec.Values(ints(0)...), Pred: Cmp(OpEQ, Arith{Op: OpDiv, L: k(1), R: ColRef{Index: 0}}, k(1))}
		if _, err := exec.Collect(f); !errors.Is(err, ErrDivisionByZero) {
			t.Fatalf("err = %v, want ErrDivisionByZero", err)
		}
	})
	t.Run("child error", func(t *testing.T) {
		f := &Filter{Child: &failingIter{rows: ints(1)}, Pred: tTrue}
		rows, err := exec.Collect(f)
		if !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want errBoom", err)
		}
		testkit.AssertRows(t, rows, ints(1))
	})
}

func TestP6_3_Project(t *testing.T) {
	src := exec.Values(testkit.R(2, "a"), testkit.R(5, nil))
	p := &Project{Child: src, Exprs: []Expr{
		ColRef{Index: 1},
		Arith{Op: OpMul, L: ColRef{Index: 0}, R: k(10)},
		IsNull{X: ColRef{Index: 1}},
	}}
	testkit.AssertRows(t, collect(t, p), testkit.Rows(
		[]any{"a", 20, false},
		[]any{nil, 50, true},
	))
	if src.Pulled != 2 || src.Closes != 1 {
		t.Fatalf("child pulled/closed = %d/%d, want 2/1", src.Pulled, src.Closes)
	}
}

func TestP6_3_Project_NewRow(t *testing.T) {
	in := testkit.R(1, 2)
	p := &Project{Child: exec.Values(in), Exprs: []Expr{ColRef{Index: 0}, ColRef{Index: 1}}}
	out := collect(t, p)
	out[0][0] = v(99)
	if !types.Identical(in[0], v(1)) {
		t.Fatalf("modifying the output changed the child's row: %v", in)
	}
}

func TestP6_3_Project_EdgeCases(t *testing.T) {
	t.Run("no expressions gives empty rows", func(t *testing.T) {
		got := collect(t, &Project{Child: exec.Values(ints(1, 2)...), Exprs: nil})
		if len(got) != 2 || len(got[0]) != 0 || len(got[1]) != 0 {
			t.Fatalf("got %v, want two empty rows", got)
		}
	})
	t.Run("constant column", func(t *testing.T) {
		got := collect(t, &Project{Child: exec.Values(ints(1)...), Exprs: []Expr{k("x"), ColRef{Index: 0}}})
		testkit.AssertRows(t, got, testkit.Rows([]any{"x", 1}))
	})
	t.Run("eval error", func(t *testing.T) {
		_, err := exec.Collect(&Project{Child: exec.Values(ints(1)...), Exprs: []Expr{ColRef{Index: 3}}})
		if !errors.Is(err, ErrBadColumn) {
			t.Fatalf("err = %v, want ErrBadColumn", err)
		}
	})
}

func TestP6_4_Limit(t *testing.T) {
	oneToTen := ints(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	tests := []struct {
		name       string
		count      int
		offset     int
		want       []types.Row
		wantPulled int
	}{
		{"offset 2 limit 3", 3, 2, ints(3, 4, 5), 5},
		{"limit 3", 3, 0, ints(1, 2, 3), 3},
		{"limit 0 never pulls", 0, 0, nil, 0},
		{"limit 0 with offset never pulls", 0, 4, nil, 0},
		{"limit all with offset", -1, 7, ints(8, 9, 10), 10},
		{"limit larger than input", 20, 0, oneToTen, 10},
		{"offset past the end", 5, 15, nil, 10},
		{"limit exactly the input", 10, 0, oneToTen, 10},
		{"offset 9 limit 1", 1, 9, ints(10), 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := exec.Values(oneToTen...)
			l := &Limit{Child: src, Count: tc.count, Offset: tc.offset}
			testkit.AssertRows(t, collect(t, l), tc.want)
			if src.Pulled != tc.wantPulled {
				t.Fatalf("child pulled %d rows, want %d", src.Pulled, tc.wantPulled)
			}
		})
	}
}

func TestP6_4_Limit_StopsPulling(t *testing.T) {
	src := exec.Values(ints(1, 2, 3, 4, 5)...)
	l := &Limit{Child: src, Count: 2}
	if err := l.Open(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		l.Next()
	}
	if src.Pulled != 2 {
		t.Fatalf("after 5 Next calls the child was pulled %d times, want 2", src.Pulled)
	}
	l.Close()
	// Re-open resets the counters.
	src2 := exec.Values(ints(1, 2, 3)...)
	l.Child = src2
	testkit.AssertRows(t, collect(t, l), ints(1, 2))
}

func TestP6_4_Limit_ChildError(t *testing.T) {
	l := &Limit{Child: &failingIter{rows: ints(1)}, Count: 5, Offset: 0}
	if _, err := exec.Collect(l); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want errBoom", err)
	}
	l = &Limit{Child: &failingIter{rows: ints(1)}, Count: 5, Offset: 3}
	if _, err := exec.Collect(l); !errors.Is(err, errBoom) {
		t.Fatalf("error during OFFSET: err = %v, want errBoom", err)
	}
}
