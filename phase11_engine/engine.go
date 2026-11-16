// Package engine is the capstone: it glues phases 1–10 into one small
// Postgres-like database with a Go API (no SQL parser).
//
//	db, _ := engine.Open(dir)                                  // creates or reopens; runs crash recovery
//	db.CreateTable("emp", types.NewSchema("id", types.KindInt32, "name", types.KindText, "dept", types.KindInt32))
//	db.CreateIndex("emp_dept", "emp", []string{"dept"}, engine.Include("name"))
//	tx := db.Begin(engine.ReadCommitted)
//	tx.Insert("emp", testkit.R(1, "alice", 10))
//	rows, _ := tx.Query(engine.Scan("emp").Where("dept", "=", 10).OrderBy("name").Limit(10))
//	tx.Commit()
//	fmt.Println(db.Explain(engine.Scan("emp").Where("dept", "=", 10)))
//	db.Close()
//
// On disk (<dir>):
//
//	catalog.json   table and index definitions (Catalog)
//	pg_wal         the write-ahead log (phase-10 record framing)
//	pg_xact        the commit log, written at checkpoints (phase-9 Clog pages)
//	base/<relid>   one heap file of 8 KB phase-2 pages per table
//
// B+Tree indexes (phase 5) live in memory and are rebuilt from the heap by
// Open, the visibility map and the free space map too.
//
// Concurrency: a DB and its transactions must be used from one goroutine at a
// time (like one Postgres backend). Several transactions may be open at once
// and interleave their statements; MVCC decides what each one sees.
//
// This file is PROVIDED.
package engine

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"

	tuple "github.com/natthphong/daTarbase/phase01_tuple"
	join "github.com/natthphong/daTarbase/phase08_join"
	txn "github.com/natthphong/daTarbase/phase09_txn"
	wal "github.com/natthphong/daTarbase/phase10_wal"
	"github.com/natthphong/daTarbase/pkg/types"
)

var (
	ErrClosed          = errors.New("engine: database is closed")
	ErrTableExists     = errors.New("engine: table already exists")
	ErrNoSuchTable     = errors.New("engine: no such table")
	ErrIndexExists     = errors.New("engine: index already exists")
	ErrNoSuchColumn    = errors.New("engine: no such column")
	ErrAmbiguousColumn = errors.New("engine: column reference is ambiguous")
	ErrBadSchema       = errors.New("engine: bad schema")
	ErrBadOperator     = errors.New("engine: unsupported comparison operator")
	ErrBadQuery        = errors.New("engine: unsupported query shape")
	ErrOutOfRange      = errors.New("engine: value out of range for column type")
	ErrTxnClosed       = errors.New("engine: transaction is already committed or rolled back")
	ErrTupleTooLarge   = errors.New("engine: tuple does not fit in a page")
	ErrTupleNotFound   = errors.New("engine: tuple not found")
	ErrWriteConflict   = errors.New("engine: row is being modified by another running transaction")
	// ErrSerialization: the row was changed by a transaction that committed
	// after our snapshot (Postgres: "could not serialize access due to concurrent update").
	ErrSerialization = txn.ErrSerialization
	ErrCorruptWAL    = errors.New("engine: corrupt WAL record")
	ErrRedoMismatch  = errors.New("engine: redo did not reproduce the logged change")

	errTypeMismatch = tuple.ErrTypeMismatch
	errArity        = tuple.ErrArity
)

// IsolationLevel of a transaction (phase 9).
type IsolationLevel = txn.IsolationLevel

const (
	ReadCommitted  = txn.ReadCommitted  // a new snapshot for every statement (Postgres default)
	RepeatableRead = txn.RepeatableRead // one snapshot, taken at the first statement
)

// JoinType of Query.Join (phase 8).
type JoinType = join.JoinType

const (
	InnerJoin = join.Inner
	LeftJoin  = join.Left
	RightJoin = join.Right
	FullJoin  = join.Full
	SemiJoin  = join.Semi // EXISTS: left rows with a match, left columns only
	AntiJoin  = join.Anti // NOT EXISTS: left rows without a match
)

// Options tune a database. Zero values mean the defaults.
type Options struct {
	PoolFrames  int  // buffer-pool frames per table (default 64)
	IndexFanout int  // max entries per B+Tree node (default 64)
	NoSync      bool // never fsync (tests); a simulated Crash loses only unflushed WAL
}

// DB is an open database.
type DB struct {
	dir     string
	opts    Options
	cat     *Catalog
	heaps   map[string]*TableHeap // by table name
	indexes map[string]*Index     // by index name
	mgr     *txn.TxnManager       // xids, running set, clog (phase 9)
	wal     *walWriter
	active  map[*Txn]bool // transactions not yet committed / rolled back
	closed  bool

	// Recovery reports what crash recovery did during Open (zero if none ran).
	Recovery RecoveryStats
}

// Open opens or creates the database in dir with default options.
func Open(dir string) (*DB, error) { return OpenWith(dir, Options{}) }

// OpenWith opens or creates the database in dir.
//
//  1. load catalog.json and pg_xact; open pg_wal and read every valid record
//  2. the last CHECKPOINT record gives NextXid; the records after it are the
//     "tail" that may not be reflected in the data files
//  3. open every table's heap file
//  4. if the tail is not empty: crash recovery — db.Recover(tail) (Problem 11.21)
//  5. every xid still "in progress" in the clog belonged to a transaction that
//     died in the crash: mark it aborted
//  6. rebuild the free space maps and the B+Tree indexes from the heap
//  7. after a recovery, take a checkpoint (Problem 11.20) like Postgres' end-of-recovery checkpoint
func OpenWith(dir string, opts Options) (*DB, error) {
	if opts.PoolFrames <= 0 {
		opts.PoolFrames = 64
	}
	if opts.IndexFanout <= 0 {
		opts.IndexFanout = 64
	}
	if err := os.MkdirAll(filepath.Join(dir, "base"), 0o755); err != nil {
		return nil, err
	}
	cat, err := LoadCatalog(filepath.Join(dir, "catalog.json"))
	if err != nil {
		return nil, err
	}
	clog, err := loadClog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		return nil, err
	}
	w, recs, err := openWAL(filepath.Join(dir, "pg_wal"), !opts.NoSync)
	if err != nil {
		return nil, err
	}
	db := &DB{
		dir: dir, opts: opts, cat: cat, wal: w,
		heaps: map[string]*TableHeap{}, indexes: map[string]*Index{},
		mgr: txn.NewTxnManager(), active: map[*Txn]bool{},
	}
	db.mgr.Clog = clog
	tail := recs
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Type == wal.RecCheckpoint {
			next, err := decodeCheckpoint(recs[i].Body)
			if err != nil {
				w.close()
				return nil, err
			}
			db.mgr.NextXid = max(next, txn.FirstNormalTxnID)
			tail = recs[i+1:]
			break
		}
	}
	fail := func(err error) (*DB, error) { db.Crash(); return nil, err }
	for _, td := range cat.Tables {
		h, err := openHeap(dir, td, w, opts)
		if err != nil {
			return fail(err)
		}
		db.heaps[td.Name] = h
	}
	if len(tail) > 0 {
		stats, err := db.Recover(tail)
		if err != nil {
			return fail(err)
		}
		db.Recovery = stats
	}
	for x := txn.FirstNormalTxnID; x < db.mgr.NextXid; x++ {
		if clog.Get(x) == txn.StatusInProgress {
			clog.Set(x, txn.StatusAborted)
		}
	}
	for _, td := range cat.Tables {
		if err := db.heaps[td.Name].rebuildFSM(); err != nil {
			return fail(err)
		}
	}
	for _, def := range cat.Indexes {
		ix, err := newIndex(def, db.heaps[def.Table], opts.IndexFanout)
		if err == nil {
			err = ix.Build()
		}
		if err != nil {
			return fail(err)
		}
		db.indexes[def.Name] = ix
	}
	if len(tail) > 0 {
		if err := db.Checkpoint(); err != nil {
			return fail(err)
		}
	}
	return db, nil
}

// Close rolls back every open transaction, takes a checkpoint (Problem 11.20)
// and closes the files. A closed DB cannot be used again.
func (db *DB) Close() error {
	if db.closed {
		return ErrClosed
	}
	open := make([]*Txn, 0, len(db.active))
	for t := range db.active {
		open = append(open, t)
	}
	slices.SortFunc(open, func(a, b *Txn) int { return int(a.xid) - int(b.xid) })
	for _, t := range open {
		if err := t.Rollback(); err != nil {
			return err
		}
	}
	if err := db.Checkpoint(); err != nil {
		return err
	}
	db.closed = true
	var first error
	for _, td := range db.cat.Tables {
		if err := db.heaps[td.Name].disk.Close(); err != nil && first == nil {
			first = err
		}
	}
	if err := db.wal.close(); err != nil && first == nil {
		first = err
	}
	return first
}

// Crash simulates a power failure: the unflushed part of the WAL is lost,
// dirty pages still in the buffer pools are lost, the files are closed.
// Whatever was already written to the files (flushed WAL, evicted pages,
// catalog, pg_xact) stays. Reopen the directory with Open to recover.
func (db *DB) Crash() {
	if db.closed {
		return
	}
	db.closed = true
	db.wal.crash()
	for _, h := range db.heaps {
		h.disk.Close()
	}
}

// Dir is the data directory.
func (db *DB) Dir() string { return db.dir }

// Catalog returns the catalog (read it, don't modify it).
func (db *DB) Catalog() *Catalog { return db.cat }

// Heap returns the storage of a table, or nil.
func (db *DB) Heap(table string) *TableHeap { return db.heaps[table] }

// IndexByName returns an index, or nil.
func (db *DB) IndexByName(name string) *Index { return db.indexes[name] }

// indexesOf returns the open indexes of a table in catalog (creation) order.
func (db *DB) indexesOf(table string) []*Index {
	var out []*Index
	for _, def := range db.cat.IndexesOf(table) {
		out = append(out, db.indexes[def.Name])
	}
	return out
}

func (db *DB) catalogPath() string { return filepath.Join(db.dir, "catalog.json") }

// CreateTable creates a table (auto-committed DDL, not WAL-logged: the catalog
// file is replaced atomically and the empty heap file is created).
// Errors: ErrClosed, ErrTableExists, ErrBadSchema (empty name, no columns,
// empty or duplicate column name, a column named "ctid", unsupported kind).
func (db *DB) CreateTable(name string, schema types.Schema) error {
	if db.closed {
		return ErrClosed
	}
	if db.cat.Table(name) != nil {
		return fmt.Errorf("%w: %q", ErrTableExists, name)
	}
	if name == "" || schema.Len() == 0 {
		return ErrBadSchema
	}
	seen := map[string]bool{}
	for _, c := range schema.Columns {
		if c.Name == "" || c.Name == "ctid" || seen[c.Name] || c.Kind == types.KindNull || c.Kind > types.KindText {
			return fmt.Errorf("%w: column %q", ErrBadSchema, c.Name)
		}
		seen[c.Name] = true
	}
	td := &TableDef{Name: name, RelID: db.cat.NextRelID, Schema: types.Schema{Columns: slices.Clone(schema.Columns)}}
	db.cat.Tables = append(db.cat.Tables, td)
	db.cat.NextRelID++
	if err := db.cat.Save(db.catalogPath(), !db.opts.NoSync); err != nil {
		db.cat.Tables = db.cat.Tables[:len(db.cat.Tables)-1]
		db.cat.NextRelID--
		return err
	}
	h, err := openHeap(db.dir, td, db.wal, db.opts)
	if err != nil {
		return err
	}
	db.heaps[name] = h
	return nil
}

// IndexOption customises CreateIndex.
type IndexOption func(*IndexDef)

// Include adds INCLUDE (covering) columns to an index.
func Include(cols ...string) IndexOption {
	return func(d *IndexDef) { d.Include = append(d.Include, cols...) }
}

// CreateIndex creates a B+Tree index on table(cols...) and builds it from the
// existing rows (Problem 11.9). Errors: ErrClosed, ErrIndexExists,
// ErrNoSuchTable, ErrBadSchema (no key column), ErrNoSuchColumn.
func (db *DB) CreateIndex(name, table string, cols []string, opts ...IndexOption) error {
	if db.closed {
		return ErrClosed
	}
	if db.cat.Index(name) != nil {
		return fmt.Errorf("%w: %q", ErrIndexExists, name)
	}
	h := db.heaps[table]
	if h == nil {
		return fmt.Errorf("%w: %q", ErrNoSuchTable, table)
	}
	if len(cols) == 0 {
		return ErrBadSchema
	}
	def := &IndexDef{Name: name, Table: table, Columns: slices.Clone(cols)}
	for _, o := range opts {
		o(def)
	}
	ix, err := newIndex(def, h, db.opts.IndexFanout)
	if err != nil {
		return err
	}
	if err := ix.Build(); err != nil {
		return err
	}
	db.cat.Indexes = append(db.cat.Indexes, def)
	if err := db.cat.Save(db.catalogPath(), !db.opts.NoSync); err != nil {
		db.cat.Indexes = db.cat.Indexes[:len(db.cat.Indexes)-1]
		return err
	}
	db.indexes[name] = ix
	return nil
}

// Begin starts a transaction. It gets no xid until its first write
// (Postgres assigns xids lazily too: read-only transactions never consume one).
func (db *DB) Begin(level IsolationLevel) *Txn {
	t := &Txn{db: db, Level: level}
	db.active[t] = true
	return t
}

// oldestXmin is the VACUUM horizon: the oldest xid that any running
// transaction or any live REPEATABLE READ snapshot may still consider running.
// A deletion by a committed xid older than this is invisible to everyone.
func (db *DB) oldestXmin() types.TxnID {
	h := db.mgr.GetSnapshot(types.InvalidTxnID).Xmin
	for t := range db.active {
		if t.Level == RepeatableRead && t.snap != nil && txn.TxnIDPrecedes(t.snap.Xmin, h) {
			h = t.snap.Xmin
		}
	}
	return h
}

// Txn is a transaction.
type Txn struct {
	db    *DB
	Level IsolationLevel
	xid   types.TxnID   // 0 until the first write (lazy xid assignment)
	snap  *txn.Snapshot // RR: the transaction snapshot (nil before the first statement); RC: the last statement's
	done  bool          // committed or rolled back
}

// XID is the transaction's xid, or 0 if it has not written anything yet.
func (t *Txn) XID() types.TxnID { return t.xid }

// assignXid gives the transaction an xid on its first write.
func (t *Txn) assignXid() types.TxnID {
	if t.xid == types.InvalidTxnID {
		t.xid = t.db.mgr.Begin()
	}
	return t.xid
}

// mine is the set of xids whose changes count as "my own".
func (t *Txn) mine() txn.CurrentXids {
	if t.xid == types.InvalidTxnID {
		return nil
	}
	return txn.CurrentXids{t.xid}
}

// finish marks the transaction ended.
func (t *Txn) finish() {
	t.done = true
	delete(t.db.active, t)
}

// ---------------------------------------------------------------- values

// toValue converts a Go literal like testkit.V does: nil → NULL, bool, int /
// int64 → int8, int32 → int4, float64, string, types.Value.
func toValue(x any) types.Value {
	switch v := x.(type) {
	case nil:
		return types.Null()
	case types.Value:
		return v
	case bool:
		return types.Bool(v)
	case int:
		return types.Int64(int64(v))
	case int64:
		return types.Int64(v)
	case int32:
		return types.Int32(v)
	case float64:
		return types.Float64(v)
	case string:
		return types.Text(v)
	}
	panic(fmt.Sprintf("engine: unsupported literal %T", x))
}

// coerceValue converts v to the column kind the way an assignment cast does:
// NULL stays NULL; int4 ↔ int8 (int4 must fit: ErrOutOfRange); int → float8.
// Anything else must already have the right kind (else tuple-style ErrTypeMismatch).
func coerceValue(k types.Kind, v types.Value) (types.Value, error) {
	if v.IsNull() || v.Kind == k {
		return v, nil
	}
	isInt := v.Kind == types.KindInt32 || v.Kind == types.KindInt64
	switch {
	case k == types.KindInt32 && isInt:
		if v.I < math.MinInt32 || v.I > math.MaxInt32 {
			return v, fmt.Errorf("%w: %d for int4", ErrOutOfRange, v.I)
		}
		return types.Int32(int32(v.I)), nil
	case k == types.KindInt64 && isInt:
		return types.Int64(v.I), nil
	case k == types.KindFloat64 && isInt:
		return types.Float64(float64(v.I)), nil
	}
	return v, fmt.Errorf("%w: %s value for %s column", errTypeMismatch, v.Kind, k)
}

// coerceRow coerces every value of row to the schema (a new row is returned).
func coerceRow(s types.Schema, row types.Row) (types.Row, error) {
	if len(row) != s.Len() {
		return nil, fmt.Errorf("%w: %d values for %d columns", errArity, len(row), s.Len())
	}
	out := make(types.Row, len(row))
	for i, v := range row {
		c, err := coerceValue(s.Columns[i].Kind, v)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", s.Columns[i].Name, err)
		}
		out[i] = c
	}
	return out, nil
}
