package wal

import "github.com/natthphong/daTarbase/pkg/types"

// Problem 10.13 — Undo [Hard]
//
// Roll back transactions by walking their PrevLSN chains backwards, writing a compensation log record (CLR) for every change undone, and finish each with an ABORT record.
//
// Undo is used both for ROLLBACK at runtime and for the losers found by
// recovery. Every undo step is itself logged (a CLR, "redo-only": it is never
// undone). Its UndoNext points past the record it compensated, so if the
// system crashes DURING undo, the next recovery redoes the CLRs and continues
// at UndoNext instead of undoing the same change twice:
//
//	BEGIN@8 ← UPD@134 ← UPD@175 ← CLR@216 ← CLR@263 ← ABORT@310   (PrevLSN chain)
//	                               UndoNext=134  UndoNext=8
//
//	crash after CLR@216 → recovery: ATT {1: 216} → read CLR@216 → jump to 134,
//	never touching UPD@175 again.
//
// Rules:
//  1. Every txn in txns must be in the ATT, else return ErrNoTxn before doing
//     anything. Each txn's undo cursor starts at its ATT LastLSN.
//  2. Repeatedly pick the txn whose cursor is the LARGEST LSN (undo all losers
//     together in reverse log order) and read that record with db.Log.ReadAt.
//     If the record's Txn differs from the txn being undone → ErrBrokenTxnChain.
//     - RecUpdate: undo it. p := db.BP.Page(page). If the page is clean, first
//     log an FPI exactly as Update does (Problem 10.7, rule 2). Append
//     Record{Type: RecCLR, Txn: txn, PrevLSN: ATT[txn], Body: CLRBody{page,
//     off, Data: the update's Before, UndoNext: update.PrevLSN}}; copy Before
//     into the page; p.LSN = CLR LSN; ATT[txn] = CLR LSN; cursor = update.PrevLSN.
//     - RecCLR: never undo a CLR; cursor = its UndoNext.
//     - RecBegin: cursor = its PrevLSN (InvalidLSN).
//     - any other type → ErrBrokenTxnChain.
//  3. When a txn's cursor becomes InvalidLSN it is fully rolled back: append
//     Record{Type: RecAbort, Txn: txn, PrevLSN: ATT[txn]} and delete it from the ATT.
//  4. Nothing is flushed (an abort needn't be durable — if it is lost, recovery
//     simply rolls the txn back again).
//
// Example (fresh DB: t=1 BEGIN@8, FPI p1@37, UPD p1 off 0 "aa"@134, UPD p1 off 1 "bb"@175):
//
//	db.Undo(1)
//	// CLR@216 {p1 off 1 data "a\x00" UndoNext 134}  (47 bytes)
//	// CLR@263 {p1 off 0 data "\x00\x00" UndoNext 8}
//	// ABORT@310 (PrevLSN 263), End 339; page 1 all zero, LSN 263; ATT empty
//
// Constraints: each record of a txn is read at most once; O(#losers) to pick the next one is fine.
// Postgres reference: none — Postgres never undoes heap changes (MVCC makes aborted tuples invisible). ARIES section 6.4; Postgres-style undo exists only for sub-structures (e.g. in zheap).
func (db *DB) Undo(txns ...types.TxnID) error {
	cursor := map[types.TxnID]types.LSN{}
	for _, t := range txns {
		l, ok := db.att[t]
		if !ok {
			return ErrNoTxn
		}
		cursor[t] = l
	}
	for len(cursor) > 0 {
		var txn types.TxnID
		var at types.LSN
		for t, l := range cursor {
			if l >= at {
				txn, at = t, l
			}
		}
		if at != InvalidLSN {
			r, err := db.Log.ReadAt(at)
			if err != nil {
				return err
			}
			if r.Txn != txn {
				return ErrBrokenTxnChain
			}
			switch r.Type {
			case RecUpdate:
				u, err := DecodeUpdate(r.Body)
				if err != nil {
					return err
				}
				p := db.BP.Page(u.Page)
				if _, dirty := db.BP.dirty[u.Page]; !dirty {
					fpi := db.Log.Append(Record{Type: RecFPI, Body: FPIBody{Page: u.Page, Image: p.Data}.Encode()})
					db.BP.dirty[u.Page] = fpi
					p.LSN = fpi
				}
				clr := db.Log.Append(Record{
					Type: RecCLR, Txn: txn, PrevLSN: db.att[txn],
					Body: CLRBody{Page: u.Page, Off: u.Off, Data: u.Before, UndoNext: r.PrevLSN}.Encode(),
				})
				copy(p.Data[u.Off:], u.Before)
				p.LSN = clr
				db.att[txn] = clr
				at = r.PrevLSN
			case RecCLR:
				c, err := DecodeCLR(r.Body)
				if err != nil {
					return err
				}
				at = c.UndoNext
			case RecBegin:
				at = r.PrevLSN
			default:
				return ErrBrokenTxnChain
			}
			cursor[txn] = at
		}
		if at == InvalidLSN {
			db.Log.Append(Record{Type: RecAbort, Txn: txn, PrevLSN: db.att[txn]})
			delete(db.att, txn)
			delete(cursor, txn)
		}
	}
	return nil
}
