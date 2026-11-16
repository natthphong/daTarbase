package engine

import (
	"bytes"
	"errors"
	"testing"

	txn "github.com/natthphong/daTarbase/phase09_txn"
	wal "github.com/natthphong/daTarbase/phase10_wal"
	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP11_6_Commit_FlushesThenFlipsClog(t *testing.T) {
	db := newDB(t, Options{})
	h := mustCreate(t, db, "emp", empSchema())
	tx := db.Begin(ReadCommitted)
	rawInsert(t, tx, h, R(int32(1), "alice", int32(10), 100), R(int32(2), "bob", int32(20), 80), R(int32(3), "carol", int32(10), 120))
	xid := tx.XID()
	if db.wal.FlushedLSN() != wal.FirstLSN {
		t.Fatalf("WAL flushed before commit (flushed %d)", db.wal.FlushedLSN())
	}
	flushes := db.wal.Flushes

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if db.wal.Flushes != flushes+1 {
		t.Fatalf("wal.Flushes = %d, want %d (exactly one flush per commit)", db.wal.Flushes, flushes+1)
	}
	if db.wal.FlushedLSN() != db.wal.End() {
		t.Fatalf("flushed %d, end %d: the COMMIT record is not durable", db.wal.FlushedLSN(), db.wal.End())
	}
	recs, err := readWALFile(db.wal.f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 4 || recs[3].Type != wal.RecCommit || recs[3].Txn != xid {
		t.Fatalf("WAL file = %v, want 3 inserts then COMMIT of txn %d", recs, xid)
	}
	if st := db.mgr.Clog.Get(xid); st != txn.StatusCommitted {
		t.Fatalf("clog[%d] = %v, want committed", xid, st)
	}
	if db.mgr.Running[xid] || db.active[tx] {
		t.Fatal("committed transaction is still running / active")
	}
	if got := seqRows(t, db.Begin(ReadCommitted), h); len(got) != 3 {
		t.Fatalf("a new transaction sees %d rows, want 3", len(got))
	}
	if err := tx.Commit(); !errors.Is(err, ErrTxnClosed) {
		t.Fatalf("second Commit: err = %v, want ErrTxnClosed", err)
	}
}

func TestP11_6_Commit_ReadOnlyWritesNothing(t *testing.T) {
	db := newDB(t, Options{})
	mustCreate(t, db, "emp", empSchema())
	tx := db.Begin(RepeatableRead)
	if _, err := tx.Snapshot(); err != nil {
		t.Fatal(err)
	}
	end, flushes := db.wal.End(), db.wal.Flushes
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if db.wal.End() != end || db.wal.Flushes != flushes || tx.XID() != 0 {
		t.Fatalf("read-only commit: WAL end %d→%d, flushes %d→%d, xid %d; want no change and no xid",
			end, db.wal.End(), flushes, db.wal.Flushes, tx.XID())
	}
	if db.active[tx] {
		t.Fatal("read-only transaction still active after Commit")
	}
}

func TestP11_6_Commit_SurvivesCrashInTheLog(t *testing.T) {
	db := newDB(t, Options{})
	h := mustCreate(t, db, "emp", empSchema())
	tx := db.Begin(ReadCommitted)
	rawInsert(t, tx, h, R(int32(1), "alice", int32(10), 100))
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	loser := db.Begin(ReadCommitted)
	rawInsert(t, loser, h, R(int32(2), "bob", int32(20), 80)) // never committed, never flushed
	path := db.wal.f.Name()
	db.Crash()
	recs, err := readWALFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Type != wal.RecUpdate || recs[1].Type != wal.RecCommit {
		t.Fatalf("WAL after crash = %v, want [UPDATE COMMIT] (the loser's record was only buffered)", recs)
	}
}

func TestP11_7_Rollback(t *testing.T) {
	db := newDB(t, Options{})
	h := mustCreate(t, db, "emp", empSchema())
	tx := db.Begin(ReadCommitted)
	tids := rawInsert(t, tx, h, R(int32(1), "a", int32(10), 1))
	before := rawTuple(t, h, tids[0])
	flushes, end := db.wal.Flushes, db.wal.End()

	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if st := db.mgr.Clog.Get(3); st != txn.StatusAborted {
		t.Fatalf("clog[3] = %v, want aborted", st)
	}
	recs := walRecords(t, db)
	if last := recs[len(recs)-1]; last.Type != wal.RecAbort || last.Txn != 3 || last.LSN != end {
		t.Fatalf("last WAL record = %v, want ABORT@%d of txn 3", last, end)
	}
	if db.wal.Flushes != flushes {
		t.Fatalf("Rollback flushed the WAL (%d → %d flushes)", flushes, db.wal.Flushes)
	}
	if after := rawTuple(t, h, tids[0]); !bytes.Equal(after, before) {
		t.Fatal("Rollback changed the page: nothing may be undone")
	}
	if got := seqRows(t, db.Begin(ReadCommitted), h); len(got) != 0 {
		t.Fatalf("rows after rollback = %v, want none", got)
	}
	if err := tx.Rollback(); !errors.Is(err, ErrTxnClosed) {
		t.Fatalf("second Rollback: err = %v, want ErrTxnClosed", err)
	}
	if err := tx.Commit(); !errors.Is(err, ErrTxnClosed) {
		t.Fatalf("Commit after Rollback: err = %v, want ErrTxnClosed", err)
	}
}

func TestP11_7_Rollback_ReadOnly(t *testing.T) {
	db := newDB(t, Options{})
	tx := db.Begin(ReadCommitted)
	end := db.wal.End()
	if err := tx.Rollback(); err != nil || db.wal.End() != end {
		t.Fatalf("read-only Rollback: err %v, WAL end %d→%d; want nil and no record", err, end, db.wal.End())
	}
	var _ types.TID
}
