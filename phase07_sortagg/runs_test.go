package sortagg

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

// writeRuns creates one run file per slice of rows (rows written as given).
func writeRuns(t *testing.T, store *RunStore, runs ...[]types.Row) []string {
	t.Helper()
	var paths []string
	for _, rows := range runs {
		w, err := store.CreateRun()
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if err := w.Write(r); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, w.Path())
	}
	return paths
}

func readRun(t *testing.T, store *RunStore, path string) []types.Row {
	t.Helper()
	rows, err := store.ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// 100 rows (id, key) with key = id*7 % 10, and NULL keys every 13th row.
func hundredRows() []types.Row {
	rows := make([]types.Row, 100)
	for i := range rows {
		if i%13 == 5 {
			rows[i] = testkit.R(i, nil)
		} else {
			rows[i] = testkit.R(i, i*7%10)
		}
	}
	return rows
}

func TestP7_4_RowCodecRoundTrip(t *testing.T) {
	row := types.Row{types.Null(), types.Bool(true), types.Int32(-7), types.Int64(1 << 40), types.Float64(2.5), types.Text("héllo")}
	var buf bytes.Buffer
	if err := WriteRow(&buf, row); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRow(&buf)
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertRows(t, []types.Row{got}, []types.Row{row})
	if got[2].Kind != types.KindInt32 {
		t.Fatalf("kind = %s, want int4", got[2].Kind)
	}
}

func TestP7_4_GenerateRuns(t *testing.T) {
	input := intRows(5, 3, 8, 1, 9, 2, 7, 4, 6, 0)
	tests := []struct {
		name    string
		rows    []types.Row
		mem     int
		keys    []SortKey
		want    [][]types.Row
		wantErr error
	}{
		{"mem 3 → 4 runs, last partial", input, 3, []SortKey{Asc(0)},
			[][]types.Row{intRows(3, 5, 8), intRows(1, 2, 9), intRows(4, 6, 7), intRows(0)}, nil},
		{"mem 5 → 2 exact runs", input, 5, []SortKey{Asc(0)},
			[][]types.Row{intRows(1, 3, 5, 8, 9), intRows(0, 2, 4, 6, 7)}, nil},
		{"desc keys", input, 5, []SortKey{Desc(0)},
			[][]types.Row{intRows(9, 8, 5, 3, 1), intRows(7, 6, 4, 2, 0)}, nil},
		{"input fits in memory → 1 run", input, 10, []SortKey{Asc(0)},
			[][]types.Row{intRows(0, 1, 2, 3, 4, 5, 6, 7, 8, 9)}, nil},
		{"mem 1 → one run per row", intRows(2, 1), 1, []SortKey{Asc(0)},
			[][]types.Row{intRows(2), intRows(1)}, nil},
		{"empty input → no runs", nil, 3, []SortKey{Asc(0)}, nil, nil},
		{"mem 0 rejected", input, 0, []SortKey{Asc(0)}, nil, ErrBadMemory},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store := NewRunStore(dir)
			child := exec.Values(tc.rows...)
			runs, err := GenerateRuns(child, tc.keys, tc.mem, store)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if len(runs) != len(tc.want) {
				t.Fatalf("got %d runs, want %d", len(runs), len(tc.want))
			}
			if store.RunsCreated != len(tc.want) {
				t.Fatalf("RunsCreated = %d, want %d", store.RunsCreated, len(tc.want))
			}
			if n := len(dirFiles(t, dir)); n != len(tc.want) {
				t.Fatalf("%d files in dir, want %d", n, len(tc.want))
			}
			for i, p := range runs {
				t.Logf("run %d: %s", i, filepath.Base(p))
				testkit.AssertRows(t, readRun(t, store, p), tc.want[i])
			}
			if tc.wantErr != nil {
				if child.Opens != 0 {
					t.Fatalf("child opened %d times, want 0", child.Opens)
				}
				return
			}
			if child.Opens != 1 || child.Closes != 1 || child.Pulled != len(tc.rows) {
				t.Fatalf("child opens/closes/pulled = %d/%d/%d, want 1/1/%d", child.Opens, child.Closes, child.Pulled, len(tc.rows))
			}
		})
	}
}

func TestP7_4_GenerateRuns_StableWithinRun(t *testing.T) {
	store := NewRunStore(t.TempDir())
	rows := testkit.Rows([]any{1, "b"}, []any{2, "a"}, []any{3, "b"}, []any{4, "a"}, []any{5, "a"})
	runs, err := GenerateRuns(exec.Values(rows...), []SortKey{Asc(1)}, 4, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	testkit.AssertRows(t, readRun(t, store, runs[0]), testkit.Rows([]any{2, "a"}, []any{4, "a"}, []any{1, "b"}, []any{3, "b"}))
	testkit.AssertRows(t, readRun(t, store, runs[1]), testkit.Rows([]any{5, "a"}))
}

func TestP7_4_GenerateRuns_ChildErrorClosesChild(t *testing.T) {
	store := NewRunStore(t.TempDir())
	child := &failingIter{rows: intRows(3, 2, 1, 0)}
	_, err := GenerateRuns(child, []SortKey{Asc(0)}, 3, store)
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want %v", err, errBoom)
	}
	if child.Closes != 1 {
		t.Fatalf("child closed %d times, want 1", child.Closes)
	}
}

func TestP7_5_MergeRuns(t *testing.T) {
	tests := []struct {
		name string
		runs [][]types.Row
		keys []SortKey
		want []types.Row
	}{
		{"three runs interleave", [][]types.Row{intRows(1, 4, 7), intRows(2, 5, 8), intRows(3, 6, 9, 10)},
			[]SortKey{Asc(0)}, intRows(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)},
		{"ties: lower run index first",
			[][]types.Row{
				testkit.Rows([]any{1, "r0a"}, []any{2, "r0b"}),
				testkit.Rows([]any{1, "r1a"}, []any{2, "r1b"}),
				testkit.Rows([]any{1, "r2"}),
			},
			[]SortKey{Asc(0)},
			testkit.Rows([]any{1, "r0a"}, []any{1, "r1a"}, []any{1, "r2"}, []any{2, "r0b"}, []any{2, "r1b"})},
		{"empty runs are fine", [][]types.Row{nil, intRows(2, 3), nil, intRows(1)},
			[]SortKey{Asc(0)}, intRows(1, 2, 3)},
		{"single run is copied", [][]types.Row{intRows(1, 2, 3)}, []SortKey{Asc(0)}, intRows(1, 2, 3)},
		{"desc with nulls first",
			[][]types.Row{testkit.Rows([]any{nil}, []any{5}, []any{1}), testkit.Rows([]any{nil}, []any{4})},
			[]SortKey{Desc(0)},
			testkit.Rows([]any{nil}, []any{nil}, []any{5}, []any{4}, []any{1})},
		{"one run exhausted early", [][]types.Row{intRows(100), intRows(1, 2, 3, 4, 5)},
			[]SortKey{Asc(0)}, intRows(1, 2, 3, 4, 5, 100)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store := NewRunStore(dir)
			in := writeRuns(t, store, tc.runs...)
			before := store.RunsCreated
			out, err := MergeRuns(store, in, tc.keys)
			if err != nil {
				t.Fatal(err)
			}
			if store.RunsCreated != before+1 {
				t.Fatalf("MergeRuns created %d runs, want 1", store.RunsCreated-before)
			}
			if store.RowsRead != len(tc.want) {
				t.Fatalf("RowsRead = %d, want %d (each input row read once)", store.RowsRead, len(tc.want))
			}
			if store.MaxOpenReaders != len(in) || store.OpenReaders != 0 {
				t.Fatalf("readers max/open = %d/%d, want %d/0", store.MaxOpenReaders, store.OpenReaders, len(in))
			}
			if n := len(dirFiles(t, dir)); n != len(in)+1 {
				t.Fatalf("%d files in dir, want %d (inputs are kept)", n, len(in)+1)
			}
			testkit.AssertRows(t, readRun(t, store, out), tc.want)
		})
	}
}

func TestP7_5_MergeRuns_NoRuns(t *testing.T) {
	store := NewRunStore(t.TempDir())
	if _, err := MergeRuns(store, nil, []SortKey{Asc(0)}); !errors.Is(err, ErrNoRuns) {
		t.Fatalf("err = %v, want %v", err, ErrNoRuns)
	}
	if store.RunsCreated != 0 {
		t.Fatalf("RunsCreated = %d, want 0", store.RunsCreated)
	}
}

func TestP7_5_MergeRuns_ManyRuns(t *testing.T) {
	// 20 runs; run k holds k, k+20, k+40, ... < 400
	store := NewRunStore(t.TempDir())
	var runs [][]types.Row
	for k := 0; k < 20; k++ {
		var vs []int
		for v := k; v < 400; v += 20 {
			vs = append(vs, v)
		}
		runs = append(runs, intRows(vs...))
	}
	out, err := MergeRuns(store, writeRuns(t, store, runs...), []SortKey{Asc(0)})
	if err != nil {
		t.Fatal(err)
	}
	got := readRun(t, store, out)
	want := make([]int, 400)
	for i := range want {
		want[i] = i
	}
	testkit.AssertRows(t, got, intRows(want...))
}

func TestP7_6_MergeAll(t *testing.T) {
	tests := []struct {
		name        string
		fanIn       int
		wantPasses  int
		wantCreated int // RunsCreated after GenerateRuns (34) + merges
		wantWritten int
	}{
		{"fan-in 2: 34→17→9→5→3→2→1", 2, 6, 34 + 33, 100 + 584},
		{"fan-in 4: 34→9→3→1", 4, 3, 34 + 12, 100 + 100 + 96 + 100},
		{"fan-in 40: one pass", 40, 1, 34 + 1, 100 + 100},
	}
	keys := []SortKey{Asc(1)}
	want, err := exec.Collect(NewSort(exec.Values(hundredRows()...), keys...))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store := NewRunStore(dir)
			runs, err := GenerateRuns(exec.Values(hundredRows()...), keys, 3, store)
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != 34 {
				t.Fatalf("got %d runs, want 34", len(runs))
			}
			final, passes, err := MergeAll(store, runs, keys, tc.fanIn)
			if err != nil {
				t.Fatal(err)
			}
			if passes != tc.wantPasses {
				t.Fatalf("passes = %d, want %d", passes, tc.wantPasses)
			}
			if store.RunsCreated != tc.wantCreated {
				t.Fatalf("RunsCreated = %d, want %d", store.RunsCreated, tc.wantCreated)
			}
			if store.RowsWritten != tc.wantWritten {
				t.Fatalf("RowsWritten = %d, want %d", store.RowsWritten, tc.wantWritten)
			}
			if store.MaxOpenReaders > tc.fanIn {
				t.Fatalf("MaxOpenReaders = %d, must be <= fan-in %d", store.MaxOpenReaders, tc.fanIn)
			}
			if files := dirFiles(t, dir); len(files) != 1 || files[0] != filepath.Base(final) {
				t.Fatalf("files left = %v, want only %s", files, filepath.Base(final))
			}
			testkit.AssertRows(t, readRun(t, store, final), want) // stable, like Sort
		})
	}
}

func TestP7_6_MergeAll_SmallCases(t *testing.T) {
	t.Run("bad fan-in", func(t *testing.T) {
		store := NewRunStore(t.TempDir())
		runs := writeRuns(t, store, intRows(1), intRows(2))
		if _, _, err := MergeAll(store, runs, []SortKey{Asc(0)}, 1); !errors.Is(err, ErrBadFanIn) {
			t.Fatalf("err = %v, want %v", err, ErrBadFanIn)
		}
	})
	t.Run("no runs", func(t *testing.T) {
		store := NewRunStore(t.TempDir())
		final, passes, err := MergeAll(store, nil, []SortKey{Asc(0)}, 2)
		if final != "" || passes != 0 || err != nil {
			t.Fatalf("got (%q, %d, %v), want (\"\", 0, nil)", final, passes, err)
		}
	})
	t.Run("single run returned as-is", func(t *testing.T) {
		store := NewRunStore(t.TempDir())
		runs := writeRuns(t, store, intRows(1, 2))
		final, passes, err := MergeAll(store, runs, []SortKey{Asc(0)}, 2)
		if err != nil || final != runs[0] || passes != 0 || store.RunsCreated != 1 || store.RowsRead != 0 {
			t.Fatalf("got (%q, %d, %v) created=%d read=%d, want (%q, 0, nil) created=1 read=0",
				final, passes, err, store.RunsCreated, store.RowsRead, runs[0])
		}
	})
	t.Run("5 runs fan-in 3: groups [0 1 2] [3 4]", func(t *testing.T) {
		dir := t.TempDir()
		store := NewRunStore(dir)
		runs := writeRuns(t, store, intRows(9), intRows(3, 4), intRows(1), intRows(0, 8), intRows(2, 5))
		final, passes, err := MergeAll(store, runs, []SortKey{Asc(0)}, 3)
		if err != nil {
			t.Fatal(err)
		}
		if passes != 2 || store.RunsCreated != 5+3 {
			t.Fatalf("passes=%d created=%d, want 2 and 8", passes, store.RunsCreated)
		}
		testkit.AssertRows(t, readRun(t, store, final), intRows(0, 1, 2, 3, 4, 5, 8, 9))
		if n := len(dirFiles(t, dir)); n != 1 {
			t.Fatalf("%d files left, want 1", n)
		}
	})
	t.Run("3 runs fan-in 2: leftover carried, not copied", func(t *testing.T) {
		store := NewRunStore(t.TempDir())
		runs := writeRuns(t, store, intRows(5), intRows(4), intRows(1, 2, 3))
		before := store.RowsWritten
		final, passes, err := MergeAll(store, runs, []SortKey{Asc(0)}, 2)
		if err != nil {
			t.Fatal(err)
		}
		// pass 1: merge [5][4] → 2 rows written, carry [1 2 3]; pass 2: merge 2+3 rows.
		if passes != 2 || store.RowsWritten-before != 7 {
			t.Fatalf("passes=%d rows written=%d, want 2 and 7", passes, store.RowsWritten-before)
		}
		testkit.AssertRows(t, readRun(t, store, final), intRows(1, 2, 3, 4, 5))
	})
}

func TestP7_6_MergeAll_IOCounts(t *testing.T) {
	// 7 one-row runs, fan-in 3: pass 1 merges [0 1 2] and [3 4 5], carries [6];
	// pass 2 merges the 3 remaining runs. Rows read: 6 + 7 = 13.
	store := NewRunStore(t.TempDir())
	runs := writeRuns(t, store, intRows(6), intRows(0), intRows(5), intRows(1), intRows(4), intRows(2), intRows(3))
	final, passes, err := MergeAll(store, runs, []SortKey{Desc(0)}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if passes != 2 || store.RunsCreated != 10 || store.RowsRead != 13 || store.RowsWritten != 7+13 {
		t.Fatalf("passes/created/read/written = %d/%d/%d/%d, want 2/10/13/20",
			passes, store.RunsCreated, store.RowsRead, store.RowsWritten)
	}
	if store.MaxOpenReaders != 3 || store.OpenReaders != 0 {
		t.Fatalf("readers max/open = %d/%d, want 3/0", store.MaxOpenReaders, store.OpenReaders)
	}
	testkit.AssertRows(t, readRun(t, store, final), intRows(6, 5, 4, 3, 2, 1, 0))
}

func TestP7_7_ExternalSort(t *testing.T) {
	tests := []struct {
		name       string
		rows       []types.Row
		keys       []SortKey
		mem, fanIn int
	}{
		{"100 rows, M=3, F=2", hundredRows(), []SortKey{Asc(1)}, 3, 2},
		{"100 rows desc, M=3, F=2", hundredRows(), []SortKey{Desc(1)}, 3, 2},
		{"100 rows two keys, M=7, F=3", hundredRows(), []SortKey{Asc(1), Desc(0)}, 7, 3},
		{"fits in memory", hundredRows()[:5], []SortKey{Asc(1)}, 10, 2},
		{"empty", nil, []SortKey{Asc(1)}, 3, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want, err := exec.Collect(NewSort(exec.Values(tc.rows...), tc.keys...))
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			child := exec.Values(tc.rows...)
			op := &ExternalSort{Child: child, Keys: tc.keys, MemRows: tc.mem, FanIn: tc.fanIn, Dir: dir}
			got, err := exec.Collect(op)
			if err != nil {
				t.Fatal(err)
			}
			testkit.AssertRows(t, got, want)
			if op.Store == nil {
				t.Fatal("Store not set by Open")
			}
			if op.Store.MaxOpenReaders > tc.fanIn {
				t.Fatalf("MaxOpenReaders = %d, want <= %d", op.Store.MaxOpenReaders, tc.fanIn)
			}
			if files := dirFiles(t, dir); len(files) != 0 {
				t.Fatalf("files left after Close: %v", files)
			}
			if child.Opens != 1 || child.Closes != 1 {
				t.Fatalf("child opens/closes = %d/%d, want 1/1", child.Opens, child.Closes)
			}
		})
	}
}

func TestP7_7_ExternalSort_SpillCounts(t *testing.T) {
	op := &ExternalSort{Child: exec.Values(hundredRows()...), Keys: []SortKey{Asc(1)}, MemRows: 3, FanIn: 2, Dir: t.TempDir()}
	if err := op.Open(); err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	// 34 initial runs + 33 merged runs; 100 + 584 rows written (see 7.6).
	if op.Store.RunsCreated != 67 || op.Store.RowsWritten != 684 {
		t.Fatalf("runs created/rows written = %d/%d, want 67/684", op.Store.RunsCreated, op.Store.RowsWritten)
	}
}

func TestP7_7_ExternalSort_Errors(t *testing.T) {
	op := &ExternalSort{Child: exec.Values(intRows(1)...), Keys: []SortKey{Asc(0)}, MemRows: 0, FanIn: 2, Dir: t.TempDir()}
	if err := op.Open(); !errors.Is(err, ErrBadMemory) {
		t.Fatalf("err = %v, want %v", err, ErrBadMemory)
	}
	op = &ExternalSort{Child: exec.Values(intRows(2, 1)...), Keys: []SortKey{Asc(0)}, MemRows: 1, FanIn: 1, Dir: t.TempDir()}
	if err := op.Open(); !errors.Is(err, ErrBadFanIn) {
		t.Fatalf("err = %v, want %v", err, ErrBadFanIn)
	}
}
