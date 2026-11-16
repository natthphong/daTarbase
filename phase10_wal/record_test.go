package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"testing"
)

func TestP10_1_EncodeRecord_GoldenBegin(t *testing.T) {
	got := EncodeRecord(Record{LSN: 8, Txn: 1, Type: RecBegin})
	want := []byte{
		0x1d, 0, 0, 0, // TotalLen 29
		0x56, 0x63, 0x73, 0x6c, // CRC 0x6c736356
		8, 0, 0, 0, 0, 0, 0, 0, // LSN 8
		0, 0, 0, 0, 0, 0, 0, 0, // PrevLSN 0
		1, 0, 0, 0, // Txn 1
		1, // BEGIN
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeRecord =\n % x\nwant\n % x", got, want)
	}
}

func TestP10_1_EncodeRecord_Fields(t *testing.T) {
	tests := []struct {
		name string
		rec  Record
	}{
		{"update with body", Record{LSN: 37, PrevLSN: 8, Txn: 1, Type: RecUpdate, Body: []byte("hello")}},
		{"big numbers", Record{LSN: 1 << 40, PrevLSN: 1<<40 - 7, Txn: 0xDEADBEEF, Type: RecCLR, Body: []byte{0xFF}}},
		{"checkpoint no txn", Record{LSN: 371, Type: RecCheckpoint, Body: make([]byte, 48)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := EncodeRecord(tc.rec)
			n := HeaderSize + len(tc.rec.Body)
			if len(b) != n {
				t.Fatalf("len = %d, want %d", len(b), n)
			}
			if got := binary.LittleEndian.Uint32(b[0:]); got != uint32(n) {
				t.Errorf("TotalLen = %d, want %d", got, n)
			}
			wantCRC := crc32.Checksum(b[8:], crc32.MakeTable(crc32.Castagnoli))
			if got := binary.LittleEndian.Uint32(b[4:]); got != wantCRC {
				t.Errorf("CRC = %#x, want %#x (crc32c of bytes [8,%d))", got, wantCRC, n)
			}
			if got := binary.LittleEndian.Uint64(b[8:]); got != uint64(tc.rec.LSN) {
				t.Errorf("LSN = %d, want %d", got, tc.rec.LSN)
			}
			if got := binary.LittleEndian.Uint64(b[16:]); got != uint64(tc.rec.PrevLSN) {
				t.Errorf("PrevLSN = %d, want %d", got, tc.rec.PrevLSN)
			}
			if got := binary.LittleEndian.Uint32(b[24:]); got != uint32(tc.rec.Txn) {
				t.Errorf("Txn = %d, want %d", got, tc.rec.Txn)
			}
			if b[28] != byte(tc.rec.Type) {
				t.Errorf("Type = %d, want %d", b[28], tc.rec.Type)
			}
			if !bytes.Equal(b[29:], tc.rec.Body) {
				t.Errorf("body = %x, want %x", b[29:], tc.rec.Body)
			}
		})
	}
}

func TestP10_2_DecodeRecord_RoundTrip(t *testing.T) {
	recs := []Record{
		{LSN: 8, Txn: 1, Type: RecBegin},
		{LSN: 37, PrevLSN: 8, Txn: 1, Type: RecUpdate, Body: []byte("hello")},
		{LSN: 1 << 33, PrevLSN: 99, Txn: 7, Type: RecFPI, Body: bytes.Repeat([]byte{0xAB}, 68)},
		{LSN: 500, Type: RecCheckpoint, Body: []byte{1, 2, 3}},
	}
	for _, want := range recs {
		b := EncodeRecord(want)
		got, n, err := DecodeRecord(append(b, 0xEE, 0xEE)) // trailing bytes ignored
		if err != nil {
			t.Fatalf("%v: err = %v", want, err)
		}
		if n != len(b) {
			t.Fatalf("%v: n = %d, want %d", want, n, len(b))
		}
		if !recordsEqual(got, want) {
			t.Fatalf("decoded %v, want %v", got, want)
		}
	}
}

func TestP10_2_DecodeRecord_Errors(t *testing.T) {
	good := EncodeRecord(Record{LSN: 37, PrevLSN: 8, Txn: 1, Type: RecUpdate, Body: []byte("hello")}) // 34 bytes
	mod := func(f func(b []byte) []byte) []byte { return f(append([]byte(nil), good...)) }
	badType := EncodeRecord(Record{LSN: 8, Txn: 1, Type: 9}) // valid CRC, unknown type
	tooBig := mod(func(b []byte) []byte { binary.LittleEndian.PutUint32(b, MaxRecordLen+1); return b })

	tests := []struct {
		name string
		buf  []byte
		want error
	}{
		{"empty", nil, ErrShortRecord},
		{"3 bytes: length unreadable", good[:3], ErrShortRecord},
		{"one byte short", good[:33], ErrShortRecord},
		{"header only", good[:29], ErrShortRecord},
		{"zero-filled tail", make([]byte, 64), ErrCorruptRecord},
		{"length smaller than header", mod(func(b []byte) []byte { b[0] = 28; return b }), ErrCorruptRecord},
		{"length above MaxRecordLen", tooBig, ErrCorruptRecord},
		{"flipped body bit", mod(func(b []byte) []byte { b[30] ^= 1; return b }), ErrBadCRC},
		{"flipped LSN bit", mod(func(b []byte) []byte { b[8] ^= 0x80; return b }), ErrBadCRC},
		{"flipped CRC bit", mod(func(b []byte) []byte { b[4] ^= 1; return b }), ErrBadCRC},
		{"length 33 cuts body: CRC over wrong bytes", mod(func(b []byte) []byte { b[0] = 33; return b }), ErrBadCRC},
		{"unknown type with valid CRC", badType, ErrCorruptRecord},
		{"type 0 with valid CRC", EncodeRecord(Record{LSN: 8, Type: 0}), ErrCorruptRecord},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, n, err := DecodeRecord(tc.buf)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if n != 0 || r.Type != 0 || r.Body != nil {
				t.Fatalf("on error got (%v, %d), want (Record{}, 0)", r, n)
			}
		})
	}
}

func TestP10_2_DecodeRecord_BodyDoesNotAlias(t *testing.T) {
	b := EncodeRecord(Record{LSN: 8, Txn: 1, Type: RecUpdate, Body: []byte("abc")})
	r, _, err := DecodeRecord(b)
	if err != nil {
		t.Fatal(err)
	}
	b[29] = 'X'
	if string(r.Body) != "abc" {
		t.Fatalf("Body = %q after changing buf, want %q (Body must be a copy)", r.Body, "abc")
	}
	r2, _, _ := DecodeRecord(EncodeRecord(Record{LSN: 8, Txn: 1, Type: RecCommit}))
	if r2.Body != nil {
		t.Fatalf("empty body = %#v, want nil", r2.Body)
	}
}
