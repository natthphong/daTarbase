package executor

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
	panic("TODO: Problem 6.16")
}
