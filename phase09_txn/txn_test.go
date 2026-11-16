package txn

import (
	"errors"
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

// seed commits rows in one transaction and returns its xid.
func seed(t *testing.T, db *DB, rows ...types.Row) types.TxnID {
	t.Helper()
	tx := db.Begin(ReadCommitted)
	for _, r := range rows {
		tx.Insert(r)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return tx.Xid
}

// rawUpdate replaces every row visible to tx's next statement with newRow,
// using only the heap primitives (Problems 9.11–9.13).
func rawUpdate(t *testing.T, tx *Txn, newRow types.Row) {
	t.Helper()
	snap := tx.StatementSnapshot()
	h := tx.DB.Heap
	for _, target := range h.Scan(snap, tx.DB.Mgr.Clog, tx.Mine()) {
		if _, res := h.Update(target, tx.WriteXid(), snap.CurCid, newRow, tx.DB.Mgr.Clog, tx.Mine()); res != TMOk {
			t.Fatalf("rawUpdate %v: %v", target, res)
		}
	}
}

func TestP9_13_StatementSnapshot_CommandIDs(t *testing.T) {
	for _, level := range []IsolationLevel{ReadCommitted, RepeatableRead} {
		db := NewDB()
		tx := db.Begin(level)
		for want := CommandID(0); want < 3; want++ {
			if s := tx.StatementSnapshot(); s.CurCid != want {
				t.Fatalf("level %d: statement %d CurCid = %d, want %d", level, want, s.CurCid, want)
			}
		}
		if tx.NextCid != 3 {
			t.Fatalf("NextCid = %d, want 3", tx.NextCid)
		}
	}
}

func TestP9_13_StatementSnapshot_RepeatableReadSnapshotAtFirstStatement(t *testing.T) {
	db := NewDB()
	t1 := db.Begin(RepeatableRead) // 3
	if t1.TxnSnap != nil {
		t.Fatal("TxnSnap set at BEGIN; Postgres takes it at the first statement")
	}
	t2 := db.Begin(ReadCommitted) // 4
	if err := t2.Commit(); err != nil {
		t.Fatal(err)
	}
	s := t1.StatementSnapshot()
	assertSnapCid := func(s *Snapshot, cid CommandID) {
		t.Helper()
		if s.Xmin != 3 || s.Xmax != 5 || len(s.Xip) != 0 || s.CurCid != cid {
			t.Fatalf("snapshot = %+v, want {Xmin:3 Xmax:5 Xip:[] CurCid:%d}", *s, cid)
		}
	}
	assertSnapCid(s, 0)
	t5 := db.Begin(ReadCommitted)
	if err := t5.Commit(); err != nil {
		t.Fatal(err)
	}
	assertSnapCid(t1.StatementSnapshot(), 1)
	assertSnapCid(t1.TxnSnap, 0) // the stored snapshot is never modified
}

func TestP9_13_StatementSnapshot_ReadCommittedFreshEachTime(t *testing.T) {
	db := NewDB()
	t1 := db.Begin(ReadCommitted) // 3
	t2 := db.Begin(ReadCommitted) // 4
	s0 := t1.StatementSnapshot()
	if s0.Xmax != 5 || !slices.Equal(s0.Xip, []types.TxnID{4}) {
		t.Fatalf("first snapshot = %+v, want Xmax 5, Xip [4]", *s0)
	}
	t2.Commit()
	db.Begin(ReadCommitted) // 5, running
	s1 := t1.StatementSnapshot()
	if s1.Xmin != 3 || s1.Xmax != 6 || !slices.Equal(s1.Xip, []types.TxnID{5}) || s1.CurCid != 1 {
		t.Fatalf("second snapshot = %+v, want {Xmin:3 Xmax:6 Xip:[5] CurCid:1}", *s1)
	}
	if t1.TxnSnap != nil {
		t.Fatal("ReadCommitted must not store a transaction snapshot")
	}
}

func TestP9_13_NonRepeatableRead(t *testing.T) {
	tests := []struct {
		level      IsolationLevel
		secondRead []types.Row
	}{
		{ReadCommitted, []types.Row{testkit.R(1, 200)}},  // sees T2's committed update
		{RepeatableRead, []types.Row{testkit.R(1, 100)}}, // still the old value
	}
	for _, tc := range tests {
		db := NewDB()
		seed(t, db, testkit.R(1, 100))
		t1 := db.Begin(tc.level)
		testkit.AssertRows(t, t1.Select(), []types.Row{testkit.R(1, 100)})

		t2 := db.Begin(ReadCommitted)
		rawUpdate(t, t2, testkit.R(1, 200))
		testkit.AssertRows(t, t1.Select(), []types.Row{testkit.R(1, 100)}) // T2 not committed yet
		if err := t2.Commit(); err != nil {
			t.Fatal(err)
		}
		testkit.AssertRows(t, t1.Select(), tc.secondRead)
	}
}

func TestP9_13_Phantom(t *testing.T) {
	tests := []struct {
		level IsolationLevel
		want  []types.Row
	}{
		{ReadCommitted, testkit.Rows([]any{1, 100}, []any{2, 50})},
		{RepeatableRead, testkit.Rows([]any{1, 100})},
	}
	for _, tc := range tests {
		db := NewDB()
		seed(t, db, testkit.R(1, 100))
		t1 := db.Begin(tc.level)
		testkit.AssertRows(t, t1.Select(), testkit.Rows([]any{1, 100}))
		seed(t, db, testkit.R(2, 50)) // T2 inserts a new matching row and commits
		testkit.AssertRows(t, t1.Select(), tc.want)
	}
}

func TestP9_13_RepeatableReadSeesCommitsBeforeFirstStatement(t *testing.T) {
	db := NewDB()
	t1 := db.Begin(RepeatableRead)
	seed(t, db, testkit.R(7, "committed between BEGIN and first SELECT"))
	testkit.AssertRows(t, t1.Select(), []types.Row{testkit.R(7, "committed between BEGIN and first SELECT")})
}

func TestP9_13_OwnWritesVisibleToLaterStatements(t *testing.T) {
	for _, level := range []IsolationLevel{ReadCommitted, RepeatableRead} {
		db := NewDB()
		tx := db.Begin(level)
		testkit.AssertRows(t, tx.Select(), nil)
		tx.Insert(testkit.R(1))
		tx.Insert(testkit.R(2))
		testkit.AssertRows(t, tx.Select(), testkit.Rows([]any{1}, []any{2}))
		other := db.Begin(level)
		testkit.AssertRows(t, other.Select(), nil)
	}
}

// ---------------------------------------------------------------- 9.14

func idIs(id int64) func(types.Row) bool {
	return func(r types.Row) bool { return r[0].I == id }
}

func vEquals(v int64) func(types.Row) bool {
	return func(r types.Row) bool { return r[1].I == v }
}

func setV(f func(int64) int64) func(types.Row) types.Row {
	return func(r types.Row) types.Row { return testkit.R(r[0].I, f(r[1].I)) }
}

func all(types.Row) bool { return true }

func TestP9_14_UpdateWhere_Basic(t *testing.T) {
	db := NewDB()
	seed(t, db, testkit.R(1, 100), testkit.R(2, 200))
	tx := db.Begin(ReadCommitted)
	n, err := tx.Update(idIs(1), setV(func(v int64) int64 { return v + 1 }))
	if n != 1 || err != nil {
		t.Fatalf("Update = (%d, %v), want (1, nil)", n, err)
	}
	// Heap order: (0,2) is row 2, the new version of row 1 is at (0,3).
	testkit.AssertRows(t, tx.Select(), testkit.Rows([]any{2, 200}, []any{1, 101}))
	n, err = tx.Update(idIs(1), setV(func(v int64) int64 { return v + 1 }))
	if n != 1 || err != nil {
		t.Fatalf("second Update = (%d, %v), want (1, nil)", n, err)
	}
	testkit.AssertRows(t, tx.Select(), testkit.Rows([]any{2, 200}, []any{1, 102}))
	if v := db.Heap.Get(tid(0, 3)); v.Xmax != tx.Xid || v.Cmax != 2 || v.Cmin != 0 || v.Ctid != tid(0, 4) {
		t.Fatalf("(0,3) = %+v, want Cmin 0, Xmax %d, Cmax 2, Ctid (0,4)", *v, tx.Xid)
	}
	other := db.Begin(ReadCommitted)
	testkit.AssertRows(t, other.Select(), testkit.Rows([]any{1, 100}, []any{2, 200}))
}

func TestP9_14_UpdateWhere_NoHalloweenProblem(t *testing.T) {
	db := NewDB()
	seed(t, db, testkit.R(1, 1), testkit.R(2, 2), testkit.R(3, 3))
	tx := db.Begin(RepeatableRead)
	n, err := tx.Update(all, setV(func(v int64) int64 { return v * 10 }))
	if n != 3 || err != nil {
		t.Fatalf("Update = (%d, %v), want (3, nil) — new versions must not be updated again", n, err)
	}
	if len(db.Heap.Versions) != 6 {
		t.Fatalf("heap has %d versions, want 6", len(db.Heap.Versions))
	}
	testkit.AssertRows(t, tx.Select(), testkit.Rows([]any{1, 10}, []any{2, 20}, []any{3, 30}))
}

func TestP9_14_UpdateWhere_ConcurrentUpdate(t *testing.T) {
	tests := []struct {
		name       string
		level      IsolationLevel
		blockerEnd bool // true = T2 commits while T1 waits, false = aborts
		wantN      int
		wantErr    error
		wantFinal  types.Row // what a fresh transaction sees afterwards (after T1 commits / aborts)
	}{
		{"RC, blocker commits: re-evaluate on newest version", ReadCommitted, true, 1, nil, testkit.R(1, 210)},
		{"RC, blocker aborts: update original row", ReadCommitted, false, 1, nil, testkit.R(1, 110)},
		{"RR, blocker commits: serialization failure", RepeatableRead, true, 0, ErrSerialization, testkit.R(1, 200)},
		{"RR, blocker aborts: update original row", RepeatableRead, false, 1, nil, testkit.R(1, 110)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := NewDB()
			seed(t, db, testkit.R(1, 100))
			t1 := db.Begin(tc.level)      // 4
			t2 := db.Begin(ReadCommitted) // 5
			if n, err := t2.Update(idIs(1), setV(func(v int64) int64 { return v * 2 })); n != 1 || err != nil {
				t.Fatalf("T2 update = (%d, %v)", n, err)
			}
			var waited []types.TxnID
			db.Wait = func(x types.TxnID) {
				waited = append(waited, x)
				if tc.blockerEnd {
					db.Mgr.Commit(x)
				} else {
					db.Mgr.Abort(x)
				}
			}
			n, err := t1.Update(idIs(1), setV(func(v int64) int64 { return v + 10 }))
			if n != tc.wantN || !errors.Is(err, tc.wantErr) {
				t.Fatalf("T1 update = (%d, %v), want (%d, %v)", n, err, tc.wantN, tc.wantErr)
			}
			if !slices.Equal(waited, []types.TxnID{t2.Xid}) {
				t.Fatalf("Wait called with %v, want [%d]", waited, t2.Xid)
			}
			if err == nil {
				t1.Commit()
			} else {
				t1.Abort()
			}
			testkit.AssertRows(t, db.Begin(ReadCommitted).Select(), []types.Row{tc.wantFinal})
		})
	}
}

func TestP9_14_UpdateWhere_RepeatableReadCommittedBeforeUpdate(t *testing.T) {
	// T1 (RR) reads, T2 updates and commits, T1 tries to update the same row.
	db := NewDB()
	seed(t, db, testkit.R(1, 100))
	t1 := db.Begin(RepeatableRead)
	testkit.AssertRows(t, t1.Select(), []types.Row{testkit.R(1, 100)})
	t2 := db.Begin(ReadCommitted)
	t2.Update(idIs(1), setV(func(v int64) int64 { return v * 2 }))
	t2.Commit()
	waits := 0
	db.Wait = func(types.TxnID) { waits++ }
	n, err := t1.Update(idIs(1), setV(func(v int64) int64 { return v + 10 }))
	if n != 0 || !errors.Is(err, ErrSerialization) {
		t.Fatalf("T1 update = (%d, %v), want (0, ErrSerialization)", n, err)
	}
	if err.Error() != "could not serialize access due to concurrent update" {
		t.Fatalf("error text = %q", err.Error())
	}
	if waits != 0 {
		t.Fatalf("Wait called %d times; T2 had already committed", waits)
	}
	// Under RC the new statement snapshot simply sees T2's row.
	t3 := db.Begin(ReadCommitted)
	if n, err := t3.Update(idIs(1), setV(func(v int64) int64 { return v + 10 })); n != 1 || err != nil {
		t.Fatalf("RC update = (%d, %v), want (1, nil)", n, err)
	}
	testkit.AssertRows(t, t3.Select(), []types.Row{testkit.R(1, 210)})
}

func TestP9_14_UpdateWhere_ReadCommittedPredicateRecheck(t *testing.T) {
	db := NewDB()
	seed(t, db, testkit.R(1, 100))
	t2 := db.Begin(ReadCommitted)
	t2.Update(vEquals(100), setV(func(v int64) int64 { return v + 1 })) // (1,101), running
	db.Wait = func(x types.TxnID) { db.Mgr.Commit(x) }
	t1 := db.Begin(ReadCommitted)
	n, err := t1.Update(vEquals(100), setV(func(int64) int64 { return 0 }))
	if n != 0 || err != nil {
		t.Fatalf("T1 update = (%d, %v), want (0, nil): newest version no longer matches v=100", n, err)
	}
	testkit.AssertRows(t, t1.Select(), []types.Row{testkit.R(1, 101)})
}

func TestP9_14_UpdateWhere_ConcurrentDelete(t *testing.T) {
	for _, level := range []IsolationLevel{ReadCommitted, RepeatableRead} {
		db := NewDB()
		seed(t, db, testkit.R(1, 100))
		t1 := db.Begin(level)
		t1.Select()
		t2 := db.Begin(ReadCommitted)
		snap := t2.StatementSnapshot()
		if res := db.Heap.Delete(tid(0, 1), t2.Xid, snap.CurCid, db.Mgr.Clog, t2.Mine()); res != TMOk {
			t.Fatal(res)
		}
		db.Wait = func(x types.TxnID) { db.Mgr.Commit(x) }
		n, err := t1.Update(all, setV(func(v int64) int64 { return v + 1 }))
		wantErr := error(nil)
		if level == RepeatableRead {
			wantErr = ErrSerialization
		}
		if n != 0 || !errors.Is(err, wantErr) {
			t.Fatalf("level %d: update of concurrently deleted row = (%d, %v), want (0, %v)", level, n, err, wantErr)
		}
		if len(db.Heap.Versions) != 1 {
			t.Fatalf("level %d: heap grew to %d versions", level, len(db.Heap.Versions))
		}
	}
}

func TestP9_14_UpdateWhere_FollowsLongChain(t *testing.T) {
	db := NewDB()
	seed(t, db, testkit.R(1, 100))
	t2 := db.Begin(ReadCommitted)
	t2.Update(idIs(1), setV(func(v int64) int64 { return v * 2 })) // (1,200) running
	t1 := db.Begin(ReadCommitted)
	// While T1 waits for T2: T2 commits, then T9 updates again and commits.
	db.Wait = func(x types.TxnID) {
		db.Mgr.Commit(x)
		db.Wait = nil
		t9 := db.Begin(ReadCommitted)
		t9.Update(idIs(1), setV(func(v int64) int64 { return v + 100 })) // (1,300)
		t9.Commit()
	}
	n, err := t1.Update(idIs(1), setV(func(v int64) int64 { return v + 10 }))
	if n != 1 || err != nil {
		t.Fatalf("T1 update = (%d, %v), want (1, nil)", n, err)
	}
	testkit.AssertRows(t, t1.Select(), []types.Row{testkit.R(1, 310)})
}

func TestP9_14_UpdateWhere_WouldBlock(t *testing.T) {
	tests := []struct {
		name string
		wait func(db *DB) func(types.TxnID)
	}{
		{"no Wait hook", func(*DB) func(types.TxnID) { return nil }},
		{"hook returns but blocker still running", func(*DB) func(types.TxnID) { return func(types.TxnID) {} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := NewDB()
			seed(t, db, testkit.R(1, 100), testkit.R(2, 200))
			t2 := db.Begin(ReadCommitted)
			t2.Update(idIs(2), setV(func(v int64) int64 { return -v }))
			db.Wait = tc.wait(db)
			t1 := db.Begin(ReadCommitted)
			n, err := t1.Update(all, setV(func(v int64) int64 { return v + 1 }))
			if n != 1 || !errors.Is(err, ErrWouldBlock) {
				t.Fatalf("Update = (%d, %v), want (1, ErrWouldBlock): row 1 updated, row 2 blocked", n, err)
			}
		})
	}
}

// ---------------------------------------------------------------- 9.15

func TestP9_15_RollbackTo_DocExample(t *testing.T) {
	db := NewDB()
	tx := db.Begin(ReadCommitted) // 3
	tx.Insert(testkit.R(1))
	if sp := tx.Savepoint("a"); sp != 4 {
		t.Fatalf("savepoint a xid = %d, want 4", sp)
	}
	tx.Insert(testkit.R(2))
	if sp := tx.Savepoint("b"); sp != 5 {
		t.Fatalf("savepoint b xid = %d, want 5", sp)
	}
	tx.Insert(testkit.R(3))
	if xs := []types.TxnID{db.Heap.Versions[0].Xmin, db.Heap.Versions[1].Xmin, db.Heap.Versions[2].Xmin}; !slices.Equal(xs, []types.TxnID{3, 4, 5}) {
		t.Fatalf("inserts stamped with %v, want [3 4 5]", xs)
	}
	testkit.AssertRows(t, tx.Select(), testkit.Rows([]any{1}, []any{2}, []any{3}))

	if err := tx.RollbackTo("a"); err != nil {
		t.Fatalf("RollbackTo(a) = %v", err)
	}
	for _, x := range []types.TxnID{4, 5} {
		if s := db.Mgr.Clog.Get(x); s != StatusAborted || db.Mgr.Running[x] {
			t.Fatalf("subxact %d: clog %v running %v, want aborted / not running", x, s, db.Mgr.Running[x])
		}
	}
	if !slices.Equal(tx.Savepoints, []Savepoint{{"a", 6}}) || !slices.Equal(tx.Subxids, []types.TxnID{6}) {
		t.Fatalf("Savepoints=%v Subxids=%v, want [{a 6}] [6]", tx.Savepoints, tx.Subxids)
	}
	if db.Mgr.Parent[6] != 3 || !db.Mgr.Running[6] || !db.Mgr.Running[3] {
		t.Fatalf("re-opened savepoint: Parent[6]=%d running=%v", db.Mgr.Parent[6], db.Mgr.Running)
	}
	testkit.AssertRows(t, tx.Select(), testkit.Rows([]any{1}))

	tx.Insert(testkit.R(4))
	if x := db.Heap.Versions[3].Xmin; x != 6 {
		t.Fatalf("insert after rollback stamped %d, want 6", x)
	}
	// Nothing was physically removed.
	if len(db.Heap.Versions) != 4 {
		t.Fatalf("heap has %d versions, want 4", len(db.Heap.Versions))
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if db.Mgr.Clog.Get(6) != StatusCommitted || db.Mgr.Clog.Get(4) != StatusAborted {
		t.Fatalf("after commit: clog[6]=%v clog[4]=%v", db.Mgr.Clog.Get(6), db.Mgr.Clog.Get(4))
	}
	testkit.AssertRows(t, db.Begin(ReadCommitted).Select(), testkit.Rows([]any{1}, []any{4}))
}

func TestP9_15_RollbackTo_UndoesUpdate(t *testing.T) {
	db := NewDB()
	seed(t, db, testkit.R(1, 100))
	tx := db.Begin(RepeatableRead)
	tx.Savepoint("s")
	tx.Update(all, setV(func(int64) int64 { return 0 }))
	testkit.AssertRows(t, tx.Select(), []types.Row{testkit.R(1, 0)})
	if err := tx.RollbackTo("s"); err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, tx.Select(), []types.Row{testkit.R(1, 100)})
	// The old version's xmax is now an aborted subxact: updating again is fine.
	if n, err := tx.Update(all, setV(func(v int64) int64 { return v + 5 })); n != 1 || err != nil {
		t.Fatalf("update after rollback = (%d, %v), want (1, nil)", n, err)
	}
	tx.Commit()
	testkit.AssertRows(t, db.Begin(ReadCommitted).Select(), []types.Row{testkit.R(1, 105)})
}

func TestP9_15_RollbackTo_IncludesReleasedInnerSavepoints(t *testing.T) {
	db := NewDB()
	seed(t, db, testkit.R(1))     // xid 3
	tx := db.Begin(ReadCommitted) // 4
	tx.Savepoint("a")             // 5
	tx.Savepoint("b")             // 6
	tx.Insert(testkit.R(2))       // stamped 6
	if err := tx.ReleaseSavepoint("b"); err != nil {
		t.Fatal(err)
	}
	tx.Insert(testkit.R(3)) // stamped 5
	testkit.AssertRows(t, tx.Select(), testkit.Rows([]any{1}, []any{2}, []any{3}))
	if err := tx.RollbackTo("a"); err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, tx.Select(), testkit.Rows([]any{1}))
	if !slices.Equal(tx.Subxids, []types.TxnID{7}) || !slices.Equal(tx.Savepoints, []Savepoint{{"a", 7}}) {
		t.Fatalf("Subxids=%v Savepoints=%v, want [7] [{a 7}]", tx.Subxids, tx.Savepoints)
	}
	if db.Mgr.Clog.Get(5) != StatusAborted || db.Mgr.Clog.Get(6) != StatusAborted {
		t.Fatalf("subxacts 5, 6 not aborted")
	}
}

func TestP9_15_RollbackTo_KeepsOuterAndEarlierWork(t *testing.T) {
	db := NewDB()
	tx := db.Begin(ReadCommitted) // 3
	tx.Savepoint("outer")         // 4
	tx.Insert(testkit.R("outer"))
	tx.Savepoint("inner") // 5
	tx.Insert(testkit.R("inner"))
	tx.Savepoint("innermost") // 6
	tx.Insert(testkit.R("innermost"))
	if err := tx.RollbackTo("inner"); err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, tx.Select(), testkit.Rows([]any{"outer"}))
	if !slices.Equal(tx.Savepoints, []Savepoint{{"outer", 4}, {"inner", 7}}) {
		t.Fatalf("Savepoints = %v, want [{outer 4} {inner 7}]", tx.Savepoints)
	}
	if !slices.Equal(tx.Subxids, []types.TxnID{4, 7}) {
		t.Fatalf("Subxids = %v, want [4 7]", tx.Subxids)
	}
	if db.Mgr.Parent[7] != 4 {
		t.Fatalf("Parent[7] = %d, want 4 (same parent as the old inner savepoint)", db.Mgr.Parent[7])
	}
	// Roll back to the same savepoint again: only the re-opened xid 7 is aborted.
	if err := tx.RollbackTo("inner"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tx.Subxids, []types.TxnID{4, 8}) || db.Mgr.Clog.Get(7) != StatusAborted {
		t.Fatalf("second rollback: Subxids = %v clog[7] = %v, want [4 8] aborted", tx.Subxids, db.Mgr.Clog.Get(7))
	}
	tx.Commit()
	testkit.AssertRows(t, db.Begin(ReadCommitted).Select(), testkit.Rows([]any{"outer"}))
	for x, want := range map[types.TxnID]TxnStatus{3: StatusCommitted, 4: StatusCommitted, 5: StatusAborted, 6: StatusAborted, 7: StatusAborted, 8: StatusCommitted} {
		if got := db.Mgr.Clog.Get(x); got != want {
			t.Errorf("clog[%d] = %v, want %v", x, got, want)
		}
	}
}

func TestP9_15_RollbackTo_DuplicateNamesUseNewest(t *testing.T) {
	db := NewDB()
	tx := db.Begin(ReadCommitted) // 3
	tx.Savepoint("a")             // 4
	tx.Insert(testkit.R("x"))
	tx.Savepoint("a") // 5
	tx.Insert(testkit.R("y"))
	if err := tx.RollbackTo("a"); err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, tx.Select(), testkit.Rows([]any{"x"}))
	if !slices.Equal(tx.Savepoints, []Savepoint{{"a", 4}, {"a", 6}}) {
		t.Fatalf("Savepoints = %v, want [{a 4} {a 6}]", tx.Savepoints)
	}
}

func TestP9_15_RollbackTo_Errors(t *testing.T) {
	db := NewDB()
	tx := db.Begin(ReadCommitted) // 3
	tx.Savepoint("a")             // 4
	tx.Insert(testkit.R(1))
	if err := tx.RollbackTo("nope"); !errors.Is(err, ErrNoSavepoint) {
		t.Fatalf("RollbackTo(nope) = %v, want ErrNoSavepoint", err)
	}
	if len(tx.Savepoints) != 1 || !slices.Equal(tx.Subxids, []types.TxnID{4}) || db.Mgr.NextXid != 5 {
		t.Fatalf("failed RollbackTo changed state: %v %v next=%d", tx.Savepoints, tx.Subxids, db.Mgr.NextXid)
	}
	if err := tx.ReleaseSavepoint("a"); err != nil {
		t.Fatal(err)
	}
	if err := tx.RollbackTo("a"); !errors.Is(err, ErrNoSavepoint) {
		t.Fatalf("RollbackTo(released a) = %v, want ErrNoSavepoint", err)
	}
	testkit.AssertRows(t, tx.Select(), testkit.Rows([]any{1}))
}

func TestP9_15_RollbackTo_OtherTransactionsView(t *testing.T) {
	db := NewDB()
	tx := db.Begin(ReadCommitted) // 3
	tx.Savepoint("a")             // 4
	tx.Insert(testkit.R(1))
	other := db.Begin(ReadCommitted) // 5
	if s := other.StatementSnapshot(); !slices.Equal(s.Xip, []types.TxnID{3, 4}) {
		t.Fatalf("other's Xip = %v, want [3 4] (subxacts are running xids)", s.Xip)
	}
	tx.RollbackTo("a") // aborts 4, opens 6
	if s := other.StatementSnapshot(); !slices.Equal(s.Xip, []types.TxnID{3, 6}) {
		t.Fatalf("other's Xip = %v, want [3 6]", s.Xip)
	}
	tx.Insert(testkit.R(2)) // stamped 6
	testkit.AssertRows(t, other.Select(), nil)
	tx.Abort()
	if db.Mgr.Clog.Get(6) != StatusAborted {
		t.Fatalf("top-level abort left subxact 6 = %v", db.Mgr.Clog.Get(6))
	}
	testkit.AssertRows(t, other.Select(), nil)
}
