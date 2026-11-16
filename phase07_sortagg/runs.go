package sortagg

import (
	"bufio"
	"container/heap"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/types"
)

var (
	ErrBadMemory = errors.New("sortagg: memory limit must be at least 1 row")
	ErrBadFanIn  = errors.New("sortagg: merge fan-in must be at least 2")
	ErrNoRuns    = errors.New("sortagg: nothing to merge")
)

// ---------------------------------------------------------------- row codec (provided)

// WriteRow appends one row to w in the run-file format (little-endian):
//
//	uint16 ncols, then per value: 1 byte Kind, then the payload
//	  NULL    → nothing
//	  bool    → 1 byte (0/1)
//	  int4/8  → 8 bytes int64
//	  float8  → 8 bytes IEEE-754 bits
//	  text    → uint32 length + bytes
func WriteRow(w io.Writer, row types.Row) error {
	buf := binary.LittleEndian.AppendUint16(nil, uint16(len(row)))
	for _, v := range row {
		buf = append(buf, byte(v.Kind))
		switch v.Kind {
		case types.KindNull:
		case types.KindBool:
			b := byte(0)
			if v.B {
				b = 1
			}
			buf = append(buf, b)
		case types.KindInt32, types.KindInt64:
			buf = binary.LittleEndian.AppendUint64(buf, uint64(v.I))
		case types.KindFloat64:
			buf = binary.LittleEndian.AppendUint64(buf, math.Float64bits(v.F))
		case types.KindText:
			buf = binary.LittleEndian.AppendUint32(buf, uint32(len(v.S)))
			buf = append(buf, v.S...)
		default:
			return fmt.Errorf("sortagg: cannot encode %s", v.Kind)
		}
	}
	_, err := w.Write(buf)
	return err
}

// ReadRow reads one row written by WriteRow. It returns io.EOF (and no row)
// at a clean end of stream, io.ErrUnexpectedEOF for a truncated row.
func ReadRow(r io.Reader) (types.Row, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:2]); err != nil {
		return nil, err // io.EOF if nothing was read
	}
	n := int(binary.LittleEndian.Uint16(hdr[:2]))
	row := make(types.Row, n)
	full := func(b []byte) error {
		if _, err := io.ReadFull(r, b); err != nil {
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		return nil
	}
	for i := range row {
		if err := full(hdr[:1]); err != nil {
			return nil, err
		}
		k := types.Kind(hdr[0])
		switch k {
		case types.KindNull:
			row[i] = types.Null()
		case types.KindBool:
			if err := full(hdr[:1]); err != nil {
				return nil, err
			}
			row[i] = types.Bool(hdr[0] == 1)
		case types.KindInt32, types.KindInt64:
			if err := full(hdr[:8]); err != nil {
				return nil, err
			}
			row[i] = types.Value{Kind: k, I: int64(binary.LittleEndian.Uint64(hdr[:8]))}
		case types.KindFloat64:
			if err := full(hdr[:8]); err != nil {
				return nil, err
			}
			row[i] = types.Float64(math.Float64frombits(binary.LittleEndian.Uint64(hdr[:8])))
		case types.KindText:
			if err := full(hdr[:4]); err != nil {
				return nil, err
			}
			s := make([]byte, binary.LittleEndian.Uint32(hdr[:4]))
			if err := full(s); err != nil {
				return nil, err
			}
			row[i] = types.Text(string(s))
		default:
			return nil, fmt.Errorf("sortagg: bad kind byte %d", k)
		}
	}
	return row, nil
}

// ---------------------------------------------------------------- run files (provided)

// RunStore creates, reads and removes sorted-run temp files inside Dir and
// counts the I/O so tests can check the algorithms (Postgres: the LogicalTapeSet
// behind tuplesort, see src/backend/utils/sort/logtape.c).
type RunStore struct {
	Dir string // directory holding the run files

	nextID         int // id of the next file CreateRun makes
	RunsCreated    int // files created by CreateRun
	RowsWritten    int // rows written through any RunWriter
	RowsRead       int // rows returned by any RunReader
	OpenReaders    int // RunReaders currently open
	MaxOpenReaders int // highest OpenReaders ever seen
}

// NewRunStore returns a store that keeps its files in dir.
func NewRunStore(dir string) *RunStore { return &RunStore{Dir: dir} }

// RunWriter appends rows to a new run file.
type RunWriter struct {
	store *RunStore
	path  string
	f     *os.File
	w     *bufio.Writer
	Rows  int // rows written so far
}

// CreateRun creates a new empty run file named run-0000.tmp, run-0001.tmp, ...
func (s *RunStore) CreateRun() (*RunWriter, error) {
	path := filepath.Join(s.Dir, fmt.Sprintf("run-%04d.tmp", s.nextID))
	s.nextID++
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	s.RunsCreated++
	return &RunWriter{store: s, path: path, f: f, w: bufio.NewWriter(f)}, nil
}

// Path is the file this writer writes to.
func (w *RunWriter) Path() string { return w.path }

// Write appends one row.
func (w *RunWriter) Write(row types.Row) error {
	if err := WriteRow(w.w, row); err != nil {
		return err
	}
	w.Rows++
	w.store.RowsWritten++
	return nil
}

// Close flushes and closes the file. The run can then be opened with OpenRun.
func (w *RunWriter) Close() error {
	if w.f == nil {
		return nil
	}
	err := w.w.Flush()
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	w.f = nil
	return err
}

// RunReader reads a run file front to back.
type RunReader struct {
	store *RunStore
	f     *os.File
	r     *bufio.Reader
}

// OpenRun opens a run file for reading.
func (s *RunStore) OpenRun(path string) (*RunReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	s.OpenReaders++
	s.MaxOpenReaders = max(s.MaxOpenReaders, s.OpenReaders)
	return &RunReader{store: s, f: f, r: bufio.NewReader(f)}, nil
}

// Next returns the next row of the run; ok=false at the end.
func (r *RunReader) Next() (types.Row, bool, error) {
	row, err := ReadRow(r.r)
	if err == io.EOF {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	r.store.RowsRead++
	return row, true, nil
}

// Close closes the file. Calling it twice is harmless.
func (r *RunReader) Close() error {
	if r.f == nil {
		return nil
	}
	r.store.OpenReaders--
	err := r.f.Close()
	r.f = nil
	return err
}

// Remove deletes a run file.
func (s *RunStore) Remove(path string) error { return os.Remove(path) }

// ReadAll returns every row of a run file (a convenience for tests; it
// counts toward RowsRead like any read).
func (s *RunStore) ReadAll(path string) ([]types.Row, error) {
	rd, err := s.OpenRun(path)
	if err != nil {
		return nil, err
	}
	defer rd.Close()
	var out []types.Row
	for {
		row, ok, err := rd.Next()
		if err != nil || !ok {
			return out, err
		}
		out = append(out, row)
	}
}

// ---------------------------------------------------------------- problems

// Problem 7.4 — GenerateRuns [Medium]
//
// Phase 1 of an external sort: cut the input into sorted runs of at most memRows rows, each written to its own temp file.
//
// When the input does not fit in work_mem, Postgres sorts what fits, writes
// it out as a sorted "run", empties memory and repeats. Later the runs are
// merged (Problems 7.5–7.6).
//
//	input (10 rows), memRows=3:
//	  5 3 8 | 1 9 2 | 7 4 6 | 0
//	   ↓       ↓       ↓      ↓     sort each chunk, write it
//	run-0000 run-0001 run-0002 run-0003
//	 3 5 8    1 2 9    4 6 7    0
//
// Rules:
//  1. memRows < 1 → return ErrBadMemory without touching the child or the store.
//  2. Open the child. Buffer rows; as soon as the buffer holds memRows rows,
//     stable-sort it with CompareRows(·,·,keys), write it to a new run
//     (store.CreateRun, Write each row, Close) and empty the buffer. At the
//     end of input, flush a non-empty buffer the same way. Never hold more
//     than memRows rows.
//  3. Return the run paths in the order they were created. Empty input →
//     no runs (empty result) and no files created.
//  4. Always close the child before returning (also on error). Return the
//     first error encountered.
//
// Example: rows 5,3,8,1,9,2,7,4,6,0, keys [Asc(0)], memRows=3 →
// 4 runs: [3 5 8] [1 2 9] [4 6 7] [0]. With memRows=5 → [1 3 5 8 9] [0 2 4 6 7].
//
// Constraints: memory O(memRows) rows.
// Postgres reference: dumptuples() in src/backend/utils/sort/tuplesort.c
func GenerateRuns(child exec.Iterator, keys []SortKey, memRows int, store *RunStore) (runs []string, err error) {
	if memRows < 1 {
		return nil, ErrBadMemory
	}
	if err := child.Open(); err != nil {
		return nil, err
	}
	defer func() {
		if cerr := child.Close(); err == nil {
			err = cerr
		}
	}()
	buf := make([]types.Row, 0, memRows)
	flush := func() error {
		slices.SortStableFunc(buf, func(a, b types.Row) int { return CompareRows(a, b, keys) })
		w, err := store.CreateRun()
		if err != nil {
			return err
		}
		for _, r := range buf {
			if err := w.Write(r); err != nil {
				w.Close()
				return err
			}
		}
		if err := w.Close(); err != nil {
			return err
		}
		runs = append(runs, w.Path())
		buf = buf[:0]
		return nil
	}
	for {
		r, ok, err := child.Next()
		if err != nil {
			return runs, err
		}
		if !ok {
			break
		}
		buf = append(buf, r)
		if len(buf) == memRows {
			if err := flush(); err != nil {
				return runs, err
			}
		}
	}
	if len(buf) > 0 {
		if err := flush(); err != nil {
			return runs, err
		}
	}
	return runs, nil
}

// Problem 7.5 — MergeRuns [Hard]
//
// Merge k sorted runs into ONE new sorted run with a min-heap of run heads, reading each input exactly once.
//
// All k runs are open at the same time; only the current head row of each
// run is in memory. Repeatedly take the smallest head, write it out, and
// replace it with the next row of the same run.
//
//	run0: 1 4 7        heap of heads {1(r0), 2(r1), 3(r2)}
//	run1: 2 5 8   →    pop 1, push 4(r0) → pop 2, push 5(r1) → ...
//	run2: 3 6 9 10     output: 1 2 3 4 5 6 7 8 9 10
//
// Rules:
//  1. len(runs) == 0 → ErrNoRuns, nothing created.
//  2. Open every input run (store.OpenRun) and create exactly one output run
//     (store.CreateRun). Return the output path.
//  3. Output order is CompareRows(·,·,keys); when two heads compare equal,
//     the head from the run with the LOWER index in `runs` goes first. (Runs
//     are produced in input order, so this keeps the whole external sort stable.)
//  4. Each input row is read exactly once (store.RowsRead grows by the total
//     number of input rows). Empty input runs are allowed.
//  5. Close every reader and the writer before returning, also on error.
//     Do NOT remove the input runs (the caller decides).
//
// Example: runs [(1,'r0a') (2,'r0b')], [(1,'r1a') (2,'r1b')], [(1,'r2')], keys [Asc(0)]
// → (1,'r0a') (1,'r1a') (1,'r2') (2,'r0b') (2,'r1b')
//
// Constraints: O(N log k) comparisons, at most one row per run in memory.
// Postgres reference: mergeonerun() / beginmerge() in src/backend/utils/sort/tuplesort.c
func MergeRuns(store *RunStore, runs []string, keys []SortKey) (out string, err error) {
	if len(runs) == 0 {
		return "", ErrNoRuns
	}
	type head struct {
		row types.Row
		run int
	}
	readers := make([]*RunReader, 0, len(runs))
	defer func() {
		for _, rd := range readers {
			if cerr := rd.Close(); err == nil {
				err = cerr
			}
		}
	}()
	h := &sliceHeap[head]{less: func(a, b head) bool {
		if c := CompareRows(a.row, b.row, keys); c != 0 {
			return c < 0
		}
		return a.run < b.run
	}}
	for i, p := range runs {
		rd, err := store.OpenRun(p)
		if err != nil {
			return "", err
		}
		readers = append(readers, rd)
		row, ok, err := rd.Next()
		if err != nil {
			return "", err
		}
		if ok {
			h.items = append(h.items, head{row, i})
		}
	}
	heap.Init(h)
	w, err := store.CreateRun()
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := w.Close(); err == nil {
			err = cerr
		}
	}()
	for h.Len() > 0 {
		top := h.items[0]
		if err := w.Write(top.row); err != nil {
			return "", err
		}
		row, ok, err := readers[top.run].Next()
		if err != nil {
			return "", err
		}
		if ok {
			h.items[0] = head{row, top.run}
			heap.Fix(h, 0)
		} else {
			heap.Pop(h)
		}
	}
	return w.Path(), nil
}

// Problem 7.6 — MergeAll [Medium]
//
// Phase 2 of an external sort: merge runs in passes, at most fanIn at a time, until a single sorted run remains.
//
// Every open run needs a file handle and an I/O buffer, so the number of runs
// merged at once (the fan-in, Postgres: the number of "tapes") is limited.
// With more runs than that, merge in several passes:
//
//	34 runs, fanIn=2:  34 → 17 → 9 → 5 → 3 → 2 → 1      (6 passes)
//	pass 2 here: 17 runs = 8 pairs + 1 leftover → 8 new runs + the leftover = 9
//
// Rules:
//  1. fanIn < 2 → ErrBadFanIn. No runs → return ("", 0, nil).
//     A single run → return it unchanged with passes = 0 (no I/O).
//  2. One pass: split the current list, in order, into consecutive groups of
//     fanIn runs (the last group may be smaller). A group of ≥ 2 runs is merged
//     with MergeRuns into one new run, and its input files are then deleted
//     with store.Remove. A group of exactly 1 run is carried over as-is (no copy).
//     The next list is the group results, in group order.
//  3. Repeat passes until one run is left. Return its path and the number of passes.
//  4. On return, the only run file left from `runs` and the intermediate runs is the final one.
//
// Example: 100 rows with memRows=3 give 34 runs; fanIn=2 → 6 passes, 33 merges,
// 684 rows written in total (100 by run generation + 584 by merging, because
// the leftover run is carried, not copied); fanIn=4 → 3 passes (34 → 9 → 3 → 1).
//
// Constraints: never more than fanIn readers open at once.
// Postgres reference: mergeruns() in src/backend/utils/sort/tuplesort.c
func MergeAll(store *RunStore, runs []string, keys []SortKey, fanIn int) (final string, passes int, err error) {
	if fanIn < 2 {
		return "", 0, ErrBadFanIn
	}
	if len(runs) == 0 {
		return "", 0, nil
	}
	for len(runs) > 1 {
		passes++
		var next []string
		for i := 0; i < len(runs); i += fanIn {
			group := runs[i:min(i+fanIn, len(runs))]
			if len(group) == 1 {
				next = append(next, group[0])
				continue
			}
			out, err := MergeRuns(store, group, keys)
			if err != nil {
				return "", passes, err
			}
			for _, p := range group {
				if err := store.Remove(p); err != nil {
					return "", passes, err
				}
			}
			next = append(next, out)
		}
		runs = next
	}
	return runs[0], passes, nil
}

// ExternalSort is ORDER BY with a memory limit: it spills sorted runs to
// files in Dir and merges them (Postgres: Sort node whose tuplesort
// exceeded work_mem — EXPLAIN ANALYZE shows "Sort Method: external merge").
type ExternalSort struct {
	Child   exec.Iterator // input rows
	Keys    []SortKey     // ORDER BY keys
	MemRows int           // memory limit in rows (work_mem)
	FanIn   int           // max runs merged at once
	Dir     string        // temp directory for run files

	Store  *RunStore  // created by Open; exposes I/O counters
	final  string     // path of the final sorted run ("" if input was empty)
	reader *RunReader // reader over final, used by Next
}

// Problem 7.7 — ExternalSort.Open [Medium]
//
// Sort the child's rows using at most MemRows rows of memory by generating runs and merging them.
//
// Rules:
//  1. Set e.Store = NewRunStore(e.Dir) (always, even if a later step fails).
//  2. Generate runs with GenerateRuns(e.Child, e.Keys, e.MemRows, e.Store)
//     — it opens and closes the child itself — then merge them with
//     MergeAll(…, e.FanIn). Return any error from either.
//  3. Remember the final run in e.final and open it into e.reader; the
//     provided Next reads from e.reader, and Close closes it and removes
//     the final file, so after Close the directory is empty.
//  4. Empty input → e.final = "", e.reader = nil (Next reports no rows).
//  5. The result must be identical to the stable in-memory Sort (7.2).
//
// Example: 100 rows, MemRows=3, FanIn=2 → 34 runs, 6 merge passes, the
// output equals Sort's output row for row, and Dir is empty after Close.
//
// Postgres reference: tuplesort_performsort() in src/backend/utils/sort/tuplesort.c
func (e *ExternalSort) Open() error {
	e.Store = NewRunStore(e.Dir)
	e.final, e.reader = "", nil
	runs, err := GenerateRuns(e.Child, e.Keys, e.MemRows, e.Store)
	if err != nil {
		return err
	}
	final, _, err := MergeAll(e.Store, runs, e.Keys, e.FanIn)
	if err != nil {
		return err
	}
	if final == "" {
		return nil
	}
	e.final = final
	e.reader, err = e.Store.OpenRun(final)
	return err
}

// Next returns the next sorted row.
func (e *ExternalSort) Next() (types.Row, bool, error) {
	if e.reader == nil {
		return nil, false, nil
	}
	return e.reader.Next()
}

// Close closes the final run and deletes it. (The child was already closed
// by run generation.)
func (e *ExternalSort) Close() error {
	var err error
	if e.reader != nil {
		err = e.reader.Close()
		e.reader = nil
	}
	if e.final != "" {
		if rerr := e.Store.Remove(e.final); err == nil {
			err = rerr
		}
		e.final = ""
	}
	return err
}
