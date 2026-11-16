package wal

import (
	"slices"
	"sync"
	"sync/atomic"

	"github.com/natthphong/daTarbase/pkg/types"
)

// Log is the in-memory WAL buffer in front of the durable WAL on a Disk
// (Postgres: the WAL insertion buffers + XLogCtl->LogwrtResult).
//
//	buf:  | file header | rec | rec | rec |  rec | rec |
//	      0             8                 ^flushed      ^End()
//	      <------ durable (on Disk) ------><-- buffered -->
//
// Bytes [0, flushed) are on the Disk; bytes [flushed, len(buf)) are lost on a
// crash. The fields are the state Problems 10.3 and 10.5 work with; you may add
// fields of your own.
type Log struct {
	disk *Disk

	mu       sync.Mutex // guards buf, flushed, flushing
	cond     *sync.Cond // cond.L == &mu; use it to wait for an in-progress flush
	buf      []byte     // the whole log: durable prefix + buffered tail
	flushed  types.LSN  // end of the durable prefix: buf[:flushed] is on disk
	flushing bool       // true while some goroutine is inside fsync

	// SyncHook, if set, is called by fsync while it "waits for the device".
	// Tests use it to make an fsync slow / blocking. Set it before concurrent use.
	SyncHook func()

	fsyncs   atomic.Int64
	inSync   atomic.Int32
	overlaps atomic.Int32
}

// newLog opens the log stored on d: everything on d is durable already.
func newLog(d *Disk) *Log {
	w := d.WAL()
	l := &Log{disk: d, buf: w, flushed: types.LSN(len(w))}
	l.cond = sync.NewCond(&l.mu)
	return l
}

// End returns the LSN the next appended record will get (= len(buf)).
func (l *Log) End() types.LSN {
	l.mu.Lock()
	defer l.mu.Unlock()
	return types.LSN(len(l.buf))
}

// FlushedLSN returns the end of the durable prefix.
func (l *Log) FlushedLSN() types.LSN {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.flushed
}

// Fsyncs returns how many times fsync was called.
func (l *Log) Fsyncs() int { return int(l.fsyncs.Load()) }

// ConcurrentFsyncs returns how many times fsync was entered while another
// fsync was still running. A correct FlushUpTo keeps this at 0.
func (l *Log) ConcurrentFsyncs() int { return int(l.overlaps.Load()) }

// Bytes returns a copy of the whole log, buffered tail included.
func (l *Log) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.buf)
}

// ReadAt decodes the record that starts at lsn (buffered records included).
// ErrInvalidLSN if lsn is outside [FirstLSN, End()).
func (l *Log) ReadAt(lsn types.LSN) (Record, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if lsn < FirstLSN || int(lsn) >= len(l.buf) {
		return Record{}, ErrInvalidLSN
	}
	r, _, err := DecodeRecord(l.buf[lsn:])
	return r, err
}

// Truncate throws away everything at and after end (a torn tail found by
// recovery), both in memory and on disk, so the next record is appended at end.
func (l *Log) Truncate(end types.LSN) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if int(end) < len(l.buf) {
		l.buf = l.buf[:end]
	}
	l.flushed = min(l.flushed, end)
	l.disk.truncateWAL(end)
}

// fsync writes data — which must be exactly buf[from:from+len(data)], with from
// == the current durable length — to the disk and waits for the device.
//
// It is SLOW (it calls SyncHook), so call it WITHOUT holding l.mu, and it must
// never run twice at the same time. It does not update l.flushed: that is the
// caller's job once it returns.
func (l *Log) fsync(from types.LSN, data []byte) {
	if l.inSync.Add(1) > 1 {
		l.overlaps.Add(1)
	}
	defer l.inSync.Add(-1)
	l.disk.appendWAL(from, data)
	if l.SyncHook != nil {
		l.SyncHook()
	}
	l.fsyncs.Add(1)
}

// Problem 10.3 — Append [Easy]
//
// Append a record to the in-memory log buffer and return its LSN (= the byte offset where it starts).
//
// Appending is cheap: it only copies bytes into memory. Nothing is durable until
// a later flush (Problem 10.5) writes the buffer out — that split is what makes
// group commit and the WAL rule possible.
//
// Rules:
//  1. The new record's LSN is the current end of the log, len(buf). Set rec.LSN
//     to it before encoding (whatever the caller put in rec.LSN is ignored).
//     PrevLSN, Txn, Type and Body are stored exactly as given.
//  2. Append EncodeRecord(rec) (Problem 10.1) to buf. Do not touch flushed and
//     do not write to the disk.
//  3. Safe for concurrent use: hold l.mu while reading and growing buf.
//
// Example (fresh log, End() == 8):
//
//	l.Append(Record{Txn: 1, Type: RecBegin})                    // → 8   (29 bytes, End=37)
//	l.Append(Record{Txn: 1, PrevLSN: 8, Type: RecUpdate,
//	                Body: []byte("hello")})                      // → 37  (34 bytes, End=71)
//	l.Append(Record{Txn: 1, PrevLSN: 37, Type: RecCommit})       // → 71  (End=100)
//	// FlushedLSN() is still 8, the disk's WAL is still just the 8-byte header.
//
// Constraints: amortised O(len(Body)).
// Postgres reference: XLogInsertRecord() / CopyXLogRecordToWAL() in src/backend/access/transam/xlog.c
func (l *Log) Append(rec Record) types.LSN {
	panic("TODO: Problem 10.3")
}
