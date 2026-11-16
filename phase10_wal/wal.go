// Package wal implements a write-ahead log and ARIES-style crash recovery
// over a tiny in-memory "disk" that can crash.
//
// Everything in this file is PROVIDED: record types, the record header layout,
// the body codecs and the errors. The problems live in record.go, log.go,
// reader.go, flush.go, bufferpool.go, db.go, redo.go, analysis.go, undo.go
// and recover.go.
//
// The WAL is one growing byte stream. It starts with an 8-byte file header
// ("DTBWAL01"), then records follow back to back. A record's LSN is the byte
// offset where it starts, so the first record has LSN 8 (FirstLSN) and LSN 0
// (InvalidLSN) can mean "none".
//
//	0        8                         8+len(r1)
//	+--------+------------------------+------------------+---
//	| header |  record r1 (LSN 8)     | record r2        | ...
//	+--------+------------------------+------------------+---
//
// Record layout (every integer little-endian, HeaderSize = 29):
//
//	off  size  field
//	 0    4    TotalLen  uint32   header + body length in bytes
//	 4    4    CRC       uint32   CRC-32C (Castagnoli) of bytes [8, TotalLen)
//	 8    8    LSN       uint64   this record's own LSN (= its byte offset)
//	16    8    PrevLSN   uint64   previous record of the SAME transaction (0 = none)
//	24    4    Txn       uint32   transaction id (0 = not part of a transaction)
//	28    1    Type      uint8    RecordType
//	29    …    Body               type-specific payload (see the *Body types below)
package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"sort"

	"github.com/natthphong/daTarbase/pkg/types"
)

const (
	// HeaderSize is the size of the fixed record header.
	HeaderSize = 29
	// FileHeader is written at offset 0 of every log. Records start right after it.
	FileHeader = "DTBWAL01"
	// FirstLSN is the LSN of the first record in a log.
	FirstLSN types.LSN = 8
	// InvalidLSN means "no record" (e.g. PrevLSN of a transaction's first record).
	InvalidLSN types.LSN = 0
	// MaxRecordLen is the largest legal TotalLen. Anything bigger is corruption.
	MaxRecordLen = 1 << 20
	// PageDataSize is the size of one data page in this phase's toy disk.
	PageDataSize = 64
)

// crcTable is the CRC-32C (Castagnoli) table — the same polynomial Postgres uses
// for WAL records (pg_crc32c).
var crcTable = crc32.MakeTable(crc32.Castagnoli)

// RecordType says what a record means.
type RecordType uint8

const (
	RecBegin      RecordType = 1 // transaction started; empty body
	RecUpdate     RecordType = 2 // physical change of page bytes; body = UpdateBody
	RecCommit     RecordType = 3 // transaction committed; empty body
	RecAbort      RecordType = 4 // transaction completely rolled back (ARIES "end"); empty body
	RecCLR        RecordType = 5 // compensation log record written by undo; body = CLRBody
	RecCheckpoint RecordType = 6 // fuzzy checkpoint; body = CheckpointBody; Txn = 0
	RecFPI        RecordType = 7 // full-page image; body = FPIBody; Txn = 0, PrevLSN = 0
)

func (t RecordType) String() string {
	switch t {
	case RecBegin:
		return "BEGIN"
	case RecUpdate:
		return "UPDATE"
	case RecCommit:
		return "COMMIT"
	case RecAbort:
		return "ABORT"
	case RecCLR:
		return "CLR"
	case RecCheckpoint:
		return "CHECKPOINT"
	case RecFPI:
		return "FPI"
	}
	return fmt.Sprintf("type(%d)", uint8(t))
}

// Valid reports whether t is one of the record types above (1..7).
func (t RecordType) Valid() bool { return t >= RecBegin && t <= RecFPI }

// Record is the decoded form of one WAL record.
type Record struct {
	LSN     types.LSN   // byte offset of the record in the log
	PrevLSN types.LSN   // previous record of the same transaction, InvalidLSN if none
	Txn     types.TxnID // owning transaction, InvalidTxnID for FPI / checkpoint
	Type    RecordType  // what kind of record
	Body    []byte      // type-specific payload (may be empty)
}

func (r Record) String() string {
	return fmt.Sprintf("%s@%d(txn=%d prev=%d body=%dB)", r.Type, r.LSN, r.Txn, r.PrevLSN, len(r.Body))
}

var (
	ErrShortRecord    = errors.New("wal: record is truncated")
	ErrBadCRC         = errors.New("wal: record CRC mismatch")
	ErrCorruptRecord  = errors.New("wal: corrupt record")
	ErrInvalidLSN     = errors.New("wal: invalid LSN")
	ErrWALNotFlushed  = errors.New("wal: page written before its WAL record was flushed")
	ErrNoTxn          = errors.New("wal: transaction is not active")
	ErrBadRange       = errors.New("wal: byte range outside the page")
	ErrNoCheckpoint   = errors.New("wal: master record does not point at a checkpoint")
	ErrBrokenTxnChain = errors.New("wal: unexpected record in a transaction's PrevLSN chain")
)

// ---------------------------------------------------------------- bodies

// UpdateBody is the body of a RecUpdate record: bytes [Off, Off+len) of page
// Page changed from Before to After (len(Before) == len(After)).
//
//	Page uint32 | Off uint16 | Len uint16 | Before [Len] | After [Len]
type UpdateBody struct {
	Page   types.PageID
	Off    uint16
	Before []byte
	After  []byte
}

// Encode serialises the body. It panics if len(Before) != len(After).
func (b UpdateBody) Encode() []byte {
	if len(b.Before) != len(b.After) {
		panic("wal: UpdateBody before/after length mismatch")
	}
	out := make([]byte, 8, 8+2*len(b.After))
	binary.LittleEndian.PutUint32(out[0:], uint32(b.Page))
	binary.LittleEndian.PutUint16(out[4:], b.Off)
	binary.LittleEndian.PutUint16(out[6:], uint16(len(b.After)))
	out = append(out, b.Before...)
	return append(out, b.After...)
}

// DecodeUpdate parses an UpdateBody; ErrCorruptRecord if the length is wrong.
func DecodeUpdate(body []byte) (UpdateBody, error) {
	if len(body) < 8 {
		return UpdateBody{}, ErrCorruptRecord
	}
	n := int(binary.LittleEndian.Uint16(body[6:]))
	if len(body) != 8+2*n {
		return UpdateBody{}, ErrCorruptRecord
	}
	return UpdateBody{
		Page:   types.PageID(binary.LittleEndian.Uint32(body[0:])),
		Off:    binary.LittleEndian.Uint16(body[4:]),
		Before: append([]byte(nil), body[8:8+n]...),
		After:  append([]byte(nil), body[8+n:]...),
	}, nil
}

// CLRBody is the body of a RecCLR record: undo wrote Data at [Off, Off+len)
// of page Page, and the next record of this transaction still to be undone is
// UndoNext (InvalidLSN = nothing left).
//
//	Page uint32 | Off uint16 | Len uint16 | UndoNext uint64 | Data [Len]
type CLRBody struct {
	Page     types.PageID
	Off      uint16
	Data     []byte
	UndoNext types.LSN
}

// Encode serialises the body.
func (b CLRBody) Encode() []byte {
	out := make([]byte, 16, 16+len(b.Data))
	binary.LittleEndian.PutUint32(out[0:], uint32(b.Page))
	binary.LittleEndian.PutUint16(out[4:], b.Off)
	binary.LittleEndian.PutUint16(out[6:], uint16(len(b.Data)))
	binary.LittleEndian.PutUint64(out[8:], uint64(b.UndoNext))
	return append(out, b.Data...)
}

// DecodeCLR parses a CLRBody; ErrCorruptRecord if the length is wrong.
func DecodeCLR(body []byte) (CLRBody, error) {
	if len(body) < 16 {
		return CLRBody{}, ErrCorruptRecord
	}
	n := int(binary.LittleEndian.Uint16(body[6:]))
	if len(body) != 16+n {
		return CLRBody{}, ErrCorruptRecord
	}
	return CLRBody{
		Page:     types.PageID(binary.LittleEndian.Uint32(body[0:])),
		Off:      binary.LittleEndian.Uint16(body[4:]),
		UndoNext: types.LSN(binary.LittleEndian.Uint64(body[8:])),
		Data:     append([]byte(nil), body[16:]...),
	}, nil
}

// FPIBody is the body of a RecFPI record: the complete data of page Page.
//
//	Page uint32 | Image [PageDataSize]
type FPIBody struct {
	Page  types.PageID
	Image [PageDataSize]byte
}

// Encode serialises the body (always 4+64 = 68 bytes).
func (b FPIBody) Encode() []byte {
	out := make([]byte, 4, 4+PageDataSize)
	binary.LittleEndian.PutUint32(out, uint32(b.Page))
	return append(out, b.Image[:]...)
}

// DecodeFPI parses an FPIBody; ErrCorruptRecord if the length is wrong.
func DecodeFPI(body []byte) (FPIBody, error) {
	if len(body) != 4+PageDataSize {
		return FPIBody{}, ErrCorruptRecord
	}
	var b FPIBody
	b.Page = types.PageID(binary.LittleEndian.Uint32(body))
	copy(b.Image[:], body[4:])
	return b, nil
}

// ATTEntry is one Active Transaction Table entry: a transaction that has not
// committed or finished aborting, and the LSN of its most recent record.
type ATTEntry struct {
	Txn     types.TxnID
	LastLSN types.LSN
}

// DPTEntry is one Dirty Page Table entry: a page that may differ from its disk
// copy, and recLSN = the LSN of the first record that dirtied it since it was
// last written to disk.
type DPTEntry struct {
	Page   types.PageID
	RecLSN types.LSN
}

// CheckpointBody is the body of a RecCheckpoint record.
//
//	NextTxn uint32 | nATT uint32 | nATT × (Txn uint32, LastLSN uint64)
//	               | nDPT uint32 | nDPT × (Page uint32, RecLSN uint64)
//
// Encode sorts ATT by Txn and DPT by Page, so the bytes are deterministic.
type CheckpointBody struct {
	NextTxn types.TxnID // the next transaction id Begin would hand out
	ATT     []ATTEntry
	DPT     []DPTEntry
}

// Encode serialises the body (4 + 4 + 12·len(ATT) + 4 + 12·len(DPT) bytes).
func (b CheckpointBody) Encode() []byte {
	att := append([]ATTEntry(nil), b.ATT...)
	sort.Slice(att, func(i, j int) bool { return att[i].Txn < att[j].Txn })
	dpt := append([]DPTEntry(nil), b.DPT...)
	sort.Slice(dpt, func(i, j int) bool { return dpt[i].Page < dpt[j].Page })
	out := binary.LittleEndian.AppendUint32(nil, uint32(b.NextTxn))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(att)))
	for _, e := range att {
		out = binary.LittleEndian.AppendUint32(out, uint32(e.Txn))
		out = binary.LittleEndian.AppendUint64(out, uint64(e.LastLSN))
	}
	out = binary.LittleEndian.AppendUint32(out, uint32(len(dpt)))
	for _, e := range dpt {
		out = binary.LittleEndian.AppendUint32(out, uint32(e.Page))
		out = binary.LittleEndian.AppendUint64(out, uint64(e.RecLSN))
	}
	return out
}

// DecodeCheckpoint parses a CheckpointBody; ErrCorruptRecord if malformed.
func DecodeCheckpoint(body []byte) (CheckpointBody, error) {
	var b CheckpointBody
	rd := func(n int) []byte {
		if len(body) < n {
			return nil
		}
		v := body[:n]
		body = body[n:]
		return v
	}
	h := rd(8)
	if h == nil {
		return b, ErrCorruptRecord
	}
	b.NextTxn = types.TxnID(binary.LittleEndian.Uint32(h))
	n := int(binary.LittleEndian.Uint32(h[4:]))
	for range n {
		e := rd(12)
		if e == nil {
			return CheckpointBody{}, ErrCorruptRecord
		}
		b.ATT = append(b.ATT, ATTEntry{types.TxnID(binary.LittleEndian.Uint32(e)), types.LSN(binary.LittleEndian.Uint64(e[4:]))})
	}
	h = rd(4)
	if h == nil {
		return CheckpointBody{}, ErrCorruptRecord
	}
	n = int(binary.LittleEndian.Uint32(h))
	for range n {
		e := rd(12)
		if e == nil {
			return CheckpointBody{}, ErrCorruptRecord
		}
		b.DPT = append(b.DPT, DPTEntry{types.PageID(binary.LittleEndian.Uint32(e)), types.LSN(binary.LittleEndian.Uint64(e[4:]))})
	}
	if len(body) != 0 {
		return CheckpointBody{}, ErrCorruptRecord
	}
	return b, nil
}

// RecordPage returns the page a record modifies. ok is false for record types
// that don't touch a page (Begin, Commit, Abort, Checkpoint). err is
// ErrCorruptRecord if the body of an Update/CLR/FPI record can't be decoded.
func RecordPage(r Record) (page types.PageID, ok bool, err error) {
	switch r.Type {
	case RecUpdate, RecCLR, RecFPI:
		if len(r.Body) < 4 {
			return 0, false, ErrCorruptRecord
		}
		return types.PageID(binary.LittleEndian.Uint32(r.Body)), true, nil
	}
	return 0, false, nil
}
