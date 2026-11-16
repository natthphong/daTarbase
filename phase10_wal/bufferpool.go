package wal

import (
	"slices"

	"github.com/natthphong/daTarbase/pkg/types"
)

// BufferPool caches pages in memory (no eviction, no pinning — phase 3 covers
// that) and tracks which of them are dirty. It is lost on a crash.
//
// dirty is the Dirty Page Table: page → recLSN, the LSN of the first record that
// dirtied the page since it was last written to disk. A page is "clean" when
// it is not in dirty.
//
// Not safe for concurrent use.
type BufferPool struct {
	disk  *Disk
	log   *Log
	pages map[types.PageID]*Page     // cached pages
	dirty map[types.PageID]types.LSN // DPT: page → recLSN
}

// NewBufferPool returns an empty pool over disk, flushing WAL through log.
func NewBufferPool(disk *Disk, log *Log) *BufferPool {
	return &BufferPool{disk: disk, log: log, pages: map[types.PageID]*Page{}, dirty: map[types.PageID]types.LSN{}}
}

// Page returns the cached page id, reading it from disk on first use. The
// returned pointer stays valid; modify it in place.
func (bp *BufferPool) Page(id types.PageID) *Page {
	if p, ok := bp.pages[id]; ok {
		return p
	}
	p := bp.disk.ReadPage(id)
	bp.pages[id] = &p
	return &p
}

// RecLSN returns the page's recLSN and whether the page is dirty.
func (bp *BufferPool) RecLSN(id types.PageID) (types.LSN, bool) {
	l, ok := bp.dirty[id]
	return l, ok
}

// DirtyPages returns the dirty page ids in ascending order.
func (bp *BufferPool) DirtyPages() []types.PageID {
	ids := make([]types.PageID, 0, len(bp.dirty))
	for id := range bp.dirty {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// FlushAll writes every dirty page with FlushPage, in ascending page order.
func (bp *BufferPool) FlushAll() error {
	for _, id := range bp.DirtyPages() {
		if err := bp.FlushPage(id); err != nil {
			return err
		}
	}
	return nil
}

// Problem 10.6 — FlushPage [Medium]
//
// Write a dirty page to disk, obeying the write-ahead rule: the WAL up to the page's LSN must be durable first.
//
// The WAL rule is the whole point of a write-ahead log. If a page reached disk
// before the record describing its change, a crash in between would leave a
// change on disk that the log knows nothing about — recovery could neither redo
// nor undo it.
//
//	page 3 in memory: LSN=134          WAL: |hdr|..|UPDATE@134|..|   flushed=37
//	FlushPage(3):  1. log.FlushUpTo(134)   → flushed ≥ 175
//	               2. disk.WritePage(3, p) → allowed now
//
// Rules:
//  1. If page id is not dirty (not in bp.dirty), do nothing and return nil —
//     no WAL flush, no disk write. (This includes pages that are not cached.)
//  2. Otherwise call bp.log.FlushUpTo(page.LSN) (Problem 10.5) FIRST, then
//     bp.disk.WritePage(id, *page). The provided Disk refuses (ErrWALNotFlushed)
//     to store a page whose LSN is not durable yet.
//  3. If WritePage fails, return its error and leave the page dirty.
//  4. On success remove id from bp.dirty (the page is clean again).
//
// Example:
//
//	// page 3 dirty, LSN 134, WAL End()=175, flushed=8
//	bp.FlushPage(3) // → nil; log fsync'ed to 175, disk page 3 has LSN 134, page clean
//	bp.FlushPage(3) // → nil; nothing happens (clean)
//
// Constraints: at most one FlushUpTo and one WritePage per call.
// Postgres reference: FlushBuffer() in src/backend/storage/buffer/bufmgr.c (XLogFlush(BufferGetLSN) before smgrwrite)
func (bp *BufferPool) FlushPage(id types.PageID) error {
	panic("TODO: Problem 10.6")
}
