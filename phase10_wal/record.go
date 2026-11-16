package wal

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
	panic("TODO: Problem 10.1")
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
	panic("TODO: Problem 10.2")
}
