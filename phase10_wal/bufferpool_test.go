package wal

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

// dirtyPage makes page id dirty in the pool the way a logged change would:
// append a record, stamp the page with its LSN, record recLSN.
func dirtyPage(bp *BufferPool, id types.PageID, s string) types.LSN {
	lsn := bp.log.Append(Record{Txn: 1, Type: RecUpdate, Body: []byte(s)})
	p := bp.Page(id)
	copy(p.Data[:], s)
	p.LSN = lsn
	if _, ok := bp.dirty[id]; !ok {
		bp.dirty[id] = lsn
	}
	return lsn
}

func TestP10_6_FlushPage_WALRule(t *testing.T) {
	d := NewDisk()
	l := newLog(d)
	bp := NewBufferPool(d, l)
	l.Append(Record{Txn: 1, Type: RecBegin}) // @8
	lsn := dirtyPage(bp, 3, "page three")    // @37, 39 bytes → End 76
	l.Append(Record{Txn: 1, Type: RecBegin}) // @76, End 105

	if err := bp.FlushPage(3); err != nil {
		t.Fatalf("FlushPage = %v", err)
	}
	got := d.ReadPage(3)
	if got.LSN != lsn || string(got.Data[:10]) != "page three" {
		t.Fatalf("disk page 3 = LSN %d %q, want LSN %d %q", got.LSN, got.Data[:10], lsn, "page three")
	}
	if l.FlushedLSN() != 105 || l.Fsyncs() != 1 {
		t.Fatalf("flushed=%d fsyncs=%d, want 105/1 (whole buffer goes out with the page's record)", l.FlushedLSN(), l.Fsyncs())
	}
	if _, dirty := bp.RecLSN(3); dirty {
		t.Fatalf("page 3 still dirty after FlushPage")
	}
	if d.PageWrites() != 1 {
		t.Fatalf("PageWrites = %d, want 1", d.PageWrites())
	}
}

func TestP10_6_FlushPage_CleanOrUnknown(t *testing.T) {
	d := NewDisk()
	l := newLog(d)
	bp := NewBufferPool(d, l)
	dirtyPage(bp, 1, "x")
	if err := bp.FlushPage(1); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		id   types.PageID
	}{
		{"already clean", 1},
		{"never cached", 42},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l.Append(Record{Txn: 1, Type: RecBegin}) // something to flush, which must NOT be flushed
			if err := bp.FlushPage(tc.id); err != nil {
				t.Fatal(err)
			}
			if d.PageWrites() != 1 || l.Fsyncs() != 1 {
				t.Fatalf("PageWrites=%d Fsyncs=%d, want 1/1 (clean page: no I/O at all)", d.PageWrites(), l.Fsyncs())
			}
		})
	}
}

func TestP10_6_FlushPage_LogAlreadyDurable(t *testing.T) {
	d := NewDisk()
	l := newLog(d)
	bp := NewBufferPool(d, l)
	dirtyPage(bp, 2, "a")
	l.FlushUpTo(l.End())
	l.Append(Record{Txn: 1, Type: RecBegin}) // newer, unrelated record
	if err := bp.FlushPage(2); err != nil {
		t.Fatal(err)
	}
	if l.Fsyncs() != 1 || l.FlushedLSN() != 38 {
		t.Fatalf("Fsyncs=%d flushed=%d, want 1/38 (page's record was durable already)", l.Fsyncs(), l.FlushedLSN())
	}
}

func TestP10_6_FlushPage_WriteErrorKeepsDirty(t *testing.T) {
	d := NewDisk()
	l := newLog(d)
	bp := NewBufferPool(d, l)
	dirtyPage(bp, 4, "z")
	bp.Page(4).LSN = 10_000 // claims a record that does not exist → can never be durable
	err := bp.FlushPage(4)
	if !errors.Is(err, ErrWALNotFlushed) {
		t.Fatalf("err = %v, want ErrWALNotFlushed", err)
	}
	if rec, dirty := bp.RecLSN(4); !dirty || rec != 8 {
		t.Fatalf("page 4 dirty=%v recLSN=%d, want still dirty with recLSN 8", dirty, rec)
	}
	if d.PageWrites() != 0 {
		t.Fatalf("PageWrites = %d, want 0", d.PageWrites())
	}
}
