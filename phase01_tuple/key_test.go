package tuple

import (
	"bytes"
	"errors"
	"math"
	"math/rand/v2"
	"sort"
	"testing"

	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP1_14_EncodeKey_Golden(t *testing.T) {
	tests := []struct {
		name string
		vals types.Row
		desc []bool
		want string
	}{
		{"int4 1", testkit.R(int32(1)), nil, "01 80000001"},
		{"int4 -1", testkit.R(int32(-1)), nil, "01 7fffffff"},
		{"int4 min", testkit.R(int32(math.MinInt32)), nil, "01 00000000"},
		{"int8 0", testkit.R(0), nil, "01 8000000000000000"},
		{"int8 -2", testkit.R(-2), nil, "01 7ffffffffffffffe"},
		{"bool false/true", testkit.R(false, true), nil, "01 00 01 01"},
		{"float8 1.5", testkit.R(1.5), nil, "01 bff8000000000000"},
		{"float8 -1.5", testkit.R(-1.5), nil, "01 4007ffffffffffff"},
		{"float8 +0", testkit.R(0.0), nil, "01 8000000000000000"},
		{"float8 -0 normalized to +0", testkit.R(math.Copysign(0, -1)), nil, "01 8000000000000000"},
		{"float8 +Inf", testkit.R(math.Inf(1)), nil, "01 fff0000000000000"},
		{"float8 NaN canonical", testkit.R(math.Float64frombits(0xfff8000000000001)), nil, "01 fff8000000000001"},
		{"text with zero byte", testkit.R("a\x00b"), nil, "01 61 00ff 62 0001"},
		{"empty text", testkit.R(""), nil, "01 0001"},
		{"NULL asc", testkit.R(nil), nil, "02"},
		{"NULL desc", testkit.R(nil), []bool{true}, "fd"},
		{"int4 1 desc", testkit.R(int32(1)), []bool{true}, "fe 7ffffffe"},
		{"composite asc, desc", testkit.R(int32(1), "b"), []bool{false, true}, "01 80000001 fe 9d fffe"},
		{"explicit all-asc equals nil", testkit.R(int32(1), nil), []bool{false, false}, "01 80000001 02"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EncodeKey(tc.vals, tc.desc)
			if err != nil {
				t.Fatal(err)
			}
			assertBytes(t, "key", got, mustHex(tc.want))
		})
	}
}

func TestP1_14_EncodeKey_Arity(t *testing.T) {
	if _, err := EncodeKey(testkit.R(1, 2), []bool{true}); !errors.Is(err, ErrArity) {
		t.Fatalf("err = %v, want ErrArity", err)
	}
}

func TestP1_14_EncodeKey_EdgeOrdering(t *testing.T) {
	// Each list is strictly increasing in ORDER BY x ASC NULLS LAST.
	lists := map[string]types.Row{
		"text":   testkit.R("", "\x00", "\x00\x00", "\x00\x01", "\x01", "a", "a\x00", "a\x00b", "a\x01", "ab", "b", "\xff", "\xff\xff", nil),
		"int4":   testkit.R(int32(math.MinInt32), int32(-1), int32(0), int32(1), int32(math.MaxInt32), nil),
		"int8":   testkit.R(int64(math.MinInt64), -256, -1, 0, 255, 256, int64(math.MaxInt64), nil),
		"float8": testkit.R(math.Inf(-1), -1e300, -1.5, -5e-324, 0.0, 5e-324, 1.5, 1e300, math.Inf(1), math.NaN(), nil),
		"bool":   testkit.R(false, true, nil),
	}
	for name, vals := range lists {
		for _, desc := range []bool{false, true} {
			var prev []byte
			for i, v := range vals {
				d := []bool{desc}
				k, err := EncodeKey(types.Row{v}, d)
				if err != nil {
					t.Fatal(err)
				}
				if i > 0 {
					c := bytes.Compare(prev, k)
					if (!desc && c >= 0) || (desc && c <= 0) {
						t.Fatalf("%s desc=%v: key(%v) vs key(%v) compare = %d", name, desc, vals[i-1], v, c)
					}
				}
				prev = k
			}
		}
	}
}

// orderBy compares two rows like ORDER BY c1 [DESC], c2 [DESC], ...
// with Postgres defaults: ASC → NULLS LAST, DESC → NULLS FIRST.
func orderBy(a, b types.Row, desc []bool) int {
	for i := range a {
		var c int
		switch {
		case a[i].IsNull() && b[i].IsNull():
			c = 0
		case a[i].IsNull():
			c = 1
		case b[i].IsNull():
			c = -1
		default:
			c = types.Compare(a[i], b[i])
		}
		if desc[i] {
			c = -c
		}
		if c != 0 {
			return c
		}
	}
	return 0
}

func sign(x int) int {
	switch {
	case x < 0:
		return -1
	case x > 0:
		return 1
	}
	return 0
}

func TestP1_14_EncodeKey_RandomSort(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 14))
	alphabet := []byte{0x00, 0x01, 'a', 'b', 0xff}
	gen := func() types.Row {
		row := make(types.Row, 5)
		for i := range row {
			if r.IntN(6) == 0 {
				row[i] = types.Null()
				continue
			}
			switch i {
			case 0:
				row[i] = types.Bool(r.IntN(2) == 1)
			case 1:
				row[i] = types.Int32(int32(r.IntN(7) - 3))
			case 2:
				b := make([]byte, r.IntN(4))
				for j := range b {
					b[j] = alphabet[r.IntN(len(alphabet))]
				}
				row[i] = types.Text(string(b))
			case 3:
				fs := []float64{math.Inf(-1), -2.5, -1, math.Copysign(0, -1), 0, 0.25, 1, math.Inf(1)}
				row[i] = types.Float64(fs[r.IntN(len(fs))])
			case 4:
				row[i] = types.Int64(r.Int64N(5) - 2)
			}
		}
		return row
	}
	for trial := 0; trial < 4; trial++ {
		desc := make([]bool, 5)
		for i := range desc {
			desc[i] = r.IntN(2) == 1
		}
		rows := make([]types.Row, 250)
		keys := make([][]byte, len(rows))
		for i := range rows {
			rows[i] = gen()
			k, err := EncodeKey(rows[i], desc)
			if err != nil {
				t.Fatal(err)
			}
			keys[i] = k
		}
		// Every pair must agree.
		for i := range rows {
			for j := range rows {
				want := orderBy(rows[i], rows[j], desc)
				if got := sign(bytes.Compare(keys[i], keys[j])); got != want {
					t.Fatalf("desc=%v\n a=%v\n b=%v\n bytes.Compare = %d, ORDER BY = %d\n ka=% x\n kb=% x",
						desc, rows[i], rows[j], got, want, keys[i], keys[j])
				}
			}
		}
		// And sorting by key gives an ORDER BY-sorted sequence.
		idx := make([]int, len(rows))
		for i := range idx {
			idx[i] = i
		}
		sort.Slice(idx, func(a, b int) bool { return bytes.Compare(keys[idx[a]], keys[idx[b]]) < 0 })
		for k := 1; k < len(idx); k++ {
			if orderBy(rows[idx[k-1]], rows[idx[k]], desc) > 0 {
				t.Fatalf("sorted by key but %v comes before %v (desc=%v)", rows[idx[k-1]], rows[idx[k]], desc)
			}
		}
	}
}
