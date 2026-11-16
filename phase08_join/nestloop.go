package join

import (
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/types"
)

// The nested loop problems (8.2 – 8.6) all receive their inputs as
// exec.Iterator and return the complete join result as a slice. Shared rules
// for all of them (each problem repeats the ones that matter most):
//
//   - The left input is the OUTER side, the right input is the INNER side.
//   - "Rescan" the inner by calling inner.Open() again, reading it to the end
//     (or until you may stop early), then inner.Close(). Postgres does the
//     same with ExecReScan(innerPlan) for every outer tuple.
//   - Every Open you call must be paired with exactly one Close, also on the
//     error path. On error return (nil, err); errors from Close on the error
//     path are ignored. On success, return the first Close error (if any).
//   - A JoinType the function does not support → (nil, ErrUnsupportedJoin)
//     before anything is opened.

// Problem 8.2 — NestedLoopInner [Easy]
//
// Compute an INNER join by scanning the whole inner input once per outer row.
//
// The simplest join: for every outer row, rescan the inner and emit
// Concat(outer, inner) for every pair where spec.Match is true. It works for
// any join condition (even non-equi), needs no memory, and costs
// |outer| × |inner| comparisons.
//
//	for each o in outer:          ← outer opened once
//	    open inner                ← one rescan per outer row
//	    for each i in inner:
//	        if Match(o, i): emit o ++ i
//	    close inner
//
// Rules:
//  1. spec.Type must be Inner, otherwise return (nil, ErrUnsupportedJoin)
//     without opening anything.
//  2. Open outer exactly once. For EVERY outer row, open the inner, read it to
//     the end, close it. An empty outer means the inner is never opened.
//  3. Output order: outer row order; for one outer row, inner row order.
//  4. Use spec.Match to test a pair (so NULL keys never match and Extra is
//     applied). Its error aborts the join.
//  5. Every opened iterator is closed exactly once, also on error;
//     on error return (nil, err).
//
// Example: employees (id, name, dept_id) ⨝ departments (id, name) ON dept_id = id.
//
//	employees:   (1,'alice',10) (2,'bob',20) (3,'carol',10) (4,'dave',NULL)
//	             (5,'erin',40) (6,'frank',30) (7,'grace',20)
//	departments: (10,'eng') (20,'sales') (20,'sales-emea') (30,'ops') (50,'legal') (NULL,'unassigned')
//	result (7 rows):
//	  (1,'alice',10,10,'eng')
//	  (2,'bob',20,20,'sales')   (2,'bob',20,20,'sales-emea')
//	  (3,'carol',10,10,'eng')
//	  (6,'frank',30,30,'ops')
//	  (7,'grace',20,20,'sales') (7,'grace',20,20,'sales-emea')
//	inner opened 7 times, 7×6 = 42 inner rows pulled.
//
// Constraints: O(|outer|·|inner|) time, O(1) extra memory besides the output.
// Postgres reference: ExecNestLoop() in src/backend/executor/nodeNestloop.c
func NestedLoopInner(outer, inner exec.Iterator, spec JoinSpec) ([]types.Row, error) {
	panic("TODO: Problem 8.2")
}

// Problem 8.3 — NestedLoopLeft [Medium]
//
// Compute a LEFT OUTER join with a nested loop: unmatched outer rows are padded with NULLs.
//
// Same loop as 8.2, but remember whether the current outer row matched at
// least one inner row. If it did not, emit it once with NULLs in place of the
// inner columns. This is the only extra state a LEFT join needs — which is
// why Postgres' nested loop supports LEFT (and SEMI/ANTI) but not RIGHT/FULL.
//
// Rules:
//  1. spec.Type must be Left, otherwise (nil, ErrUnsupportedJoin) without
//     opening anything.
//  2. Open outer once; rescan (open, read to end, close) the inner once per outer row.
//  3. Matching pairs → Concat(outer, inner) in inner order. If an outer row
//     matched nothing → Concat(outer, NullRow(spec.RightWidth)), emitted at
//     that outer row's position (right after where its matches would be).
//  4. "Matched" means spec.Match returned true: a pair whose keys are equal
//     but whose Extra is false does not count. An outer row with a NULL key
//     never matches, so it always comes out padded.
//  5. Every opened iterator is closed exactly once, also on error.
//
// Example (data of Problem 8.2), 9 rows:
//
//	(1,'alice',10,10,'eng')
//	(2,'bob',20,20,'sales') (2,'bob',20,20,'sales-emea')
//	(3,'carol',10,10,'eng')
//	(4,'dave',NULL,NULL,NULL)       ← NULL key
//	(5,'erin',40,NULL,NULL)         ← no department 40
//	(6,'frank',30,30,'ops')
//	(7,'grace',20,20,'sales') (7,'grace',20,20,'sales-emea')
//
// With Extra = "d.name <> 'sales-emea'" bob and grace lose their second row
// but still have one match, so they are NOT padded (7 rows).
//
// Constraints: O(|outer|·|inner|) time.
// Postgres reference: ExecNestLoop() (JOIN_LEFT, nl_MatchedOuter) in src/backend/executor/nodeNestloop.c
func NestedLoopLeft(outer, inner exec.Iterator, spec JoinSpec) ([]types.Row, error) {
	panic("TODO: Problem 8.3")
}

// Problem 8.4 — NestedLoopRightFull [Medium]
//
// Compute a RIGHT or FULL outer join with a nested loop by tracking which inner rows ever matched.
//
// A RIGHT join must also return inner rows that matched NO outer row. You
// only know that after the last outer row, so keep one "matched" flag per
// inner row (inner rows come back in the same order on every rescan, so the
// position in the scan identifies a row). After the outer loop, emit every
// inner row whose flag is still false, padded with NULLs on the left.
// FULL = LEFT behaviour for outer rows + this.
//
// (Real Postgres refuses to plan RIGHT/FULL as a nested loop for exactly this
// reason — the flags need memory proportional to the inner side. Hash and
// merge joins handle them; see 8.8 and 8.11.)
//
// Rules:
//  1. spec.Type must be Right or Full, otherwise (nil, ErrUnsupportedJoin)
//     without opening anything.
//  2. Open outer once; rescan the inner once per outer row. Keep the inner
//     rows of the first scan so you can emit unmatched ones at the end. If
//     the outer is empty, open and read the inner exactly once (all its rows
//     are unmatched). So the inner is opened max(|outer|, 1) times.
//  3. Phase 1, in outer order: matching pairs Concat(outer, inner) in inner
//     order; for Full only, an outer row with no match →
//     Concat(outer, NullRow(spec.RightWidth)) at its position.
//  4. Phase 2, after all outer rows: every inner row that never matched, in
//     inner order → Concat(NullRow(spec.LeftWidth), inner).
//  5. "Matched" means spec.Match was true. NULL-key rows never match, so they
//     always come out padded.
//  6. Every opened iterator is closed exactly once, also on error.
//
// Example (data of Problem 8.2):
//
//	RIGHT (9 rows) = the 7 INNER rows, then
//	  (NULL,NULL,NULL,50,'legal')  (NULL,NULL,NULL,NULL,'unassigned')
//	FULL (11 rows) = the 9 LEFT rows of 8.3, then the same 2 rows.
//	FULL with Extra "e.id <> 6": frank comes out as (6,'frank',30,NULL,NULL)
//	and 'ops' as (NULL,NULL,NULL,30,'ops') — 12 rows.
//
// Constraints: O(|outer|·|inner|) time, O(|inner|) memory for the flags.
// Postgres reference: match_unsorted_outer() in src/backend/optimizer/path/joinpath.c (no nestloop for RIGHT/FULL)
func NestedLoopRightFull(outer, inner exec.Iterator, spec JoinSpec) ([]types.Row, error) {
	panic("TODO: Problem 8.4")
}

// Problem 8.5 — NestedLoopSemiAnti [Medium]
//
// Compute a SEMI join (EXISTS) or ANTI join (NOT EXISTS), stopping each inner scan at the first match.
//
//	SELECT * FROM employees e WHERE     EXISTS (SELECT 1 FROM departments d WHERE d.id = e.dept_id)  -- SEMI
//	SELECT * FROM employees e WHERE NOT EXISTS (SELECT 1 FROM departments d WHERE d.id = e.dept_id)  -- ANTI
//
// The planner turns these subqueries into joins. Unlike INNER, a SEMI join
// returns each outer row at most once, no matter how many inner rows match —
// so as soon as one match is found the rest of the inner scan is useless.
//
// Rules:
//  1. spec.Type must be Semi or Anti, otherwise (nil, ErrUnsupportedJoin)
//     without opening anything.
//  2. Open outer once. For each outer row: open the inner and pull rows until
//     the first row for which spec.Match is true, or until the inner ends.
//     Then close the inner. Do NOT pull further inner rows after a match.
//  3. Semi: output the outer row (the row itself, only the outer columns) if
//     it found a match. Anti: output it if it found none.
//  4. Output order = outer order. An outer row with a NULL key never
//     matches, so Anti returns it (that is NOT EXISTS semantics — compare 8.6).
//  5. Every opened iterator is closed exactly once, also on error.
//
// Example (data of Problem 8.2):
//
//	Semi → (1,'alice',10) (2,'bob',20) (3,'carol',10) (6,'frank',30) (7,'grace',20)
//	Anti → (4,'dave',NULL) (5,'erin',40)
//	inner rows pulled, both cases: 1+2+1+6+6+4+2 = 22   (a full scan would be 42)
//
// Constraints: stop each inner scan at the first match.
// Postgres reference: ExecNestLoop() (node->js.single_match, JOIN_SEMI/JOIN_ANTI) in src/backend/executor/nodeNestloop.c
func NestedLoopSemiAnti(outer, inner exec.Iterator, spec JoinSpec) ([]types.Row, error) {
	panic("TODO: Problem 8.5")
}

// Problem 8.6 — NotIn [Medium]
//
// Evaluate `outer.col NOT IN (SELECT inner.col FROM inner)` with SQL's three-valued logic.
//
// NOT IN looks like NOT EXISTS but is not. `x NOT IN (a, b, c)` means
// `x <> a AND x <> b AND x <> c`. If any of a, b, c is NULL, `x <> NULL` is
// UNKNOWN, so the whole AND can be FALSE or UNKNOWN but never TRUE — and
// WHERE keeps only TRUE rows. Likewise, if x is NULL every comparison is
// UNKNOWN. The famous trap:
//
//	SELECT * FROM employees WHERE dept_id NOT IN (SELECT id FROM departments);
//	-- returns NOTHING because one department has id NULL,
//	-- while the NOT EXISTS version (8.5) returns dave and erin.
//
// Postgres cannot use a plain anti join for NOT IN (it only does so when it
// can prove both sides are NOT NULL); it evaluates a "hashed SubPlan":
// read the subquery once into a hash table, remember whether it saw a NULL.
//
// Rules:
//  1. Read the inner ONCE: open it, read every row, close it. Collect the
//     non-NULL values of inner[innerCol] in a hash set (use Value.HashKey,
//     so 20 and 20.0 are the same) and note whether any value was NULL.
//  2. Then open the outer once, read it and close it; keep an outer row iff
//     its NOT IN predicate is TRUE:
//     a. inner has no rows at all        → TRUE for every outer row, even if
//     outer[outerCol] is NULL (x NOT IN (empty set) is TRUE).
//     b. otherwise, outer value is NULL  → not kept.
//     c. otherwise, value in the set     → not kept (FALSE).
//     d. otherwise, inner had a NULL     → not kept (UNKNOWN).
//     e. otherwise                       → kept.
//  3. Output: the kept outer rows, unchanged, in outer order.
//  4. Every opened iterator is closed exactly once, also on error;
//     on error return (nil, err).
//
// Example (employees of 8.2, column 2 NOT IN departments column 0):
//
//	departments of 8.2 (contains a NULL id)       → no rows
//	departments without (NULL,'unassigned')         → (5,'erin',40)   (dave's NULL is excluded)
//	empty departments                               → all 7 employees, dave included
//
// Constraints: O(|outer| + |inner|) expected time; the inner is read once.
// Postgres reference: ExecHashSubPlan() in src/backend/executor/nodeSubplan.c
func NotIn(outer, inner exec.Iterator, outerCol, innerCol int) ([]types.Row, error) {
	panic("TODO: Problem 8.6")
}
