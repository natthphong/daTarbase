package wal

import "github.com/natthphong/daTarbase/pkg/types"

// Problem 10.14 — Recover [Hard]
//
// Bring a freshly opened DB back to a consistent state after a crash: analysis, redo, undo — committed changes present, uncommitted ones gone.
//
//	          master
//	            v
//	log: ...  CKPT ..................................... end | torn tail
//	     1. Analysis  ────────────────────────────────▶      (ATT, DPT)
//	     2. Redo      from min(recLSN) ───────────────▶      (repeat history)
//	     3. Undo      ◀──────────────── losers, newest first (CLRs, ABORTs)
//
// Rules (db was just created with Open(crashedDisk); its pool is empty):
//  1. a := Analyze(db.Log.Bytes(), db.Disk.Master()) (Problem 10.11); return its error.
//  2. db.Log.Truncate(a.End): drop any torn tail so new records follow the last
//     valid one.
//  3. db.RedoPass(a.DPT, a.RedoLSN) (Problem 10.12).
//  4. Install the analysis results: db.att = a.ATT, db.nextTxn = a.NextTxn.
//  5. db.Undo(every txn in a.ATT) (Problem 10.13).
//  6. db.Log.FlushUpTo(db.Log.End()) so the CLRs and ABORTs are durable. Pages
//     stay in the buffer pool (dirty); they need not be written.
//  7. Recover must be safe to re-run after a crash at any point during it:
//     running it twice gives the same pages.
//
// Example (the 10.11 log, crashed with nothing on disk but the WAL):
//
//	db := Open(crashed); db.Recover()
//	// page 1 has t1's and t3's bytes, page 5 has t3's bytes, page 2 is all
//	// zero again with LSN 773 (t2 was a loser: CLR@726, CLR@773, ABORT@820
//	// written and flushed, End 849)
//	// db.Active() is empty; the next Begin returns txn 4
//
// Constraints: none beyond the subproblems.
// Postgres reference: StartupXLOG() in src/backend/access/transam/xlog.c + PerformWalRecovery() in xlogrecovery.c
func (db *DB) Recover() error {
	a, err := Analyze(db.Log.Bytes(), db.Disk.Master())
	if err != nil {
		return err
	}
	db.Log.Truncate(a.End)
	if _, err := db.RedoPass(a.DPT, a.RedoLSN); err != nil {
		return err
	}
	db.mu.Lock()
	db.att = a.ATT
	db.nextTxn = a.NextTxn
	losers := make([]types.TxnID, 0, len(a.ATT))
	for t := range a.ATT {
		losers = append(losers, t)
	}
	db.mu.Unlock()
	if err := db.Undo(losers...); err != nil {
		return err
	}
	db.Log.FlushUpTo(db.Log.End())
	return nil
}
