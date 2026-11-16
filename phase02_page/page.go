// Package page implements a Postgres-style slotted page: one 8 KB block that
// stores variable-length tuples behind an array of line pointers.
//
// Everything in this file is PROVIDED. The problems live in lines.go,
// tuple.go, compact.go, checksum.go and dump.go.
//
// Page layout (8192 bytes, every integer little-endian):
//
//	0       2       4       6          8
//	+-------+-------+-------+----------+------+------+-- free --+---------+---------+
//	| lower | upper | flags | checksum | lp 0 | lp 1 |  space   | tuple 1 | tuple 0 |
//	+-------+-------+-------+----------+------+------+----------+---------+---------+
//	                                    slots grow ->         <- tuple data grows
//
//	lower    uint16  offset where free space starts (= end of the line-pointer array)
//	upper    uint16  offset where free space ends   (= start of the tuple data)
//	flags    uint16  page flags; no bits are defined in this phase, always 0
//	checksum uint16  page checksum (see Problem 2.11); 0 = not stamped yet
//
// Line pointer (Postgres: ItemIdData), 4 bytes at HeaderSize + 4*slot:
//
//	byte 0..1  off   uint16                    offset of the tuple inside the page
//	byte 2..3  word  uint16 = flags<<14 | len  top 2 bits: LPFlags, low 14 bits: length
//
// An UNUSED line pointer is all four bytes zero.
package page

import (
	"encoding/binary"
	"errors"

	"github.com/natthphong/daTarbase/pkg/types"
)

const (
	// PageSize is the size of one page in bytes (Postgres: BLCKSZ).
	PageSize = types.PageSize
	// HeaderSize is the size of the page header: lower, upper, flags, checksum.
	HeaderSize = 8
	// LinePointerLen is the size of one line pointer (Postgres: sizeof(ItemIdData)).
	LinePointerLen = 4
	// MaxItemLen is the largest length that fits in the 14 length bits of a line pointer.
	MaxItemLen = 1<<14 - 1 // 16383
	// MaxTupleLen is the largest tuple an empty page can hold: 8192-8-4 = 8180.
	MaxTupleLen = PageSize - HeaderSize - LinePointerLen
)

// LPFlags is the 2-bit state of a line pointer (Postgres: lp_flags).
type LPFlags uint8

const (
	// LPUnused: the slot is free and may be reused by InsertTuple. off=0, len=0.
	LPUnused LPFlags = 0
	// LPNormal: the slot points at a live tuple of length len at offset off.
	LPNormal LPFlags = 1
	// LPRedirect: used by HOT chains in Postgres. Reserved: never produced in this phase.
	LPRedirect LPFlags = 2
	// LPDead: the tuple was deleted. off/len still describe its (now garbage) bytes
	// until Compact reclaims them; the slot number stays reserved.
	LPDead LPFlags = 3
)

// String returns "UNUSED", "NORMAL", "REDIRECT" or "DEAD".
func (f LPFlags) String() string {
	switch f {
	case LPUnused:
		return "UNUSED"
	case LPNormal:
		return "NORMAL"
	case LPRedirect:
		return "REDIRECT"
	case LPDead:
		return "DEAD"
	}
	return "INVALID"
}

// ItemID is the decoded form of one line pointer (Postgres: ItemIdData).
type ItemID struct {
	Off   uint16  // byte offset of the tuple inside the page (lp_off)
	Flags LPFlags // slot state (lp_flags), must fit in 2 bits
	Len   uint16  // tuple length in bytes (lp_len), must fit in 14 bits
}

var (
	ErrPageFull         = errors.New("page: not enough free space")
	ErrEmptyTuple       = errors.New("page: empty tuple")
	ErrInvalidSlot      = errors.New("page: invalid slot")
	ErrBadItemID        = errors.New("page: line pointer field out of range")
	ErrNoRoom           = errors.New("page: new tuple version does not fit in place")
	ErrBadFillFactor    = errors.New("page: fillfactor must be between 10 and 100")
	ErrChecksumMismatch = errors.New("page: checksum mismatch")
	ErrCorruptPage      = errors.New("page: corrupt page")
)

// SlotID is an index into the line-pointer array, starting at 0.
// (Postgres' OffsetNumber starts at 1 — we use 0-based slots.)
type SlotID = types.SlotID

// Page is one 8 KB slotted page. The zero value is NOT a valid page: use NewPage.
type Page struct {
	buf [PageSize]byte // the raw page image, exactly what goes to disk
}

// NewPage returns an empty, initialized page: lower=8, upper=8192, flags=0,
// checksum=0, no line pointers (Postgres: PageInit).
func NewPage() *Page {
	p := &Page{}
	p.setLower(HeaderSize)
	p.setUpper(PageSize)
	return p
}

// Lower returns pd_lower: the offset where free space starts.
func (p *Page) Lower() uint16 { return binary.LittleEndian.Uint16(p.buf[0:2]) }

// Upper returns pd_upper: the offset where free space ends.
func (p *Page) Upper() uint16 { return binary.LittleEndian.Uint16(p.buf[2:4]) }

// Flags returns pd_flags (always 0 in this phase).
func (p *Page) Flags() uint16 { return binary.LittleEndian.Uint16(p.buf[4:6]) }

// StoredChecksum returns the checksum field as stored in the header (bytes 6..8).
func (p *Page) StoredChecksum() uint16 { return binary.LittleEndian.Uint16(p.buf[6:8]) }

// SetStoredChecksum overwrites the checksum field in the header (bytes 6..8).
func (p *Page) SetStoredChecksum(v uint16) { binary.LittleEndian.PutUint16(p.buf[6:8], v) }

// NumSlots returns the number of line pointers (any state): (lower-8)/4.
func (p *Page) NumSlots() int { return (int(p.Lower()) - HeaderSize) / LinePointerLen }

// Bytes returns the raw page image. It aliases the page: do not keep it
// across modifications.
func (p *Page) Bytes() []byte { return p.buf[:] }

func (p *Page) setLower(v uint16) { binary.LittleEndian.PutUint16(p.buf[0:2], v) }
func (p *Page) setUpper(v uint16) { binary.LittleEndian.PutUint16(p.buf[2:4], v) }
