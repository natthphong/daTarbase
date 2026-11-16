package txn

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP9_1_ClogSet_ByteLayout(t *testing.T) {
	c := NewClog()
	steps := []struct {
		xid      types.TxnID
		status   TxnStatus
		page     uint32
		byteIdx  int
		wantByte byte
	}{
		{3, StatusCommitted, 0, 0, 0x40},      // bits 6-7 of byte 0
		{4, StatusAborted, 0, 1, 0x02},        // bits 0-1 of byte 1
		{5, StatusSubCommitted, 0, 1, 0x0E},   // bits 2-3 added
		{7, StatusCommitted, 0, 1, 0x4E},      // bits 6-7 added
		{4, StatusCommitted, 0, 1, 0x4D},      // 10 → 01 in bits 0-1, neighbours kept
		{5, StatusInProgress, 0, 1, 0x41},     // clear bits 2-3
		{32767, StatusAborted, 0, 8191, 0x80}, // last xid of page 0
		{32768, StatusAborted, 1, 0, 0x02},    // first xid of page 1
		{4294967295, StatusCommitted, 131071, 8191, 0x40},
	}
	for _, s := range steps {
		if err := c.Set(s.xid, s.status); err != nil {
			t.Fatalf("Set(%d, %v) = %v, want nil", s.xid, s.status, err)
		}
		p := c.Page(s.page)
		if len(p) != ClogPageSize {
			t.Fatalf("after Set(%d): page %d has len %d, want %d", s.xid, s.page, len(p), ClogPageSize)
		}
		if p[s.byteIdx] != s.wantByte {
			t.Fatalf("after Set(%d, %v): page %d byte %d = %#02x, want %#02x", s.xid, s.status, s.page, s.byteIdx, p[s.byteIdx], s.wantByte)
		}
	}
	if b := c.Page(0)[0]; b != 0x40 {
		t.Fatalf("page 0 byte 0 = %#02x, want 0x40 (untouched by later sets)", b)
	}
	if len(c.Pages) != 3 {
		t.Fatalf("pages allocated = %d, want 3 (0, 1, 131071)", len(c.Pages))
	}
}

func TestP9_1_ClogSet_Errors(t *testing.T) {
	tests := []struct {
		name    string
		xid     types.TxnID
		status  TxnStatus
		wantErr error
	}{
		{"status 4 does not fit in 2 bits", 10, 4, ErrBadStatus},
		{"invalid xid", 0, StatusCommitted, ErrSpecialXid},
		{"bootstrap xid", BootstrapTxnID, StatusAborted, ErrSpecialXid},
		{"frozen xid", FrozenTxnID, StatusAborted, ErrSpecialXid},
		{"bad status checked before special xid", 1, 9, ErrBadStatus},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClog()
			if err := c.Set(tc.xid, tc.status); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Set(%d, %d) = %v, want %v", tc.xid, tc.status, err, tc.wantErr)
			}
			if len(c.Pages) != 0 {
				t.Fatalf("failed Set allocated %d page(s), want 0", len(c.Pages))
			}
		})
	}
}

func TestP9_2_ClogGet(t *testing.T) {
	c := NewClog()
	if got := c.Get(10); got != StatusInProgress {
		t.Fatalf("Get(10) on empty clog = %v, want in-progress", got)
	}
	if len(c.Pages) != 0 {
		t.Fatalf("Get allocated a page")
	}
	must := func(xid types.TxnID, s TxnStatus) {
		t.Helper()
		if err := c.Set(xid, s); err != nil {
			t.Fatal(err)
		}
	}
	must(10, StatusAborted)
	must(12, StatusCommitted)
	must(13, StatusSubCommitted)
	must(40000, StatusCommitted)
	must(4294967295, StatusAborted)

	tests := []struct {
		xid  types.TxnID
		want TxnStatus
	}{
		{0, StatusAborted},
		{BootstrapTxnID, StatusCommitted},
		{FrozenTxnID, StatusCommitted},
		{9, StatusInProgress},
		{10, StatusAborted},
		{11, StatusInProgress},
		{12, StatusCommitted},
		{13, StatusSubCommitted},
		{40000, StatusCommitted},
		{40001, StatusInProgress},
		{4294967295, StatusAborted},
		{4294967294, StatusInProgress},
		{70000, StatusInProgress}, // page 2 never written
	}
	for _, tc := range tests {
		if got := c.Get(tc.xid); got != tc.want {
			t.Errorf("Get(%d) = %v, want %v", tc.xid, got, tc.want)
		}
	}
}

func TestP9_2_ClogGet_RoundTripEveryStatus(t *testing.T) {
	c := NewClog()
	// 16 consecutive xids, cycling through all four statuses, so every bit
	// position of 4 bytes is exercised with every value.
	for i := 0; i < 16; i++ {
		if err := c.Set(types.TxnID(100+i), TxnStatus(i%4)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 16; i++ {
		if got, want := c.Get(types.TxnID(100+i)), TxnStatus(i%4); got != want {
			t.Fatalf("Get(%d) = %v, want %v", 100+i, got, want)
		}
	}
}

func TestP9_3_TxnIDPrecedes(t *testing.T) {
	tests := []struct {
		name string
		a, b types.TxnID
		want bool
	}{
		{"plain smaller", 100, 200, true},
		{"plain larger", 200, 100, false},
		{"equal", 100, 100, false},
		{"just before wrap precedes small xid", 4294967290, 5, true},
		{"small xid after wrap does not precede", 5, 4294967290, false},
		{"2^31-1 ahead is still the future", 100, 2147483747, true},
		{"2^31+1 ahead has wrapped into the past", 100, 2147483749, false},
		{"reverse of wrapped pair", 2147483749, 100, true},
		{"exactly 2^31 apart: a precedes b", 100, 2147483748, true},
		{"exactly 2^31 apart: b precedes a too", 2147483748, 100, true},
		{"special vs normal is plain", FrozenTxnID, 4294967290, true},
		{"normal vs special is plain", 4294967290, FrozenTxnID, false},
		{"invalid precedes bootstrap", 0, BootstrapTxnID, true},
		{"special equal", FrozenTxnID, FrozenTxnID, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TxnIDPrecedes(tc.a, tc.b); got != tc.want {
				t.Fatalf("TxnIDPrecedes(%d, %d) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
