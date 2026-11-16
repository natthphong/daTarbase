package executor

import (
	"math"

	"github.com/natthphong/daTarbase/pkg/types"
)

// Problem 6.1 — Eval [Medium]
//
// Evaluate an expression tree against one row using SQL three-valued logic (TRUE / FALSE / NULL).
//
// In SQL a boolean has three values. NULL means "unknown", and it spreads:
// an unknown compared with anything is unknown. AND / OR are the exception —
// when one side already decides the answer, the unknown side does not matter:
//
//	AND   | T  F  N        OR    | T  F  N        NOT
//	------+---------       ------+---------       ----------
//	  T   | T  F  N          T   | T  T  T         T → F
//	  F   | F  F  F          F   | T  F  N         F → T
//	  N   | N  F  N          N   | T  N  N         N → N
//
// Rules (by node type):
//  1. Const → its Val. ColRef → row[Index]; Index < 0 or ≥ len(row) → ErrBadColumn.
//  2. Compare: evaluate L then R. If either is NULL → NULL. The two values must be
//     comparable: both numeric (int4/int8/float8 mix freely), both text, or both
//     bool — otherwise ErrTypeMismatch. Use types.Compare; the result is a bool.
//  3. And: evaluate L first. If L is FALSE, return FALSE WITHOUT evaluating R
//     (so FALSE AND (1/0 = 1) is FALSE, no error). Otherwise evaluate R and apply
//     the truth table. Or: same, short-circuit when L is TRUE.
//     Not: truth table. Operands of And/Or/Not that are neither bool nor NULL → ErrTypeMismatch.
//  4. IsNull / IsNotNull: never NULL; work on any type.
//  5. Arith: evaluate L then R (errors from L win). Either NULL → NULL. Both must be
//     numeric, else ErrTypeMismatch. Result type:
//     - any float8 operand → float8 (Go float arithmetic);
//     - else any int8 operand → int8;
//     - else (int4 op int4) → int4.
//     Integer division truncates toward zero (7 / 2 = 3, -7 / 2 = -3).
//     Division by zero (int 0 or float 0.0) → ErrDivisionByZero.
//     An integer result that does not fit its type → ErrIntegerOverflow
//     (int4: outside [-2147483648, 2147483647]; int8: outside int64, including
//     MinInt64 / -1). Float overflow is NOT an error (it yields ±Inf).
//  6. In: evaluate X; NULL → NULL. Then evaluate the list items left to right;
//     an item that equals X (comparable as in rule 2, else ErrTypeMismatch) → TRUE
//     immediately (later items are not evaluated). No match: NULL if any item was
//     NULL, else FALSE. An empty list → FALSE.
//  7. A nil Expr or an unknown node type → ErrBadExpr.
//
// Example (row = (10, NULL, 'bob')):
//
//	$0 > 5                 → true
//	$1 = 3                 → NULL
//	($1 = 3) OR ($0 > 5)   → true      (NULL OR TRUE)
//	($1 = 3) AND ($0 < 5)  → false     (NULL AND FALSE)
//	NOT ($1 = 3)           → NULL
//	$0 IN (1, NULL)        → NULL      ($0 IN (1, 10) → true)
//	2147483647::int4 + 1::int4 → ErrIntegerOverflow;  $0 / 0 → ErrDivisionByZero
//
// Constraints: one pass over the tree; do not modify the row.
// Postgres reference: ExecInterpExpr() in src/backend/executor/execExprInterp.c,
// int4pl()/int4div() in src/backend/utils/adt/int.c.
func Eval(e Expr, row types.Row) (types.Value, error) {
	isNum := func(v types.Value) bool {
		return v.Kind == types.KindInt32 || v.Kind == types.KindInt64 || v.Kind == types.KindFloat64
	}
	comparable := func(a, b types.Value) bool {
		return (isNum(a) && isNum(b)) || (a.Kind == b.Kind && (a.Kind == types.KindText || a.Kind == types.KindBool))
	}
	// asBool returns 0=false, 1=true, 2=null.
	asBool := func(v types.Value) (int, error) {
		switch v.Kind {
		case types.KindNull:
			return 2, nil
		case types.KindBool:
			if v.B {
				return 1, nil
			}
			return 0, nil
		}
		return 0, ErrTypeMismatch
	}

	switch n := e.(type) {
	case Const:
		return n.Val, nil
	case ColRef:
		if n.Index < 0 || n.Index >= len(row) {
			return types.Value{}, ErrBadColumn
		}
		return row[n.Index], nil
	case Compare:
		l, err := Eval(n.L, row)
		if err != nil {
			return types.Value{}, err
		}
		r, err := Eval(n.R, row)
		if err != nil {
			return types.Value{}, err
		}
		if l.IsNull() || r.IsNull() {
			return types.Null(), nil
		}
		if !comparable(l, r) {
			return types.Value{}, ErrTypeMismatch
		}
		c := types.Compare(l, r)
		var b bool
		switch n.Op {
		case OpLT:
			b = c < 0
		case OpLE:
			b = c <= 0
		case OpEQ:
			b = c == 0
		case OpNE:
			b = c != 0
		case OpGT:
			b = c > 0
		case OpGE:
			b = c >= 0
		default:
			return types.Value{}, ErrBadExpr
		}
		return types.Bool(b), nil
	case And, Or:
		var le, re Expr
		isAnd := false
		if a, ok := n.(And); ok {
			le, re, isAnd = a.L, a.R, true
		} else {
			o := n.(Or)
			le, re = o.L, o.R
		}
		lv, err := Eval(le, row)
		if err != nil {
			return types.Value{}, err
		}
		l, err := asBool(lv)
		if err != nil {
			return types.Value{}, err
		}
		decider := 0 // FALSE decides AND
		if !isAnd {
			decider = 1 // TRUE decides OR
		}
		if l == decider {
			return types.Bool(decider == 1), nil
		}
		rv, err := Eval(re, row)
		if err != nil {
			return types.Value{}, err
		}
		r, err := asBool(rv)
		if err != nil {
			return types.Value{}, err
		}
		if r == decider {
			return types.Bool(decider == 1), nil
		}
		if l == 2 || r == 2 {
			return types.Null(), nil
		}
		return types.Bool(decider == 0), nil
	case Not:
		v, err := Eval(n.X, row)
		if err != nil {
			return types.Value{}, err
		}
		b, err := asBool(v)
		if err != nil {
			return types.Value{}, err
		}
		if b == 2 {
			return types.Null(), nil
		}
		return types.Bool(b == 0), nil
	case IsNull:
		v, err := Eval(n.X, row)
		if err != nil {
			return types.Value{}, err
		}
		return types.Bool(v.IsNull()), nil
	case IsNotNull:
		v, err := Eval(n.X, row)
		if err != nil {
			return types.Value{}, err
		}
		return types.Bool(!v.IsNull()), nil
	case Arith:
		l, err := Eval(n.L, row)
		if err != nil {
			return types.Value{}, err
		}
		r, err := Eval(n.R, row)
		if err != nil {
			return types.Value{}, err
		}
		if l.IsNull() || r.IsNull() {
			return types.Null(), nil
		}
		if !isNum(l) || !isNum(r) {
			return types.Value{}, ErrTypeMismatch
		}
		if l.Kind == types.KindFloat64 || r.Kind == types.KindFloat64 {
			lf, rf := l.F, r.F
			if l.Kind != types.KindFloat64 {
				lf = float64(l.I)
			}
			if r.Kind != types.KindFloat64 {
				rf = float64(r.I)
			}
			switch n.Op {
			case OpAdd:
				return types.Float64(lf + rf), nil
			case OpSub:
				return types.Float64(lf - rf), nil
			case OpMul:
				return types.Float64(lf * rf), nil
			case OpDiv:
				if rf == 0 {
					return types.Value{}, ErrDivisionByZero
				}
				return types.Float64(lf / rf), nil
			}
			return types.Value{}, ErrBadExpr
		}
		a, b := l.I, r.I
		var res int64
		switch n.Op {
		case OpAdd:
			res = a + b
			if (b > 0 && res < a) || (b < 0 && res > a) {
				return types.Value{}, ErrIntegerOverflow
			}
		case OpSub:
			res = a - b
			if (b < 0 && res < a) || (b > 0 && res > a) {
				return types.Value{}, ErrIntegerOverflow
			}
		case OpMul:
			res = a * b
			if a != 0 && (res/a != b || (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64)) {
				return types.Value{}, ErrIntegerOverflow
			}
		case OpDiv:
			if b == 0 {
				return types.Value{}, ErrDivisionByZero
			}
			if a == math.MinInt64 && b == -1 {
				return types.Value{}, ErrIntegerOverflow
			}
			res = a / b
		default:
			return types.Value{}, ErrBadExpr
		}
		if l.Kind == types.KindInt64 || r.Kind == types.KindInt64 {
			return types.Int64(res), nil
		}
		if res < math.MinInt32 || res > math.MaxInt32 {
			return types.Value{}, ErrIntegerOverflow
		}
		return types.Int32(int32(res)), nil
	case In:
		x, err := Eval(n.X, row)
		if err != nil {
			return types.Value{}, err
		}
		if x.IsNull() {
			return types.Null(), nil
		}
		sawNull := false
		for _, item := range n.List {
			v, err := Eval(item, row)
			if err != nil {
				return types.Value{}, err
			}
			if v.IsNull() {
				sawNull = true
				continue
			}
			if !comparable(x, v) {
				return types.Value{}, ErrTypeMismatch
			}
			if types.Compare(x, v) == 0 {
				return types.Bool(true), nil
			}
		}
		if sawNull {
			return types.Null(), nil
		}
		return types.Bool(false), nil
	}
	return types.Value{}, ErrBadExpr
}
