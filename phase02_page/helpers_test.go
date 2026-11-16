package page

import (
	"encoding/binary"
	"testing"
)

// lp describes one slot for mkPage.
type lp struct {
	flags LPFlags
	data  string // tuple bytes; ignored for LPUnused
}

// mkPage lays out a page by writing raw bytes (it calls NO problem code, so any
// test may use it). Slots are filled in order; every non-UNUSED slot's data is
// placed just below the previous one, exactly like repeated InsertTuple calls.
func mkPage(t testing.TB, slots ...lp) *Page {
	t.Helper()
	p := NewPage()
	upper := PageSize
	for i, s := range slots {
		at := HeaderSize + i*LinePointerLen
		if s.flags == LPUnused {
			continue
		}
		upper -= len(s.data)
		copy(p.buf[upper:], s.data)
		binary.LittleEndian.PutUint16(p.buf[at:], uint16(upper))
		binary.LittleEndian.PutUint16(p.buf[at+2:], uint16(s.flags)<<14|uint16(len(s.data)))
	}
	p.setLower(uint16(HeaderSize + len(slots)*LinePointerLen))
	p.setUpper(uint16(upper))
	return p
}

// rawLP returns the 4 raw bytes of a line pointer.
func rawLP(p *Page, slot int) []byte {
	at := HeaderSize + slot*LinePointerLen
	return append([]byte(nil), p.buf[at:at+LinePointerLen]...)
}

func snapshot(p *Page) [PageSize]byte { return p.buf }

func assertUnchanged(t testing.TB, p *Page, before [PageSize]byte) {
	t.Helper()
	if p.buf != before {
		for i := range p.buf {
			if p.buf[i] != before[i] {
				t.Fatalf("page modified: first difference at byte %d (%#x → %#x), want page unchanged", i, before[i], p.buf[i])
			}
		}
	}
}

func repeat(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
