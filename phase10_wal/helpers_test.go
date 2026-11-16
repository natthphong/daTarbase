package wal

import (
	"bytes"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

// logOf builds a raw log: file header + the given records appended at their
// natural offsets (each record's LSN is set to its offset).
func logOf(recs ...Record) []byte {
	b := []byte(FileHeader)
	for _, r := range recs {
		r.LSN = types.LSN(len(b))
		b = append(b, EncodeRecord(r)...)
	}
	return b
}

// sample3 is BEGIN@8 (29 B) + UPDATE@37 (34 B, body "hello") + COMMIT@71 (29 B); len 100.
func sample3() []byte {
	return logOf(
		Record{Txn: 1, Type: RecBegin},
		Record{Txn: 1, PrevLSN: 8, Type: RecUpdate, Body: []byte("hello")},
		Record{Txn: 1, PrevLSN: 37, Type: RecCommit},
	)
}

func lsnsOf(recs []Record) []types.LSN {
	out := make([]types.LSN, len(recs))
	for i, r := range recs {
		out[i] = r.LSN
	}
	return out
}

func equalLSNs(a, b []types.LSN) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func recordsEqual(a, b Record) bool {
	return a.LSN == b.LSN && a.PrevLSN == b.PrevLSN && a.Txn == b.Txn && a.Type == b.Type && bytes.Equal(a.Body, b.Body)
}

// pageData builds a 64-byte page image with s at offset off.
func pageData(off int, s string) [PageDataSize]byte {
	var d [PageDataSize]byte
	copy(d[off:], s)
	return d
}

// scenario builds the log used by the examples of Problems 10.11–10.14:
//
//	BEGIN t1@8, FPI p1@37, UPD t1 p1 off0 "t1"@134, BEGIN t2@175, FPI p2@204,
//	UPD t2 p2 off0 "t2"@301, COMMIT t1@342, CKPT@371, BEGIN t3@448,
//	UPD t3 p1 off2 "t3"@477, FPI p5@518, UPD t3 p5 off0 "t3"@615,
//	UPD t2 p2 off2 "xx"@656, COMMIT t3@697 (flushed: durable end 726),
//	BEGIN t4@726, FPI p6@755, UPD t4 p6@852 (NOT flushed, End 893).
//
// No data page is ever written to disk.
func scenario(t *testing.T) *DB {
	t.Helper()
	db := Open(NewDisk())
	must := func(_ types.LSN, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	t1 := db.Begin()
	must(db.Update(t1, 1, 0, []byte("t1")))
	t2 := db.Begin()
	must(db.Update(t2, 2, 0, []byte("t2")))
	must(db.Commit(t1))
	must(db.Checkpoint())
	t3 := db.Begin()
	must(db.Update(t3, 1, 2, []byte("t3")))
	must(db.Update(t3, 5, 0, []byte("t3")))
	must(db.Update(t2, 2, 2, []byte("xx")))
	must(db.Commit(t3))
	t4 := db.Begin()
	must(db.Update(t4, 6, 0, []byte("t4")))
	if db.Log.End() != 893 || db.Log.FlushedLSN() != 726 || db.Disk.Master() != 371 {
		t.Fatalf("scenario: End/flushed/master = %d/%d/%d, want 893/726/371",
			db.Log.End(), db.Log.FlushedLSN(), db.Disk.Master())
	}
	return db
}
