package wal

import "github.com/natthphong/daTarbase/pkg/types"

// Problem 10.9 — RedoRecord [Medium]
//
// Re-apply one logged change to a page, but only if the page does not already contain it (page LSN test); full-page images are restored unconditionally.
//
// During redo the page on disk may be older than the record (the change never
// reached disk), or already contain it (the page was written before the crash).
// The page LSN tells which: a page with LSN ≥ record LSN already has the change.
// This makes redo IDEMPOTENT — running it twice, or crashing during recovery
// and running it again, gives the same page.
//
// Rules (p is the page the record touches — the caller fetched it):
//  1. RecUpdate: if p.LSN < rec.LSN, copy UpdateBody.After to p.Data[Off:] and
//     set p.LSN = rec.LSN → applied=true. Otherwise leave p alone → false.
//  2. RecCLR: same as RecUpdate, with CLRBody.Data as the bytes to write.
//  3. RecFPI: ALWAYS set p.Data = FPIBody.Image and p.LSN = rec.LSN → true.
//     The page on disk may be torn, so its LSN cannot be trusted; the image is
//     a known-good starting point and every later record re-applies on top.
//  4. Any other type (Begin, Commit, Abort, Checkpoint) → false, nil, p untouched.
//  5. If the body can't be decoded, or Off+len > PageDataSize → false,
//     ErrCorruptRecord, p untouched.
//
// Example (page {LSN 100, Data all 0}):
//
//	UPDATE@150 off 2 "ab"  → applied; LSN 150, Data[2:4]="ab"
//	UPDATE@150 again       → skipped (150 is not < 150)
//	UPDATE@120 off 0 "zz"  → skipped (120 < 150)
//	CLR@160 off 2 "\x00\x00" → applied; LSN 160, Data[2:4]=00 00
//	FPI@90 image "FPI…"    → applied even though 90 < 160; LSN 90
//
// Constraints: O(len(body)).
// Postgres reference: XLogReadBufferForRedo() (BLK_NEEDS_REDO / BLK_RESTORED / BLK_DONE) in src/backend/access/transam/xlogutils.c
func RedoRecord(p *Page, rec Record) (bool, error) {
	panic("TODO: Problem 10.9")
}

// Problem 10.12 — RedoPass [Medium]
//
// Repeat history: replay every page-changing record from `from` to the end of the valid log, filtered by the dirty page table.
//
// ARIES redo restores the database to exactly the state at the crash —
// including changes of transactions that will be rolled back afterwards
// ("repeating history"). Undo then works on a consistent state.
//
// Rules:
//  1. Read records with ReadRecords(db.Log.Bytes(), from) (Problem 10.4). Pass
//     its error through (return 0, err).
//  2. Skip records that don't touch a page (RecordPage ok=false).
//  3. Skip a record if its page is not in dpt, or rec.LSN < dpt[page]: the
//     change is already on disk. The filter applies to FPIs exactly like to
//     updates: an FPI older than recLSN that slipped through would roll the
//     page back, and the updates needed to bring it forward again would be
//     filtered out.
//  4. Otherwise apply it with RedoRecord (Problem 10.9) to db.BP.Page(page) and
//     count it if applied. Stop and return the error on a RedoRecord error.
//  5. Every page redo applied a record to must end up dirty: if it isn't in
//     db.BP.dirty yet, set db.BP.dirty[page] = dpt[page].
//  6. Return the number of applied records. Do not write anything to the log
//     or the disk.
//
// Example (after the 10.11 example's crash: DPT {1:37, 2:204, 5:518}, from 37,
// nothing on disk):
//
//	db.RedoPass(dpt, 37) // → 8 applied: FPI@37 UPD@134 FPI@204 UPD@301 UPD@477 FPI@518 UPD@615 UPD@656
//	db.RedoPass(dpt, 37) // → 8 again: each FPI resets its page to the image (LSN = FPI's LSN),
//	                     //   so the updates after it pass the LSN test once more.
//	                     //   The final pages are byte-for-byte identical.
//
// Constraints: one pass over the log.
// Postgres reference: PerformWalRecovery() / ApplyWalRecord() in src/backend/access/transam/xlogrecovery.c
func (db *DB) RedoPass(dpt map[types.PageID]types.LSN, from types.LSN) (int, error) {
	panic("TODO: Problem 10.12")
}
