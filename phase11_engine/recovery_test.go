package engine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	txn "github.com/natthphong/daTarbase/phase09_txn"
	wal "github.com/natthphong/daTarbase/phase10_wal"
	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

func allRows(t testing.TB, db *DB, q *Query) []types.Row {
	t.Helper()
	tx := db.Begin(ReadCommitted)
	defer tx.Commit()
	rows, err := tx.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestP11_20_Checkpoint_MakesDataFilesCurrent(t *testing.T) {
	db := emp3(t)
	h := db.Heap("emp")
	if !h.pool.IsDirty(0) {
		t.Fatal("setup: page 0 should be dirty before the checkpoint")
	}
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	for _, id := range h.pool.Resident() {
		if h.pool.IsDirty(id) {
			t.Fatalf("page %d still dirty after the checkpoint", id)
		}
	}
	clog, err := loadClog(filepath.Join(db.Dir(), "pg_xact"))
	if err != nil || clog.Get(3) != txn.StatusCommitted {
		t.Fatalf("pg_xact: err %v, status of xid 3 = %v; want committed", err, clog.Get(3))
	}
	recs, _ := readWALFile(filepath.Join(db.Dir(), "pg_wal"))
	last := recs[len(recs)-1]
	if next, _ := decodeCheckpoint(last.Body); last.Type != wal.RecCheckpoint || next != 4 || last.Txn != 0 {
		t.Fatalf("last durable record = %v (NextXid %d), want CHECKPOINT with NextXid 4", last, next)
	}

	db2 := reopen(t, db, Options{})
	if db2.Recovery != (RecoveryStats{}) {
		t.Fatalf("Recovery = %+v, want none (nothing after the checkpoint)", db2.Recovery)
	}
	testkit.AssertRows(t, allRows(t, db2, Scan("emp").Select("name")), []types.Row{R("alice"), R("bob"), R("carol")})
	if x := beginXid(db2, ReadCommitted).XID(); x != 4 {
		t.Fatalf("first xid after reopen = %d, want 4 (NextXid from the checkpoint)", x)
	}
}

func TestP11_20_Checkpoint_RunningTransactionIsAbortedByCrash(t *testing.T) {
	db := emp3(t)
	running := db.Begin(ReadCommitted)
	running.Insert("emp", R(4, "dave", 30, 1)) // xid 4, its page reaches disk at the checkpoint
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	db2 := reopen(t, db, Options{})
	if db2.Recovery != (RecoveryStats{}) {
		t.Fatalf("Recovery = %+v, want none", db2.Recovery)
	}
	if st := db2.mgr.Clog.Get(4); st != txn.StatusAborted {
		t.Fatalf("clog[4] = %v, want aborted (it was in progress at the crash)", st)
	}
	testkit.AssertRows(t, allRows(t, db2, Scan("emp").Select("name")), []types.Row{R("alice"), R("bob"), R("carol")})
	if x := beginXid(db2, ReadCommitted).XID(); x != 5 {
		t.Fatalf("first xid after reopen = %d, want 5 (xid 4 must not be reused)", x)
	}
}

func TestP11_20_Checkpoint_CloseAndReopen(t *testing.T) {
	db := emp3(t)
	open := db.Begin(ReadCommitted)
	open.Insert("emp", R(4, "dave", 30, 1))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); !errors.Is(err, ErrClosed) {
		t.Fatalf("second Close: err = %v, want ErrClosed", err)
	}
	if err := db.Checkpoint(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Checkpoint after Close: err = %v, want ErrClosed", err)
	}
	db2, err := OpenWith(db.Dir(), Options{NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db2.Crash)
	if db2.Recovery != (RecoveryStats{}) {
		t.Fatalf("Recovery after a clean Close = %+v, want none", db2.Recovery)
	}
	testkit.AssertRows(t, allRows(t, db2, Scan("emp").Select("name")), []types.Row{R("alice"), R("bob"), R("carol")})
	k := allRows(t, db2, Scan("emp").Where("dept", "=", 10).Select("name"))
	testkit.AssertRows(t, k, []types.Row{R("alice"), R("carol")})
}

func TestP11_21_Recover_RedoesCommittedWork(t *testing.T) {
	db := emp3(t) // 3 inserts + COMMIT flushed; no page ever written
	db2 := reopen(t, db, Options{})
	if want := (RecoveryStats{Records: 4, Applied: 3, Committed: 1}); db2.Recovery != want {
		t.Fatalf("Recovery = %+v, want %+v", db2.Recovery, want)
	}
	testkit.AssertRows(t, allRows(t, db2, Scan("emp").Where("dept", "=", 10).Select("name")), []types.Row{R("alice"), R("carol")})

	db3 := reopen(t, db2, Options{})
	if db3.Recovery != (RecoveryStats{}) {
		t.Fatalf("second reopen: Recovery = %+v, want none (end-of-recovery checkpoint)", db3.Recovery)
	}
	if x := beginXid(db3, ReadCommitted).XID(); x != 4 {
		t.Fatalf("first xid = %d, want 4", x)
	}
}

func TestP11_21_Recover_LoserFlushedByAnotherCommit(t *testing.T) {
	db := emp3(t)
	loser := db.Begin(ReadCommitted)
	loser.Insert("emp", R(4, "dave", 30, 1)) // xid 4
	loser.Delete(Scan("emp").Where("id", "=", 1))
	winner := db.Begin(ReadCommitted)
	winner.Insert("emp", R(5, "erin", 40, 1)) // xid 5
	winner.Commit()                           // flushes the loser's records too

	db2 := reopen(t, db, Options{})
	if want := (RecoveryStats{Records: 8, Applied: 6, Committed: 2}); db2.Recovery != want {
		t.Fatalf("Recovery = %+v, want %+v", db2.Recovery, want)
	}
	if db2.mgr.Clog.Get(4) != txn.StatusAborted || db2.mgr.Clog.Get(5) != txn.StatusCommitted {
		t.Fatalf("clog[4] = %v, clog[5] = %v; want aborted, committed", db2.mgr.Clog.Get(4), db2.mgr.Clog.Get(5))
	}
	testkit.AssertRows(t, allRows(t, db2, Scan("emp").Select("name")), []types.Row{R("alice"), R("bob"), R("carol"), R("erin")})
	if hdr := rawHeader(t, db2.Heap("emp"), alice); hdr.Xmax != 4 {
		t.Fatalf("alice xmax = %d: redo must repeat the loser's delete too (no undo)", hdr.Xmax)
	}
	tx := db2.Begin(ReadCommitted)
	if n, err := tx.Delete(Scan("emp").Where("id", "=", 1)); n != 1 || err != nil {
		t.Fatalf("deleting alice after recovery = %d, %v (an aborted xmax is no conflict)", n, err)
	}
}

// Small buffer pool: pages are evicted (stolen) before commit, so some redo
// records find their change already on disk.
func TestP11_21_Recover_StealNoForce5000Rows(t *testing.T) {
	opts := Options{PoolFrames: 4}
	db := newDB(t, opts)
	mustCreate(t, db, "events", eventsSchema())
	if err := db.CreateIndex("events_kind", "events", []string{"kind"}, Include("amount")); err != nil {
		t.Fatal(err)
	}
	ev := events5000()
	tx := db.Begin(ReadCommitted)
	for _, r := range ev {
		if _, err := tx.Insert("events", r); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	loser := db.Begin(ReadCommitted)
	for i := 0; i < 300; i++ {
		loser.Insert("events", R(10000+i, "loser", i))
	}
	if n, err := loser.Delete(Scan("events").Where("id", "<=", 100)); n != 100 || err != nil {
		t.Fatalf("loser Delete = %d, %v", n, err)
	}
	winner := db.Begin(ReadCommitted) // its commit flushes the loser's records too
	winner.Insert("events", R(20000, "winner", 1))
	winner.Commit()
	ev = append(ev, R(20000, "winner", 1))
	flushedHeap := 0
	for _, r := range walRecords(t, db) {
		if r.Type == wal.RecUpdate && r.LSN < db.wal.FlushedLSN() {
			flushedHeap++
		}
	}

	db2 := reopen(t, db, opts)
	rs := db2.Recovery
	if want := (RecoveryStats{Records: 5403, Applied: 101, Skipped: 5300, Committed: 2}); rs != want {
		t.Fatalf("Recovery = %+v, want %+v", rs, want)
	}
	if rs.Applied+rs.Skipped != flushedHeap || rs.Skipped == 0 || rs.Applied == 0 || rs.Committed != 2 {
		t.Fatalf("Recovery = %+v; want Applied+Skipped = %d durable heap records, both > 0, Committed 2", rs, flushedHeap)
	}
	got, err := exec.Collect(&SeqScan{Heap: db2.Heap("events"), Snap: db2.mgr.GetSnapshot(0), Clog: db2.mgr.Clog})
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertRowsUnordered(t, got, ev)
	rows := allRows(t, db2, Scan("events").Where("kind", "=", "refund").Aggregate(CountStar(), Sum("amount")))
	var n, sum int64
	for _, r := range ev {
		if r[1].S == "refund" {
			n, sum = n+1, sum+r[2].I
		}
	}
	testkit.AssertRows(t, rows, []types.Row{R(n, sum)})
	if x := beginXid(db2, ReadCommitted).XID(); x != 6 {
		t.Fatalf("first xid after recovery = %d, want 6", x)
	}
}

func TestP11_21_Recover_ReplaysDeletesAndPrunes(t *testing.T) {
	db, _ := vacuumFixture(t, false)
	if _, err := db.Vacuum("emp"); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin(ReadCommitted)
	tx.Insert("emp", R(100, "new", 10, 1)) // reuses slot 1 freed by the prune
	tx.Update(Scan("emp").Where("id", "=", 11), map[string]any{"salary": 999})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	want := allRows(t, db, Scan("emp").OrderBy("id"))

	db2 := reopen(t, db, Options{})
	if db2.Recovery.Applied == 0 {
		t.Fatalf("Recovery = %+v, want redo work", db2.Recovery)
	}
	testkit.AssertRows(t, allRows(t, db2, Scan("emp").OrderBy("id")), want)
	testkit.AssertRows(t, allRows(t, db2, Scan("emp").Where("id", "=", 11).Select("salary")), []types.Row{R(999)})
}

func TestP11_21_Recover_TornWALTailAndBadRecords(t *testing.T) {
	db := emp3(t)
	path := filepath.Join(db.Dir(), "pg_wal")
	db.Crash()
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	f.Write([]byte{0x40, 0, 0, 0, 1, 2, 3}) // half a record
	f.Close()
	db2, err := OpenWith(db.Dir(), Options{NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db2.Crash)
	if db2.Recovery.Applied != 3 {
		t.Fatalf("Recovery = %+v, want the 3 inserts redone and the torn tail ignored", db2.Recovery)
	}

	bad := []wal.Record{{LSN: 1 << 40, Txn: 9, Type: wal.RecUpdate, Body: heapRecord{Op: opInsert, Rel: 999, Page: 0, Slot: 1, Tuple: []byte{1}}.encode()}}
	if _, err := db2.Recover(bad); !errors.Is(err, ErrCorruptWAL) {
		t.Fatalf("unknown relation: err = %v, want ErrCorruptWAL", err)
	}
	h := db2.Heap("emp")
	tup := rawTuple(t, h, alice)
	bad = []wal.Record{{LSN: 1 << 40, Txn: 9, Type: wal.RecUpdate, Body: heapRecord{Op: opInsert, Rel: h.Def.RelID, Page: 0, Slot: 1, Tuple: tup}.encode()}}
	if _, err := db2.Recover(bad); !errors.Is(err, ErrRedoMismatch) {
		t.Fatalf("insert landing in another slot: err = %v, want ErrRedoMismatch", err)
	}
}

// The whole story: create → insert → index → query → update → rollback →
// reopen → crash → recover, checked against an in-memory oracle.
func TestP11_21_EndToEnd(t *testing.T) {
	opts := Options{PoolFrames: 6, IndexFanout: 8}
	db := newDB(t, opts)
	mustCreate(t, db, "emp", empSchema())
	mustCreate(t, db, "dept", deptSchema())
	tx := db.Begin(ReadCommitted)
	for _, r := range emp50() {
		tx.Insert("emp", r)
	}
	for _, r := range dept5() {
		tx.Insert("dept", r)
	}
	tx.Commit()
	if err := db.CreateIndex("emp_dept", "emp", []string{"dept"}, Include("name")); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateIndex("emp_id", "emp", []string{"id"}); err != nil {
		t.Fatal(err)
	}

	oracle := emp50()
	check := func(db *DB, stage string) {
		t.Helper()
		got := allRows(t, db, Scan("emp").OrderBy("id"))
		testkit.AssertRows(t, got, oracle)
		left := allRows(t, db, Scan("emp").Join(Scan("dept"), LeftJoin, "dept", "dept.id").Select("emp.id", "dept.name").OrderBy("emp.id"))
		if len(left) != len(oracle) {
			t.Fatalf("%s: LEFT JOIN returned %d rows, want %d", stage, len(left), len(oracle))
		}
		right := allRows(t, db, Scan("emp").Join(Scan("dept"), RightJoin, "dept", "dept.id").Where("id", "<", 0))
		testkit.AssertRows(t, right, []types.Row{
			R(nil, nil, nil, nil, 10, "eng"), R(nil, nil, nil, nil, 20, "sales"), R(nil, nil, nil, nil, 30, "ops"),
			R(nil, nil, nil, nil, 40, "hr"), R(nil, nil, nil, nil, 50, "legal"),
		})
	}
	check(db, "after load")

	// update dept 20 → salary +0 set to 77, committed
	up := db.Begin(ReadCommitted)
	n, err := up.Update(Scan("emp").Where("dept", "=", 20), map[string]any{"salary": 77})
	if err != nil {
		t.Fatal(err)
	}
	up.Commit()
	cnt := 0
	for i, r := range oracle {
		if !r[2].IsNull() && r[2].I == 20 {
			oracle[i] = types.Row{r[0], r[1], r[2], types.Int64(77)}
			cnt++
		}
	}
	if n != cnt {
		t.Fatalf("Update = %d rows, want %d", n, cnt)
	}
	// an update that is rolled back changes nothing
	rb := db.Begin(ReadCommitted)
	rb.Update(Scan("emp"), map[string]any{"salary": 0})
	rb.Delete(Scan("emp").Where("dept", "=", 30))
	rb.Rollback()
	check(db, "after update + rollback")

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenWith(db.Dir(), Options{PoolFrames: 6, IndexFanout: 8, NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Crash)
	check(db, "after clean reopen")

	// committed delete + uncommitted insert, then power loss
	d := db.Begin(ReadCommitted)
	d.Delete(Scan("emp").Where("id", ">", 45))
	d.Commit()
	oracle = oracle[:45]
	ghost := db.Begin(ReadCommitted)
	ghost.Insert("emp", R(1000, "ghost", 10, 1))
	db = reopen(t, db, opts)
	if db.Recovery.Committed != 1 {
		t.Fatalf("Recovery = %+v, want 1 committed transaction", db.Recovery)
	}
	check(db, "after crash recovery")
	vs, err := db.Vacuum("emp")
	if err != nil || vs.Removed == 0 {
		t.Fatalf("Vacuum = %+v, %v", vs, err)
	}
	check(db, "after vacuum")
	explain := db.Explain(Scan("emp").Where("dept", "=", 10).Select("name"))
	if explain != "Index Only Scan using emp_dept on emp\n  Index Cond: (emp.dept = 10)" {
		t.Fatalf("Explain = %q", explain)
	}
}
