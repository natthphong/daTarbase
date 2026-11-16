package executor

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP6_16_FoldConstants(t *testing.T) {
	x, y := ColRef{Index: 0}, ColRef{Index: 1}
	add := func(l, r Expr) Expr { return Arith{Op: OpAdd, L: l, R: r} }
	tests := []struct {
		name string
		in   Expr
		want string
	}{
		{"constant stays", k(5), "5"},
		{"column stays", x, "$0"},
		{"1 + 2 → 3", add(k(1), k(2)), "3"},
		{"(1 + 2) * $0 → (3 * $0)", Arith{Op: OpMul, L: add(k(1), k(2)), R: x}, "(3 * $0)"},
		{"$0 < 2 + 3 → ($0 < 5)", Cmp(OpLT, x, add(k(2), k(3))), "($0 < 5)"},
		{"x AND TRUE → x", And{L: Cmp(OpGT, x, k(5)), R: tTrue}, "($0 > 5)"},
		{"TRUE AND x → x", And{L: tTrue, R: Cmp(OpGT, x, k(5))}, "($0 > 5)"},
		{"x AND FALSE → FALSE", And{L: Cmp(OpGT, x, k(5)), R: tFalse}, "false"},
		{"x OR TRUE → TRUE", Or{L: Cmp(OpGT, x, k(5)), R: tTrue}, "true"},
		{"FALSE OR x → x", Or{L: tFalse, R: IsNull{X: x}}, "($0 IS NULL)"},
		{"($0 = 1) OR (2 > 1) → TRUE", Or{L: Cmp(OpEQ, x, k(1)), R: Cmp(OpGT, k(2), k(1))}, "true"},
		{"x AND (1 > 2) → FALSE", And{L: Cmp(OpEQ, x, k(1)), R: Cmp(OpGT, k(1), k(2))}, "false"},
		{"NULL = x → NULL", Cmp(OpEQ, tNull, x), "NULL"},
		{"x + NULL → NULL", add(x, tNull), "NULL"},
		{"NULL AND x is kept", And{L: tNull, R: Cmp(OpEQ, x, k(1))}, "(NULL AND ($0 = 1))"},
		{"NULL OR x is kept", Or{L: Cmp(OpEQ, x, k(1)), R: tNull}, "(($0 = 1) OR NULL)"},
		{"x AND (NULL = y) folds the NULL compare only", And{L: Cmp(OpEQ, x, k(1)), R: Cmp(OpEQ, tNull, y)}, "(($0 = 1) AND NULL)"},
		{"NOT NOT p → p", Not{X: Not{X: IsNull{X: x}}}, "($0 IS NULL)"},
		{"NOT constant", Not{X: Cmp(OpLT, k(1), k(2))}, "false"},
		{"NOT column comparison kept", Not{X: Cmp(OpLT, x, add(k(1), k(1)))}, "(NOT ($0 < 2))"},
		{"NULL IS NULL → TRUE", IsNull{X: tNull}, "true"},
		{"(1 + 1) IS NOT NULL → TRUE", IsNotNull{X: add(k(1), k(1))}, "true"},
		{"x IS NULL kept", IsNull{X: add(x, add(k(1), k(1)))}, "(($0 + 2) IS NULL)"},
		{"constant IN", In{X: k(3), List: []Expr{k(1), add(k(1), k(2))}}, "true"},
		{"constant IN with NULL and no match", In{X: k(3), List: []Expr{k(1), tNull}}, "NULL"},
		{"x IN (1+1, 3) folds items", In{X: x, List: []Expr{add(k(1), k(1)), k(3)}}, "($0 IN (2, 3))"},
		{"NULL IN (x) → NULL", In{X: tNull, List: []Expr{x}}, "NULL"},
		{"nested: (TRUE AND ($0 > 1+1)) OR FALSE", Or{L: And{L: tTrue, R: Cmp(OpGT, x, add(k(1), k(1)))}, R: tFalse}, "($0 > 2)"},
		{"FALSE AND x whatever x is", And{L: tFalse, R: Cmp(OpEQ, x, tNull)}, "false"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FoldConstants(tc.in)
			if err != nil {
				t.Fatalf("FoldConstants(%s) error %v", tc.in, err)
			}
			if got.String() != tc.want {
				t.Fatalf("FoldConstants(%s) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestP6_16_FoldConstants_ResultKinds(t *testing.T) {
	got, err := FoldConstants(Arith{Op: OpAdd, L: k(int32(1)), R: k(int32(2))})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := got.(Const)
	if !ok || c.Val.Kind != types.KindInt32 || c.Val.I != 3 {
		t.Fatalf("int4 1 + int4 2 folded to %#v, want Const int4 3", got)
	}
	got, _ = FoldConstants(Cmp(OpEQ, tNull, ColRef{Index: 0}))
	if c, ok := got.(Const); !ok || !c.Val.IsNull() {
		t.Fatalf("NULL = $0 folded to %#v, want Const NULL", got)
	}
}

func TestP6_16_FoldConstants_Errors(t *testing.T) {
	x := ColRef{Index: 0}
	tests := []struct {
		name    string
		in      Expr
		wantErr error
	}{
		{"division by zero at plan time", Cmp(OpLT, x, Arith{Op: OpDiv, L: k(10), R: k(0)}), ErrDivisionByZero},
		{"overflow at plan time", Arith{Op: OpAdd, L: k(int32(2147483647)), R: k(int32(1))}, ErrIntegerOverflow},
		{"error under an AND with FALSE still raised", And{L: tFalse, R: Cmp(OpEQ, Arith{Op: OpDiv, L: k(1), R: k(0)}, x)}, ErrDivisionByZero},
		{"type error", Arith{Op: OpAdd, L: k("a"), R: k(1)}, ErrTypeMismatch},
		{"error inside IN list", In{X: x, List: []Expr{Arith{Op: OpDiv, L: k(1), R: k(0)}}}, ErrDivisionByZero},
		{"nil", nil, ErrBadExpr},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := FoldConstants(tc.in); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestP6_16_FoldConstants_DoesNotMutate(t *testing.T) {
	list := []Expr{Arith{Op: OpAdd, L: k(1), R: k(1)}, k(3)}
	in := And{L: In{X: ColRef{Index: 0}, List: list}, R: tTrue}
	before := in.String()
	got, err := FoldConstants(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "($0 IN (2, 3))" {
		t.Fatalf("folded = %s", got)
	}
	if in.String() != before || list[0].String() != "(1 + 1)" {
		t.Fatalf("input was modified: %s (was %s)", in, before)
	}
	// Folding is idempotent.
	again, _ := FoldConstants(got)
	if again.String() != got.String() {
		t.Fatalf("folding twice = %s, want %s", again, got)
	}
}
