package join

import (
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/types"
)

// In the hash join problems the RIGHT input is the BUILD side (the one loaded
// into the hash table — Postgres' Hash node, always the inner/right child;
// the planner puts the smaller relation there) and the LEFT input is the
// PROBE side (the outer). The output is still left ++ right.

// Problem 8.7 — HashJoinInner [Medium]
//
// Compute an INNER equi-join by hashing the build (right) input and probing it with each left row.
//
//	build:  for each r in right:  table[key(r)] += r        (one pass)
//	probe:  for each l in left:   for r in table[key(l)]:  if Match(l, r): emit l ++ r
//
// O(|left| + |right| + output) instead of the nested loop's |left|·|right|,
// at the price of memory for the table — and only for equality conditions.
//
// Rules:
//  1. spec.Type must be Inner, otherwise (nil, ErrUnsupportedJoin) without
//     opening anything.
//  2. Build first: open build, read all of it, close it. Hash every row by
//     KeyOf(row, spec.RightCols()). Rows whose key contains a NULL are NOT
//     inserted (they can never match). Keep the rows of one key in build
//     input order.
//  3. Empty-table shortcut: if no row was inserted (build input empty, or
//     every key NULL), return (nil, nil) WITHOUT opening the probe input —
//     an inner join with an empty side is empty.
//  4. Probe: open left once, read it to the end, close it. A left row whose
//     key (KeyOf(row, spec.LeftCols())) has a NULL is skipped. Otherwise,
//     for each build row with the same KeyOf, confirm with spec.Match (this
//     also applies Extra) and emit Concat(left, right).
//  5. Output order: left order; for one left row, build input order.
//     (This equals the nested loop order of 8.2.)
//  6. Each input is opened and closed exactly once (Rule 3: probe zero
//     times). Every opened iterator is closed, also on error; on error
//     return (nil, err).
//
// Example (data of Problem 8.2, departments = build side): the same 7 rows
// as 8.2, in the same order; departments opened once, 6 rows pulled;
// employees opened once, 7 rows pulled. With departments = [(NULL,'unassigned')]
// only, the employees input is never opened.
//
// Constraints: O(|left| + |right| + output) expected time, each input read once.
// Postgres reference: ExecHashJoin() (ExecHashJoinImpl) in src/backend/executor/nodeHashjoin.c, MultiExecHash() in nodeHash.c
func HashJoinInner(left, right exec.Iterator, spec JoinSpec) ([]types.Row, error) {
	if spec.Type != Inner {
		return nil, ErrUnsupportedJoin
	}
	rcols, lcols := spec.RightCols(), spec.LeftCols()
	if err := right.Open(); err != nil {
		return nil, err
	}
	table := map[string][]types.Row{}
	inserted := 0
	for {
		r, ok, err := right.Next()
		if err != nil {
			right.Close()
			return nil, err
		}
		if !ok {
			break
		}
		k, hasNull := KeyOf(r, rcols)
		if hasNull {
			continue
		}
		table[k] = append(table[k], r)
		inserted++
	}
	if err := right.Close(); err != nil {
		return nil, err
	}
	if inserted == 0 {
		return nil, nil
	}
	if err := left.Open(); err != nil {
		return nil, err
	}
	var out []types.Row
	for {
		l, ok, err := left.Next()
		if err != nil {
			left.Close()
			return nil, err
		}
		if !ok {
			break
		}
		k, hasNull := KeyOf(l, lcols)
		if hasNull {
			continue
		}
		for _, r := range table[k] {
			m, err := spec.Match(l, r)
			if err != nil {
				left.Close()
				return nil, err
			}
			if m {
				out = append(out, Concat(l, r))
			}
		}
	}
	if err := left.Close(); err != nil {
		return nil, err
	}
	return out, nil
}

// Problem 8.8 — HashJoinOuter [Hard]
//
// Compute LEFT, RIGHT and FULL hash joins using a "matched" flag on every build-side row.
//
// LEFT is easy: a probe row that found nothing is padded on the spot. RIGHT
// and FULL need the build rows that NO probe row matched; the hash table
// marks every entry it hands out as matched (Postgres sets
// HEAP_TUPLE_HAS_MATCH in the tuple header) and, after the probe phase, a
// final scan of the table emits the unmarked ones.
//
// Important: for RIGHT/FULL the build rows with a NULL key cannot match, but
// they still belong in the result (padded) — so they must be kept somewhere
// even though they are never looked up.
//
// Rules:
//  1. spec.Type must be Left, Right or Full, otherwise (nil, ErrUnsupportedJoin)
//     without opening anything.
//  2. Build: open right, read it all, close it. Index non-NULL keys by
//     KeyOf(row, spec.RightCols()) and keep one matched flag per build row.
//  3. Shortcut: if the build input had NO rows at all and the type is Right,
//     return (nil, nil) without opening the left input. (Left/Full must still
//     read the left: every left row comes out padded.)
//  4. Probe: open left once, read to the end, close. For each left row, the
//     build rows with the same key that pass spec.Match are matches: emit
//     Concat(left, right) in build input order and set their flags. A left
//     row with a NULL key has no matches. For Left/Full, a left row with no
//     match → Concat(left, NullRow(spec.RightWidth)) at its position.
//  5. Then, for Right/Full: every build row whose flag is still false
//     (including NULL-key rows), in BUILD INPUT order →
//     Concat(NullRow(spec.LeftWidth), right).
//  6. Output order: probe-phase rows in left order, then rule 5's rows.
//     (This is exactly the output of 8.3 / 8.4.)
//  7. Each input is opened and closed exactly once (rule 3: left zero
//     times); close what you opened on error and return (nil, err).
//
// Example (data of Problem 8.2, departments = build side):
//
//	LEFT  → the 9 rows of 8.3     RIGHT → the 9 rows of 8.4     FULL → the 11 rows of 8.4
//	FULL with Extra "e.id <> 6" → 12 rows: (6,'frank',30,NULL,NULL) in frank's position,
//	                              (NULL,NULL,NULL,30,'ops') first among the unmatched departments.
//	RIGHT with an empty departments input → no rows, employees never opened.
//
// Constraints: O(|left| + |right| + output) expected time, each input read once.
// Postgres reference: ExecScanHashTableForUnmatched() in src/backend/executor/nodeHash.c, HJ_FILL_INNER/HJ_FILL_OUTER in nodeHashjoin.c
func HashJoinOuter(left, right exec.Iterator, spec JoinSpec) ([]types.Row, error) {
	switch spec.Type {
	case Left, Right, Full:
	default:
		return nil, ErrUnsupportedJoin
	}
	fillLeft := spec.Type == Left || spec.Type == Full
	fillRight := spec.Type == Right || spec.Type == Full
	rcols, lcols := spec.RightCols(), spec.LeftCols()

	if err := right.Open(); err != nil {
		return nil, err
	}
	var build []types.Row
	table := map[string][]int{} // key → positions in build
	for {
		r, ok, err := right.Next()
		if err != nil {
			right.Close()
			return nil, err
		}
		if !ok {
			break
		}
		if k, hasNull := KeyOf(r, rcols); !hasNull {
			table[k] = append(table[k], len(build))
		}
		build = append(build, r)
	}
	if err := right.Close(); err != nil {
		return nil, err
	}
	if len(build) == 0 && spec.Type == Right {
		return nil, nil
	}
	matched := make([]bool, len(build))

	if err := left.Open(); err != nil {
		return nil, err
	}
	var out []types.Row
	for {
		l, ok, err := left.Next()
		if err != nil {
			left.Close()
			return nil, err
		}
		if !ok {
			break
		}
		found := false
		if k, hasNull := KeyOf(l, lcols); !hasNull {
			for _, pos := range table[k] {
				m, err := spec.Match(l, build[pos])
				if err != nil {
					left.Close()
					return nil, err
				}
				if m {
					found, matched[pos] = true, true
					out = append(out, Concat(l, build[pos]))
				}
			}
		}
		if !found && fillLeft {
			out = append(out, Concat(l, NullRow(spec.RightWidth)))
		}
	}
	if err := left.Close(); err != nil {
		return nil, err
	}
	if fillRight {
		for pos, r := range build {
			if !matched[pos] {
				out = append(out, Concat(NullRow(spec.LeftWidth), r))
			}
		}
	}
	return out, nil
}

// GraceConfig sets the memory budget of GraceHashJoin.
type GraceConfig struct {
	MemRows  int // max build (right) rows one in-memory hash table may hold; >= 1
	Fanout   int // partitions created by each partitioning step; >= 2
	MaxDepth int // max recursion depth; a partition at this depth is joined even if too big; >= 0
}

// GraceStats reports what GraceHashJoin did.
type GraceStats struct {
	Partitionings int   // number of partitioning steps (each splits one left/right pair into Fanout pairs)
	Overflows     int   // in-memory joins done with more than MemRows build rows (depth limit reached)
	Deepest       int   // largest depth at which an in-memory join happened (0 = no partitioning)
	BuildSizes    []int // build-side row count of every in-memory join, in the order they ran
}

// Problem 8.9 — GraceHashJoin [Hard]
//
// Join inputs too big for memory by hash-partitioning both sides, then joining partition pairs one at a time.
//
// If the build side does not fit in memory, split BOTH inputs with the same
// hash function into Fanout partitions. Rows with equal keys land in the same
// partition number on both sides, so partition i of the left only has to be
// joined with partition i of the right — and each right partition is ~1/Fanout
// of the original. A partition that is still too big is split again, with a
// different hash seed (the next depth), recursively.
//
//	depth 0:   L ─┬─ L0 ⨝ R0   ← fits: in-memory hash join
//	           R ─┼─ L1 ⨝ R1   ← too big: split again at depth 1 → L1.0⨝R1.0, L1.1⨝R1.1, ...
//	              └─ ...
//
// Skew is the enemy: if many rows share ONE key, no hash function can split
// them. Postgres then stops increasing the number of batches and lets that
// batch exceed work_mem; we do the same at MaxDepth.
//
// Inputs are given as slices (think: already spilled to temp files). INNER join only.
//
// Rules:
//  1. spec.Type must be Inner, else ErrUnsupportedJoin. cfg.MemRows < 1,
//     cfg.Fanout < 2 or cfg.MaxDepth < 0 → ErrBadConfig. (Check type first.)
//  2. First drop every row whose key has a NULL (KeyOf(row, spec.LeftCols())
//     for left rows, spec.RightCols() for right rows) — they cannot match and
//     are never counted anywhere.
//  3. join(L, R, depth), starting with depth 0:
//     a. If L or R is empty → nothing (no in-memory join, no stats).
//     b. If len(R) <= cfg.MemRows → in-memory hash join (build R, probe L,
//     confirm pairs with spec.Match). Append len(R) to stats.BuildSizes and
//     set stats.Deepest = max(stats.Deepest, depth).
//     c. Else if depth == cfg.MaxDepth → stats.Overflows++, then do b's
//     in-memory join anyway.
//     d. Else stats.Partitionings++; put each row of L and R into partition
//     PartitionOf(key, depth, cfg.Fanout) (key from KeyOf); then call
//     join(L_i, R_i, depth+1) for i = 0 .. Fanout-1.
//  4. Output order is not checked; the multiset must equal the INNER join.
//  5. An error from spec.Match aborts: return (nil, GraceStats{}, err).
//
// Example (data of 8.2, NULL keys dropped: 6 employees, 5 departments):
//
//	MemRows 10, Fanout 4, MaxDepth 3 → 7 rows, Partitionings 0, BuildSizes [5], Deepest 0
//	30 left rows and 12 right rows, ALL with key 7; MemRows 5, Fanout 4, MaxDepth 2
//	  → 360 rows, Partitionings 2 (depth 0 and 1), Overflows 1, BuildSizes [12], Deepest 2
//
// Constraints: every in-memory join holds at most MemRows build rows unless it is an overflow.
// Postgres reference: ExecHashIncreaseNumBatches(), ExecHashJoinNewBatch() in src/backend/executor/nodeHash.c / nodeHashjoin.c
func GraceHashJoin(left, right []types.Row, spec JoinSpec, cfg GraceConfig) ([]types.Row, GraceStats, error) {
	var st GraceStats
	if spec.Type != Inner {
		return nil, st, ErrUnsupportedJoin
	}
	if cfg.MemRows < 1 || cfg.Fanout < 2 || cfg.MaxDepth < 0 {
		return nil, st, ErrBadConfig
	}
	type keyed struct {
		key string
		row types.Row
	}
	withKeys := func(rows []types.Row, cols []int) []keyed {
		var out []keyed
		for _, r := range rows {
			if k, hasNull := KeyOf(r, cols); !hasNull {
				out = append(out, keyed{k, r})
			}
		}
		return out
	}
	var out []types.Row
	var join func(L, R []keyed, depth int) error
	join = func(L, R []keyed, depth int) error {
		if len(L) == 0 || len(R) == 0 {
			return nil
		}
		if len(R) > cfg.MemRows && depth < cfg.MaxDepth {
			st.Partitionings++
			lp := make([][]keyed, cfg.Fanout)
			rp := make([][]keyed, cfg.Fanout)
			for _, x := range L {
				p := PartitionOf(x.key, depth, cfg.Fanout)
				lp[p] = append(lp[p], x)
			}
			for _, x := range R {
				p := PartitionOf(x.key, depth, cfg.Fanout)
				rp[p] = append(rp[p], x)
			}
			for i := 0; i < cfg.Fanout; i++ {
				if err := join(lp[i], rp[i], depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		if len(R) > cfg.MemRows {
			st.Overflows++
		}
		st.BuildSizes = append(st.BuildSizes, len(R))
		st.Deepest = max(st.Deepest, depth)
		table := map[string][]types.Row{}
		for _, x := range R {
			table[x.key] = append(table[x.key], x.row)
		}
		for _, x := range L {
			for _, r := range table[x.key] {
				m, err := spec.Match(x.row, r)
				if err != nil {
					return err
				}
				if m {
					out = append(out, Concat(x.row, r))
				}
			}
		}
		return nil
	}
	if err := join(withKeys(left, spec.LeftCols()), withKeys(right, spec.RightCols()), 0); err != nil {
		return nil, GraceStats{}, err
	}
	return out, st, nil
}
