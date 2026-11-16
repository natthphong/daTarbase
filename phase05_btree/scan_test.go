package btree

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

func drain(c *Cursor) []Item {
	var out []Item
	for {
		it, ok := c.Next()
		if !ok {
			return out
		}
		out = append(out, it)
	}
}

func b(s string) []byte { return []byte(s) }

func TestP5_12_Scan(t *testing.T) {
	tests := []struct {
		name      string
		lo, hi    Bound
		desc      bool
		want      string
		wantReads int
	}{
		{"full ascending", NoBound(), NoBound(), false, "a b c d e f g h i j", 4},
		{"full descending", NoBound(), NoBound(), true, "j i h g f e d c b a", 4},
		{"ASC [b, e]", Incl(b("b")), Incl(b("e")), false, "b c d e", 3},
		{"ASC (b, e)", Excl(b("b")), Excl(b("e")), false, "c d", 3},
		{"ASC [c, c] reads the next leaf to learn d > c", Incl(b("c")), Incl(b("c")), false, "c", 3},
		{"ASC [d, d] starts at the end of leaf 0", Incl(b("d")), Incl(b("d")), false, "d", 3},
		{"ASC (c, +inf)", Excl(b("c")), NoBound(), false, "d e f g h i j", 4},
		{"ASC [x, +inf) is empty", Incl(b("x")), NoBound(), false, "", 2},
		{"ASC (-inf, b]", NoBound(), Incl(b("b")), false, "a b", 2},
		{"ASC lo > hi is empty", Incl(b("e")), Incl(b("b")), false, "", 2},
		{"DESC [b, e]", Incl(b("b")), Incl(b("e")), true, "e d c b", 3},
		{"DESC (b, e)", Excl(b("b")), Excl(b("e")), true, "d c", 3},
		{"DESC (-inf, d)", NoBound(), Excl(b("d")), true, "c b a", 2},
		{"DESC [g, +inf)", Incl(b("g")), NoBound(), true, "j i h g", 3},
		{"DESC [a, a]", Incl(b("a")), Incl(b("a")), true, "a", 2},
		{"DESC (-inf, a) is empty", NoBound(), Excl(b("a")), true, "", 2},
		{"DESC lo > hi is empty", Incl(b("e")), Incl(b("b")), true, "", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := abcTree(t)
			got := keysOf(drain(tr.Scan(tc.lo, tc.hi, tc.desc)))
			if got != tc.want {
				t.Fatalf("scan = %q, want %q", got, tc.want)
			}
			wantCounters(t, tr.Store, tc.wantReads, 0, 0, 0)
		})
	}
}

func TestP5_12_Scan_Duplicates(t *testing.T) {
	ks := []types.TID{tid(1, 1), tid(1, 2), tid(2, 1), tid(2, 5), tid(3, 1), tid(3, 2), tid(4, 4), tid(5, 3)}
	rev := slices.Clone(ks)
	slices.Reverse(rev)
	tests := []struct {
		name      string
		lo, hi    Bound
		desc      bool
		wantKeys  string
		wantTIDs  []types.TID // nil = don't check
		wantReads int
	}{
		{"ASC [k, k] walks three leaves", Incl(b("k")), Incl(b("k")), false, "k k k k k k k k", ks, 4},
		{"DESC [k, k] walks them backwards", Incl(b("k")), Incl(b("k")), true, "k k k k k k k k", rev, 4},
		{"ASC (k, +inf) jumps past every k", Excl(b("k")), NoBound(), false, "m", nil, 2},
		{"DESC (-inf, k) jumps before every k", NoBound(), Excl(b("k")), true, "a", nil, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := dupTree(t)
			items := drain(tr.Scan(tc.lo, tc.hi, tc.desc))
			if got := keysOf(items); got != tc.wantKeys {
				t.Fatalf("scan = %q, want %q", got, tc.wantKeys)
			}
			if tc.wantTIDs != nil {
				var got []types.TID
				for _, it := range items {
					got = append(got, it.TID)
				}
				if !slices.Equal(got, tc.wantTIDs) {
					t.Fatalf("TIDs = %v, want %v", got, tc.wantTIDs)
				}
			}
			wantCounters(t, tr.Store, tc.wantReads, 0, 0, 0)
		})
	}
}

func TestP5_12_Scan_LazyAndDone(t *testing.T) {
	tr := abcTree(t)
	c := tr.Scan(NoBound(), NoBound(), false)
	if tr.Store.Reads != 0 {
		t.Fatalf("Scan read %d nodes before the first Next", tr.Store.Reads)
	}
	c.Next()
	c.Next()
	if tr.Store.Reads != 2 {
		t.Fatalf("after 2 entries reads = %d, want 2 (root + first leaf)", tr.Store.Reads)
	}
	drain(c)
	reads := tr.Store.Reads
	for range 3 {
		if it, ok := c.Next(); ok {
			t.Fatalf("Next after end returned %s", it.Key)
		}
	}
	if tr.Store.Reads != reads {
		t.Fatalf("Next after end read %d more nodes", tr.Store.Reads-reads)
	}
}

func TestP5_12_Scan_EmptyTreeAndPayload(t *testing.T) {
	tr := New(NewNodeStore(), 4)
	for _, desc := range []bool{false, true} {
		if got := drain(tr.Scan(NoBound(), NoBound(), desc)); len(got) != 0 {
			t.Fatalf("empty tree (desc=%v) returned %d entries", desc, len(got))
		}
	}
	tr = build(t, 4, LI(Item{Key: b("k"), TID: tid(1, 1), Payload: b("alice")}))
	got := drain(tr.Scan(Incl(b("k")), Incl(b("k")), false))
	if len(got) != 1 || string(got[0].Payload) != "alice" {
		t.Fatalf("scan lost the payload: %+v", got)
	}
}

func TestP5_12_Scan_RandomRangesAgainstOracle(t *testing.T) {
	rng := rand.New(rand.NewPCG(12, 12))
	tr := New(NewNodeStore(), 4)
	var want oracle
	for range 600 {
		it := e(fmt.Sprintf("%02d", rng.IntN(100)), rng.IntN(10), 0)
		if tr.Insert(it) == nil {
			want.insert(it)
		}
	}
	randBound := func() Bound {
		switch rng.IntN(5) {
		case 0:
			return NoBound()
		case 1, 2:
			return Incl(b(fmt.Sprintf("%02d", rng.IntN(105))))
		default:
			return Excl(b(fmt.Sprintf("%02d", rng.IntN(105))))
		}
	}
	in := func(key []byte, lo, hi Bound) bool {
		if !lo.Unbounded {
			c := bytes.Compare(key, lo.Key)
			if c < 0 || (c == 0 && !lo.Inclusive) {
				return false
			}
		}
		if !hi.Unbounded {
			c := bytes.Compare(key, hi.Key)
			if c > 0 || (c == 0 && !hi.Inclusive) {
				return false
			}
		}
		return true
	}
	for q := range 300 {
		lo, hi, desc := randBound(), randBound(), rng.IntN(2) == 0
		var exp []Item
		for _, it := range want {
			if in(it.Key, lo, hi) {
				exp = append(exp, it)
			}
		}
		if desc {
			slices.Reverse(exp)
		}
		got := drain(tr.Scan(lo, hi, desc))
		if len(got) != len(exp) {
			t.Fatalf("query %d (%+v, %+v, desc=%v): %d entries, want %d", q, lo, hi, desc, len(got), len(exp))
		}
		sameItems(t, got, exp)
	}
}

func TestP5_13_Prefix_CompositeKey(t *testing.T) {
	// Index on orders(customer_id, order_date).
	orders := []struct {
		cust int64
		date string
		tid  types.TID
	}{
		{7, "2026-03-15", tid(1, 1)},
		{3, "2026-01-05", tid(1, 2)},
		{7, "2026-02-10", tid(1, 3)},
		{8, "2026-01-01", tid(2, 1)},
		{-1, "2026-03-01", tid(2, 2)},
		{7, "2026-04-02", tid(2, 3)},
		{6, "2026-03-03", tid(3, 1)},
		{7, "2026-03-01", tid(3, 2)},
		{70, "2026-03-01", tid(3, 3)},
	}
	tr := New(NewNodeStore(), 4)
	for _, o := range orders {
		if err := tr.Insert(Item{Key: Key(EncodeInt(o.cust), EncodeText(o.date)), TID: o.tid}); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name   string
		prefix []byte
		want   []types.TID
	}{
		{"customer 7, ordered by date", EncodeInt(7), []types.TID{tid(1, 3), tid(3, 2), tid(1, 1), tid(2, 3)}},
		{"customer 7 in March (LIKE '2026-03%')", Key(EncodeInt(7), b("2026-03")), []types.TID{tid(3, 2), tid(1, 1)}},
		{"customer 7 on an exact date", Key(EncodeInt(7), EncodeText("2026-03-01")), []types.TID{tid(3, 2)}},
		{"negative customer sorts and matches", EncodeInt(-1), []types.TID{tid(2, 2)}},
		{"customer without orders", EncodeInt(9), nil},
		{"empty prefix scans everything in key order", nil, []types.TID{tid(2, 2), tid(1, 2), tid(3, 1), tid(1, 3), tid(3, 2), tid(1, 1), tid(2, 3), tid(2, 1), tid(3, 3)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []types.TID
			for _, it := range drain(tr.Prefix(tc.prefix)) {
				got = append(got, it.TID)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Prefix = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestP5_13_Prefix_TextTerminator(t *testing.T) {
	// Index on people(last_name, first_name).
	people := [][2]string{{"smith", "john"}, {"smithers", "waylon"}, {"smith", "anna"}, {"smit", "x"}, {"smyth", "zoe"}}
	tr := New(NewNodeStore(), 4)
	for i, p := range people {
		key := Key(EncodeText(p[0]), EncodeText(p[1]))
		if err := tr.Insert(Item{Key: key, TID: tid(1, i), Payload: b(p[0] + " " + p[1])}); err != nil {
			t.Fatal(err)
		}
	}
	names := func(c *Cursor) string {
		var out []string
		for _, it := range drain(c) {
			out = append(out, string(it.Payload))
		}
		return fmt.Sprint(out)
	}
	if got := names(tr.Prefix(EncodeText("smith"))); got != "[smith anna smith john]" {
		t.Fatalf("last_name = 'smith': %s, want [smith anna smith john]", got)
	}
	if got := names(tr.Prefix(b("smith"))); got != "[smith anna smith john smithers waylon]" {
		t.Fatalf("last_name LIKE 'smith%%': %s", got)
	}
	if got := names(tr.Prefix(b("smi"))); got != "[smit x smith anna smith john smithers waylon]" {
		t.Fatalf("last_name LIKE 'smi%%': %s", got)
	}
}

func TestP5_13_Prefix_FFBytes(t *testing.T) {
	raw := [][]byte{{0x01, 0xFF, 0x00}, {0x01, 0xFF}, {0x01}, {0x02}, {0xFF, 0xFF, 0x01}, {0xFF}, {0xFF, 0xFF}}
	tr := New(NewNodeStore(), 4)
	for i, k := range raw {
		if err := tr.Insert(Item{Key: k, TID: tid(1, i)}); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name   string
		prefix []byte
		want   string
	}{
		{"trailing ff is dropped before incrementing", []byte{0x01, 0xFF}, "[01ff 01ff00]"},
		{"single byte", []byte{0x01}, "[01 01ff 01ff00]"},
		{"all ff: no upper bound", []byte{0xFF, 0xFF}, "[ffff ffff01]"},
		{"single ff", []byte{0xFF}, "[ff ffff ffff01]"},
		{"empty prefix", []byte{}, "[01 01ff 01ff00 02 ff ffff ffff01]"},
		{"nothing matches", []byte{0x03}, "[]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.prefix)
			var got []string
			for _, it := range drain(tr.Prefix(tc.prefix)) {
				got = append(got, fmt.Sprintf("%x", it.Key))
			}
			if s := fmt.Sprint(got); s != tc.want {
				t.Fatalf("Prefix(%x) = %s, want %s", tc.prefix, s, tc.want)
			}
			if !bytes.Equal(before, tc.prefix) {
				t.Fatalf("Prefix modified the caller's slice: %x -> %x", before, tc.prefix)
			}
		})
	}
}

// coveringTree: index on id INCLUDE (name); ids 1..6 live on heap pages 1,1,2,2,3,3.
func coveringTree(t testing.TB) *Tree {
	tr := New(NewNodeStore(), 4)
	for i := 1; i <= 6; i++ {
		it := Item{Key: EncodeInt(int64(i)), TID: tid((i+1)/2, (i+1)%2+1), Payload: b(fmt.Sprintf("name-%d", i))}
		if err := tr.Insert(it); err != nil {
			t.Fatal(err)
		}
	}
	return tr
}

func TestP5_14_IndexOnlyScan(t *testing.T) {
	tests := []struct {
		name        string
		lo, hi      Bound
		visiblePage map[types.PageID]bool // all-visible pages
		dead        map[types.TID]bool
		want        string
		wantFetches int
	}{
		{"page 2 not all-visible, (2,2) dead", Incl(EncodeInt(2)), Incl(EncodeInt(5)),
			map[types.PageID]bool{1: true, 3: true}, map[types.TID]bool{tid(2, 2): true}, "[name-2 name-3 name-5]", 2},
		{"everything all-visible: zero heap fetches", NoBound(), NoBound(),
			map[types.PageID]bool{1: true, 2: true, 3: true}, nil, "[name-1 name-2 name-3 name-4 name-5 name-6]", 0},
		{"nothing all-visible: one fetch per entry", NoBound(), NoBound(),
			nil, map[types.TID]bool{tid(1, 1): true, tid(3, 2): true}, "[name-2 name-3 name-4 name-5]", 6},
		{"range outside the invisible page", Excl(EncodeInt(4)), NoBound(),
			map[types.PageID]bool{1: true, 3: true}, map[types.TID]bool{tid(2, 2): true}, "[name-5 name-6]", 0},
		{"empty range", Incl(EncodeInt(10)), NoBound(), nil, nil, "[]", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := coveringTree(t)
			calls := 0
			allVisible := func(p types.PageID) bool { return tc.visiblePage[p] }
			heapVisible := func(x types.TID) bool {
				calls++
				if tc.visiblePage[x.Page] {
					t.Fatalf("heapVisible(%v) called for an all-visible page", x)
				}
				return !tc.dead[x]
			}
			items, fetches := tr.IndexOnlyScan(tc.lo, tc.hi, allVisible, heapVisible)
			var names []string
			for _, it := range items {
				names = append(names, string(it.Payload))
			}
			if got := fmt.Sprint(names); got != tc.want {
				t.Fatalf("rows = %s, want %s", got, tc.want)
			}
			if fetches != tc.wantFetches || calls != tc.wantFetches {
				t.Fatalf("heap fetches = %d (heapVisible calls %d), want %d", fetches, calls, tc.wantFetches)
			}
			for _, it := range items {
				if !bytes.Equal(it.Key, EncodeInt(int64(it.Payload[len(it.Payload)-1]-'0'))) {
					t.Fatalf("entry key %x does not match payload %s", it.Key, it.Payload)
				}
			}
		})
	}
}
