package wal

import "github.com/natthphong/daTarbase/pkg/types"

// Analysis is the result of the ARIES analysis pass.
type Analysis struct {
	ATT     map[types.TxnID]types.LSN  // losers: txns with no COMMIT/ABORT → LastLSN
	DPT     map[types.PageID]types.LSN // pages that may be dirty → recLSN
	RedoLSN types.LSN                  // where redo starts: min recLSN, or End if DPT is empty
	End     types.LSN                  // end of the valid log
	NextTxn types.TxnID                // first txn id that is safe to hand out again
}

// Problem 10.11 — Analyze [Hard]
//
// ARIES analysis pass: starting from the last checkpoint, rebuild the Active Transaction Table and the Dirty Page Table as of the crash.
//
// The checkpoint gives a snapshot of both tables at checkpoint time; scanning
// forward from it brings them up to the moment of the crash:
//
//	log:  ... | CKPT{ATT,DPT} | BEGIN t3 | UPD t3 p1 | FPI p5 | UPD t3 p5 | COMMIT t3 | ...
//	                ^ master
//
// Rules:
//  1. If master == InvalidLSN, start from FirstLSN with empty tables and
//     NextTxn = 1. Otherwise ReadRecords(log, master) (Problem 10.4): the first
//     record must exist, start at master and be RecCheckpoint, else
//     ErrNoCheckpoint (also when master is outside the log). Seed ATT, DPT and
//     NextTxn from its body, and continue with the records after it.
//  2. For each record, in log order:
//     - Txn != 0 and Type is Begin, Update or CLR → ATT[Txn] = rec.LSN.
//     - Commit or Abort → delete Txn from the ATT (the txn is finished).
//     - Update, CLR or FPI: if its page is not in the DPT, DPT[page] = rec.LSN
//     (an existing entry keeps its older recLSN).
//     - Checkpoint records after the master one are ignored.
//     - NextTxn = max(NextTxn, Txn+1) for every record with Txn != 0.
//  3. RedoLSN = the smallest recLSN in the DPT; if the DPT is empty, RedoLSN = End.
//  4. End = end of the valid log as returned by ReadRecords (a torn tail is not an error).
//  5. ATT and DPT are always non-nil maps.
//
// Example (BEGIN t1@8, FPI p1@37, UPD t1 p1@134, BEGIN t2@175, FPI p2@204,
// UPD t2 p2@301, COMMIT t1@342, CKPT@371 {NextTxn 3, ATT {2:301}, DPT {1:37, 2:204}},
// BEGIN t3@448, UPD t3 p1@477, FPI p5@518, UPD t3 p5@615, UPD t2 p2@656,
// COMMIT t3@697; durable end 726; master 371):
//
//	Analyze(log, 371) // → ATT {2:656}, DPT {1:37, 2:204, 5:518}, RedoLSN 37, End 726, NextTxn 4
//	Analyze(log, 0)   // → same ATT/DPT/RedoLSN/End/NextTxn, found by scanning from LSN 8
//	Analyze(log, 372) // → ErrNoCheckpoint
//
// Constraints: one forward pass from the checkpoint.
// Postgres reference: none — Postgres has no analysis pass (StartupXLOG starts redo at the checkpoint's redo pointer). See ARIES (Mohan et al., 1992), section 6.1.
func Analyze(log []byte, master types.LSN) (Analysis, error) {
	a := Analysis{ATT: map[types.TxnID]types.LSN{}, DPT: map[types.PageID]types.LSN{}, NextTxn: 1}
	start := FirstLSN
	if master != InvalidLSN {
		start = master
	}
	recs, end, err := ReadRecords(log, start)
	if err != nil {
		if master != InvalidLSN {
			return Analysis{}, ErrNoCheckpoint
		}
		return Analysis{}, err
	}
	a.End = end
	if master != InvalidLSN {
		if len(recs) == 0 || recs[0].Type != RecCheckpoint {
			return Analysis{}, ErrNoCheckpoint
		}
		cp, err := DecodeCheckpoint(recs[0].Body)
		if err != nil {
			return Analysis{}, err
		}
		a.NextTxn = max(a.NextTxn, cp.NextTxn)
		for _, e := range cp.ATT {
			a.ATT[e.Txn] = e.LastLSN
		}
		for _, e := range cp.DPT {
			a.DPT[e.Page] = e.RecLSN
		}
		recs = recs[1:]
	}
	for _, r := range recs {
		if r.Txn != types.InvalidTxnID {
			a.NextTxn = max(a.NextTxn, r.Txn+1)
		}
		switch r.Type {
		case RecBegin, RecUpdate, RecCLR:
			if r.Txn != types.InvalidTxnID {
				a.ATT[r.Txn] = r.LSN
			}
		case RecCommit, RecAbort:
			delete(a.ATT, r.Txn)
		}
		page, ok, err := RecordPage(r)
		if err != nil {
			return Analysis{}, err
		}
		if ok {
			if _, seen := a.DPT[page]; !seen {
				a.DPT[page] = r.LSN
			}
		}
	}
	a.RedoLSN = a.End
	for _, l := range a.DPT {
		a.RedoLSN = min(a.RedoLSN, l)
	}
	return a, nil
}
