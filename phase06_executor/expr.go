// Package executor implements the "pipelined" half of a query executor:
// expression evaluation with SQL three-valued logic, the streaming operators
// (Filter, Projection, Limit), the access paths (Seq Scan, Index Scan,
// Index Only Scan, Bitmap Index/Heap Scan), and the planner pieces that pick
// between them (selectivity estimation, the cost model, EXPLAIN output and
// constant folding).
//
// Everything in this file is PROVIDED: the expression tree and its errors.
// The problems live in eval.go, operators.go, scans.go, bitmap.go,
// selectivity.go, cost.go, explain.go and fold.go.
package executor

import (
	"errors"
	"strconv"
	"strings"

	"github.com/natthphong/daTarbase/pkg/types"
)

var (
	// ErrDivisionByZero is returned for x / 0 (int or float), like Postgres
	// "ERROR: division by zero" (SQLSTATE 22012).
	ErrDivisionByZero = errors.New("executor: division by zero")
	// ErrIntegerOverflow is returned when an integer result does not fit its
	// type, like Postgres "ERROR: integer out of range" (SQLSTATE 22003).
	ErrIntegerOverflow = errors.New("executor: integer out of range")
	// ErrTypeMismatch is returned when an operator gets operands it cannot
	// handle (e.g. 'a' + 1, NOT 5, a WHERE clause that yields an int).
	ErrTypeMismatch = errors.New("executor: type mismatch")
	// ErrBadColumn is returned when a ColRef points outside the row.
	ErrBadColumn = errors.New("executor: column index out of range")
	// ErrBadExpr is returned for a nil or unknown Expr node.
	ErrBadExpr = errors.New("executor: unknown expression node")
)

// Expr is a node of a scalar expression tree (Postgres: Expr / Node).
// String renders it the way EXPLAIN prints conditions (see each String method).
type Expr interface {
	String() string
	isExpr()
}

// Const is a literal value (Postgres: Const).
type Const struct {
	Val types.Value // the literal; may be NULL
}

// ColRef reads column Index of the current input row (Postgres: Var).
type ColRef struct {
	Index int    // 0-based position in the row
	Name  string // display name for EXPLAIN; "" prints as $<Index>
}

// CmpOp is a comparison operator.
type CmpOp uint8

const (
	OpLT CmpOp = iota // <
	OpLE              // <=
	OpEQ              // =
	OpNE              // <>
	OpGT              // >
	OpGE              // >=
)

func (o CmpOp) String() string {
	return [...]string{"<", "<=", "=", "<>", ">", ">="}[o]
}

// Compare is "L op R" (Postgres: OpExpr with a btree comparison operator).
type Compare struct {
	Op   CmpOp // which comparison
	L, R Expr  // operands
}

// And is "L AND R" (Postgres: BoolExpr AND_EXPR, binary here).
type And struct {
	L, R Expr // operands, must evaluate to bool or NULL
}

// Or is "L OR R" (Postgres: BoolExpr OR_EXPR, binary here).
type Or struct {
	L, R Expr // operands, must evaluate to bool or NULL
}

// Not is "NOT X" (Postgres: BoolExpr NOT_EXPR).
type Not struct {
	X Expr // operand, must evaluate to bool or NULL
}

// IsNull is "X IS NULL" (Postgres: NullTest IS_NULL).
type IsNull struct {
	X Expr // operand of any type
}

// IsNotNull is "X IS NOT NULL" (Postgres: NullTest IS_NOT_NULL).
type IsNotNull struct {
	X Expr // operand of any type
}

// ArithOp is an arithmetic operator.
type ArithOp uint8

const (
	OpAdd ArithOp = iota // +
	OpSub                // -
	OpMul                // *
	OpDiv                // /
)

func (o ArithOp) String() string {
	return [...]string{"+", "-", "*", "/"}[o]
}

// Arith is "L op R" for + - * / (Postgres: OpExpr calling int4pl, int8div, float8mul, ...).
type Arith struct {
	Op   ArithOp // which operator
	L, R Expr    // numeric operands
}

// In is "X IN (List...)" (Postgres: ScalarArrayOpExpr "= ANY").
type In struct {
	X    Expr   // the probe
	List []Expr // candidate values
}

func (Const) isExpr()     {}
func (ColRef) isExpr()    {}
func (Compare) isExpr()   {}
func (And) isExpr()       {}
func (Or) isExpr()        {}
func (Not) isExpr()       {}
func (IsNull) isExpr()    {}
func (IsNotNull) isExpr() {}
func (Arith) isExpr()     {}
func (In) isExpr()        {}

// String: the value as types.Value prints it: 5, 'abc', NULL, true.
func (c Const) String() string { return c.Val.String() }

// String: Name, or "$<Index>" when Name is empty.
func (c ColRef) String() string {
	if c.Name != "" {
		return c.Name
	}
	return "$" + strconv.Itoa(c.Index)
}

// String: "(L op R)", e.g. "(id < 10)".
func (c Compare) String() string {
	return "(" + c.L.String() + " " + c.Op.String() + " " + c.R.String() + ")"
}

// String: "(L AND R)".
func (a And) String() string { return "(" + a.L.String() + " AND " + a.R.String() + ")" }

// String: "(L OR R)".
func (o Or) String() string { return "(" + o.L.String() + " OR " + o.R.String() + ")" }

// String: "(NOT X)".
func (n Not) String() string { return "(NOT " + n.X.String() + ")" }

// String: "(X IS NULL)".
func (n IsNull) String() string { return "(" + n.X.String() + " IS NULL)" }

// String: "(X IS NOT NULL)".
func (n IsNotNull) String() string { return "(" + n.X.String() + " IS NOT NULL)" }

// String: "(L op R)", e.g. "(a + 1)".
func (a Arith) String() string {
	return "(" + a.L.String() + " " + a.Op.String() + " " + a.R.String() + ")"
}

// String: "(X IN (a, b, c))".
func (in In) String() string {
	parts := make([]string, len(in.List))
	for i, e := range in.List {
		parts[i] = e.String()
	}
	return "(" + in.X.String() + " IN (" + strings.Join(parts, ", ") + "))"
}

// Small constructors that keep test data and examples short.

// C is a constant: C(types.Int64(5)).
func C(v types.Value) Const { return Const{Val: v} }

// Col is a named column reference: Col(0, "id").
func Col(i int, name string) ColRef { return ColRef{Index: i, Name: name} }

// Cmp builds a comparison: Cmp(OpLT, Col(0,"id"), C(types.Int64(10))).
func Cmp(op CmpOp, l, r Expr) Compare { return Compare{Op: op, L: l, R: r} }
