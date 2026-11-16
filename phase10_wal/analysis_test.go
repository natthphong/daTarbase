package wal

import (
	"errors"
	"maps"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func checkAnalysis(t *testing.T, got Analysis, att map[types.TxnID]types.LSN, dpt map[types.PageID]types.LSN, redo, end types.LSN, next types.TxnID) {
	t.Helper()
	if got.ATT == nil || got.DPT == nil {
		t.Fatalf("ATT/DPT must be non-nil maps")
	}
	if !maps.Equal(got.ATT, att) {
		t.Errorf("ATT = %v, want %v", got.ATT, att)
	}
	if !maps.Equal(got.DPT, dpt) {
		t.Errorf("DPT = %v, want %v", got.DPT, dpt)
	}
	if got.RedoLSN != redo || got.End != end || got.NextTxn != next {
		t.Errorf("RedoLSN/End/NextTxn = %d/%d/%d, want %d/%d/%d", got.RedoLSN, got.End, got.NextTxn, redo, end, next)
	}
}

func TestP10_11_Analyze_FromCheckpoint(t *testing.T) {
	d := scenario(t).Crash(0)
	a, err := Analyze(d.WAL(), d.Master())
	if err != nil {
		t.Fatal(err)
	}
	checkAnalysis(t, a,
		map[types.TxnID]types.LSN{2: 656},
		map[types.PageID]types.LSN{1: 37, 2: 204, 5: 518},
		37, 726, 4)
}

func TestP10_11_Analyze_NoCheckpoint(t *testing.T) {
	d := scenario(t).Crash(0)
	a, err := Analyze(d.WAL(), InvalidLSN)
	if err != nil {
		t.Fatal(err)
	}
	checkAnalysis(t, a,
		map[types.TxnID]types.LSN{2: 656},
		map[types.PageID]types.LSN{1: 37, 2: 204, 5: 518},
		37, 726, 4)

	empty, err := Analyze([]byte(FileHeader), InvalidLSN)
	if err != nil {
		t.Fatal(err)
	}
	checkAnalysis(t, empty, map[types.TxnID]types.LSN{}, map[types.PageID]types.LSN{}, 8, 8, 1)
}

func TestP10_11_Analyze_LeakedTail(t *testing.T) {
	db := scenario(t)
	// BEGIN t4@726 (29 B) and FPI p6@755 (97 B) reach the disk, UPD@852 only partially.
	d := db.Crash(29 + 97 + 10)
	a, err := Analyze(d.WAL(), d.Master())
	if err != nil {
		t.Fatal(err)
	}
	checkAnalysis(t, a,
		map[types.TxnID]types.LSN{2: 656, 4: 726},
		map[types.PageID]types.LSN{1: 37, 2: 204, 5: 518, 6: 755},
		37, 852, 5)
}

func TestP10_11_Analyze_CheckpointTablesAndEndRecords(t *testing.T) {
	// Hand-built log: the checkpoint says txn 7 and 9 are active and page 4
	// is dirty since 20; afterwards txn 7 aborts (ABORT = rollback finished),
	// txn 9 writes a CLR, a later checkpoint is ignored.
	ckpt := CheckpointBody{NextTxn: 10, ATT: []ATTEntry{{7, 20}, {9, 21}}, DPT: []DPTEntry{{4, 20}}}.Encode()
	log := logOf(
		Record{Txn: 3, Type: RecBegin},              // @8, before the checkpoint: ignored
		Record{Type: RecCheckpoint, Body: ckpt},     // @37 (29+48=77 B)
		Record{Txn: 7, PrevLSN: 20, Type: RecAbort}, // @114
		Record{Txn: 9, PrevLSN: 21, Type: RecCLR, Body: CLRBody{Page: 4, Off: 0, Data: []byte("q"), UndoNext: 8}.Encode()}, // @143 (29+17=46 B)
		Record{Type: RecFPI, Body: FPIBody{Page: 8}.Encode()},                                                              // @189
		Record{Type: RecCheckpoint, Body: CheckpointBody{NextTxn: 99}.Encode()},                                            // @286, ignored
	)
	a, err := Analyze(log, 37)
	if err != nil {
		t.Fatal(err)
	}
	checkAnalysis(t, a,
		map[types.TxnID]types.LSN{9: 143},
		map[types.PageID]types.LSN{4: 20, 8: 189},
		20, types.LSN(len(log)), 10)
}

func TestP10_11_Analyze_BadMaster(t *testing.T) {
	d := scenario(t).Crash(0)
	log := d.WAL()
	for _, m := range []types.LSN{372, 8, 5000} { // garbage offset, a BEGIN record, past the end
		if _, err := Analyze(log, m); !errors.Is(err, ErrNoCheckpoint) {
			t.Errorf("Analyze(master=%d) err = %v, want ErrNoCheckpoint", m, err)
		}
	}
}
