package engine

import (
	"errors"
	"slices"
	"testing"

	tuple "github.com/natthphong/daTarbase/phase01_tuple"
	txn "github.com/natthphong/daTarbase/phase09_txn"
	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP11_2_Visible(t *testing.T) {
	clog := txn.NewClog()
	clog.Set(3, txn.StatusCommitted)
	clog.Set(4, txn.StatusAborted)
	clog.Set(5, txn.StatusInProgress)
	clog.Set(7, txn.StatusCommitted) // committed, but after the snapshot was taken
	snap := &txn.Snapshot{Xmin: 5, Xmax: 7, Xip: []types.TxnID{5}}
	mine := txn.CurrentXids{6}

	tests := []struct {
		name       string
		xmin, xmax types.TxnID
		want       bool
	}{
		{"committed insert", 3, 0, true},
		{"aborted insert", 4, 0, false},
		{"inserter still running", 5, 0, false},
		{"inserter started after the snapshot", 7, 0, false},
		{"deleter aborted", 3, 4, true},
		{"deleter still running", 3, 5, true},
		{"deleter committed before the snapshot", 3, 3, false},
		{"deleter committed after the snapshot", 3, 7, true},
		{"my own insert", 6, 0, true},
		{"my own insert, then my own delete", 6, 6, false},
		{"committed row I deleted", 3, 6, false},
		{"frozen xid", txn.FrozenTxnID, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Visible(tuple.TupleHeader{Xmin: tc.xmin, Xmax: tc.xmax}, snap, clog, mine)
			if got != tc.want {
				t.Fatalf("Visible(xmin %d, xmax %d) = %v, want %v", tc.xmin, tc.xmax, got, tc.want)
			}
		})
	}
	if snap.CurCid != 0 {
		t.Fatalf("snap.CurCid = %d: Visible must not modify the caller's snapshot", snap.CurCid)
	}
}

func TestP11_2_Visible_NoXidMeansNothingIsMine(t *testing.T) {
	clog := txn.NewClog()
	snap := &txn.Snapshot{Xmin: 6, Xmax: 7, Xip: []types.TxnID{6}}
	if Visible(tuple.TupleHeader{Xmin: 6}, snap, clog, nil) {
		t.Fatal("a read-only transaction (mine = nil) saw another transaction's uncommitted insert")
	}
}

func TestP11_3_Snapshot_Values(t *testing.T) {
	db := newDB(t, Options{})
	t1 := beginXid(db, ReadCommitted)
	t2 := beginXid(db, ReadCommitted)
	if t1.XID() != 3 || t2.XID() != 4 {
		t.Fatalf("xids = %d, %d; want 3, 4", t1.XID(), t2.XID())
	}
	s, err := t2.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if s.Xmin != 3 || s.Xmax != 5 || !slices.Equal(s.Xip, []types.TxnID{3}) {
		t.Fatalf("t2 snapshot = {Xmin:%d Xmax:%d Xip:%v}, want {3 5 [3]}", s.Xmin, s.Xmax, s.Xip)
	}
	s, _ = t1.Snapshot()
	if s.Xmin != 3 || s.Xmax != 5 || !slices.Equal(s.Xip, []types.TxnID{4}) {
		t.Fatalf("t1 snapshot = {Xmin:%d Xmax:%d Xip:%v}, want {3 5 [4]}", s.Xmin, s.Xmax, s.Xip)
	}
	ro := db.Begin(ReadCommitted)
	s, _ = ro.Snapshot()
	if !slices.Equal(s.Xip, []types.TxnID{3, 4}) || ro.XID() != 0 {
		t.Fatalf("read-only snapshot Xip = %v (xid %d), want [3 4] and no xid", s.Xip, ro.XID())
	}
}

func TestP11_3_Snapshot_ReadCommittedVsRepeatableRead(t *testing.T) {
	db := newDB(t, Options{})
	t1 := beginXid(db, ReadCommitted) // 3
	t2 := beginXid(db, ReadCommitted) // 4
	rc := db.Begin(ReadCommitted)
	rr := db.Begin(RepeatableRead)
	commitRaw(t, t1) // after rr's BEGIN, before its first statement

	s, _ := rr.Snapshot()
	if !slices.Equal(s.Xip, []types.TxnID{4}) {
		t.Fatalf("RR first snapshot Xip = %v, want [4] (taken at the first statement, not at Begin)", s.Xip)
	}
	s, _ = rc.Snapshot()
	if !slices.Equal(s.Xip, []types.TxnID{4}) {
		t.Fatalf("RC snapshot Xip = %v, want [4]", s.Xip)
	}

	commitRaw(t, t2)
	beginXid(db, ReadCommitted) // 5, running

	s, _ = rr.Snapshot()
	if !slices.Equal(s.Xip, []types.TxnID{4}) || s.Xmax != 5 {
		t.Fatalf("RR second snapshot = Xip %v Xmax %d, want the first one again (Xip [4], Xmax 5)", s.Xip, s.Xmax)
	}
	s, _ = rc.Snapshot()
	if !slices.Equal(s.Xip, []types.TxnID{5}) || s.Xmax != 6 {
		t.Fatalf("RC second snapshot = Xip %v Xmax %d, want a fresh one (Xip [5], Xmax 6)", s.Xip, s.Xmax)
	}
}

func TestP11_3_Snapshot_ReturnsCopyAndChecksDone(t *testing.T) {
	db := newDB(t, Options{})
	rr := db.Begin(RepeatableRead)
	s1, _ := rr.Snapshot()
	s1.Xmax = 999
	s2, _ := rr.Snapshot()
	if s2.Xmax == 999 || s1 == s2 {
		t.Fatalf("Snapshot returned the stored snapshot itself (Xmax %d)", s2.Xmax)
	}
	rr.finish()
	if _, err := rr.Snapshot(); !errors.Is(err, ErrTxnClosed) {
		t.Fatalf("Snapshot after the end: err = %v, want ErrTxnClosed", err)
	}
}
