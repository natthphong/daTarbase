package heap

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

// tid is a short TID constructor for tests.
func tid(p, s int) types.TID { return types.TID{Page: types.PageID(p), Slot: types.SlotID(s)} }

// mkStore builds a PageStore whose page i holds the strings pages[i], all
// live. It does not count reads.
func mkStore(pages ...[]string) *PageStore {
	s := NewPageStore()
	for _, tuples := range pages {
		_, p := s.Extend()
		for _, t := range tuples {
			p.Add([]byte(t))
		}
	}
	return s
}

// kill marks a slot dead (deleted) directly, without counting reads.
func kill(s *PageStore, t types.TID) { s.Peek(t.Page).Dead[t.Slot] = true }

// forward marks a slot dead and points it at a newer version.
func forward(s *PageStore, from, to types.TID) {
	p := s.Peek(from.Page)
	p.Dead[from.Slot] = true
	p.Forward[from.Slot] = to
}

// fill returns n copies of byte b.
func fill(n int, b byte) []byte { return bytes.Repeat([]byte{b}, n) }

// drain pulls every tuple out of a Next-style iterator.
func drain(t *testing.T, next func() (Tuple, bool, error)) []Tuple {
	t.Helper()
	var out []Tuple
	for i := 0; i < 10000; i++ {
		tu, ok, err := next()
		if err != nil {
			t.Fatalf("Next: unexpected error %v", err)
		}
		if !ok {
			return out
		}
		out = append(out, tu)
	}
	t.Fatalf("Next never returned ok=false")
	return nil
}

// fmtTuples renders tuples as "(p,s)=data ..." for readable comparisons.
func fmtTuples(ts []Tuple) string {
	var b bytes.Buffer
	for i, tu := range ts {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%v=%s", tu.TID, tu.Data)
	}
	return b.String()
}

func assertTuples(t *testing.T, got []Tuple, want string) {
	t.Helper()
	if g := fmtTuples(got); g != want {
		t.Fatalf("tuples:\n  got:  %q\n  want: %q", g, want)
	}
}
