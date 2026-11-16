package wal

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func checkPages(t *testing.T, db *DB, want map[types.PageID][PageDataSize]byte) {
	t.Helper()
	for id, w := range want {
		if got := db.BP.Page(id).Data; got != w {
			t.Fatalf("page %d = %q, want %q", id, bytes.TrimRight(got[:], "\x00"), bytes.TrimRight(w[:], "\x00"))
		}
	}
}

func TestP10_14_Recover_Scenario(t *testing.T) {
	crashed := scenario(t).Crash(0)
	db := Open(crashed)
	if err := db.Recover(); err != nil {
		t.Fatal(err)
	}
	checkPages(t, db, map[types.PageID][PageDataSize]byte{
		1: pageData(0, "t1t3"), // t1 + t3 committed
		2: {},                  // t2 was a loser
		5: pageData(0, "t3"),
		6: {}, // t4's records never reached disk
	})
	if db.BP.Page(2).LSN != 773 {
		t.Fatalf("page 2 LSN = %d, want 773 (the last CLR)", db.BP.Page(2).LSN)
	}
	checkRecs(t, summarize(t, db.Log.Bytes(), 726), []recSummary{
		{LSN: 726, Type: RecCLR, Txn: 2, PrevLSN: 656, Page: 2, Off: 2, Data: "\x00\x00", UndoNext: 301},
		{LSN: 773, Type: RecCLR, Txn: 2, PrevLSN: 726, Page: 2, Off: 0, Data: "\x00\x00", UndoNext: 175},
		{LSN: 820, Type: RecAbort, Txn: 2, PrevLSN: 773},
	})
	if db.Log.FlushedLSN() != 849 || len(db.Disk.WAL()) != 849 {
		t.Fatalf("flushed=%d disk WAL=%d, want 849/849 (recovery's records must be durable)", db.Log.FlushedLSN(), len(db.Disk.WAL()))
	}
	if len(db.Active()) != 0 {
		t.Fatalf("ATT = %v, want empty", db.Active())
	}
	if tx := db.Begin(); tx != 4 {
		t.Fatalf("next Begin = %d, want 4", tx)
	}
}

func TestP10_14_Recover_FreshDisk(t *testing.T) {
	db := Open(NewDisk())
	if err := db.Recover(); err != nil {
		t.Fatal(err)
	}
	if db.Log.End() != 8 || db.Log.Fsyncs() != 0 || db.Begin() != 1 {
		t.Fatalf("recovering an empty disk must be a no-op")
	}
}

func TestP10_14_Recover_Twice(t *testing.T) {
	db := Open(scenario(t).Crash(0))
	if err := db.Recover(); err != nil {
		t.Fatal(err)
	}
	// crash again immediately: nothing but the WAL was made durable
	db2 := Open(db.Crash(0))
	if err := db2.Recover(); err != nil {
		t.Fatal(err)
	}
	checkPages(t, db2, map[types.PageID][PageDataSize]byte{1: pageData(0, "t1t3"), 2: {}, 5: pageData(0, "t3")})
	if db2.Log.End() != 849 {
		t.Fatalf("second recovery appended records (End %d, want 849): t2 already ended with ABORT", db2.Log.End())
	}
}

func TestP10_14_Recover_TornWALTail(t *testing.T) {
	db := scenario(t)
	d := db.Crash(29 + 50) // BEGIN t4 complete, FPI p6 torn
	db2 := Open(d)
	if err := db2.Recover(); err != nil {
		t.Fatal(err)
	}
	// The torn FPI is cut off; t4 (BEGIN@726 only) is a loser with the newest
	// LastLSN, so it is rolled back first and its ABORT lands where the torn
	// record was.
	checkRecs(t, summarize(t, db2.Log.Bytes(), 726), []recSummary{
		{LSN: 726, Type: RecBegin, Txn: 4},
		{LSN: 755, Type: RecAbort, Txn: 4, PrevLSN: 726},
		{LSN: 784, Type: RecCLR, Txn: 2, PrevLSN: 656, Page: 2, Off: 2, Data: "\x00\x00", UndoNext: 301},
		{LSN: 831, Type: RecCLR, Txn: 2, PrevLSN: 784, Page: 2, Off: 0, Data: "\x00\x00", UndoNext: 175},
		{LSN: 878, Type: RecAbort, Txn: 2, PrevLSN: 831},
	})
	if wal := db2.Disk.WAL(); !bytes.Equal(wal, db2.Log.Bytes()) || len(wal) != 907 {
		t.Fatalf("disk WAL has %d bytes, want exactly the 907-byte recovered log (torn tail removed)", len(wal))
	}
	checkPages(t, db2, map[types.PageID][PageDataSize]byte{1: pageData(0, "t1t3"), 2: {}, 5: pageData(0, "t3"), 6: {}})
}

func TestP10_14_Recover_TornPage(t *testing.T) {
	db := Open(NewDisk())
	t1 := db.Begin()
	full := bytes.Repeat([]byte("A"), PageDataSize)
	db.Update(t1, 0, 0, full)
	db.Commit(t1)
	if err := db.BP.FlushPage(0); err != nil { // disk: all 'A'
		t.Fatal(err)
	}
	db.Checkpoint()
	t2 := db.Begin()
	db.Update(t2, 0, 0, bytes.Repeat([]byte("B"), PageDataSize))
	db.Commit(t2)
	// power fails in the middle of writing page 0: first half 'B', second half 'A'
	db.Disk.TearNextWrite()
	if err := db.BP.FlushPage(0); err != nil {
		t.Fatal(err)
	}
	torn := db.Disk.ReadPage(0)
	if torn.Data[0] != 'B' || torn.Data[PageDataSize-1] != 'A' {
		t.Fatalf("expected a torn page, got %q", torn.Data[:])
	}
	db2 := Open(db.Crash(0))
	if err := db2.Recover(); err != nil {
		t.Fatal(err)
	}
	var want [PageDataSize]byte
	copy(want[:], bytes.Repeat([]byte("B"), PageDataSize))
	checkPages(t, db2, map[types.PageID][PageDataSize]byte{0: want})
}

// ---------------------------------------------------------------- end to end

// workload drives a DB with random transactions under strict page-level
// locking (a page belongs to at most one running txn), and tracks the
// committed contents of every page.
type workload struct {
	t         *testing.T
	rng       *rand.Rand
	db        *DB
	committed map[types.PageID][PageDataSize]byte
	owner     map[types.PageID]types.TxnID
	pages     map[types.TxnID][]types.PageID
	running   []types.TxnID
}

const e2ePages = 8

func (w *workload) step() {
	db, rng := w.db, w.rng
	switch op := rng.IntN(100); {
	case op < 10 && len(w.running) < 4:
		tx := db.Begin()
		w.running = append(w.running, tx)
	case op < 60 && len(w.running) > 0:
		tx := w.running[rng.IntN(len(w.running))]
		page := types.PageID(rng.IntN(e2ePages))
		if o, ok := w.owner[page]; ok && o != tx {
			return
		}
		n := 1 + rng.IntN(8)
		off := rng.IntN(PageDataSize - n + 1)
		data := make([]byte, n)
		for i := range data {
			data[i] = byte('a' + rng.IntN(26))
		}
		if _, err := db.Update(tx, page, off, data); err != nil {
			w.t.Fatalf("Update: %v", err)
		}
		if _, ok := w.owner[page]; !ok {
			w.owner[page] = tx
			w.pages[tx] = append(w.pages[tx], page)
		}
	case op < 72 && len(w.running) > 0:
		i := rng.IntN(len(w.running))
		tx := w.running[i]
		if _, err := db.Commit(tx); err != nil {
			w.t.Fatalf("Commit: %v", err)
		}
		for _, p := range w.pages[tx] {
			w.committed[p] = db.BP.Page(p).Data
		}
		w.end(i)
	case op < 78 && len(w.running) > 0:
		i := rng.IntN(len(w.running))
		tx := w.running[i]
		if err := db.Abort(tx); err != nil {
			w.t.Fatalf("Abort: %v", err)
		}
		for _, p := range w.pages[tx] {
			if db.BP.Page(p).Data != w.committed[p] {
				w.t.Fatalf("after Abort(%d) page %d differs from its committed state", tx, p)
			}
		}
		w.end(i)
	case op < 82:
		if _, err := db.Checkpoint(); err != nil {
			w.t.Fatalf("Checkpoint: %v", err)
		}
	case op < 94:
		if dirty := db.BP.DirtyPages(); len(dirty) > 0 {
			if err := db.BP.FlushPage(dirty[rng.IntN(len(dirty))]); err != nil {
				w.t.Fatalf("FlushPage: %v", err)
			}
		}
	default:
		db.Log.FlushUpTo(db.Log.End())
	}
}

func (w *workload) end(i int) {
	tx := w.running[i]
	for _, p := range w.pages[tx] {
		delete(w.owner, p)
	}
	delete(w.pages, tx)
	w.running = slices.Delete(w.running, i, i+1)
}

// crash simulates a power failure (optionally tearing a page write and leaking
// part of the WAL tail), recovers, and checks committed state.
func (w *workload) crashAndRecover(round int) {
	db, rng := w.db, w.rng
	if dirty := db.BP.DirtyPages(); len(dirty) > 0 && rng.IntN(3) == 0 {
		db.Disk.TearNextWrite()
		if err := db.BP.FlushPage(dirty[rng.IntN(len(dirty))]); err != nil {
			w.t.Fatalf("torn FlushPage: %v", err)
		}
	}
	unflushed := int(db.Log.End() - db.Log.FlushedLSN())
	d := db.Crash(rng.IntN(unflushed + 1))

	recoverAndCheck := func(d *Disk, what string) *DB {
		ndb := Open(d)
		if err := ndb.Recover(); err != nil {
			w.t.Fatalf("round %d %s: Recover: %v", round, what, err)
		}
		for p := types.PageID(0); p < e2ePages; p++ {
			if got := ndb.BP.Page(p).Data; got != w.committed[p] {
				w.t.Fatalf("round %d %s: page %d =\n %q\nwant committed\n %q", round, what, p, got[:], w.committed[p])
			}
		}
		if len(ndb.Active()) != 0 {
			w.t.Fatalf("round %d %s: ATT not empty after recovery: %v", round, what, ndb.Active())
		}
		return ndb
	}
	ndb := recoverAndCheck(d, "recovery")
	if rng.IntN(2) == 0 { // crash again straight after recovery, maybe after some page writes
		if rng.IntN(2) == 0 {
			if err := ndb.BP.FlushAll(); err != nil {
				w.t.Fatal(err)
			}
		}
		ndb = recoverAndCheck(ndb.Crash(0), "second recovery")
	}
	w.db = ndb
	w.owner = map[types.PageID]types.TxnID{}
	w.pages = map[types.TxnID][]types.PageID{}
	w.running = nil
}

func TestP10_14_Recover_RandomCrashes(t *testing.T) {
	for seed := uint64(1); seed <= 40; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			w := &workload{
				t:         t,
				rng:       rand.New(rand.NewPCG(seed, 10)),
				db:        Open(NewDisk()),
				committed: map[types.PageID][PageDataSize]byte{},
				owner:     map[types.PageID]types.TxnID{},
				pages:     map[types.TxnID][]types.PageID{},
			}
			for round := range 4 {
				steps := 20 + w.rng.IntN(150)
				for range steps {
					w.step()
				}
				w.crashAndRecover(round)
			}
		})
	}
}
