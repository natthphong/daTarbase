package wal

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP10_7_Update_LogsFPIThenUpdate(t *testing.T) {
	db := Open(NewDisk())
	tx := db.Begin() // BEGIN@8
	lsn, err := db.Update(tx, 3, 4, []byte("hi"))
	if err != nil || lsn != 134 {
		t.Fatalf("Update = %d, %v; want 134, nil", lsn, err)
	}
	lsn, err = db.Update(tx, 3, 5, []byte("ey"))
	if err != nil || lsn != 175 {
		t.Fatalf("second Update = %d, %v; want 175, nil", lsn, err)
	}
	if db.Log.End() != 216 {
		t.Fatalf("End = %d, want 216", db.Log.End())
	}

	recs, _, _ := ReadRecords(db.Log.Bytes(), FirstLSN)
	wantHdr := []Record{
		{LSN: 8, Txn: 1, Type: RecBegin},
		{LSN: 37, Txn: 0, PrevLSN: 0, Type: RecFPI},
		{LSN: 134, Txn: 1, PrevLSN: 8, Type: RecUpdate},
		{LSN: 175, Txn: 1, PrevLSN: 134, Type: RecUpdate},
	}
	if len(recs) != len(wantHdr) {
		t.Fatalf("log has %v, want 4 records", recs)
	}
	for i, w := range wantHdr {
		r := recs[i]
		if r.LSN != w.LSN || r.Txn != w.Txn || r.PrevLSN != w.PrevLSN || r.Type != w.Type {
			t.Fatalf("record %d = %v, want %v", i, r, w)
		}
	}
	fpi, _ := DecodeFPI(recs[1].Body)
	if fpi.Page != 3 || fpi.Image != ([PageDataSize]byte{}) {
		t.Fatalf("FPI = page %d image %x, want page 3 all-zero image (state BEFORE the change)", fpi.Page, fpi.Image[:8])
	}
	u1, _ := DecodeUpdate(recs[2].Body)
	if u1.Page != 3 || u1.Off != 4 || string(u1.Before) != "\x00\x00" || string(u1.After) != "hi" {
		t.Fatalf("update 1 = %+v", u1)
	}
	u2, _ := DecodeUpdate(recs[3].Body)
	if u2.Page != 3 || u2.Off != 5 || string(u2.Before) != "i\x00" || string(u2.After) != "ey" {
		t.Fatalf("update 2 = %+v, want before %q after %q", u2, "i\x00", "ey")
	}

	p := db.BP.Page(3)
	if p.LSN != 175 || p.Data != pageData(4, "hey") {
		t.Fatalf("page 3 = LSN %d %q, want LSN 175 with \"hey\" at 4", p.LSN, p.Data[:8])
	}
	if rec, dirty := db.BP.RecLSN(3); !dirty || rec != 37 {
		t.Fatalf("recLSN = %d (dirty %v), want 37 (the FPI)", rec, dirty)
	}
	if got := db.Active()[tx]; got != 175 {
		t.Fatalf("ATT[%d] = %d, want 175", tx, got)
	}
	if db.Log.FlushedLSN() != 8 || db.Disk.PageWrites() != 0 {
		t.Fatalf("Update must not flush anything (flushed=%d writes=%d)", db.Log.FlushedLSN(), db.Disk.PageWrites())
	}
}

func TestP10_7_Update_FPIAgainAfterPageWrite(t *testing.T) {
	db := Open(NewDisk())
	tx := db.Begin()
	db.Update(tx, 0, 0, []byte("AB")) // FPI@37, UPD@134
	if err := db.BP.FlushPage(0); err != nil {
		t.Fatal(err)
	}
	// clean again → the next change must log a new image, which already contains "AB"
	lsn, err := db.Update(tx, 0, 2, []byte("C")) // FPI@175, UPD@272
	if err != nil || lsn != 272 {
		t.Fatalf("Update = %d, %v; want 272", lsn, err)
	}
	r, _ := db.Log.ReadAt(175)
	fpi, err := DecodeFPI(r.Body)
	if r.Type != RecFPI || err != nil || fpi.Image != pageData(0, "AB") {
		t.Fatalf("record@175 = %v, image %q; want FPI with \"AB\"", r, fpi.Image[:4])
	}
	if rec, _ := db.BP.RecLSN(0); rec != 175 {
		t.Fatalf("recLSN = %d, want 175", rec)
	}
	u, _ := db.Log.ReadAt(272)
	if u.PrevLSN != 134 {
		t.Fatalf("update PrevLSN = %d, want 134 (FPIs are not in the txn chain)", u.PrevLSN)
	}
}

func TestP10_7_Update_Errors(t *testing.T) {
	db := Open(NewDisk())
	tx := db.Begin()
	tests := []struct {
		name string
		txn  types.TxnID
		off  int
		data []byte
		want error
	}{
		{"unknown txn", 99, 0, []byte("x"), ErrNoTxn},
		{"empty data", tx, 0, nil, ErrBadRange},
		{"one past the end", tx, 63, []byte("xy"), ErrBadRange},
		{"offset beyond page", tx, 64, []byte("x"), ErrBadRange},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Update(tc.txn, 3, tc.off, tc.data)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if db.Log.End() != 37 {
				t.Fatalf("End = %d, want 37 (nothing logged on error)", db.Log.End())
			}
			if _, dirty := db.BP.RecLSN(3); dirty {
				t.Fatalf("page 3 dirty after a failed update")
			}
		})
	}
	// exact fit at the end of the page is fine
	if _, err := db.Update(tx, 3, 62, []byte("xy")); err != nil {
		t.Fatalf("Update at off 62 len 2 = %v, want nil", err)
	}
	if got := db.BP.Page(3).Data[62:]; string(got) != "xy" {
		t.Fatalf("bytes 62..64 = %q, want %q", got, "xy")
	}
}

func TestP10_8_Commit(t *testing.T) {
	db := Open(NewDisk())
	tx := db.Begin()
	db.Update(tx, 3, 4, []byte("hi"))
	db.Update(tx, 3, 5, []byte("ey"))
	lsn, err := db.Commit(tx)
	if err != nil || lsn != 216 {
		t.Fatalf("Commit = %d, %v; want 216, nil", lsn, err)
	}
	if db.Log.FlushedLSN() != 245 || db.Log.Fsyncs() != 1 {
		t.Fatalf("flushed=%d fsyncs=%d, want 245/1", db.Log.FlushedLSN(), db.Log.Fsyncs())
	}
	r, _ := db.Log.ReadAt(216)
	if r.Type != RecCommit || r.Txn != tx || r.PrevLSN != 175 {
		t.Fatalf("commit record = %v, want COMMIT txn %d prev 175", r, tx)
	}
	if len(db.Active()) != 0 {
		t.Fatalf("ATT = %v, want empty", db.Active())
	}
	if db.Disk.PageWrites() != 0 {
		t.Fatalf("commit wrote %d data pages, want 0 (no-force)", db.Disk.PageWrites())
	}
	if !bytes.Equal(db.Disk.WAL(), db.Log.Bytes()) {
		t.Fatalf("disk WAL differs from log after commit")
	}
	if _, err := db.Commit(tx); !errors.Is(err, ErrNoTxn) {
		t.Fatalf("second Commit err = %v, want ErrNoTxn", err)
	}
	if db.Log.End() != 245 {
		t.Fatalf("End = %d, want 245 (failed commit appends nothing)", db.Log.End())
	}
}

func TestP10_8_Commit_GroupCommit(t *testing.T) {
	db := Open(NewDisk())
	var txns []types.TxnID
	for i := range 10 {
		tx := db.Begin()
		db.Update(tx, types.PageID(i), 0, []byte{byte('a' + i)})
		txns = append(txns, tx)
	}
	end0 := db.Log.End()

	entered := make(chan struct{}, 100)
	release := make(chan struct{})
	db.Log.SyncHook = func() {
		entered <- struct{}{}
		<-release
	}
	var once sync.Once
	defer once.Do(func() { close(release) })

	var wg sync.WaitGroup
	lsns := make([]types.LSN, len(txns))
	commit := func(i int) {
		defer wg.Done()
		lsn, err := db.Commit(txns[i])
		if err != nil {
			t.Errorf("Commit(%d) = %v", txns[i], err)
		}
		lsns[i] = lsn
	}
	wg.Add(1)
	go commit(0)
	<-entered
	for i := 1; i < len(txns); i++ {
		wg.Add(1)
		go commit(i)
	}
	waitFor(t, "9 commit records appended during a blocked fsync (is db.mu or l.mu held during the flush?)", func() bool {
		return db.Log.End() == end0+9*29+29
	})
	once.Do(func() { close(release) })
	wg.Wait()

	if got := db.Log.Fsyncs(); got != 2 {
		t.Fatalf("Fsyncs = %d, want 2", got)
	}
	for i, lsn := range lsns {
		if lsn >= db.Log.FlushedLSN() {
			t.Fatalf("commit of txn %d at %d not durable (flushed %d)", txns[i], lsn, db.Log.FlushedLSN())
		}
	}
	if len(db.Active()) != 0 {
		t.Fatalf("ATT = %v, want empty", db.Active())
	}
}

func TestP10_10_Checkpoint(t *testing.T) {
	db := Open(NewDisk())
	t1 := db.Begin()
	db.Update(t1, 1, 0, []byte("t1"))
	t2 := db.Begin()
	db.Update(t2, 2, 0, []byte("t2"))
	db.Commit(t1)
	writes := db.Disk.PageWrites()

	lsn, err := db.Checkpoint()
	if err != nil || lsn != 371 {
		t.Fatalf("Checkpoint = %d, %v; want 371", lsn, err)
	}
	if db.Log.End() != 448 || db.Log.FlushedLSN() != 448 {
		t.Fatalf("End/flushed = %d/%d, want 448/448", db.Log.End(), db.Log.FlushedLSN())
	}
	if db.Disk.Master() != 371 {
		t.Fatalf("Master = %d, want 371", db.Disk.Master())
	}
	if db.Disk.PageWrites() != writes {
		t.Fatalf("checkpoint wrote %d pages, want 0 (fuzzy)", db.Disk.PageWrites()-writes)
	}
	r, _ := db.Log.ReadAt(371)
	if r.Type != RecCheckpoint || r.Txn != 0 || r.PrevLSN != 0 {
		t.Fatalf("record@371 = %v, want CHECKPOINT txn 0 prev 0", r)
	}
	want := CheckpointBody{NextTxn: 3, ATT: []ATTEntry{{2, 301}}, DPT: []DPTEntry{{1, 37}, {2, 204}}}.Encode()
	if !bytes.Equal(r.Body, want) {
		t.Fatalf("body =\n % x\nwant\n % x", r.Body, want)
	}
	if _, dirty := db.BP.RecLSN(1); !dirty {
		t.Fatalf("checkpoint must not clean pages")
	}
}

func TestP10_10_Checkpoint_Empty(t *testing.T) {
	db := Open(NewDisk())
	lsn, err := db.Checkpoint()
	if err != nil || lsn != 8 {
		t.Fatalf("Checkpoint = %d, %v; want 8", lsn, err)
	}
	r, _ := db.Log.ReadAt(8)
	cp, err := DecodeCheckpoint(r.Body)
	if err != nil || cp.NextTxn != 1 || len(cp.ATT) != 0 || len(cp.DPT) != 0 {
		t.Fatalf("checkpoint body = %+v, %v; want NextTxn 1, empty tables", cp, err)
	}
	if db.Disk.Master() != 8 || db.Log.FlushedLSN() != 8+29+12 {
		t.Fatalf("master=%d flushed=%d, want 8/49", db.Disk.Master(), db.Log.FlushedLSN())
	}
}

// The master record must only move after the checkpoint record is durable.
func TestP10_10_Checkpoint_MasterAfterFlush(t *testing.T) {
	db := Open(NewDisk())
	db.Begin()
	var masterDuringFsync types.LSN = 12345
	db.Log.SyncHook = func() { masterDuringFsync = db.Disk.Master() }
	lsn, _ := db.Checkpoint()
	if masterDuringFsync != InvalidLSN {
		t.Fatalf("master was %d while the checkpoint record was being fsync'ed, want 0 (set it after the flush)", masterDuringFsync)
	}
	if db.Disk.Master() != lsn {
		t.Fatalf("master = %d, want %d", db.Disk.Master(), lsn)
	}
}

func TestP10_8_Commit_ConcurrentStress(t *testing.T) {
	db := Open(NewDisk())
	db.Log.SyncHook = func() { time.Sleep(100 * time.Microsecond) }
	const n = 64
	txns := make([]types.TxnID, n)
	for i := range txns {
		txns[i] = db.Begin()
		db.Update(txns[i], types.PageID(i), 0, []byte("v"))
	}
	var wg sync.WaitGroup
	for _, tx := range txns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lsn, err := db.Commit(tx)
			if err != nil {
				t.Errorf("Commit(%d) = %v", tx, err)
				return
			}
			if got := types.LSN(len(db.Disk.WAL())); got <= lsn {
				t.Errorf("Commit(%d) returned before its record @%d was on disk (disk WAL %d bytes)", tx, lsn, got)
			}
		}()
	}
	wg.Wait()
	if db.Log.ConcurrentFsyncs() != 0 {
		t.Fatalf("ConcurrentFsyncs = %d, want 0", db.Log.ConcurrentFsyncs())
	}
	if f := db.Log.Fsyncs(); f < 1 || f > n {
		t.Fatalf("Fsyncs = %d, want 1..%d", f, n)
	}
	if len(db.Active()) != 0 {
		t.Fatalf("ATT = %v, want empty", db.Active())
	}
	recs, _, _ := ReadRecords(db.Disk.WAL(), FirstLSN)
	commits := 0
	for _, r := range recs {
		if r.Type == RecCommit {
			commits++
		}
	}
	if commits != n {
		t.Fatalf("%d COMMIT records on disk, want %d", commits, n)
	}
}
