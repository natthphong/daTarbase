package wal

import (
	"maps"
	"slices"
	"sync"

	"github.com/natthphong/daTarbase/pkg/types"
)

// Everything in this file is PROVIDED: the toy page, and a "disk" that is the
// only thing surviving a crash.

// Page is one tiny data page. Like a Postgres page header's pd_lsn, LSN is the
// LSN of the last WAL record whose change is contained in Data.
type Page struct {
	LSN  types.LSN          // pd_lsn: last record applied to this page
	Data [PageDataSize]byte // page contents
}

// Disk is durable storage: data pages, the WAL bytes that were fsync'ed, and
// the master record (Postgres: pg_control's checkPoint field) that says where
// the last checkpoint record is. A crash keeps exactly what is in a Disk and
// loses everything else (buffer pool pages, unflushed WAL).
//
// It is safe for concurrent use.
type Disk struct {
	mu     sync.Mutex
	pages  map[types.PageID]Page
	wal    []byte
	master types.LSN

	pageWrites int
	tearNext   bool
}

// NewDisk returns an empty disk whose WAL holds just the 8-byte file header.
func NewDisk() *Disk {
	return &Disk{pages: map[types.PageID]Page{}, wal: []byte(FileHeader)}
}

// ReadPage returns the durable image of page id. A page never written reads as
// the zero Page (LSN 0, all-zero data).
func (d *Disk) ReadPage(id types.PageID) Page {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pages[id]
}

// WritePage stores p as the durable image of page id.
//
// It enforces the WAL rule: if p.LSN != InvalidLSN and the record at p.LSN is
// not yet durable (p.LSN >= length of the durable WAL), it writes nothing and
// returns ErrWALNotFlushed.
//
// If TearNextWrite was called, this write is torn: the stored image gets the
// new LSN and the new Data[:32], but keeps the OLD Data[32:] — like a crash
// in the middle of an 8 KB write that only got the first sectors out.
func (d *Disk) WritePage(id types.PageID, p Page) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p.LSN != InvalidLSN && int(p.LSN) >= len(d.wal) {
		return ErrWALNotFlushed
	}
	if d.tearNext {
		d.tearNext = false
		old := d.pages[id]
		copy(p.Data[PageDataSize/2:], old.Data[PageDataSize/2:])
	}
	d.pages[id] = p
	d.pageWrites++
	return nil
}

// TearNextWrite makes the next WritePage a torn write (see WritePage).
func (d *Disk) TearNextWrite() {
	d.mu.Lock()
	d.tearNext = true
	d.mu.Unlock()
}

// PageWrites returns how many WritePage calls succeeded.
func (d *Disk) PageWrites() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pageWrites
}

// WAL returns a copy of the durable WAL bytes (file header included).
func (d *Disk) WAL() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.wal)
}

// Master returns the LSN of the last completed checkpoint (InvalidLSN = none).
func (d *Disk) Master() types.LSN {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.master
}

// SetMaster durably records lsn as the last completed checkpoint.
func (d *Disk) SetMaster(lsn types.LSN) {
	d.mu.Lock()
	d.master = lsn
	d.mu.Unlock()
}

// Clone returns an independent deep copy (used to take crash images).
func (d *Disk) Clone() *Disk {
	d.mu.Lock()
	defer d.mu.Unlock()
	return &Disk{pages: maps.Clone(d.pages), wal: slices.Clone(d.wal), master: d.master}
}

// appendWAL appends data to the durable WAL. from must equal the current WAL
// length — the log is written strictly in order, without gaps or overlaps.
func (d *Disk) appendWAL(from types.LSN, data []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if int(from) != len(d.wal) {
		panic("wal: log written out of order (gap or overlap)")
	}
	d.wal = append(d.wal, data...)
}

// truncateWAL cuts the durable WAL to n bytes (n never below the file header).
func (d *Disk) truncateWAL(n types.LSN) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if int(n) < len(d.wal) {
		d.wal = d.wal[:max(int(n), len(FileHeader))]
	}
}

// AppendGarbageWAL appends raw bytes to the durable WAL without any record
// framing — tests use it to fake a torn or zero-filled log tail.
func (d *Disk) AppendGarbageWAL(b []byte) {
	d.mu.Lock()
	d.wal = append(d.wal, b...)
	d.mu.Unlock()
}
