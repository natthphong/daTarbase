package executor

import (
	"github.com/natthphong/daTarbase/pkg/types"
)

// Problem 6.16 — FoldConstants [Medium]
//
// Simplify an expression at plan time: evaluate constant sub-trees and apply the safe boolean/NULL shortcuts.
//
// The planner runs eval_const_expressions on every qual before costing it:
// `WHERE id < 2 + 3 AND true` becomes `WHERE id < 5`, which an index can use,
// and `WHERE x = NULL` becomes the constant NULL, which the planner turns into
// "One-Time Filter: false" (no scan at all).
//
// Rules (work bottom-up: fold children first, left to right, then the node):
//  1. Const and ColRef are returned unchanged. nil → ErrBadExpr.
//  2. If, after folding, every child of the node is a Const (for In: X and every
//     list item; for Not/IsNull/IsNotNull: X), replace the node by
//     Const{Eval(node, nil)}. Eval errors are returned — so 1/0 fails at plan
//     time with ErrDivisionByZero, exactly like Postgres.
//  3. Otherwise (some child is not constant):
//     - Compare, Arith: if either side is Const NULL → Const NULL (these operators
//     are strict: NULL in, NULL out, whatever the other side is).
//     - And: if either side is Const FALSE → Const FALSE; else if one side is
//     Const TRUE → the other side.
//     - Or: if either side is Const TRUE → Const TRUE; else if one side is
//     Const FALSE → the other side.
//     - Not: if X is itself a Not → X.X (NOT NOT p = p, also under NULL).
//     - In: if X is Const NULL → Const NULL.
//     - Anything else: the same node type rebuilt with the folded children.
//  4. Never modify the input tree (build new nodes; In needs a new List slice).
//
// Example:
//
//	(1 + 2) * $0                  → (3 * $0)
//	($0 > 5) AND true             → ($0 > 5)
//	(NULL = $0)                   → NULL
//	($0 = 1) OR (2 > 1)           → true
//	(NOT (NOT ($0 IS NULL)))      → ($0 IS NULL)
//	($0 < (10 / 0))               → ErrDivisionByZero
//
// Constraints: O(size of tree).
// Postgres reference: eval_const_expressions() in src/backend/optimizer/util/clauses.c
func FoldConstants(e Expr) (Expr, error) {
	isConst := func(x Expr) bool { _, ok := x.(Const); return ok }
	isNull := func(x Expr) bool { c, ok := x.(Const); return ok && c.Val.IsNull() }
	isBool := func(x Expr, b bool) bool {
		c, ok := x.(Const)
		return ok && c.Val.Kind == types.KindBool && c.Val.B == b
	}
	evalConst := func(n Expr) (Expr, error) {
		v, err := Eval(n, nil)
		if err != nil {
			return nil, err
		}
		return Const{Val: v}, nil
	}
	pair := func(l, r Expr) (Expr, Expr, error) {
		fl, err := FoldConstants(l)
		if err != nil {
			return nil, nil, err
		}
		fr, err := FoldConstants(r)
		if err != nil {
			return nil, nil, err
		}
		return fl, fr, nil
	}

	switch n := e.(type) {
	case Const, ColRef:
		return n, nil
	case Compare:
		l, r, err := pair(n.L, n.R)
		if err != nil {
			return nil, err
		}
		out := Compare{Op: n.Op, L: l, R: r}
		if isConst(l) && isConst(r) {
			return evalConst(out)
		}
		if isNull(l) || isNull(r) {
			return Const{Val: types.Null()}, nil
		}
		return out, nil
	case Arith:
		l, r, err := pair(n.L, n.R)
		if err != nil {
			return nil, err
		}
		out := Arith{Op: n.Op, L: l, R: r}
		if isConst(l) && isConst(r) {
			return evalConst(out)
		}
		if isNull(l) || isNull(r) {
			return Const{Val: types.Null()}, nil
		}
		return out, nil
	case And:
		l, r, err := pair(n.L, n.R)
		if err != nil {
			return nil, err
		}
		out := And{L: l, R: r}
		switch {
		case isConst(l) && isConst(r):
			return evalConst(out)
		case isBool(l, false) || isBool(r, false):
			return Const{Val: types.Bool(false)}, nil
		case isBool(l, true):
			return r, nil
		case isBool(r, true):
			return l, nil
		}
		return out, nil
	case Or:
		l, r, err := pair(n.L, n.R)
		if err != nil {
			return nil, err
		}
		out := Or{L: l, R: r}
		switch {
		case isConst(l) && isConst(r):
			return evalConst(out)
		case isBool(l, true) || isBool(r, true):
			return Const{Val: types.Bool(true)}, nil
		case isBool(l, false):
			return r, nil
		case isBool(r, false):
			return l, nil
		}
		return out, nil
	case Not:
		x, err := FoldConstants(n.X)
		if err != nil {
			return nil, err
		}
		if isConst(x) {
			return evalConst(Not{X: x})
		}
		if inner, ok := x.(Not); ok {
			return inner.X, nil
		}
		return Not{X: x}, nil
	case IsNull:
		x, err := FoldConstants(n.X)
		if err != nil {
			return nil, err
		}
		if isConst(x) {
			return evalConst(IsNull{X: x})
		}
		return IsNull{X: x}, nil
	case IsNotNull:
		x, err := FoldConstants(n.X)
		if err != nil {
			return nil, err
		}
		if isConst(x) {
			return evalConst(IsNotNull{X: x})
		}
		return IsNotNull{X: x}, nil
	case In:
		x, err := FoldConstants(n.X)
		if err != nil {
			return nil, err
		}
		out := In{X: x, List: make([]Expr, len(n.List))}
		allConst := isConst(x)
		for i, item := range n.List {
			f, err := FoldConstants(item)
			if err != nil {
				return nil, err
			}
			out.List[i] = f
			allConst = allConst && isConst(f)
		}
		if allConst {
			return evalConst(out)
		}
		if isNull(x) {
			return Const{Val: types.Null()}, nil
		}
		return out, nil
	}
	return nil, ErrBadExpr
}
