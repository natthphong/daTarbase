package page

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func helloDB(t testing.TB) *Page {
	t.Helper()
	p := NewPage()
	mustInsert(t, p, "hello")
	mustInsert(t, p, "db")
	return p
}

func TestP2_11_ComputeChecksum(t *testing.T) {
	tests := []struct {
		name string
		page func(t *testing.T) *Page
		id   types.PageID
		want uint16
	}{
		{"empty page block 0", func(t *testing.T) *Page { return NewPage() }, 0, 16637},
		{"empty page block 1", func(t *testing.T) *Page { return NewPage() }, 1, 16636},
		{"hello+db block 7", func(t *testing.T) *Page { return helloDB(t) }, 7, 9245},
		{"stored checksum is ignored", func(t *testing.T) *Page {
			p := helloDB(t)
			p.SetStoredChecksum(0xFFFF)
			return p
		}, 7, 9245},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.page(t)
			before := snapshot(p)
			if got := p.ComputeChecksum(tc.id); got != tc.want {
				t.Fatalf("ComputeChecksum(%d) = %d, want %d", tc.id, got, tc.want)
			}
			assertUnchanged(t, p, before)
		})
	}
}

// Cross-check against hash/fnv: FNV-1a 32 over the page with bytes 6..8 zeroed.
func TestP2_11_ComputeChecksum_MatchesFNV1a(t *testing.T) {
	p := helloDB(t)
	p.SetStoredChecksum(0x1234)
	for _, id := range []types.PageID{0, 3, 65535, 65536, 1 << 31} {
		img := p.buf
		img[6], img[7] = 0, 0
		h := fnv.New32a()
		h.Write(img[:])
		want := uint16((h.Sum32()^uint32(id))%65535) + 1
		if got := p.ComputeChecksum(id); got != want {
			t.Fatalf("ComputeChecksum(%d) = %d, want %d", id, got, want)
		}
	}
}

func TestP2_11_ComputeChecksum_DetectsChanges(t *testing.T) {
	p := helloDB(t)
	base := p.ComputeChecksum(7)
	p.buf[8190] ^= 0x01 // flip one bit inside "hello"
	if p.ComputeChecksum(7) == base {
		t.Fatalf("single bit flip in tuple data did not change the checksum")
	}
	p.buf[8190] ^= 0x01
	if p.ComputeChecksum(7) != base {
		t.Fatalf("checksum not deterministic")
	}
	if p.ComputeChecksum(8) == base {
		t.Fatalf("checksum ignores the block number")
	}
}

func TestP2_12_VerifyChecksum(t *testing.T) {
	p := NewPage()
	if err := p.VerifyChecksum(0); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("unstamped page: err = %v, want ErrChecksumMismatch", err)
	}
	p.SetStoredChecksum(p.ComputeChecksum(0))
	before := snapshot(p)
	if err := p.VerifyChecksum(0); err != nil {
		t.Fatalf("stamped page: err = %v, want nil", err)
	}
	if err := p.VerifyChecksum(1); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("wrong block: err = %v, want ErrChecksumMismatch", err)
	}
	assertUnchanged(t, p, before)
	p.buf[5000] = 1 // bit rot in the free hole
	if err := p.VerifyChecksum(0); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("corrupted page: err = %v, want ErrChecksumMismatch", err)
	}
}

func TestP2_13_Serialize(t *testing.T) {
	p := NewPage()
	b := p.Serialize(0)
	if len(b) != PageSize {
		t.Fatalf("len = %d, want %d", len(b), PageSize)
	}
	if !bytes.Equal(b[6:8], []byte{0xFD, 0x40}) {
		t.Fatalf("b[6:8] = % X, want FD 40 (16637)", b[6:8])
	}
	if p.StoredChecksum() != 0 {
		t.Fatalf("page StoredChecksum = %d, want 0 (only the copy is stamped)", p.StoredChecksum())
	}
	b[100] = 0xEE
	if p.buf[100] != 0 {
		t.Fatalf("Serialize result aliases the page")
	}
}

func TestP2_13_Serialize_CopiesEverythingElse(t *testing.T) {
	p := helloDB(t)
	p.SetStoredChecksum(0xAAAA)
	b := p.Serialize(7)
	if got := binary.LittleEndian.Uint16(b[6:]); got != 9245 {
		t.Fatalf("stamped checksum = %d, want 9245", got)
	}
	if !bytes.Equal(b[:6], p.buf[:6]) || !bytes.Equal(b[8:], p.buf[8:]) {
		t.Fatalf("copy differs from page outside bytes 6..8")
	}
	if p.StoredChecksum() != 0xAAAA {
		t.Fatalf("page StoredChecksum = %#x, want 0xaaaa untouched", p.StoredChecksum())
	}
}

// image returns a page image for block id with a freshly stamped (valid)
// checksum after applying mutate — so Load gets past the checksum check.
func image(t testing.TB, p *Page, id types.PageID, mutate func(b []byte)) []byte {
	t.Helper()
	q := &Page{buf: p.buf}
	mutate(q.buf[:])
	q.SetStoredChecksum(q.ComputeChecksum(id))
	return append([]byte(nil), q.buf[:]...)
}

func TestP2_14_Load_RoundTrip(t *testing.T) {
	p := helloDB(t)
	p.DeleteTuple(0)
	b := p.Serialize(5)
	q, err := Load(b, 5)
	if err != nil {
		t.Fatalf("Load = %v, want nil", err)
	}
	if !bytes.Equal(q.Bytes(), b) {
		t.Fatalf("loaded page differs from image")
	}
	b[8185] = 'X'
	if got, _ := q.GetTuple(1); string(got) != "db" {
		t.Fatalf("GetTuple(1) = %q after changing the image; Load must copy", got)
	}
	if _, err := Load(p.Serialize(5), 6); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("wrong block: err = %v, want ErrChecksumMismatch", err)
	}
}

func TestP2_14_Load_AllZeroIsNewPage(t *testing.T) {
	q, err := Load(make([]byte, PageSize), 9)
	if err != nil {
		t.Fatalf("all-zero image: err = %v, want nil", err)
	}
	if q.Lower() != 8 || q.Upper() != 8192 || q.NumSlots() != 0 {
		t.Fatalf("all-zero image → lower/upper %d/%d, want a NewPage (8/8192)", q.Lower(), q.Upper())
	}
}

func TestP2_14_Load_Errors(t *testing.T) {
	base := mkPage(t, lp{LPNormal, "hello"}, lp{LPDead, "db"}, lp{}) // lower=20 upper=8185
	set16 := func(at int, v uint16) func([]byte) {
		return func(b []byte) { binary.LittleEndian.PutUint16(b[at:], v) }
	}
	tests := []struct {
		name    string
		img     func() []byte
		wantErr error
	}{
		{"too short", func() []byte { return make([]byte, 100) }, ErrCorruptPage},
		{"too long", func() []byte { return make([]byte, PageSize+1) }, ErrCorruptPage},
		{"nil", func() []byte { return nil }, ErrCorruptPage},
		{"never stamped", func() []byte { return append([]byte(nil), base.buf[:]...) }, ErrChecksumMismatch},
		{"bit flip after stamping", func() []byte {
			b := image(t, base, 0, func([]byte) {})
			b[8190] ^= 0x10
			return b
		}, ErrChecksumMismatch},
		{"checksum checked before header", func() []byte {
			b := append([]byte(nil), base.buf[:]...)
			binary.LittleEndian.PutUint16(b[0:], 10)
			return b
		}, ErrChecksumMismatch},
		{"lower misaligned", func() []byte { return image(t, base, 0, set16(0, 10)) }, ErrCorruptPage},
		{"lower inside header", func() []byte { return image(t, base, 0, set16(0, 4)) }, ErrCorruptPage},
		{"lower above upper", func() []byte { return image(t, base, 0, set16(0, 8188)) }, ErrCorruptPage},
		{"upper past page end", func() []byte { return image(t, base, 0, set16(2, 8200)) }, ErrCorruptPage},
		{"unknown flags", func() []byte { return image(t, base, 0, set16(4, 1)) }, ErrCorruptPage},
		{"unused slot with an offset", func() []byte { return image(t, base, 0, set16(16, 8000)) }, ErrCorruptPage},
		{"normal slot with length 0", func() []byte { return image(t, base, 0, set16(10, uint16(LPNormal)<<14)) }, ErrCorruptPage},
		{"normal slot below upper", func() []byte { return image(t, base, 0, set16(8, 8184)) }, ErrCorruptPage},
		{"normal slot past page end", func() []byte { return image(t, base, 0, set16(8, 8188)) }, ErrCorruptPage},
		{"dead slot past page end", func() []byte { return image(t, base, 0, set16(14, uint16(LPDead)<<14|8)) }, ErrCorruptPage},
		{"redirect slot", func() []byte { return image(t, base, 0, set16(14, uint16(LPRedirect)<<14|2)) }, ErrCorruptPage},
		{"valid page with dead and unused slots", func() []byte { return image(t, base, 0, func([]byte) {}) }, nil},
		{"lower == upper is valid", func() []byte {
			return image(t, mkPage(t, lp{LPNormal, repeat('x', 8180)}), 0, func([]byte) {})
		}, nil},
		{"dead tuple ending exactly at page end is valid", func() []byte { return image(t, base, 0, set16(14, uint16(LPDead)<<14|7)) }, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q, err := Load(tc.img(), 0)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if (err == nil) != (q != nil) {
				t.Fatalf("page = %v with err = %v; want a page iff err == nil", q != nil, err)
			}
		})
	}
}

func TestP2_15_Dump(t *testing.T) {
	p := NewPage()
	for _, s := range []string{"aaaa", "bbbbbbbb", "cc", "ddd", "hello world!"} {
		mustInsert(t, p, s)
	}
	p.DeleteTuple(3)
	p.Compact()
	p.DeleteTuple(1)
	before := snapshot(p)

	want := "" +
		"lower=28 upper=8166 free=8138 slots=5\n" +
		" lp | lp_off | lp_flags   | lp_len | t_data\n" +
		"  0 |   8188 | 1 NORMAL   |      4 | 61616161\n" +
		"  1 |   8180 | 3 DEAD     |      8\n" +
		"  2 |   8178 | 1 NORMAL   |      2 | 6363\n" +
		"  3 |      0 | 0 UNUSED   |      0\n" +
		"  4 |   8166 | 1 NORMAL   |     12 | 68656c6c6f20776f..\n"
	if got := p.Dump(); got != want {
		t.Fatalf("Dump() =\n%s\nwant:\n%s", got, want)
	}
	assertUnchanged(t, p, before)
}

func TestP2_15_Dump_Edges(t *testing.T) {
	tests := []struct {
		name string
		page *Page
		want string
	}{
		{"empty page", NewPage(),
			"lower=8 upper=8192 free=8180 slots=0\n" +
				" lp | lp_off | lp_flags   | lp_len | t_data\n"},
		{"exactly 8 bytes has no ellipsis, redirect has no data",
			mkPage(t, lp{LPNormal, "12345678"}, lp{LPRedirect, "r"}),
			"lower=16 upper=8183 free=8163 slots=2\n" +
				" lp | lp_off | lp_flags   | lp_len | t_data\n" +
				"  0 |   8184 | 1 NORMAL   |      8 | 3132333435363738\n" +
				"  1 |   8183 | 2 REDIRECT |      1\n"},
		{"9 bytes is cut to 8",
			mkPage(t, lp{LPNormal, "\x00\x01\x02\x03\x04\x05\x06\x07\xff"}),
			"lower=12 upper=8183 free=8167 slots=1\n" +
				" lp | lp_off | lp_flags   | lp_len | t_data\n" +
				"  0 |   8183 | 1 NORMAL   |      9 | 0001020304050607..\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.page.Dump(); got != tc.want {
				t.Fatalf("Dump() =\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}
