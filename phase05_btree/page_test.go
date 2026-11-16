package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func leafExample() *Node {
	return &Node{ID: 3, Leaf: true, Prev: 7, Next: types.InvalidPageID, Items: []Item{
		{Key: b("ab"), TID: tid(1, 2)},
		{Key: b("c"), TID: tid(3, 4), Payload: b("xy")},
	}}
}

func internalExample() *Node {
	return &Node{ID: 5, Prev: types.InvalidPageID, Next: types.InvalidPageID,
		Items: []Item{{Key: b("m"), TID: tid(4, 2)}}, Children: []types.PageID{3, 9}}
}

func hexEq(t testing.TB, what string, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Fatalf("%s = % x\n want % x", what, got, want)
	}
}

func TestP5_20_EncodePage_LeafGolden(t *testing.T) {
	buf, err := leafExample().EncodePage()
	if err != nil {
		t.Fatal(err)
	}
	if len(buf) != types.PageSize {
		t.Fatalf("page is %d bytes, want 8192", len(buf))
	}
	hexEq(t, "header", buf[0:16], []byte{0x01, 0x00, 0x02, 0x00, 0x18, 0x00, 0xe7, 0x1f, 0x07, 0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0xff})
	hexEq(t, "line pointers", buf[16:24], []byte{0xf4, 0x1f, 0x0c, 0x00, 0xe7, 0x1f, 0x0d, 0x00}) // (8180,12) (8167,13)
	hexEq(t, "tuple 0", buf[8180:8192], []byte{0x02, 0x00, 'a', 'b', 0x01, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00})
	hexEq(t, "tuple 1", buf[8167:8180], []byte{0x01, 0x00, 'c', 0x03, 0x00, 0x00, 0x00, 0x04, 0x00, 0x02, 0x00, 'x', 'y'})
	if !bytes.Equal(buf[24:8167], make([]byte, 8167-24)) {
		t.Fatal("free space between lower and upper is not zeroed")
	}
}

func TestP5_20_EncodePage_InternalGolden(t *testing.T) {
	buf, err := internalExample().EncodePage()
	if err != nil {
		t.Fatal(err)
	}
	hexEq(t, "header", buf[0:16], []byte{0x00, 0x00, 0x02, 0x00, 0x18, 0x00, 0xe7, 0x1f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	hexEq(t, "line pointers", buf[16:24], []byte{0xf4, 0x1f, 0x0c, 0x00, 0xe7, 0x1f, 0x0d, 0x00}) // (8180,12) (8167,13)
	// tuple 0: minus-infinity pivot -> child 3
	hexEq(t, "tuple 0", buf[8180:8192], []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00})
	// tuple 1: "m" (4,2) -> child 9
	hexEq(t, "tuple 1", buf[8167:8180], []byte{0x01, 0x00, 'm', 0x04, 0x00, 0x00, 0x00, 0x02, 0x00, 0x09, 0x00, 0x00, 0x00})
}

func TestP5_20_EncodePage_Sizes(t *testing.T) {
	big := func(n int) *Node {
		return &Node{Leaf: true, Prev: types.InvalidPageID, Next: types.InvalidPageID,
			Items: []Item{{Key: bytes.Repeat([]byte{'k'}, n), TID: tid(1, 1)}}}
	}
	tests := []struct {
		name               string
		node               *Node
		wantErr            error
		wantLower, wantUpp uint16
	}{
		{"empty leaf", &Node{Leaf: true, Prev: types.InvalidPageID, Next: types.InvalidPageID}, nil, 16, 8192},
		{"exact fit: 16 + 4 + (2+8162+6+2) = 8192", big(8162), nil, 20, 20},
		{"one byte too many", big(8163), ErrNodeTooLarge, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf, err := tc.node.EncodePage()
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err != nil {
				if buf != nil {
					t.Fatal("buffer returned with an error")
				}
				return
			}
			lower, upper := binary.LittleEndian.Uint16(buf[4:]), binary.LittleEndian.Uint16(buf[6:])
			if lower != tc.wantLower || upper != tc.wantUpp {
				t.Fatalf("lower/upper = %d/%d, want %d/%d", lower, upper, tc.wantLower, tc.wantUpp)
			}
		})
	}
}

func sameNode(t testing.TB, got, want *Node) {
	t.Helper()
	if got.ID != want.ID || got.Leaf != want.Leaf || got.Prev != want.Prev || got.Next != want.Next {
		t.Fatalf("header = id %d leaf %v prev %d next %d, want id %d leaf %v prev %d next %d",
			got.ID, got.Leaf, got.Prev, got.Next, want.ID, want.Leaf, want.Prev, want.Next)
	}
	if !slices.Equal(got.Children, want.Children) {
		t.Fatalf("children = %v, want %v", got.Children, want.Children)
	}
	sameItems(t, got.Items, want.Items)
}

func TestP5_21_DecodePage_Examples(t *testing.T) {
	for _, n := range []*Node{leafExample(), internalExample(), {ID: 0, Leaf: true, Prev: types.InvalidPageID, Next: types.InvalidPageID}} {
		t.Run(fmt.Sprintf("node %d leaf=%v", n.ID, n.Leaf), func(t *testing.T) {
			buf, err := n.EncodePage()
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodePage(n.ID, buf)
			if err != nil {
				t.Fatalf("DecodePage: %v", err)
			}
			sameNode(t, got, n)
			if got.Leaf && got.Children != nil {
				t.Fatalf("leaf decoded with children %v", got.Children)
			}
		})
	}
}

func TestP5_21_DecodePage_CopiesAndNils(t *testing.T) {
	n := &Node{Leaf: true, Prev: types.InvalidPageID, Next: types.InvalidPageID, Items: []Item{
		{Key: []byte{}, TID: tid(0, 0)},
		{Key: b("a"), TID: tid(0, 1), Payload: b("p")},
	}}
	buf, _ := n.EncodePage()
	got, err := DecodePage(1, buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Items[0].Key == nil || len(got.Items[0].Key) != 0 {
		t.Fatalf("empty key decoded as %#v, want []byte{}", got.Items[0].Key)
	}
	if got.Items[0].Payload != nil {
		t.Fatalf("empty payload decoded as %#v, want nil", got.Items[0].Payload)
	}
	for i := range buf {
		buf[i] = 0xEE
	}
	if string(got.Items[1].Key) != "a" || string(got.Items[1].Payload) != "p" {
		t.Fatalf("decoded node aliases the page buffer: %q %q", got.Items[1].Key, got.Items[1].Payload)
	}
}

func TestP5_21_DecodePage_RoundTripRandom(t *testing.T) {
	rng := rand.New(rand.NewPCG(21, 21))
	randBytes := func(max int) []byte {
		n := rng.IntN(max + 1)
		out := make([]byte, n)
		for i := range out {
			out[i] = byte(rng.IntN(256))
		}
		return out
	}
	for i := range 200 {
		n := &Node{ID: types.PageID(i), Leaf: rng.IntN(2) == 0, Prev: types.PageID(rng.Uint32()), Next: types.PageID(rng.Uint32())}
		k := rng.IntN(40)
		for j := 0; j < k; j++ {
			it := Item{Key: randBytes(30), TID: types.TID{Page: types.PageID(rng.Uint32()), Slot: types.SlotID(rng.IntN(65536))}}
			if n.Leaf {
				it.Payload = randBytes(20)
				if len(it.Payload) == 0 {
					it.Payload = nil
				}
			}
			n.Items = append(n.Items, it)
		}
		if !n.Leaf {
			for j := 0; j <= k; j++ {
				n.Children = append(n.Children, types.PageID(rng.Uint32()))
			}
		}
		buf, err := n.EncodePage()
		if err != nil {
			t.Fatalf("node %d: EncodePage: %v", i, err)
		}
		got, err := DecodePage(n.ID, buf)
		if err != nil {
			t.Fatalf("node %d: DecodePage: %v", i, err)
		}
		sameNode(t, got, n)
	}
}

func TestP5_21_DecodePage_Corrupt(t *testing.T) {
	put16 := func(off int, v uint16) func([]byte) []byte {
		return func(p []byte) []byte { binary.LittleEndian.PutUint16(p[off:], v); return p }
	}
	tests := []struct {
		name     string
		internal bool
		mutate   func([]byte) []byte
	}{
		{"short buffer", false, func(p []byte) []byte { return p[:8191] }},
		{"long buffer", false, func(p []byte) []byte { return append(p, 0) }},
		{"all zero page (lower 0)", false, func(p []byte) []byte { return make([]byte, 8192) }},
		{"unknown flag bit", false, put16(0, 2)},
		{"lower does not match nitems", false, put16(4, 28)},
		{"nitems does not match lower", false, put16(2, 3)},
		{"upper below lower", false, put16(6, 10)},
		{"upper beyond the page", false, put16(6, 8193)},
		{"line pointer below upper", false, put16(16, 8100)},
		{"line pointer runs off the page", false, put16(18, 13)},
		{"keyLen too large for the tuple", false, put16(8180, 5)},
		{"keyLen 0xffff", false, put16(8180, 0xffff)},
		{"payloadLen does not match", false, put16(8167+9, 3)},
		{"internal tuple with a payload-sized length", true, put16(18, 11)},
		{"internal keyLen does not match", true, put16(8167, 2)},
		{"internal page with no tuples", true, func(p []byte) []byte {
			q := make([]byte, 8192)
			binary.LittleEndian.PutUint16(q[4:], 16)
			binary.LittleEndian.PutUint16(q[6:], 8192)
			return q
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := leafExample()
			if tc.internal {
				n = internalExample()
			}
			buf, err := n.EncodePage()
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodePage(1, tc.mutate(buf))
			if !errors.Is(err, ErrCorruptPage) || got != nil {
				t.Fatalf("DecodePage = (%v, %v), want (nil, ErrCorruptPage)", got, err)
			}
		})
	}
}

func TestP5_21_DecodePage_GarbageNeverPanics(t *testing.T) {
	rng := rand.New(rand.NewPCG(21, 99))
	for _, n := range []*Node{leafExample(), internalExample()} {
		good, _ := n.EncodePage()
		for range 3000 {
			p := slices.Clone(good)
			for range 1 + rng.IntN(3) {
				// Mostly hit the header, line pointers and tuples.
				off := rng.IntN(24)
				if rng.IntN(2) == 0 {
					off = 8167 + rng.IntN(25)
				}
				p[off] = byte(rng.IntN(256))
			}
			got, err := DecodePage(1, p)
			if (got == nil) == (err == nil) {
				t.Fatalf("DecodePage returned node %v and error %v", got, err)
			}
			if err != nil && !errors.Is(err, ErrCorruptPage) {
				t.Fatalf("unexpected error %v", err)
			}
		}
	}
}
