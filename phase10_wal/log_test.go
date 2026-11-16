package wal

import (
	"bytes"
	"sync"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP10_3_Append(t *testing.T) {
	d := NewDisk()
	l := newLog(d)
	steps := []struct {
		rec     Record
		wantLSN types.LSN
		wantEnd types.LSN
	}{
		{Record{Txn: 1, Type: RecBegin}, 8, 37},
		{Record{Txn: 1, PrevLSN: 8, Type: RecUpdate, Body: []byte("hello")}, 37, 71},
		{Record{LSN: 9999, Txn: 1, PrevLSN: 37, Type: RecCommit}, 71, 100}, // caller's LSN ignored
	}
	for _, s := range steps {
		if got := l.Append(s.rec); got != s.wantLSN {
			t.Fatalf("Append(%v) = %d, want %d", s.rec, got, s.wantLSN)
		}
		if got := l.End(); got != s.wantEnd {
			t.Fatalf("End() = %d, want %d", got, s.wantEnd)
		}
	}
	if !bytes.Equal(l.Bytes(), sample3()) {
		t.Fatalf("log bytes =\n % x\nwant\n % x", l.Bytes(), sample3())
	}
	r, err := l.ReadAt(71)
	if err != nil || r.LSN != 71 || r.PrevLSN != 37 || r.Type != RecCommit {
		t.Fatalf("ReadAt(71) = %v, %v; want COMMIT@71 prev 37", r, err)
	}
}

func TestP10_3_Append_NotDurable(t *testing.T) {
	d := NewDisk()
	l := newLog(d)
	l.Append(Record{Txn: 1, Type: RecBegin})
	l.Append(Record{Txn: 1, PrevLSN: 8, Type: RecCommit})
	if got := l.FlushedLSN(); got != 8 {
		t.Fatalf("FlushedLSN() = %d, want 8 (Append must not flush)", got)
	}
	if got := d.WAL(); string(got) != FileHeader {
		t.Fatalf("disk WAL = %q, want just the header %q", got, FileHeader)
	}
	if l.Fsyncs() != 0 {
		t.Fatalf("Fsyncs = %d, want 0", l.Fsyncs())
	}
}

func TestP10_3_Append_Concurrent(t *testing.T) {
	l := newLog(NewDisk())
	const workers, each = 8, 50
	var wg sync.WaitGroup
	lsns := make([][]types.LSN, workers)
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				lsns[w] = append(lsns[w], l.Append(Record{Txn: types.TxnID(w + 1), Type: RecBegin}))
			}
		}()
	}
	wg.Wait()
	if got, want := l.End(), types.LSN(8+workers*each*29); got != want {
		t.Fatalf("End() = %d, want %d", got, want)
	}
	seen := map[types.LSN]bool{}
	for w := range workers {
		for _, lsn := range lsns[w] {
			if seen[lsn] || (lsn-8)%29 != 0 {
				t.Fatalf("bad or duplicate LSN %d", lsn)
			}
			seen[lsn] = true
			r, err := l.ReadAt(lsn)
			if err != nil || r.Txn != types.TxnID(w+1) {
				t.Fatalf("ReadAt(%d) = %v, %v; want txn %d", lsn, r, err, w+1)
			}
		}
	}
}
