package wal

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP10_4_ReadRecords(t *testing.T) {
	full := sample3() // BEGIN@8, UPDATE@37, COMMIT@71, len 100
	flip := func(b []byte, i int) []byte { c := append([]byte(nil), b...); c[i] ^= 0x10; return c }
	// a valid record whose stored LSN is 500 although it sits at offset 100
	stale := append(append([]byte(nil), full...), EncodeRecord(Record{LSN: 500, Txn: 9, Type: RecBegin})...)
	tests := []struct {
		name     string
		log      []byte
		from     types.LSN
		wantLSNs []types.LSN
		wantEnd  types.LSN
	}{
		{"whole log", full, 8, []types.LSN{8, 37, 71}, 100},
		{"from the middle", full, 37, []types.LSN{37, 71}, 100},
		{"from the end", full, 100, nil, 100},
		{"empty log", []byte(FileHeader), 8, nil, 8},
		{"torn last record", full[:90], 8, []types.LSN{8, 37}, 71},
		{"torn: only 2 bytes of the last record", full[:73], 8, []types.LSN{8, 37}, 71},
		{"zero-filled tail", append(append([]byte(nil), full...), make([]byte, 40)...), 8, []types.LSN{8, 37, 71}, 100},
		{"bad CRC in the middle stops there", flip(full, 67), 8, []types.LSN{8}, 37},
		{"stale record from an old file", stale, 8, []types.LSN{8, 37, 71}, 100},
		{"garbage tail", append(append([]byte(nil), full...), 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF), 8, []types.LSN{8, 37, 71}, 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recs, end, err := ReadRecords(tc.log, tc.from)
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if got := lsnsOf(recs); !equalLSNs(got, tc.wantLSNs) {
				t.Fatalf("LSNs = %v, want %v", got, tc.wantLSNs)
			}
			if end != tc.wantEnd {
				t.Fatalf("end = %d, want %d", end, tc.wantEnd)
			}
		})
	}
}

func TestP10_4_ReadRecords_Contents(t *testing.T) {
	recs, _, err := ReadRecords(sample3(), 8)
	if err != nil || len(recs) != 3 {
		t.Fatalf("got %d records, err %v; want 3", len(recs), err)
	}
	want := Record{LSN: 37, PrevLSN: 8, Txn: 1, Type: RecUpdate, Body: []byte("hello")}
	if !recordsEqual(recs[1], want) {
		t.Fatalf("recs[1] = %v %q, want %v %q", recs[1], recs[1].Body, want, want.Body)
	}
}

func TestP10_4_ReadRecords_InvalidStart(t *testing.T) {
	full := sample3()
	for _, from := range []types.LSN{0, 7, 101} {
		recs, _, err := ReadRecords(full, from)
		if !errors.Is(err, ErrInvalidLSN) || recs != nil {
			t.Fatalf("ReadRecords(log, %d) = %v, %v; want nil, ErrInvalidLSN", from, recs, err)
		}
	}
}
