package executor

import (
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/types"
)

// Filter passes through the child rows for which Pred is TRUE
// (Postgres has no separate Filter node: the qual is attached to the scan or
// join node and checked by ExecQual; we model it as its own operator).
type Filter struct {
	Child exec.Iterator // input rows
	Pred  Expr          // the WHERE condition

	RowsRemoved int // rows rejected so far (EXPLAIN ANALYZE "Rows Removed by Filter")
}

// Open opens the child and resets RowsRemoved.
func (f *Filter) Open() error { f.RowsRemoved = 0; return f.Child.Open() }

// Close closes the child.
func (f *Filter) Close() error { return f.Child.Close() }

// Problem 6.2 — Filter [Easy]
//
// Return the next child row for which the predicate evaluates to TRUE.
//
// WHERE keeps a row only when its condition is TRUE. FALSE and NULL ("unknown")
// both reject the row — that is why `WHERE x <> 5` does not return rows where
// x IS NULL.
//
// Rules:
//  1. Pull rows from f.Child until one satisfies Pred (Eval(Pred, row) is bool true),
//     and return it unchanged. Child exhausted → (nil, false, nil).
//  2. A FALSE or NULL result rejects the row and increments f.RowsRemoved.
//  3. A result that is neither bool nor NULL → ErrTypeMismatch. Errors from the
//     child or from Eval are returned as-is.
//
// Example: child (1),(NULL),(7),(3), Pred = $0 > 2  →  (7),(3); RowsRemoved = 2.
//
// Constraints: do not buffer rows; pull exactly as many as needed.
// Postgres reference: ExecQual() in src/include/executor/executor.h, ExecScan() in execScan.c
func (f *Filter) Next() (types.Row, bool, error) {
	for {
		row, ok, err := f.Child.Next()
		if err != nil || !ok {
			return nil, ok, err
		}
		v, err := Eval(f.Pred, row)
		if err != nil {
			return nil, false, err
		}
		switch v.Kind {
		case types.KindBool:
			if v.B {
				return row, true, nil
			}
		case types.KindNull:
		default:
			return nil, false, ErrTypeMismatch
		}
		f.RowsRemoved++
	}
}

// Project computes a new row from each child row: the SELECT list
// (Postgres: ProjectionInfo / ExecProject, usually attached to another node).
type Project struct {
	Child exec.Iterator // input rows
	Exprs []Expr        // one output column per expression
}

// Open opens the child.
func (p *Project) Open() error { return p.Child.Open() }

// Close closes the child.
func (p *Project) Close() error { return p.Child.Close() }

// Problem 6.3 — Project [Easy]
//
// Return the next child row transformed by the SELECT-list expressions.
//
// Rules:
//  1. Pull one child row; the output row has len(p.Exprs) columns, column i
//     being Eval(p.Exprs[i], childRow). Child exhausted → (nil, false, nil).
//  2. Always build a NEW row slice (never return or modify the child's row).
//  3. Any Eval or child error is returned as-is.
//
// Example: child (2, 'a'), (5, NULL); Exprs = [$1, $0 * 10, $1 IS NULL]
//
//	→ ('a', 20, false), (NULL, 50, true)
//
// Constraints: one child row per output row.
// Postgres reference: ExecProject() in src/include/executor/executor.h
func (p *Project) Next() (types.Row, bool, error) {
	in, ok, err := p.Child.Next()
	if err != nil || !ok {
		return nil, ok, err
	}
	out := make(types.Row, len(p.Exprs))
	for i, e := range p.Exprs {
		v, err := Eval(e, in)
		if err != nil {
			return nil, false, err
		}
		out[i] = v
	}
	return out, true, nil
}

// Limit implements LIMIT / OFFSET (Postgres: Limit node).
type Limit struct {
	Child  exec.Iterator // input rows
	Count  int           // LIMIT; a negative value means no limit (LIMIT ALL)
	Offset int           // OFFSET; rows to skip first (≤ 0 means none)

	skipped  int // rows skipped so far (towards Offset)
	returned int // rows returned so far (towards Count)
}

// Open opens the child and resets the counters.
func (l *Limit) Open() error { l.skipped, l.returned = 0, 0; return l.Child.Open() }

// Close closes the child.
func (l *Limit) Close() error { return l.Child.Close() }

// Problem 6.4 — Limit [Easy]
//
// Skip Offset rows, then return at most Count rows — and stop pulling the child as soon as the limit is reached.
//
// Limit is what makes `SELECT ... LIMIT 10` over a billion-row table cheap: the
// executor is pull-based, so if Limit stops calling Next, nothing below it
// does any more work.
//
// Rules:
//  1. The first Offset child rows are pulled and discarded (use l.skipped).
//  2. Then child rows are returned until Count rows have been returned (use l.returned).
//  3. Once Count rows were returned, Next returns (nil, false, nil) WITHOUT calling
//     the child again. With Count = 0 the child is never pulled at all
//     (not even for the offset).
//  4. Count < 0 = no limit. Child errors are returned as-is.
//
// Example: child 1..10, Offset 2, Count 3 → 3, 4, 5 and the child was pulled
// exactly 5 times (child.Pulled == 5).
//
// Constraints: O(1) state.
// Postgres reference: ExecLimit() in src/backend/executor/nodeLimit.c
func (l *Limit) Next() (types.Row, bool, error) {
	if l.Count >= 0 && l.returned >= l.Count {
		return nil, false, nil
	}
	for l.skipped < l.Offset {
		_, ok, err := l.Child.Next()
		if err != nil || !ok {
			return nil, false, err
		}
		l.skipped++
	}
	row, ok, err := l.Child.Next()
	if err != nil || !ok {
		return nil, false, err
	}
	l.returned++
	return row, true, nil
}
