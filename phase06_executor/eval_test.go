package executor

import (
	"errors"
	"math"
	"testing"

	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

// row used by most Eval tests: (10, NULL, 'bob', 2.5, true)
var evalRow = testkit.R(10, nil, "bob", 2.5, true)

func checkEval(t *testing.T, e Expr, row types.Row, want types.Value, wantErr error) {
	t.Helper()
	got, err := Eval(e, row)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Eval(%s) err = %v, want %v", e, err, wantErr)
	}
	if wantErr != nil {
		return
	}
	if got.Kind != want.Kind || !types.Identical(got, want) {
		t.Fatalf("Eval(%s) = %s (%s), want %s (%s)", e, got, got.Kind, want, want.Kind)
	}
}

func TestP6_1_Eval_Basics(t *testing.T) {
	a, b, name, f := ColRef{Index: 0}, ColRef{Index: 1}, ColRef{Index: 2}, ColRef{Index: 3}
	tests := []struct {
		name string
		e    Expr
		want types.Value
	}{
		{"constant", k(42), v(42)},
		{"column", a, v(10)},
		{"null column", b, v(nil)},
		{"10 > 5", Cmp(OpGT, a, k(5)), v(true)},
		{"10 < 5", Cmp(OpLT, a, k(5)), v(false)},
		{"10 <= 10", Cmp(OpLE, a, k(10)), v(true)},
		{"10 >= 11", Cmp(OpGE, a, k(11)), v(false)},
		{"10 = 10", Cmp(OpEQ, a, k(10)), v(true)},
		{"10 <> 10", Cmp(OpNE, a, k(10)), v(false)},
		{"int vs float: 10 > 2.5", Cmp(OpGT, a, f), v(true)},
		{"int4 vs int8: 10 = 10::int4", Cmp(OpEQ, a, k(int32(10))), v(true)},
		{"text compare", Cmp(OpLT, name, k("carol")), v(true)},
		{"bool compare: true > false", Cmp(OpGT, ColRef{Index: 4}, k(false)), v(true)},
		{"NULL = 3 is NULL", Cmp(OpEQ, b, k(3)), v(nil)},
		{"NULL = NULL is NULL", Cmp(OpEQ, b, tNull), v(nil)},
		{"NULL <> 3 is NULL", Cmp(OpNE, b, k(3)), v(nil)},
		{"IS NULL on NULL", IsNull{X: b}, v(true)},
		{"IS NULL on value", IsNull{X: a}, v(false)},
		{"IS NOT NULL on NULL", IsNotNull{X: b}, v(false)},
		{"IS NOT NULL on value", IsNotNull{X: name}, v(true)},
		{"NULL OR TRUE", Or{L: Cmp(OpEQ, b, k(3)), R: Cmp(OpGT, a, k(5))}, v(true)},
		{"NULL AND FALSE", And{L: Cmp(OpEQ, b, k(3)), R: Cmp(OpLT, a, k(5))}, v(false)},
		{"NOT NULL comparison", Not{X: Cmp(OpEQ, b, k(3))}, v(nil)},
		{"IS NULL of a NULL comparison", IsNull{X: Cmp(OpEQ, b, k(3))}, v(true)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { checkEval(t, tc.e, evalRow, tc.want, nil) })
	}
}

func TestP6_1_Eval_ThreeValuedLogic(t *testing.T) {
	vals := map[string]Const{"T": tTrue, "F": tFalse, "N": tNull}
	res := map[string]types.Value{"T": v(true), "F": v(false), "N": v(nil)}
	andTable := map[string]string{
		"TT": "T", "TF": "F", "TN": "N",
		"FT": "F", "FF": "F", "FN": "F",
		"NT": "N", "NF": "F", "NN": "N",
	}
	orTable := map[string]string{
		"TT": "T", "TF": "T", "TN": "T",
		"FT": "T", "FF": "F", "FN": "N",
		"NT": "T", "NF": "N", "NN": "N",
	}
	for pair, want := range andTable {
		l, r := vals[pair[:1]], vals[pair[1:]]
		t.Run(pair[:1]+" AND "+pair[1:], func(t *testing.T) { checkEval(t, And{L: l, R: r}, nil, res[want], nil) })
	}
	for pair, want := range orTable {
		l, r := vals[pair[:1]], vals[pair[1:]]
		t.Run(pair[:1]+" OR "+pair[1:], func(t *testing.T) { checkEval(t, Or{L: l, R: r}, nil, res[want], nil) })
	}
	for in, want := range map[string]string{"T": "F", "F": "T", "N": "N"} {
		t.Run("NOT "+in, func(t *testing.T) { checkEval(t, Not{X: vals[in]}, nil, res[want], nil) })
	}
}

func TestP6_1_Eval_ShortCircuit(t *testing.T) {
	boom := Cmp(OpEQ, Arith{Op: OpDiv, L: k(1), R: k(0)}, k(1)) // 1/0 = 1
	tests := []struct {
		name    string
		e       Expr
		want    types.Value
		wantErr error
	}{
		{"FALSE AND error skips the right side", And{L: tFalse, R: boom}, v(false), nil},
		{"TRUE OR error skips the right side", Or{L: tTrue, R: boom}, v(true), nil},
		{"TRUE AND error evaluates the right side", And{L: tTrue, R: boom}, types.Value{}, ErrDivisionByZero},
		{"NULL AND error evaluates the right side", And{L: tNull, R: boom}, types.Value{}, ErrDivisionByZero},
		{"FALSE OR error evaluates the right side", Or{L: tFalse, R: boom}, types.Value{}, ErrDivisionByZero},
		{"error on the left is returned", And{L: boom, R: tFalse}, types.Value{}, ErrDivisionByZero},
		{"right side decides AND: NULL AND FALSE", And{L: tNull, R: tFalse}, v(false), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { checkEval(t, tc.e, nil, tc.want, tc.wantErr) })
	}
}

func TestP6_1_Eval_Arithmetic(t *testing.T) {
	ar := func(op ArithOp, l, r any) Expr { return Arith{Op: op, L: k(l), R: k(r)} }
	tests := []struct {
		name    string
		e       Expr
		want    types.Value
		wantErr error
	}{
		{"int8 + int8", ar(OpAdd, 2, 3), v(5), nil},
		{"int4 + int4 stays int4", ar(OpAdd, int32(2), int32(3)), types.Int32(5), nil},
		{"int4 * int8 is int8", ar(OpMul, int32(6), 7), types.Int64(42), nil},
		{"int - int", ar(OpSub, 3, 10), v(-7), nil},
		{"int / int truncates", ar(OpDiv, 7, 2), v(3), nil},
		{"negative division truncates toward zero", ar(OpDiv, -7, 2), v(-3), nil},
		{"float wins: 1 + 0.5", ar(OpAdd, 1, 0.5), v(1.5), nil},
		{"float division", ar(OpDiv, 7, 2.0), v(3.5), nil},
		{"column arithmetic $0 * 10", Arith{Op: OpMul, L: ColRef{Index: 0}, R: k(10)}, v(100), nil},
		{"NULL + 1", Arith{Op: OpAdd, L: ColRef{Index: 1}, R: k(1)}, v(nil), nil},
		{"1 / NULL is NULL (not an error)", ar(OpDiv, 1, nil), v(nil), nil},
		{"int / 0", ar(OpDiv, 10, 0), types.Value{}, ErrDivisionByZero},
		{"float / 0.0", ar(OpDiv, 1.5, 0.0), types.Value{}, ErrDivisionByZero},
		{"int / 0.0", ar(OpDiv, 1, 0.0), types.Value{}, ErrDivisionByZero},
		{"int4 max + 1 overflows", ar(OpAdd, int32(math.MaxInt32), int32(1)), types.Value{}, ErrIntegerOverflow},
		{"int4 min - 1 overflows", ar(OpSub, int32(math.MinInt32), int32(1)), types.Value{}, ErrIntegerOverflow},
		{"int4 46341*46341 overflows", ar(OpMul, int32(46341), int32(46341)), types.Value{}, ErrIntegerOverflow},
		{"int4 46340*46340 fits", ar(OpMul, int32(46340), int32(46340)), types.Int32(2147395600), nil},
		{"int4 min / -1 overflows", ar(OpDiv, int32(math.MinInt32), int32(-1)), types.Value{}, ErrIntegerOverflow},
		{"int4 max + 1::int8 is fine (int8 result)", ar(OpAdd, int32(math.MaxInt32), 1), types.Int64(2147483648), nil},
		{"int8 max + 1 overflows", ar(OpAdd, math.MaxInt64, 1), types.Value{}, ErrIntegerOverflow},
		{"int8 min - 1 overflows", ar(OpSub, math.MinInt64, 1), types.Value{}, ErrIntegerOverflow},
		{"int8 min * -1 overflows", ar(OpMul, math.MinInt64, -1), types.Value{}, ErrIntegerOverflow},
		{"int8 -1 * min overflows", ar(OpMul, -1, math.MinInt64), types.Value{}, ErrIntegerOverflow},
		{"int8 big * big overflows", ar(OpMul, int64(1)<<32, int64(1)<<31), types.Value{}, ErrIntegerOverflow},
		{"int8 min / -1 overflows", ar(OpDiv, math.MinInt64, -1), types.Value{}, ErrIntegerOverflow},
		{"int8 max - (-1) overflows", ar(OpSub, math.MaxInt64, -1), types.Value{}, ErrIntegerOverflow},
		{"int8 min + max fits", ar(OpAdd, math.MinInt64, math.MaxInt64), v(-1), nil},
		{"text + int is a type error", ar(OpAdd, "a", 1), types.Value{}, ErrTypeMismatch},
		{"bool * int is a type error", ar(OpMul, true, 1), types.Value{}, ErrTypeMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { checkEval(t, tc.e, evalRow, tc.want, tc.wantErr) })
	}
}

func TestP6_1_Eval_In(t *testing.T) {
	a := ColRef{Index: 0}
	in := func(x Expr, items ...Expr) Expr { return In{X: x, List: items} }
	boom := Arith{Op: OpDiv, L: k(1), R: k(0)}
	tests := []struct {
		name    string
		e       Expr
		want    types.Value
		wantErr error
	}{
		{"match", in(a, k(1), k(10)), v(true), nil},
		{"no match", in(a, k(1), k(2)), v(false), nil},
		{"no match but a NULL item → NULL", in(a, k(1), tNull), v(nil), nil},
		{"match beats NULL item", in(a, tNull, k(10)), v(true), nil},
		{"NULL probe", in(ColRef{Index: 1}, k(1)), v(nil), nil},
		{"empty list", in(a), v(false), nil},
		{"float item matches int", in(a, k(10.0)), v(true), nil},
		{"text", in(ColRef{Index: 2}, k("al"), k("bob")), v(true), nil},
		{"stops at first match", in(a, k(10), boom), v(true), nil},
		{"error before a match", in(a, boom, k(10)), types.Value{}, ErrDivisionByZero},
		{"type mismatch", in(a, k("x")), types.Value{}, ErrTypeMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { checkEval(t, tc.e, evalRow, tc.want, tc.wantErr) })
	}
}

func TestP6_1_Eval_Errors(t *testing.T) {
	tests := []struct {
		name    string
		e       Expr
		wantErr error
	}{
		{"column past end", ColRef{Index: 5}, ErrBadColumn},
		{"negative column", ColRef{Index: -1}, ErrBadColumn},
		{"compare text with int", Cmp(OpEQ, ColRef{Index: 2}, k(1)), ErrTypeMismatch},
		{"compare bool with int", Cmp(OpEQ, k(true), k(1)), ErrTypeMismatch},
		{"AND on an int", And{L: k(1), R: tTrue}, ErrTypeMismatch},
		{"OR with an int on the right", Or{L: tFalse, R: k(1)}, ErrTypeMismatch},
		{"NOT on text", Not{X: k("x")}, ErrTypeMismatch},
		{"nil expression", nil, ErrBadExpr},
		{"error inside comparison", Cmp(OpLT, ColRef{Index: 9}, k(1)), ErrBadColumn},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { checkEval(t, tc.e, evalRow, types.Value{}, tc.wantErr) })
	}
}
