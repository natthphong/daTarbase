package wal

import (
	"encoding/binary"
	"hash/crc32"

	"github.com/natthphong/daTarbase/pkg/types"
)

// Problem 10.1 — EncodeRecord [Easy]
//
// Serialise a record into its on-disk bytes: 29-byte header + body, with a CRC-32C over everything after the CRC field.
//
// Every WAL record starts with a fixed header (Postgres: XLogRecord) so a
// reader can find the next record (TotalLen) and detect garbage (CRC):
//
//	off  size  field
//	 0    4    TotalLen  uint32   = HeaderSize + len(Body)
//	 4    4    CRC       uint32   crc32 Castagnoli of bytes [8, TotalLen)
//	 8    8    LSN       uint64
//	16    8    PrevLSN   uint64
//	24    4    Txn       uint32
//	28    1    Type      uint8
//	29    …    Body
//
// Rules:
//  1. All integers are little-endian. The output is exactly HeaderSize+len(Body) bytes.
//  2. The CRC covers bytes [8, TotalLen): LSN, PrevLSN, Txn, Type and the body.
//     It does NOT cover TotalLen itself (a corrupt length is caught because the
//     CRC is then computed over the wrong bytes). Use crcTable (Castagnoli).
//  3. The fields are written as given — EncodeRecord does not validate them.
//
// Example:
//
//	EncodeRecord(Record{LSN: 8, Txn: 1, Type: RecBegin}) // 29 bytes:
//	1d 00 00 00 | 56 63 73 6c | 08 00 00 00 00 00 00 00 | 00 00 00 00 00 00 00 00 | 01 00 00 00 | 01
//	// TotalLen=29, CRC=0x6c736356, LSN=8, PrevLSN=0, Txn=1, Type=BEGIN
//
// Constraints: one allocation.
// Postgres reference: XLogRecordAssemble() + COMP_CRC32C in src/backend/access/transam/xloginsert.c
func EncodeRecord(r Record) []byte {
	n := HeaderSize + len(r.Body)
	b := make([]byte, n)
	binary.LittleEndian.PutUint32(b[0:], uint32(n))
	binary.LittleEndian.PutUint64(b[8:], uint64(r.LSN))
	binary.LittleEndian.PutUint64(b[16:], uint64(r.PrevLSN))
	binary.LittleEndian.PutUint32(b[24:], uint32(r.Txn))
	b[28] = byte(r.Type)
	copy(b[HeaderSize:], r.Body)
	binary.LittleEndian.PutUint32(b[4:], crc32.Checksum(b[8:], crcTable))
	return b
}

// Problem 10.2 — DecodeRecord [Medium]
//
// Parse and verify the record at the start of buf; return it and its length, or say why the bytes are not a valid record.
//
// When the server crashes in the middle of writing the WAL, the end of the
// log holds a partial record, zeroes, or bytes left over from an old file.
// DecodeRecord is the gatekeeper that rejects all of them.
//
// Rules — check in this order and return the first failure:
//  1. len(buf) < 4 (TotalLen can't even be read)                  → ErrShortRecord.
//  2. TotalLen < HeaderSize or TotalLen > MaxRecordLen            → ErrCorruptRecord.
//     (A zero-filled tail has TotalLen 0 and lands here.)
//  3. len(buf) < TotalLen                                         → ErrShortRecord.
//  4. stored CRC != crc32c(buf[8:TotalLen])                       → ErrBadCRC.
//  5. Type is not one of RecBegin..RecFPI (1..7)                  → ErrCorruptRecord.
//  6. Otherwise return the record and n = TotalLen. Bytes after TotalLen are
//     ignored (they belong to the next record).
//
// Body must be a fresh copy that does not alias buf (the caller may reuse buf);
// a record without body has Body == nil. On error return Record{}, 0.
//
// Example:
//
//	b := EncodeRecord(Record{LSN: 37, PrevLSN: 8, Txn: 1, Type: RecUpdate, Body: []byte("hello")}) // 34 bytes
//	DecodeRecord(b)              // → that record, 34, nil
//	DecodeRecord(append(b, 0xEE)) // → same, 34, nil (trailing byte ignored)
//	DecodeRecord(b[:33])         // → ErrShortRecord
//	DecodeRecord(b[:3])          // → ErrShortRecord
//	b[30] ^= 1; DecodeRecord(b)  // → ErrBadCRC (one flipped body bit)
//	DecodeRecord(make([]byte, 64)) // → ErrCorruptRecord (TotalLen 0)
//
// Constraints: O(TotalLen).
// Postgres reference: ValidXLogRecordHeader() + ValidXLogRecord() in src/backend/access/transam/xlogreader.c
func DecodeRecord(buf []byte) (Record, int, error) {
	if len(buf) < 4 {
		return Record{}, 0, ErrShortRecord
	}
	n := binary.LittleEndian.Uint32(buf)
	if n < HeaderSize || n > MaxRecordLen {
		return Record{}, 0, ErrCorruptRecord
	}
	if len(buf) < int(n) {
		return Record{}, 0, ErrShortRecord
	}
	if binary.LittleEndian.Uint32(buf[4:]) != crc32.Checksum(buf[8:n], crcTable) {
		return Record{}, 0, ErrBadCRC
	}
	t := RecordType(buf[28])
	if !t.Valid() {
		return Record{}, 0, ErrCorruptRecord
	}
	r := Record{
		LSN:     types.LSN(binary.LittleEndian.Uint64(buf[8:])),
		PrevLSN: types.LSN(binary.LittleEndian.Uint64(buf[16:])),
		Txn:     types.TxnID(binary.LittleEndian.Uint32(buf[24:])),
		Type:    t,
	}
	if n > HeaderSize {
		r.Body = append([]byte(nil), buf[HeaderSize:n]...)
	}
	return r, int(n), nil
}
