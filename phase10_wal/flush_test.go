package wal

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP10_5_FlushUpTo_Basic(t *testing.T) {
	d := NewDisk()
	l := newLog(d)
	l.FlushUpTo(0)
	l.FlushUpTo(500)
	if l.Fsyncs() != 0 {
		t.Fatalf("empty buffer: Fsyncs = %d, want 0", l.Fsyncs())
	}
	l.Append(Record{Txn: 1, Type: RecBegin})
	l.Append(Record{Txn: 1, PrevLSN: 8, Type: RecUpdate, Body: []byte("hello")})
	l.Append(Record{Txn: 1, PrevLSN: 37, Type: RecCommit})

	steps := []struct {
		name        string
		lsn         types.LSN
		wantFlushed types.LSN
		wantFsyncs  int
	}{
		{"flush up to the 2nd record writes everything", 37, 100, 1},
		{"already durable", 71, 100, 1},
		{"invalid LSN is a no-op", 0, 100, 1},
		{"beyond End with empty buffer", 1000, 100, 1},
	}
	for _, s := range steps {
		l.FlushUpTo(s.lsn)
		if l.FlushedLSN() != s.wantFlushed || l.Fsyncs() != s.wantFsyncs {
			t.Fatalf("%s: flushed=%d fsyncs=%d, want %d/%d", s.name, l.FlushedLSN(), l.Fsyncs(), s.wantFlushed, s.wantFsyncs)
		}
	}
	if !bytes.Equal(d.WAL(), sample3()) {
		t.Fatalf("disk WAL =\n % x\nwant\n % x", d.WAL(), sample3())
	}

	// one more record, flushed by asking for an LSN past End
	l.Append(Record{Txn: 2, Type: RecBegin}) // @100
	l.FlushUpTo(5000)
	if l.FlushedLSN() != 129 || l.Fsyncs() != 2 || len(d.WAL()) != 129 {
		t.Fatalf("flushed=%d fsyncs=%d disk=%d, want 129/2/129", l.FlushedLSN(), l.Fsyncs(), len(d.WAL()))
	}
}

// waitFor polls cond for up to 2 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestP10_5_FlushUpTo_GroupCommit(t *testing.T) {
	l := newLog(NewDisk())
	entered := make(chan struct{}, 100)
	release := make(chan struct{})
	l.SyncHook = func() {
		entered <- struct{}{}
		<-release
	}
	var once sync.Once
	defer once.Do(func() { close(release) })

	var wg sync.WaitGroup
	commit := func(txn types.TxnID) {
		defer wg.Done()
		lsn := l.Append(Record{Txn: txn, Type: RecCommit})
		l.FlushUpTo(lsn)
		if l.FlushedLSN() <= lsn {
			t.Errorf("txn %d: FlushUpTo(%d) returned with flushed=%d", txn, lsn, l.FlushedLSN())
		}
	}
	wg.Add(1)
	go commit(1)
	<-entered // leader A is now inside the (blocked) fsync

	for i := 2; i <= 10; i++ {
		wg.Add(1)
		go commit(types.TxnID(i))
	}
	// all 9 followers must be able to append while the device is busy
	waitFor(t, "9 appends during a blocked fsync (is l.mu held during fsync?)", func() bool {
		return l.End() == 8+10*29
	})
	once.Do(func() { close(release) })
	wg.Wait()

	if got := l.Fsyncs(); got != 2 {
		t.Fatalf("Fsyncs = %d, want 2 (leader + one group flush for the 9 followers)", got)
	}
	if l.ConcurrentFsyncs() != 0 {
		t.Fatalf("ConcurrentFsyncs = %d, want 0", l.ConcurrentFsyncs())
	}
	if l.FlushedLSN() != 298 {
		t.Fatalf("FlushedLSN = %d, want 298", l.FlushedLSN())
	}
}

func TestP10_5_FlushUpTo_Stress(t *testing.T) {
	d := NewDisk()
	l := newLog(d)
	l.SyncHook = func() { time.Sleep(50 * time.Microsecond) }
	const workers, each = 16, 25
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				lsn := l.Append(Record{Txn: types.TxnID(w + 1), Type: RecUpdate, Body: []byte{byte(i)}})
				l.FlushUpTo(lsn)
				if wal := d.WAL(); types.LSN(len(wal)) <= lsn {
					t.Errorf("record %d not on disk after FlushUpTo (disk WAL len %d)", lsn, len(wal))
					return
				}
			}
		}()
	}
	wg.Wait()
	if l.ConcurrentFsyncs() != 0 {
		t.Fatalf("ConcurrentFsyncs = %d, want 0", l.ConcurrentFsyncs())
	}
	if f := l.Fsyncs(); f < 1 || f > workers*each {
		t.Fatalf("Fsyncs = %d, want between 1 and %d", f, workers*each)
	}
	recs, end, _ := ReadRecords(d.WAL(), FirstLSN)
	if len(recs) != workers*each || end != l.End() {
		t.Fatalf("disk has %d records ending at %d, want %d ending at %d", len(recs), end, workers*each, l.End())
	}
}
