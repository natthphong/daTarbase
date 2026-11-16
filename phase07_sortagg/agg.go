package sortagg

import (
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/types"
)

// AggFunc is an aggregate function.
type AggFunc uint8

const (
	AggCountStar AggFunc = iota // COUNT(*)      counts rows, NULL or not
	AggCount                    // COUNT(col)    counts non-NULL values
	AggSum                      // SUM(col)
	AggAvg                      // AVG(col)
	AggMin                      // MIN(col)
	AggMax                      // MAX(col)
)

// AggSpec is one aggregate in a SELECT list, e.g. COUNT(DISTINCT col 2) is
// AggSpec{Func: AggCount, Col: 2, Distinct: true}.
type AggSpec struct {
	Func     AggFunc
	Col      int  // input column (ignored for AggCountStar)
	Distinct bool // aggregate each distinct non-NULL value only once (ignored for AggCountStar)
}

// Accum is the running "transition state" of one aggregate for one group
// (Postgres: AggStatePerGroup — transValue + transValueIsNull).
//
// SUM/AVG inputs are numeric (int4, int8, float8). MIN/MAX inputs are any
// comparable kind.
type Accum struct {
	Spec AggSpec // which aggregate this state belongs to

	Count    int64           // COUNT(*): rows seen. Every other func: non-NULL values accepted
	IntSum   int64           // SUM/AVG: sum of accepted int4/int8 values
	FloatSum float64         // SUM/AVG: sum of accepted float8 values
	SawFloat bool            // SUM/AVG: true once a float8 value was accepted
	Min, Max types.Value     // MIN/MAX: smallest/largest accepted value; NULL until the first one
	Seen     map[string]bool // DISTINCT: Value.HashKey() of every accepted value (nil until needed)
}

// NewAccum returns an empty transition state for spec (Min/Max start as NULL).
func NewAccum(spec AggSpec) *Accum {
	return &Accum{Spec: spec, Min: types.Null(), Max: types.Null()}
}

// Problem 7.8 — Accum.Advance [Medium]
//
// Feed one input row into an aggregate's transition state (COUNT/SUM/AVG/MIN/MAX, optionally DISTINCT).
//
// Postgres evaluates an aggregate as   state = transfn(state, value)   per
// row, then   result = finalfn(state)   once per group (Problem 7.9).
// This problem is the transition function.
//
// Rules (v = row[a.Spec.Col]):
//  1. AggCountStar: Count++ for every row (even all-NULL rows). Nothing else changes.
//  2. Every other func: if v is NULL, the row is ignored entirely.
//  3. If Spec.Distinct: a value whose v.HashKey() is already in a.Seen is
//     ignored; otherwise add the key to a.Seen (allocate the map on first use)
//     and accept it. (HashKey makes 1 and 1.0 the same value.)
//  4. An accepted value increments Count, and additionally:
//     AggSum, AggAvg: int4/int8 → IntSum += v.I; float8 → FloatSum += v.F and SawFloat = true.
//     AggMin: replace Min if Min is NULL or types.Compare(v, Min) < 0.
//     AggMax: replace Max if Max is NULL or types.Compare(v, Max) > 0.
//     AggCount: nothing more.
//     Fields not listed for the func stay untouched.
//
// Example:
//
//	SUM(col0) over 1, NULL, 2.5, 3     → Count=3 IntSum=4 FloatSum=2.5 SawFloat=true
//	COUNT(DISTINCT col0) over 1, 1, NULL, 2, 1.0 → Count=2, len(Seen)=2
//	MIN/MAX(col0) over 'b','a','c',NULL → Min='a' Max='c'
//
// Constraints: O(1) amortized per row.
// Postgres reference: advance_transition_function() in src/backend/executor/nodeAgg.c
func (a *Accum) Advance(row types.Row) {
	if a.Spec.Func == AggCountStar {
		a.Count++
		return
	}
	v := row[a.Spec.Col]
	if v.IsNull() {
		return
	}
	if a.Spec.Distinct {
		k := v.HashKey()
		if a.Seen[k] {
			return
		}
		if a.Seen == nil {
			a.Seen = map[string]bool{}
		}
		a.Seen[k] = true
	}
	a.Count++
	switch a.Spec.Func {
	case AggSum, AggAvg:
		if v.Kind == types.KindFloat64 {
			a.FloatSum += v.F
			a.SawFloat = true
		} else {
			a.IntSum += v.I
		}
	case AggMin:
		if a.Min.IsNull() || types.Compare(v, a.Min) < 0 {
			a.Min = v
		}
	case AggMax:
		if a.Max.IsNull() || types.Compare(v, a.Max) > 0 {
			a.Max = v
		}
	}
}

// Problem 7.9 — Accum.Finalize [Easy]
//
// Turn a transition state into the aggregate's SQL result value.
//
// Rules:
//  1. AggCountStar, AggCount → Int64(Count). Counts are never NULL (0 for no input).
//  2. AggSum: Count == 0 → NULL. Otherwise, if SawFloat →
//     Float64(float64(IntSum) + FloatSum), else Int64(IntSum).
//  3. AggAvg: Count == 0 → NULL. Otherwise ALWAYS a float8:
//     Float64((float64(IntSum) + FloatSum) / float64(Count)).
//  4. AggMin → Min, AggMax → Max (NULL if nothing was accepted).
//  5. Do not modify the state.
//
// Example:
//
//	SUM over 2, 3            → int8 5        AVG over 1, 2      → float8 1.5
//	SUM over 1, 2.5          → float8 3.5    AVG over nothing   → NULL
//	COUNT over NULL, NULL    → 0             SUM over NULL      → NULL
//	SUM(DISTINCT) over 2,2,3 → 5
//
// Postgres reference: finalize_aggregate() in src/backend/executor/nodeAgg.c,
// int8_avg / float8_avg in src/backend/utils/adt/
func (a *Accum) Finalize() types.Value {
	switch a.Spec.Func {
	case AggCountStar, AggCount:
		return types.Int64(a.Count)
	case AggSum:
		if a.Count == 0 {
			return types.Null()
		}
		if a.SawFloat {
			return types.Float64(float64(a.IntSum) + a.FloatSum)
		}
		return types.Int64(a.IntSum)
	case AggAvg:
		if a.Count == 0 {
			return types.Null()
		}
		return types.Float64((float64(a.IntSum) + a.FloatSum) / float64(a.Count))
	case AggMin:
		return a.Min
	case AggMax:
		return a.Max
	}
	return types.Null()
}

// Problem 7.10 — HashAggregate [Medium]
//
// GROUP BY with a hash table of groups → transition states, plus a HAVING filter on the finished rows.
//
// Postgres' HashAgg reads its whole input once, looking up each row's group
// in a hash table and advancing that group's aggregates; only then can it
// emit results. Input order does not matter.
//
// Rules:
//  1. Open the child, read it to the end, close it. Return the first error.
//  2. The group of a row is its values at groupCols. Use Row/Value HashKey
//     to build map keys, so NULLs form ONE group (unlike "=" in WHERE).
//  3. Each group keeps one Accum per spec in aggs (NewAccum), fed with Advance.
//  4. Output one row per group: the group values (in groupCols order),
//     followed by Finalize() of each aggregate (in aggs order).
//  5. Output groups in order of FIRST APPEARANCE in the input (real hash
//     aggregation is unordered; we fix the order to make results deterministic).
//  6. having (may be nil = keep all) is called with each finished output row;
//     rows for which it returns false are dropped.
//  7. No groupCols (plain aggregate, SELECT count(*) FROM t): exactly ONE
//     group exists even for empty input — e.g. (0, NULL) for COUNT(*), SUM(x).
//     With groupCols and empty input → no rows.
//
// Example (dept, name, salary, bonus):
//
//	('eng','ann',100,10) ('ops','bob',80,NULL) ('eng','cat',120,NULL) (NULL,'dan',50,5)
//	('ops','eve',90,20)  (NULL,'fay',70,NULL)  ('eng','gus',110,10)
//	GROUP BY dept: COUNT(*), COUNT(bonus), SUM(salary), AVG(salary), MIN(name), MAX(bonus)
//	→ ('eng', 3, 2, 330, 110.0, 'ann', 10)
//	  ('ops', 2, 1, 170,  85.0, 'bob', 20)
//	  (NULL,  2, 1, 120,  60.0, 'dan',  5)
//	HAVING COUNT(*) > 2 → only the 'eng' row.
//	No GROUP BY: COUNT(*), COUNT(DISTINCT bonus), SUM(DISTINCT bonus), SUM(bonus) → (7, 3, 35, 45)
//
// Constraints: one pass over the input; memory O(groups).
// Postgres reference: agg_fill_hash_table() / agg_retrieve_hash_table() in src/backend/executor/nodeAgg.c
func HashAggregate(child exec.Iterator, groupCols []int, aggs []AggSpec, having func(types.Row) bool) (out []types.Row, err error) {
	if err := child.Open(); err != nil {
		return nil, err
	}
	defer func() {
		if cerr := child.Close(); err == nil {
			err = cerr
		}
	}()
	type group struct {
		key   types.Row
		accum []*Accum
	}
	newGroup := func(key types.Row) *group {
		g := &group{key: key}
		for _, s := range aggs {
			g.accum = append(g.accum, NewAccum(s))
		}
		return g
	}
	table := map[string]*group{}
	var order []*group
	if len(groupCols) == 0 {
		g := newGroup(nil)
		table[""] = g
		order = append(order, g)
	}
	for {
		r, ok, err := child.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		key := make(types.Row, len(groupCols))
		for i, c := range groupCols {
			key[i] = r[c]
		}
		hk := key.HashKey()
		g := table[hk]
		if g == nil {
			g = newGroup(key)
			table[hk] = g
			order = append(order, g)
		}
		for _, a := range g.accum {
			a.Advance(r)
		}
	}
	for _, g := range order {
		row := append(types.Row{}, g.key...)
		for _, a := range g.accum {
			row = append(row, a.Finalize())
		}
		if having == nil || having(row) {
			out = append(out, row)
		}
	}
	return out, nil
}

// GroupAggregate is a streaming GROUP BY over input already sorted on the
// group columns (Postgres: Agg node with strategy AGG_SORTED, usually above
// a Sort or an index scan). It holds only one group's state at a time.
type GroupAggregate struct {
	Child     exec.Iterator        // input, sorted (or at least clustered) by GroupCols
	GroupCols []int                // GROUP BY columns
	Aggs      []AggSpec            // aggregates
	Having    func(types.Row) bool // HAVING filter on output rows; nil = keep all

	pending types.Row // first row of the next group, already pulled from Child
	hasPend bool      // pending is valid
	started bool      // at least one Next call happened
	done    bool      // child exhausted and every group emitted
}

// NewGroupAggregate builds a GroupAggregate.
func NewGroupAggregate(child exec.Iterator, groupCols []int, aggs ...AggSpec) *GroupAggregate {
	return &GroupAggregate{Child: child, GroupCols: groupCols, Aggs: aggs}
}

// Open opens the child and resets state.
func (g *GroupAggregate) Open() error {
	g.pending, g.hasPend, g.started, g.done = nil, false, false, false
	return g.Child.Open()
}

// Close closes the child.
func (g *GroupAggregate) Close() error { return g.Child.Close() }

// Problem 7.11 — GroupAggregate.Next [Medium]
//
// Return the next group's result row, pulling from the sorted child only until the group ends.
//
// Because equal group keys are adjacent, a group is finished the moment a
// row with a different key shows up. That row is the first row of the NEXT
// group: keep it in g.pending instead of losing it.
//
//	child:  eng eng eng | ops ops | NULL NULL
//	Next #1 pulls 4 rows (3 eng + the first ops, kept pending) → eng result
//
// Rules:
//  1. A group is a maximal run of consecutive rows whose GroupCols values are
//     all types.Identical (so NULL matches NULL).
//  2. Output row format, aggregate semantics and HAVING are the same as
//     HashAggregate (7.10): group values, then Finalize() of each Aggs entry
//     (fresh NewAccum per group, fed with Advance). Rows rejected by Having
//     are skipped — keep going to the next group.
//  3. Groups are emitted in input order, one per Next call.
//  4. Streaming: never read more than one row past the end of the group
//     being returned.
//  5. No GroupCols: the whole input is one group, emitted even when the
//     input is empty (then e.g. COUNT(*) = 0). With GroupCols, empty input → no rows.
//  6. After the last group, return ok=false (and keep doing so).
//
// Example (input sorted by dept, ASC NULLS LAST; same data as 7.10):
//
//	('eng',3,2,330,110.0,'ann',10) ('ops',2,1,170,85.0,'bob',20) (NULL,2,1,120,60.0,'dan',5)
//	child.Pulled after the first Next = 4
//
// Postgres reference: agg_retrieve_direct() in src/backend/executor/nodeAgg.c
func (g *GroupAggregate) Next() (types.Row, bool, error) {
	sameGroup := func(a, b types.Row) bool {
		for _, c := range g.GroupCols {
			if !types.Identical(a[c], b[c]) {
				return false
			}
		}
		return true
	}
	for !g.done {
		first := !g.started
		g.started = true
		if !g.hasPend {
			r, ok, err := g.Child.Next()
			if err != nil {
				return nil, false, err
			}
			if !ok {
				g.done = true
				if !(first && len(g.GroupCols) == 0) {
					return nil, false, nil
				}
			} else {
				g.pending, g.hasPend = r, true
			}
		}
		accs := make([]*Accum, len(g.Aggs))
		for i, s := range g.Aggs {
			accs[i] = NewAccum(s)
		}
		var key types.Row
		if g.hasPend {
			start := g.pending
			g.hasPend = false
			for _, c := range g.GroupCols {
				key = append(key, start[c])
			}
			for _, a := range accs {
				a.Advance(start)
			}
			for {
				r, ok, err := g.Child.Next()
				if err != nil {
					return nil, false, err
				}
				if !ok {
					g.done = true
					break
				}
				if !sameGroup(start, r) {
					g.pending, g.hasPend = r, true
					break
				}
				for _, a := range accs {
					a.Advance(r)
				}
			}
		}
		row := append(types.Row{}, key...)
		for _, a := range accs {
			row = append(row, a.Finalize())
		}
		if g.Having == nil || g.Having(row) {
			return row, true, nil
		}
	}
	return nil, false, nil
}
