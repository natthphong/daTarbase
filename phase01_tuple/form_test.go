package tuple

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

func TestP1_10_ComputeTupleSize(t *testing.T) {
	s := sampleSchema()
	boolText := types.NewSchema("b", types.KindBool, "t", types.KindText)
	oneText := types.NewSchema("t", types.KindText)
	wide := types.Schema{}
	for i := 0; i < 25; i++ {
		wide.Columns = append(wide.Columns, types.Column{Name: "c", Kind: types.KindInt32})
	}
	wideRow := make(types.Row, 25)
	for i := range wideRow {
		wideRow[i] = types.Null()
	}
	wideRow[24] = types.Int32(1)

	tests := []struct {
		name   string
		schema types.Schema
		row    types.Row
		want   int
	}{
		{"row A: no NULLs, short text", s, rowA(), 48},
		{"row B: NULLs, 4-byte varlena", s, rowB(), 168},
		{"all NULL: header + bitmap only", s, testkit.R(nil, nil, nil, nil, nil), 16},
		{"single bool", types.NewSchema("b", types.KindBool), testkit.R(true), 17},
		{"empty text is 1 byte", oneText, testkit.R(""), 17},
		{"126-byte text, short header", oneText, testkit.R(strings.Repeat("a", 126)), 143},
		{"127-byte text, 4-byte header", oneText, testkit.R(strings.Repeat("a", 127)), 147},
		{"bool + 126-byte text: short header is not aligned", boolText, testkit.R(true, strings.Repeat("a", 126)), 144},
		{"bool + 127-byte text: 4-byte header aligned to 20", boolText, testkit.R(true, strings.Repeat("a", 127)), 151},
		{"25 columns: 4-byte bitmap pushes hoff to 24", wide, wideRow, 28},
		{"int8 after int4 pads to 8", types.NewSchema("a", types.KindInt32, "b", types.KindInt64), testkit.R(int32(1), 2), 32},
		{"NULL int4 removes the padding need", types.NewSchema("a", types.KindInt32, "b", types.KindInt64), testkit.R(nil, 2), 24},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputeTupleSize(tc.schema, tc.row); got != tc.want {
				t.Fatalf("ComputeTupleSize(%v) = %d, want %d", tc.row, got, tc.want)
			}
		})
	}
}

func TestP1_11_FormTuple_Golden(t *testing.T) {
	s := sampleSchema()
	t.Run("row A", func(t *testing.T) {
		got, err := FormTuple(s, rowA(), 42)
		if err != nil {
			t.Fatal(err)
		}
		assertBytes(t, "tuple", got, golden48())
	})
	t.Run("row B", func(t *testing.T) {
		got, err := FormTuple(s, rowB(), 7)
		if err != nil {
			t.Fatal(err)
		}
		assertBytes(t, "tuple", got, golden168())
	})
}

func TestP1_11_FormTuple_Infomask(t *testing.T) {
	s := sampleSchema()
	tests := []struct {
		name     string
		row      types.Row
		wantMask string // bytes 8..9
		wantLen  int
	}{
		{"NULL text does not set HASVARWIDTH", testkit.R(int32(1), true, 2.0, nil, 3), "0100", 40},
		{"empty text still sets HASVARWIDTH", testkit.R(int32(1), true, 2.0, "", 3), "0200", 48},
		{"all NULL", testkit.R(nil, nil, nil, nil, nil), "0100", 16},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FormTuple(s, tc.row, 1)
			if err != nil {
				t.Fatal(err)
			}
			assertBytes(t, "infomask", got[8:10], mustHex(tc.wantMask))
			if len(got) != tc.wantLen {
				t.Fatalf("len = %d, want %d", len(got), tc.wantLen)
			}
		})
	}
	t.Run("no text column at all", func(t *testing.T) {
		got, err := FormTuple(types.NewSchema("a", types.KindInt64), testkit.R(5), 1)
		if err != nil {
			t.Fatal(err)
		}
		assertBytes(t, "tuple", got, mustHex("01000000 00000000 0000 0100 10 000000 0500000000000000"))
	})
}

func TestP1_11_FormTuple_Errors(t *testing.T) {
	s := sampleSchema()
	nn := types.Schema{Columns: []types.Column{
		{Name: "id", Kind: types.KindInt32, NotNull: true},
		{Name: "name", Kind: types.KindText},
	}}
	tests := []struct {
		name    string
		schema  types.Schema
		row     types.Row
		wantErr error
	}{
		{"too few values", s, testkit.R(int32(7), true, 1.5, "hi"), ErrArity},
		{"too many values", s, testkit.R(int32(7), true, 1.5, "hi", 100, 1), ErrArity},
		{"NULL in NOT NULL column", nn, testkit.R(nil, "x"), ErrNotNull},
		{"int8 value in int4 column", s, testkit.R(7, true, 1.5, "hi", 100), ErrTypeMismatch},
		{"text in float8 column", s, testkit.R(int32(7), true, "1.5", "hi", 100), ErrTypeMismatch},
		{"first error in column order wins", nn, testkit.R(nil, 5), ErrNotNull},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FormTuple(tc.schema, tc.row, 1)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got != nil {
				t.Fatalf("got % x on error, want nil", got)
			}
		})
	}
}

func TestP1_11_FormTuple_SizeMatchesCompute(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 11))
	s := sampleSchema()
	for i := 0; i < 1000; i++ {
		row := randomRow(r, 0.3)
		got, err := FormTuple(s, row, types.TxnID(i+1))
		if err != nil {
			t.Fatalf("row %v: %v", row, err)
		}
		if want := ComputeTupleSize(s, row); len(got) != want {
			t.Fatalf("row %v: len = %d, want ComputeTupleSize = %d", row, len(got), want)
		}
	}
}

func TestP1_12_DeformTuple_Golden(t *testing.T) {
	s := sampleSchema()
	got, err := DeformTuple(s, golden48())
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, []types.Row{got}, []types.Row{rowA()})
	got, err = DeformTuple(s, golden168())
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, []types.Row{got}, []types.Row{rowB()})
	if got[1].Kind != types.KindBool || got[4].Kind != types.KindInt64 {
		t.Fatalf("kinds = %s,%s, want bool,int8", got[1].Kind, got[4].Kind)
	}
}

func TestP1_12_DeformTuple_ExactKinds(t *testing.T) {
	got, err := DeformTuple(sampleSchema(), golden48())
	if err != nil {
		t.Fatal(err)
	}
	want := []types.Kind{types.KindInt32, types.KindBool, types.KindFloat64, types.KindText, types.KindInt64}
	for i, v := range got {
		if v.Kind != want[i] {
			t.Fatalf("column %d kind = %s, want %s", i, v.Kind, want[i])
		}
	}
}

func TestP1_12_DeformTuple_AddedColumns(t *testing.T) {
	old := types.NewSchema("a", types.KindInt32, "b", types.KindText, "c", types.KindFloat64)
	data, err := FormTuple(old, testkit.R(int32(1), "a", 2.5), 3)
	if err != nil {
		t.Fatal(err)
	}
	altered := types.NewSchema("a", types.KindInt32, "b", types.KindText, "c", types.KindFloat64,
		"d", types.KindInt64, "e", types.KindText)
	got, err := DeformTuple(altered, data)
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, []types.Row{got}, testkit.Rows([]any{int32(1), "a", 2.5, nil, nil}))
}

func TestP1_12_DeformTuple_Errors(t *testing.T) {
	s := sampleSchema()
	badVarlena := golden48()
	badVarlena[32] = 0x01 // TOAST-pointer tag where 'hi' header was
	tests := []struct {
		name    string
		schema  types.Schema
		data    []byte
		wantErr error
	}{
		{"truncated inside last attribute", s, golden48()[:40], ErrCorrupt},
		{"truncated inside text", s, golden168()[:100], ErrCorrupt},
		{"truncated header", s, golden48()[:10], ErrShortBuffer},
		{"more attributes than schema", types.NewSchema("id", types.KindInt32), golden48(), ErrCorrupt},
		{"invalid varlena header", s, badVarlena, ErrCorrupt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DeformTuple(tc.schema, tc.data); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestP1_12_DeformTuple_RandomRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 12))
	s := sampleSchema()
	var want, got []types.Row
	for i := 0; i < 1000; i++ {
		row := randomRow(r, 0.25)
		data, err := FormTuple(s, row, 1)
		if err != nil {
			t.Fatalf("FormTuple(%v): %v", row, err)
		}
		back, err := DeformTuple(s, data)
		if err != nil {
			t.Fatalf("DeformTuple(FormTuple(%v)): %v", row, err)
		}
		want, got = append(want, row), append(got, back)
	}
	testkit.AssertRows(t, got, want)
}

func TestP1_13_GetAttr_CacheAndStats(t *testing.T) {
	s := sampleSchema()
	td := NewTupleDesc(s)
	a := golden48()
	steps := []struct {
		name      string
		data      []byte
		col       int
		want      types.Value
		wantCache []int
		wantStats GetAttrStats
	}{
		{"active via fast path", a, 1, types.Bool(true), []int{0, 4, -1, -1, -1}, GetAttrStats{FastPath: 1}},
		{"big after text: slow, fills score", a, 4, types.Int64(100), []int{0, 4, 8, -1, -1}, GetAttrStats{FastPath: 1, SlowPath: 1}},
		{"score fast", a, 2, types.Float64(1.5), []int{0, 4, 8, -1, -1}, GetAttrStats{FastPath: 2, SlowPath: 1}},
		{"text column is never cached", a, 3, types.Text("hi"), []int{0, 4, 8, -1, -1}, GetAttrStats{FastPath: 2, SlowPath: 2}},
		{"NULL result changes no stats", golden168(), 0, types.Null(), []int{0, 4, 8, -1, -1}, GetAttrStats{FastPath: 2, SlowPath: 2}},
		{"NULL before col forces slow path", golden168(), 1, types.Bool(false), []int{0, 4, 8, -1, -1}, GetAttrStats{FastPath: 2, SlowPath: 3}},
	}
	for _, st := range steps {
		got, err := td.GetAttr(st.data, st.col)
		if err != nil {
			t.Fatalf("%s: err %v", st.name, err)
		}
		if got.Kind != st.want.Kind || !types.Identical(got, st.want) {
			t.Fatalf("%s: got %v, want %v", st.name, got, st.want)
		}
		for j := range st.wantCache {
			if td.AttCacheOff[j] != st.wantCache[j] {
				t.Fatalf("%s: AttCacheOff = %v, want %v", st.name, td.AttCacheOff, st.wantCache)
			}
		}
		if td.Stats != st.wantStats {
			t.Fatalf("%s: stats = %+v, want %+v", st.name, td.Stats, st.wantStats)
		}
	}
}

func TestP1_13_GetAttr_CacheRules(t *testing.T) {
	tests := []struct {
		name      string
		schema    types.Schema
		col       int
		wantCache []int
	}{
		{"text first: only column 0 cached", types.NewSchema("t", types.KindText, "i", types.KindInt32), 1, []int{0, -1}},
		{"bool then int8 aligns to 8", types.NewSchema("b", types.KindBool, "i", types.KindInt64, "t", types.KindText, "x", types.KindInt32), 3, []int{0, 8, -1, -1}},
		{"only up to col", types.NewSchema("a", types.KindInt32, "b", types.KindInt32, "c", types.KindInt32), 1, []int{0, 4, -1}},
		{"all fixed", types.NewSchema("a", types.KindBool, "b", types.KindInt32, "c", types.KindBool, "d", types.KindFloat64), 3, []int{0, 4, 8, 16}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			td := NewTupleDesc(tc.schema)
			row := make(types.Row, tc.schema.Len())
			for i := range row {
				row[i] = types.Null()
			}
			data, err := FormTuple(tc.schema, row, 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := td.GetAttr(data, tc.col); err != nil {
				t.Fatal(err)
			}
			for j := range tc.wantCache {
				if td.AttCacheOff[j] != tc.wantCache[j] {
					t.Fatalf("AttCacheOff = %v, want %v", td.AttCacheOff, tc.wantCache)
				}
			}
		})
	}
}

func TestP1_13_GetAttr_Errors(t *testing.T) {
	td := NewTupleDesc(sampleSchema())
	for _, col := range []int{-1, 5} {
		if _, err := td.GetAttr(golden48(), col); !errors.Is(err, ErrBadAttnum) {
			t.Fatalf("col %d: err = %v, want ErrBadAttnum", col, err)
		}
	}
	for j, off := range td.AttCacheOff {
		if off != -1 {
			t.Fatalf("cache touched by bad attnum: AttCacheOff[%d] = %d", j, off)
		}
	}
	if _, err := td.GetAttr(golden48()[:12], 0); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("short header: err = %v, want ErrShortBuffer", err)
	}
	if _, err := td.GetAttr(golden48()[:40], 4); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("truncated: err = %v, want ErrCorrupt", err)
	}
	if _, err := NewTupleDesc(types.NewSchema("id", types.KindInt32)).GetAttr(golden48(), 0); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("natts > schema: err = %v, want ErrCorrupt", err)
	}
}

func TestP1_13_GetAttr_MissingColumn(t *testing.T) {
	old := types.NewSchema("a", types.KindInt32)
	data, err := FormTuple(old, testkit.R(int32(9)), 1)
	if err != nil {
		t.Fatal(err)
	}
	td := NewTupleDesc(types.NewSchema("a", types.KindInt32, "b", types.KindInt64))
	got, err := td.GetAttr(data, 1)
	if err != nil || !got.IsNull() {
		t.Fatalf("added column: got %v err %v, want NULL", got, err)
	}
	if td.Stats != (GetAttrStats{}) {
		t.Fatalf("stats = %+v, want zero", td.Stats)
	}
}

func TestP1_13_GetAttr_MatchesDeform(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 13))
	s := sampleSchema()
	td := NewTupleDesc(s)
	for i := 0; i < 1000; i++ {
		nullP := 0.0
		if i%2 == 1 {
			nullP = 0.3
		}
		row := randomRow(r, nullP)
		data, err := FormTuple(s, row, 1)
		if err != nil {
			t.Fatal(err)
		}
		full, err := DeformTuple(s, data)
		if err != nil {
			t.Fatal(err)
		}
		for col := s.Len() - 1; col >= 0; col-- {
			got, err := td.GetAttr(data, col)
			if err != nil {
				t.Fatalf("row %v col %d: %v", row, col, err)
			}
			if got.Kind != full[col].Kind || !types.Identical(got, full[col]) {
				t.Fatalf("row %v col %d: GetAttr = %v, DeformTuple = %v", row, col, got, full[col])
			}
		}
	}
	if td.Stats.FastPath == 0 || td.Stats.SlowPath == 0 {
		t.Fatalf("stats = %+v, want both paths used", td.Stats)
	}
}

func TestP1_10_ComputeTupleSize_BitmapGrowth(t *testing.T) {
	tests := []struct{ natts, want int }{
		{1, 16}, {8, 16}, {24, 16}, {25, 24}, {88, 24}, {89, 32},
	}
	for _, tc := range tests {
		s := types.Schema{}
		row := make(types.Row, tc.natts)
		for i := range row {
			s.Columns = append(s.Columns, types.Column{Name: "c", Kind: types.KindInt64})
			row[i] = types.Null()
		}
		if got := ComputeTupleSize(s, row); got != tc.want {
			t.Fatalf("%d all-NULL columns: size = %d, want %d", tc.natts, got, tc.want)
		}
	}
}

func TestP1_10_ComputeTupleSize_NoNullsNoBitmap(t *testing.T) {
	s := types.Schema{}
	row := types.Row{}
	for i := 0; i < 30; i++ {
		s.Columns = append(s.Columns, types.Column{Name: "c", Kind: types.KindBool})
		row = append(row, types.Bool(true))
	}
	// 30 bools, no NULL: hoff 16, 30 one-byte values.
	if got := ComputeTupleSize(s, row); got != 46 {
		t.Fatalf("size = %d, want 46", got)
	}
	row[0] = types.Null()
	// One NULL: 4-byte bitmap → hoff 24, 29 values.
	if got := ComputeTupleSize(s, row); got != 53 {
		t.Fatalf("size = %d, want 53", got)
	}
}
