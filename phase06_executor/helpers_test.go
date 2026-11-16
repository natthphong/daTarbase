package executor

import (
	"errors"
	"math"
	"testing"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

var errBoom = errors.New("boom")

// failingIter returns its rows, then errBoom instead of end-of-input.
type failingIter struct {
	rows []types.Row
	pos  int
}

func (f *failingIter) Open() error  { f.pos = 0; return nil }
func (f *failingIter) Close() error { return nil }
func (f *failingIter) Next() (types.Row, bool, error) {
	if f.pos >= len(f.rows) {
		return nil, false, errBoom
	}
	f.pos++
	return f.rows[f.pos-1], true, nil
}

var _ exec.Iterator = (*failingIter)(nil)

// v / k are short constant builders: v(5) is an int8 constant, k(nil) is NULL.
func v(x any) types.Value { return testkit.V(x) }
func k(x any) Const       { return C(testkit.V(x)) }

var (
	tTrue  = k(true)
	tFalse = k(false)
	tNull  = k(nil)
)

func near(a, b float64) bool {
	if math.IsInf(a, 0) || math.IsInf(b, 0) {
		return a == b
	}
	return math.Abs(a-b) < 1e-9
}

func ge(lo float64) *Bound { return &Bound{Val: types.Int64(int64(lo)), Inclusive: true} }
func gt(lo float64) *Bound { return &Bound{Val: types.Int64(int64(lo)), Inclusive: false} }

// seqTable builds a one-column table holding the int8 values from..to.
func seqTable(name string, rowsPerPage, from, to int) *Table {
	var rows []types.Row
	for i := from; i <= to; i++ {
		rows = append(rows, testkit.R(i))
	}
	return NewTable(name, rowsPerPage, rows...)
}

// ints builds one-column int8 rows.
func ints(xs ...int) []types.Row {
	out := make([]types.Row, len(xs))
	for i, x := range xs {
		out[i] = testkit.R(x)
	}
	return out
}

func collect(t *testing.T, it exec.Iterator) []types.Row {
	t.Helper()
	rows, err := exec.Collect(it)
	if err != nil {
		t.Fatalf("Collect: unexpected error %v", err)
	}
	return rows
}
