package tuple

import (
	"encoding/hex"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

// sampleSchema is the schema used across this phase's tests and doc examples.
func sampleSchema() types.Schema {
	return types.NewSchema(
		"id", types.KindInt32,
		"active", types.KindBool,
		"score", types.KindFloat64,
		"name", types.KindText,
		"big", types.KindInt64,
	)
}

// rowA = (7, true, 1.5, 'hi', 100) — no NULLs, short varlena.
func rowA() types.Row {
	return testkit.R(int32(7), true, 1.5, "hi", 100)
}

// rowB = (NULL, false, NULL, 130×'x', -1) — NULLs + 4-byte varlena.
func rowB() types.Row {
	return testkit.R(nil, false, nil, strings.Repeat("x", 130), -1)
}

// golden48 is FormTuple(sampleSchema, rowA, 42).
func golden48() []byte {
	return mustHex(
		"2a000000" + "00000000" + "0200" + "0500" + "10" + "000000" +
			"07000000" + "01" + "000000" + "000000000000f83f" +
			"076869" + "0000000000" + "6400000000000000")
}

// golden168 is FormTuple(sampleSchema, rowB, 7).
func golden168() []byte {
	b := mustHex("07000000" + "00000000" + "0300" + "0500" + "10" + "1a" + "0000" +
		"00" + "000000" + "18020000")
	b = append(b, strings.Repeat("x", 130)...)
	b = append(b, make([]byte, 6)...)
	b = append(b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)
	return b
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		panic(err)
	}
	return b
}

// randomRow makes a row valid for sampleSchema; each column is NULL with
// probability nullP. Text lengths span both varlena header forms.
func randomRow(r *rand.Rand, nullP float64) types.Row {
	row := make(types.Row, 5)
	for i := range row {
		if r.Float64() < nullP {
			row[i] = types.Null()
			continue
		}
		switch i {
		case 0:
			row[i] = types.Int32(int32(r.Uint32()))
		case 1:
			row[i] = types.Bool(r.IntN(2) == 1)
		case 2:
			row[i] = types.Float64((r.Float64() - 0.5) * math.Pow(10, float64(r.IntN(20))))
		case 3:
			n := r.IntN(300)
			if r.IntN(3) == 0 {
				n = 120 + r.IntN(15) // around the 126/127 boundary
			}
			b := make([]byte, n)
			for j := range b {
				b[j] = byte('a' + r.IntN(26))
			}
			row[i] = types.Text(string(b))
		case 4:
			row[i] = types.Int64(int64(r.Uint64()))
		}
	}
	return row
}

func assertBytes(t *testing.T, what string, got, want []byte) {
	t.Helper()
	if string(got) != string(want) {
		t.Fatalf("%s mismatch\n got: % x\nwant: % x", what, got, want)
	}
}
