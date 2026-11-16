// Package storage is the bottom of the database: it moves 8 KB pages between
// disk and memory.
//
//	                +-----------------------------+
//	FetchPage(7) -> |  BufferPool (shared_buffers) |  frames, page table, pins,
//	                |    + Replacer (clock/LRU)    |  dirty bits, eviction
//	                +--------------+--------------+
//	                               | ReadPage / WritePage / AllocatePage
//	                +--------------v--------------+
//	                | DiskManager (smgr / md.c)   |  FileDisk, SegmentedDisk
//	                +-----------------------------+
//
// Provided here: the DiskManager interface, the errors, and MemDisk — an
// in-memory, instrumented DiskManager that counts every read and write so the
// tests can prove "a cache hit does not touch the disk".
package storage

import (
	"encoding/binary"
	"errors"
	"sync"

	"github.com/natthphong/daTarbase/pkg/types"
)

// PageSize is the size of one page in bytes (Postgres BLCKSZ).
const PageSize = types.PageSize

var (
	// ErrPageNotFound: the page does not exist on disk (beyond end of file).
	ErrPageNotFound = errors.New("storage: page not found")
	// ErrBadBufferSize: a page buffer is not exactly PageSize bytes.
	ErrBadBufferSize = errors.New("storage: buffer must be exactly PageSize bytes")
	// ErrShortWrite: the OS accepted zero bytes of a write and reported no error.
	ErrShortWrite = errors.New("storage: short write")
	// ErrTooManyPages: the relation already has the maximum number of pages.
	ErrTooManyPages = errors.New("storage: relation has too many pages")
	// ErrNoFreeFrames: every buffer frame is pinned, nothing can be evicted.
	ErrNoFreeFrames = errors.New("storage: no free or evictable frame")
	// ErrPageNotResident: the page is not in the buffer pool.
	ErrPageNotResident = errors.New("storage: page not in buffer pool")
	// ErrNotPinned: UnpinPage on a page whose pin count is already 0.
	ErrNotPinned = errors.New("storage: page is not pinned")
)

// DiskManager reads and writes whole pages of one relation
// (Postgres: the smgr API — smgrread / smgrwrite / smgrextend / smgrnblocks).
//
// Pages are numbered 0..NumPages()-1. A page must be created with
// AllocatePage before it can be read or written.
type DiskManager interface {
	// ReadPage copies page id into buf (len(buf) == PageSize).
	ReadPage(id types.PageID, buf []byte) error
	// WritePage copies buf (len(buf) == PageSize) to page id.
	WritePage(id types.PageID, buf []byte) error
	// AllocatePage appends a new zero-filled page and returns its id.
	AllocatePage() (types.PageID, error)
	// NumPages is the number of allocated pages.
	NumPages() uint32
}

// StampPage writes id into the first 4 bytes of buf (little-endian). Tests
// use it to give every page recognisable content.
func StampPage(buf []byte, id types.PageID) { binary.LittleEndian.PutUint32(buf[0:4], uint32(id)) }

// PageStamp reads back what StampPage wrote.
func PageStamp(buf []byte) types.PageID { return types.PageID(binary.LittleEndian.Uint32(buf[0:4])) }

// MemDisk is an in-memory DiskManager that records every I/O. It is safe for
// concurrent use. Read the public counters only when no other goroutine is
// using the disk (e.g. after wg.Wait()).
type MemDisk struct {
	mu    sync.Mutex
	pages [][]byte

	Reads    int            // successful ReadPage calls
	Writes   int            // successful WritePage calls
	Allocs   int            // successful AllocatePage calls
	ReadLog  []types.PageID // page ids of successful reads, in order
	WriteLog []types.PageID // page ids of successful writes, in order

	FailRead  map[types.PageID]error // injected: ReadPage(id) returns this error
	FailWrite map[types.PageID]error // injected: WritePage(id) returns this error
	FailAlloc error                  // injected: AllocatePage returns this error
}

// NewMemDisk returns a disk with n pages; page i is stamped with i
// (PageStamp(page) == i), the rest of the page is zero.
func NewMemDisk(n int) *MemDisk {
	m := &MemDisk{FailRead: map[types.PageID]error{}, FailWrite: map[types.PageID]error{}}
	for i := 0; i < n; i++ {
		p := make([]byte, PageSize)
		StampPage(p, types.PageID(i))
		m.pages = append(m.pages, p)
	}
	return m
}

func (m *MemDisk) ReadPage(id types.PageID, buf []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(buf) != PageSize {
		return ErrBadBufferSize
	}
	if err := m.FailRead[id]; err != nil {
		return err
	}
	if int64(id) >= int64(len(m.pages)) {
		return ErrPageNotFound
	}
	copy(buf, m.pages[id])
	m.Reads++
	m.ReadLog = append(m.ReadLog, id)
	return nil
}

func (m *MemDisk) WritePage(id types.PageID, buf []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(buf) != PageSize {
		return ErrBadBufferSize
	}
	if err := m.FailWrite[id]; err != nil {
		return err
	}
	if int64(id) >= int64(len(m.pages)) {
		return ErrPageNotFound
	}
	copy(m.pages[id], buf)
	m.Writes++
	m.WriteLog = append(m.WriteLog, id)
	return nil
}

func (m *MemDisk) AllocatePage() (types.PageID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailAlloc != nil {
		return 0, m.FailAlloc
	}
	m.pages = append(m.pages, make([]byte, PageSize))
	m.Allocs++
	return types.PageID(len(m.pages) - 1), nil
}

func (m *MemDisk) NumPages() uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return uint32(len(m.pages))
}

// Page returns a copy of the bytes currently stored for page id.
func (m *MemDisk) Page(id types.PageID) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.pages[id]...)
}

// ResetStats zeroes the counters and logs.
func (m *MemDisk) ResetStats() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Reads, m.Writes, m.Allocs = 0, 0, 0
	m.ReadLog, m.WriteLog = nil, nil
}
