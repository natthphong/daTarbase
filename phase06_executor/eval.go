package executor

import (
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
	panic("TODO: Problem 6.1")
}
