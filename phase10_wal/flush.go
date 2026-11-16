package wal

import "github.com/natthphong/daTarbase/pkg/types"

// Problem 10.5 — FlushUpTo [Hard]
//
// Make the record at `lsn` (and everything before it) durable, sharing one fsync among all concurrent callers (group commit).
//
// An fsync costs milliseconds; appending a commit record costs nanoseconds. If
// every committing transaction did its own fsync, a disk doing 1000 fsyncs/s
// would cap the server at 1000 commits/s. Group commit fixes that: while one
// fsync is running, new commit records pile up in the buffer, and the NEXT
// fsync writes all of them at once.
//
//	t ─────────────────────────────────────────────────────────▶
//	A: append ─ fsync[A] ─────────────────┐ return
//	B:     append ─ wait ─────────────────┴ fsync[B,C,D] ─┐ return
//	C:         append ─ wait ─────────────────────────────┤ return
//	D:            append ─ wait ──────────────────────────┘ return   → 2 fsyncs, not 4
//
// Rules:
//  1. The record at lsn is durable once flushed > lsn (flushes always end on a
//     record boundary). Return immediately, without an fsync, if flushed > lsn
//     OR there is nothing buffered (flushed == len(buf)) — so FlushUpTo(InvalidLSN)
//     and FlushUpTo of an LSN ≥ End() with an empty buffer are no-ops.
//  2. Otherwise write out EVERYTHING buffered so far — buf[flushed:len(buf)], not
//     just up to lsn — with a single l.fsync(flushed, buf[flushed:end]) call, then
//     set flushed = end.
//  3. Call l.fsync WITHOUT holding l.mu: other goroutines must be able to Append
//     while the device is busy. (Slicing buf under the lock and passing the slice
//     out is safe: Append never rewrites bytes before len(buf).)
//  4. Never let two fsyncs run at the same time (use the flushing flag). A caller
//     that finds a flush in progress waits for it (l.cond); when it ends, every
//     waiter re-checks rule 1, and exactly one of the waiters still uncovered
//     becomes the next "leader" and flushes everything appended by then.
//  5. On return, flushed > lsn or the buffer is empty (all of it durable).
//
// Example:
//
//	// End()=100, flushed=8
//	l.FlushUpTo(37)   // fsync #1 writes [8,100) → flushed=100
//	l.FlushUpTo(71)   // already durable, no fsync
//	l.FlushUpTo(0)    // no-op
//
//	// group commit: A's fsync is blocked in the device; 9 more goroutines each
//	// Append a 29-byte record and call FlushUpTo on it; the device is released.
//	// → exactly 2 fsyncs in total: A's, then one covering all 9.
//
// Constraints: no busy waiting; must pass -race.
// Postgres reference: XLogFlush() in src/backend/access/transam/xlog.c (WALWriteLock + re-check of LogwrtResult)
func (l *Log) FlushUpTo(lsn types.LSN) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for {
		end := types.LSN(len(l.buf))
		if l.flushed > lsn || l.flushed == end {
			return
		}
		if l.flushing {
			l.cond.Wait()
			continue
		}
		l.flushing = true
		from, data := l.flushed, l.buf[l.flushed:end]
		l.mu.Unlock()
		l.fsync(from, data)
		l.mu.Lock()
		l.flushed = end
		l.flushing = false
		l.cond.Broadcast()
	}
}
