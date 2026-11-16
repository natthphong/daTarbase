package sortagg

import (
	"errors"
	"os"
	"testing"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/types"
)

var errBoom = errors.New("boom")

// failingIter returns its rows, then errBoom instead of end-of-input.
type failingIter struct {
	rows   []types.Row
	pos    int
	Closes int
}

func (f *failingIter) Open() error { f.pos = 0; return nil }
func (f *failingIter) Next() (types.Row, bool, error) {
	if f.pos >= len(f.rows) {
		return nil, false, errBoom
	}
	f.pos++
	return f.rows[f.pos-1], true, nil
}
func (f *failingIter) Close() error { f.Closes++; return nil }

var _ exec.Iterator = (*failingIter)(nil)

// pullN calls Next n times and returns the rows.
func pullN(t *testing.T, it exec.Iterator, n int) []types.Row {
	t.Helper()
	var out []types.Row
	for i := 0; i < n; i++ {
		r, ok, err := it.Next()
		if err != nil || !ok {
			t.Fatalf("Next #%d: ok=%v err=%v, want a row", i+1, ok, err)
		}
		out = append(out, r)
	}
	return out
}

// intRows makes single-column int8 rows.
func intRows(vs ...int) []types.Row {
	out := make([]types.Row, len(vs))
	for i, v := range vs {
		out[i] = types.Row{types.Int64(int64(v))}
	}
	return out
}

// dirFiles lists the file names in dir.
func dirFiles(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}
