package engine

import (
	"encoding/binary"
	"fmt"
	"os"
	"unsafe"

	tuple "github.com/natthphong/daTarbase/phase01_tuple"
	page "github.com/natthphong/daTarbase/phase02_page"
	storage "github.com/natthphong/daTarbase/phase03_storage"
	heap "github.com/natthphong/daTarbase/phase04_heap"

	"github.com/natthphong/daTarbase/pkg/types"
)

// This file is PROVIDED except for Problem 11.4 (TableHeap.Insert).
//
// A table is one file of 8 KB phase-2 slotted pages, cached by its own
// phase-3 buffer pool:
//
//	TableHeap ──► BufferPool (clock replacer) ──► walDisk ──► FileDisk  base/<relid>
//	                                               │
//	                                               └─ before writing a page: flush the WAL up to
//	                                                  the page's LSN (the write-ahead rule), stamp
//	                                                  the checksum into a copy (PageSetChecksumCopy);
//	                                                  after reading a page: verify it (page.Load)
//
// Heap page layout. Phase 2's page header has no pd_lsn field, so every heap
// page keeps its LSN in a reserved 8-byte item in slot 0 ("the LSN item"):
//
//	slot 0      8 bytes, uint64 LE: LSN of the last WAL record applied to this page
//	slot 1..n   heap tuples (phase-1 format: xmin, xmax, infomask, natts, hoff, data)
//
// User tuples therefore start at slot 1, and scans skip slot 0.

// lsnSlot is the slot that holds a heap page's LSN.
const lsnSlot types.SlotID = 0

// MaxTupleLen is the largest heap tuple the engine stores (there is no TOAST):
// an empty page minus the LSN item and its line pointer: 8180 - (8+4) = 8168.
const MaxTupleLen = page.MaxTupleLen - (8 + page.LinePointerLen)

func init() {
	if unsafe.Sizeof(page.Page{}) != types.PageSize {
		panic("engine: page.Page must be exactly one PageSize byte array")
	}
}

// pageView returns the phase-2 Page that lives INSIDE buf (no copy): changes
// made through it change buf. This is how Postgres works on shared buffers
// (a Page is just a pointer into the buffer). page.Page is a single
// [PageSize]byte array, so the conversion is exact.
func pageView(buf []byte) *page.Page {
	if len(buf) != types.PageSize {
		panic("engine: pageView needs a PageSize buffer")
	}
	return (*page.Page)(unsafe.Pointer(unsafe.SliceData(buf)))
}

// heapPage returns the page held by a pinned frame, formatting it first if it
// is still blank: an all-zero frame becomes an empty page, and an empty page
// gets its LSN item (LSN 0) in slot 0. Formatting is deterministic, so it is
// harmless to redo it after a crash.
func heapPage(f *storage.Frame) *page.Page {
	p := pageView(f.Data)
	if p.Lower() == 0 {
		copy(f.Data, page.NewPage().Bytes())
	}
	if p.NumSlots() == 0 {
		if _, err := p.InsertTuple(make([]byte, 8)); err != nil {
			panic("engine: cannot format heap page: " + err.Error())
		}
	}
	return p
}

// pageLSN reads the LSN item of a heap page (0 for an unformatted page).
func pageLSN(p *page.Page) types.LSN {
	if p.Lower() == 0 || p.NumSlots() == 0 {
		return 0
	}
	b, err := p.GetTuple(lsnSlot)
	if err != nil || len(b) != 8 {
		return 0
	}
	return types.LSN(binary.LittleEndian.Uint64(b))
}

// setPageLSN overwrites the LSN item of a formatted heap page (PageSetLSN).
func setPageLSN(p *page.Page, lsn types.LSN) {
	b, err := p.GetTuple(lsnSlot)
	if err != nil || len(b) != 8 {
		panic("engine: heap page has no LSN item")
	}
	binary.LittleEndian.PutUint64(b, uint64(lsn))
}

// setXmax overwrites bytes 4..8 of a stored tuple (its xmax) in place.
func setXmax(data []byte, xmax types.TxnID) {
	binary.LittleEndian.PutUint32(data[4:8], uint32(xmax))
}

// walDisk is the DiskManager of a table's buffer pool (see the diagram above).
type walDisk struct {
	*storage.FileDisk
	path string
	wal  *walWriter
	sync bool
}

// ReadPage reads a block and verifies it with page.Load (checksum + header).
// An all-zero block (allocated, never written) comes back as an empty page.
func (d *walDisk) ReadPage(id types.PageID, buf []byte) error {
	if err := d.FileDisk.ReadPage(id, buf); err != nil {
		return err
	}
	p, err := page.Load(buf, id)
	if err != nil {
		return fmt.Errorf("engine: %s block %d: %w", d.path, id, err)
	}
	copy(buf, p.Bytes())
	return nil
}

// WritePage enforces the write-ahead rule: the WAL is flushed up to the page's
// LSN BEFORE the page is written. The page is written with its checksum
// stamped into a private copy.
func (d *walDisk) WritePage(id types.PageID, buf []byte) error {
	p := pageView(buf)
	if p.Lower() == 0 {
		return d.FileDisk.WritePage(id, buf)
	}
	if err := d.wal.Flush(pageLSN(p)); err != nil {
		return err
	}
	return d.FileDisk.WritePage(id, p.Serialize(id))
}

// fsync makes the file's written pages durable (part of a checkpoint).
func (d *walDisk) fsync() error {
	if !d.sync {
		return nil
	}
	f, err := os.OpenFile(d.path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// TableHeap is the storage of one table: its file, buffer pool, free space map
// and visibility map.
type TableHeap struct {
	Def  *TableDef
	disk *walDisk
	pool *storage.BufferPool
	wal  *walWriter
	fsm  *heap.FSM             // free bytes per page as phase-4 categories; rebuilt on Open
	vm   map[types.PageID]bool // visibility map: true = every tuple on the page is visible to everyone (memory only)
}

// openHeap opens (creating if needed) the heap file of a table.
func openHeap(dir string, def *TableDef, w *walWriter, opts Options) (*TableHeap, error) {
	path := relPath(dir, def.RelID)
	fd, err := storage.OpenFileDisk(path)
	if err != nil {
		return nil, err
	}
	d := &walDisk{FileDisk: fd, path: path, wal: w, sync: !opts.NoSync}
	return &TableHeap{
		Def:  def,
		disk: d,
		pool: storage.NewBufferPool(opts.PoolFrames, d, storage.NewClockReplacer(opts.PoolFrames)),
		wal:  w,
		fsm:  heap.NewFSM(),
		vm:   map[types.PageID]bool{},
	}, nil
}

func relPath(dir string, relID uint32) string {
	return fmt.Sprintf("%s/base/%d", dir, relID)
}

// NumPages is the number of pages in the table's file.
func (h *TableHeap) NumPages() types.PageID { return types.PageID(h.disk.NumPages()) }

// rebuildFSM records the free space of every page (run by Open; the FSM is
// not persisted — Postgres keeps it in a separate "_fsm" fork).
func (h *TableHeap) rebuildFSM() error {
	h.fsm = heap.NewFSM()
	for pid := types.PageID(0); pid < h.NumPages(); pid++ {
		f, err := h.pool.FetchPage(pid)
		if err != nil {
			return err
		}
		h.fsm.Update(pid, heapPage(f).FreeSpace())
		h.pool.UnpinPage(pid, false)
	}
	return nil
}

// fetch copies out the header and bytes of the tuple at tid. ok=false when the
// slot holds no live tuple (unused, dead, or the LSN item).
func (h *TableHeap) fetch(tid types.TID) (hdr tuple.TupleHeader, data []byte, ok bool, err error) {
	if tid.Page >= h.NumPages() || tid.Slot == lsnSlot {
		return hdr, nil, false, nil
	}
	f, err := h.pool.FetchPage(tid.Page)
	if err != nil {
		return hdr, nil, false, err
	}
	defer h.pool.UnpinPage(tid.Page, false)
	b, gerr := heapPage(f).GetTuple(tid.Slot)
	if gerr != nil {
		return hdr, nil, false, nil
	}
	data = append([]byte(nil), b...)
	hdr, err = tuple.DecodeHeader(data)
	return hdr, data, err == nil, err
}

// tidValue encodes a TID as an int8 "ctid" column: page<<16 | slot.
func tidValue(t types.TID) types.Value {
	return types.Int64(int64(t.Page)<<16 | int64(t.Slot))
}

// valueTID decodes a column made by tidValue.
func valueTID(v types.Value) types.TID {
	return types.TID{Page: types.PageID(v.I >> 16), Slot: types.SlotID(v.I & 0xFFFF)}
}

// Problem 11.4 — TableHeap.Insert [Medium]
//
// Store a new tuple version stamped with xmin = xid: form it, find a page with room, insert it, WAL-log it, return its TID.
//
// This is heap_insert(): the row becomes bytes (phase 1), the free space map
// (phase 4) picks a page, the buffer pool (phase 3) pins it, the slotted page
// (phase 2) stores it, and the change is described in the WAL (phase 10)
// BEFORE the page is unpinned, with the record's LSN stamped on the page:
//
//	FormTuple ─► fsm.Search ─► FetchPage / NewPage ─► InsertTuple ─► wal.Append ─► setPageLSN ─► Unpin(dirty)
//
// Rules:
//  1. data := tuple.FormTuple(h.Def.Schema, row, xid). Its error is returned
//     as-is (ErrArity, ErrTypeMismatch, ErrNotNull, …) and nothing changes.
//  2. len(data) > MaxTupleLen (8168) → ErrTupleTooLarge, nothing changes.
//  3. Page choice: pid := h.fsm.Search(len(data)) (the FSM stores
//     FreeSpace(), i.e. the largest tuple a page can still take). A valid pid →
//     h.pool.FetchPage(pid). InvalidPageID → h.pool.NewPage() (the file grows
//     by one page). Either way get the page with heapPage(frame), which formats
//     a new page and gives it its LSN item in slot 0.
//  4. slot := InsertTuple(data) on that page.
//  5. lsn := h.wal.Append(xid, wal.RecUpdate, heapRecord{Op: opInsert,
//     Rel: h.Def.RelID, Page: pid, Slot: slot, Tuple: data}.encode()), then
//     setPageLSN(page, lsn).
//  6. The page is no longer all-visible: delete(h.vm, pid).
//     Record its new free space: h.fsm.Update(pid, page.FreeSpace()).
//  7. Unpin the page as dirty and return TID{pid, slot}. Every pinned page is
//     unpinned on every path (an error after pinning unpins it clean).
//  8. Do NOT flush the WAL or write the page: that happens at commit / eviction.
//
// Example (schema (id int4, pad text); a row with a 1000-byte pad is a 1024-byte
// tuple: hoff 16 | id 16..20 | 4-byte varlena header 20..24 | 1000 bytes):
//
//	empty table: rows 1..7 → (0,1) … (0,7); row 8 → (1,1)   (8172 bytes after the LSN item: 7 × 1028)
//	page 0 now has FreeSpace 972 (lower 40, upper 1016)
//	a row with a 500-byte pad (524-byte tuple) → (0,8)      (FSM category 30 ≥ ceil(524/32) = 17)
//	a row with an 8144-byte pad (8168-byte tuple) → (2,1)   exact fit on a new page
//	an 8145-byte pad → ErrTupleTooLarge;  ("x", "y") in an int4 column → tuple.ErrTypeMismatch
//
// Constraints: one page pinned at a time; one WAL record per call.
// Postgres reference: heap_insert() in src/backend/access/heap/heapam.c,
// RelationGetBufferForTuple() in hio.c
func (h *TableHeap) Insert(xid types.TxnID, row types.Row) (types.TID, error) {
	panic("TODO: Problem 11.4")
}
