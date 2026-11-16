package wal

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

type recSummary struct {
	LSN      types.LSN
	Type     RecordType
	Txn      types.TxnID
	PrevLSN  types.LSN
	Page     types.PageID
	Off      uint16
	Data     string
	UndoNext types.LSN
}

func summarize(t *testing.T, log []byte, from types.LSN) []recSummary {
	t.Helper()
	recs, _, err := ReadRecords(log, from)
	if err != nil {
		t.Fatal(err)
	}
	var out []recSummary
	for _, r := range recs {
		s := recSummary{LSN: r.LSN, Type: r.Type, Txn: r.Txn, PrevLSN: r.PrevLSN}
		switch r.Type {
		case RecCLR:
			c, _ := DecodeCLR(r.Body)
			s.Page, s.Off, s.Data, s.UndoNext = c.Page, c.Off, string(c.Data), c.UndoNext
		case RecFPI:
			f, _ := DecodeFPI(r.Body)
			s.Page = f.Page
		}
		out = append(out, s)
	}
	return out
}

func checkRecs(t *testing.T, got, want []recSummary) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("records =\n %+v\nwant\n %+v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("record %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestP10_13_Undo_OneTxn(t *testing.T) {
	db := Open(NewDisk())
	tx := db.Begin()
	db.Update(tx, 1, 0, []byte("aa")) // FPI@37, UPD@134
	db.Update(tx, 1, 1, []byte("bb")) // UPD@175, End 216
	if err := db.Undo(tx); err != nil {
		t.Fatal(err)
	}
	checkRecs(t, summarize(t, db.Log.Bytes(), 216), []recSummary{
		{LSN: 216, Type: RecCLR, Txn: 1, PrevLSN: 175, Page: 1, Off: 1, Data: "a\x00", UndoNext: 134},
		{LSN: 263, Type: RecCLR, Txn: 1, PrevLSN: 216, Page: 1, Off: 0, Data: "\x00\x00", UndoNext: 8},
		{LSN: 310, Type: RecAbort, Txn: 1, PrevLSN: 263},
	})
	if db.Log.End() != 339 {
		t.Fatalf("End = %d, want 339", db.Log.End())
	}
	if p := *db.BP.Page(1); p != (Page{LSN: 263}) {
		t.Fatalf("page 1 = LSN %d %q, want LSN 263 all zero", p.LSN, p.Data[:4])
	}
	if len(db.Active()) != 0 {
		t.Fatalf("ATT = %v, want empty", db.Active())
	}
	if db.Log.FlushedLSN() != 8 {
		t.Fatalf("Undo must not flush (flushed = %d)", db.Log.FlushedLSN())
	}
}

func TestP10_13_Undo_InterleavedLosersReverseOrder(t *testing.T) {
	db := Open(NewDisk())
	a := db.Begin()                  // BEGIN a@8
	b := db.Begin()                  // BEGIN b@37
	db.Update(a, 1, 0, []byte("A1")) // FPI p1@66, UPD a@163
	db.Update(b, 2, 0, []byte("B1")) // FPI p2@204, UPD b@301
	db.Update(a, 1, 2, []byte("A2")) // UPD a@342
	db.Update(b, 2, 2, []byte("B2")) // UPD b@383, End 424
	keeper := db.Begin()             // BEGIN c@424, must survive
	db.Update(keeper, 3, 0, []byte("K"))

	if err := db.Undo(a, b); err != nil {
		t.Fatal(err)
	}
	// keeper: BEGIN c@424, FPI p3@453, UPD c@550 → End 589 before the undo
	got := summarize(t, db.Log.Bytes(), 589)
	checkRecs(t, got, []recSummary{
		{LSN: 589, Type: RecCLR, Txn: b, PrevLSN: 383, Page: 2, Off: 2, Data: "\x00\x00", UndoNext: 301},
		{LSN: 636, Type: RecCLR, Txn: a, PrevLSN: 342, Page: 1, Off: 2, Data: "\x00\x00", UndoNext: 163},
		{LSN: 683, Type: RecCLR, Txn: b, PrevLSN: 589, Page: 2, Off: 0, Data: "\x00\x00", UndoNext: 37},
		{LSN: 730, Type: RecCLR, Txn: a, PrevLSN: 636, Page: 1, Off: 0, Data: "\x00\x00", UndoNext: 8},
		{LSN: 777, Type: RecAbort, Txn: b, PrevLSN: 683},
		{LSN: 806, Type: RecAbort, Txn: a, PrevLSN: 730},
	})
	if att := db.Active(); len(att) != 1 || att[keeper] == 0 {
		t.Fatalf("ATT = %v, want only txn %d", att, keeper)
	}
	if db.BP.Page(1).Data != ([PageDataSize]byte{}) || db.BP.Page(2).Data != ([PageDataSize]byte{}) {
		t.Fatalf("pages 1/2 not restored")
	}
	if db.BP.Page(3).Data != pageData(0, "K") {
		t.Fatalf("page 3 = %q, want keeper's \"K\"", db.BP.Page(3).Data[:2])
	}
}

func TestP10_13_Undo_CleanPageGetsFPI(t *testing.T) {
	db := Open(NewDisk())
	tx := db.Begin()
	db.Update(tx, 4, 0, []byte("xy")) // FPI@37, UPD@134, End 175
	if err := db.BP.FlushPage(4); err != nil {
		t.Fatal(err)
	}
	if err := db.Abort(tx); err != nil {
		t.Fatal(err)
	}
	checkRecs(t, summarize(t, db.Log.Bytes(), 175), []recSummary{
		{LSN: 175, Type: RecFPI, Page: 4},
		{LSN: 272, Type: RecCLR, Txn: 1, PrevLSN: 134, Page: 4, Off: 0, Data: "\x00\x00", UndoNext: 8},
		{LSN: 319, Type: RecAbort, Txn: 1, PrevLSN: 272},
	})
	if rec, dirty := db.BP.RecLSN(4); !dirty || rec != 175 {
		t.Fatalf("page 4 recLSN = %d dirty=%v, want 175", rec, dirty)
	}
	r, _ := db.Log.ReadAt(175)
	f, _ := DecodeFPI(r.Body)
	if f.Image != pageData(0, "xy") {
		t.Fatalf("FPI image = %q, want the page before the CLR (\"xy\")", f.Image[:2])
	}
}

func TestP10_13_Undo_Errors(t *testing.T) {
	db := Open(NewDisk())
	tx := db.Begin()
	db.Update(tx, 1, 0, []byte("aa"))
	end := db.Log.End()
	if err := db.Undo(tx, 42); !errors.Is(err, ErrNoTxn) {
		t.Fatalf("err = %v, want ErrNoTxn", err)
	}
	if db.Log.End() != end || db.BP.Page(1).Data != pageData(0, "aa") || db.Active()[tx] == 0 {
		t.Fatalf("failed Undo changed state")
	}
}

// Crash in the middle of undo, then recover by hand: every update must be
// compensated exactly once.
func TestP10_13_Undo_CrashDuringUndo(t *testing.T) {
	db := Open(NewDisk())
	tx := db.Begin()                   // @8
	db.Update(tx, 1, 0, []byte("one")) // FPI@37, UPD@134
	db.Update(tx, 1, 3, []byte("two"))
	db.Update(tx, 1, 6, []byte("six"))
	beforeUndo := db.Log.End()
	if err := db.Undo(tx); err != nil {
		t.Fatal(err)
	}
	db.Log.FlushUpTo(db.Log.End())
	recs, _, _ := ReadRecords(db.Log.Bytes(), beforeUndo)
	if len(recs) != 4 || recs[0].Type != RecCLR {
		t.Fatalf("undo wrote %v, want 3 CLRs + ABORT", recs)
	}

	// crash right after the first CLR became durable
	d := db.Disk.Clone()
	d.truncateWAL(recs[1].LSN)
	db2 := Open(d)
	a, err := Analyze(db2.Log.Bytes(), d.Master())
	if err != nil {
		t.Fatal(err)
	}
	if a.ATT[tx] != recs[0].LSN {
		t.Fatalf("after crash ATT[%d] = %d, want the CLR %d", tx, a.ATT[tx], recs[0].LSN)
	}
	if _, err := db2.RedoPass(a.DPT, a.RedoLSN); err != nil {
		t.Fatal(err)
	}
	db2.att = a.ATT
	if err := db2.Undo(tx); err != nil {
		t.Fatal(err)
	}
	all, _, _ := ReadRecords(db2.Log.Bytes(), FirstLSN)
	clrs := 0
	undone := map[types.LSN]int{}
	for _, r := range all {
		if r.Type == RecCLR {
			clrs++
			c, _ := DecodeCLR(r.Body)
			undone[c.UndoNext]++
		}
	}
	if clrs != 3 {
		t.Fatalf("%d CLRs in the log, want 3 (no update undone twice)", clrs)
	}
	for next, n := range undone {
		if n != 1 {
			t.Fatalf("UndoNext %d appears %d times, want once", next, n)
		}
	}
	if p := db2.BP.Page(1); p.Data != ([PageDataSize]byte{}) {
		t.Fatalf("page 1 = %q, want all zero", p.Data[:9])
	}
	if len(db2.Active()) != 0 {
		t.Fatalf("ATT = %v, want empty", db2.Active())
	}
}
