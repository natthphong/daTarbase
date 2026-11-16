// Package heap builds a heap file — an unordered bag of pages holding
// tuples — on top of a tiny in-memory page abstraction, plus the access
// methods Postgres uses to read it: sequential, synchronized, TID-range,
// bitmap and sampling scans, and TOAST for oversized values.
//
// This file is PROVIDED: the page / page-store abstraction every problem
// in this phase works with. It is deliberately simpler than phase 2's
// byte-level slotted page so this phase can focus on the heap file.
package heap

import (
	"errors"

	"github.com/natthphong/daTarbase/pkg/types"
)

const (
	// PageCapacity is the number of bytes a MemPage can hold for tuples and
	// line pointers: an 8 KB block minus an 8-byte page header.
	PageCapacity = types.PageSize - 8 // 8184

	// ItemOverhead is the per-tuple cost of a line pointer. Storing a tuple of
	// n bytes consumes n+ItemOverhead bytes of the page.
	ItemOverhead = 4

	// MaxTupleSize is the largest tuple that fits on an empty page.
	MaxTupleSize = PageCapacity - ItemOverhead // 8180
)

// InvalidTID means "no tuple". It is the value of MemPage.Forward[i] for a
// tuple version that has not been updated.
var InvalidTID = types.TID{Page: types.InvalidPageID, Slot: 0}

var (
	ErrNoSuchPage     = errors.New("heap: page does not exist")
	ErrEmptyTuple     = errors.New("heap: empty tuple")
	ErrTupleTooBig    = errors.New("heap: tuple larger than MaxTupleSize")
	ErrInvalidTID     = errors.New("heap: TID does not address a tuple")
	ErrTupleDead      = errors.New("heap: tuple is dead (deleted or updated)")
	ErrPageOutOfRange = errors.New("heap: page beyond FSM capacity")
	ErrBadPercent     = errors.New("heap: sample percentage must be within [0, 100]")
	ErrCorruptToast   = errors.New("heap: corrupt TOAST value")
)

// Tuple is one tuple returned by a scan: where it lives and its bytes.
type Tuple struct {
	TID  types.TID // physical address (page, slot)
	Data []byte    // tuple bytes (not a copy — do not modify)
}

// MemPage is one heap page, kept as Go slices instead of raw bytes.
//
// Slot i is described by Slots[i], Dead[i] and Forward[i] (the three slices
// always have the same length). Slots are never removed or reused: deleting
// or updating a tuple only marks it dead, and its bytes keep occupying the
// page (reclaiming them is VACUUM's job, which this phase does not build).
type MemPage struct {
	Slots   [][]byte    // Slots[i] = bytes of the tuple version in slot i
	Dead    []bool      // Dead[i] = slot i was deleted or superseded by an update; scans skip it
	Forward []types.TID // Forward[i] = TID of the newer version if slot i was updated, else InvalidTID (Postgres: t_ctid)
	Used    int         // bytes consumed: sum of len(Slots[i])+ItemOverhead over ALL slots, dead ones included
}

// NewMemPage returns an empty page.
func NewMemPage() *MemPage { return &MemPage{} }

// NumSlots is the number of slots ever allocated on the page (live or dead).
func (p *MemPage) NumSlots() int { return len(p.Slots) }

// FreeSpace is the number of bytes still available: PageCapacity - Used.
func (p *MemPage) FreeSpace() int { return PageCapacity - p.Used }

// Add stores a copy of data in a new slot (live, no forward pointer) and
// returns the slot number. The caller must check FreeSpace() first: Add
// panics if len(data)+ItemOverhead > FreeSpace().
func (p *MemPage) Add(data []byte) types.SlotID {
	if len(data)+ItemOverhead > p.FreeSpace() {
		panic("heap: MemPage.Add called without enough free space")
	}
	p.Slots = append(p.Slots, append([]byte(nil), data...))
	p.Dead = append(p.Dead, false)
	p.Forward = append(p.Forward, InvalidTID)
	p.Used += len(data) + ItemOverhead
	return types.SlotID(len(p.Slots) - 1)
}

// PageStore is a relation file: an array of pages addressed by PageID
// 0..NumPages()-1. It counts page reads so tests can assert I/O behaviour.
type PageStore struct {
	pages []*MemPage // pages[i] is block i of the file

	Reads int // successful ReadPage calls so far (the "I/O" counter tests check)
}

// NewPageStore returns an empty relation file (0 pages).
func NewPageStore() *PageStore { return &PageStore{} }

// NumPages is the current length of the file in pages
// (Postgres: RelationGetNumberOfBlocks).
func (s *PageStore) NumPages() int { return len(s.pages) }

// ReadPage returns page id and increments Reads. Problems MUST use ReadPage
// to look at pages. It returns ErrNoSuchPage (without counting) if
// id >= NumPages().
func (s *PageStore) ReadPage(id types.PageID) (*MemPage, error) {
	if uint64(id) >= uint64(len(s.pages)) {
		return nil, ErrNoSuchPage
	}
	s.Reads++
	return s.pages[id], nil
}

// Extend appends a new empty page to the file and returns its id and the
// page itself. Extending is not counted as a read.
func (s *PageStore) Extend() (types.PageID, *MemPage) {
	p := NewMemPage()
	s.pages = append(s.pages, p)
	return types.PageID(len(s.pages) - 1), p
}

// Peek returns page id WITHOUT counting a read, or nil if it does not exist.
// It exists for tests (to build and inspect pages); problems must not use it.
func (s *PageStore) Peek(id types.PageID) *MemPage {
	if uint64(id) >= uint64(len(s.pages)) {
		return nil
	}
	return s.pages[id]
}
