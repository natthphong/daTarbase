package engine

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"slices"

	txn "github.com/natthphong/daTarbase/phase09_txn"
	wal "github.com/natthphong/daTarbase/phase10_wal"
	"github.com/natthphong/daTarbase/pkg/types"
)

// Everything in this file is PROVIDED: the on-disk WAL file (pg_wal), the
// bodies of the engine's WAL records and the clog file (pg_xact).
//
// The engine reuses phase 10's record framing (EncodeRecord / ReadRecords:
// 29-byte header, CRC-32C, LSN = byte offset) but writes it to a real file,
// and gives RecUpdate records a heap-specific body ("resource manager" data
// in Postgres terms, like XLOG_HEAP_INSERT / XLOG_HEAP_DELETE / XLOG_HEAP2_PRUNE).
//
//	RecUpdate     Txn = writer (0 for VACUUM)   body = heapRecord (see below)
//	RecCommit     Txn = committing xid          empty body
//	RecAbort      Txn = aborting xid            empty body
//	RecCheckpoint Txn = 0                       body = NextXid uint32 LE

// walWriter appends records to the pg_wal file. Records go to an in-memory
// buffer first (Postgres: WAL buffers) and reach the file only on Flush
// (XLogFlush). A simulated crash throws the buffer away.
type walWriter struct {
	f       *os.File
	sync    bool      // fsync on every Flush
	buf     []byte    // encoded records appended but not yet written
	flushed types.LSN // everything below this offset is in the file
	end     types.LSN // LSN the next record will get (= flushed + len(buf))

	Flushes int // number of Flush calls that actually wrote something
}

// openWAL opens (creating if needed) the WAL file, returns every valid record
// in it, and cuts off a torn tail so new records are appended right after the
// last valid one.
func openWAL(path string, sync bool) (*walWriter, []wal.Record, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if len(data) < len(wal.FileHeader) {
		data = []byte(wal.FileHeader)
		if _, err := f.WriteAt(data, 0); err != nil {
			f.Close()
			return nil, nil, err
		}
	} else if string(data[:len(wal.FileHeader)]) != wal.FileHeader {
		f.Close()
		return nil, nil, fmt.Errorf("%w: bad WAL file header", ErrCorruptWAL)
	}
	recs, end, err := wal.ReadRecords(data, wal.FirstLSN)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if err := f.Truncate(int64(end)); err != nil {
		f.Close()
		return nil, nil, err
	}
	return &walWriter{f: f, sync: sync, flushed: end, end: end}, recs, nil
}

// Append adds a record to the WAL buffer and returns its LSN. Nothing is
// written to the file until Flush.
func (w *walWriter) Append(xid types.TxnID, typ wal.RecordType, body []byte) types.LSN {
	lsn := w.end
	b := wal.EncodeRecord(wal.Record{LSN: lsn, Txn: xid, Type: typ, Body: body})
	w.buf = append(w.buf, b...)
	w.end += types.LSN(len(b))
	return lsn
}

// End is the LSN the next record will get.
func (w *walWriter) End() types.LSN { return w.end }

// FlushedLSN is the end of the durable part of the log.
func (w *walWriter) FlushedLSN() types.LSN { return w.flushed }

// Flush makes the record at LSN upTo (and everything before it) durable.
// Like XLogFlush it writes everything buffered so far, not just up to upTo.
// upTo below the durable end (e.g. 0, or an already flushed record) → no I/O.
func (w *walWriter) Flush(upTo types.LSN) error {
	if upTo < w.flushed || len(w.buf) == 0 {
		return nil
	}
	if _, err := w.f.WriteAt(w.buf, int64(w.flushed)); err != nil {
		return err
	}
	if w.sync {
		if err := w.f.Sync(); err != nil {
			return err
		}
	}
	w.flushed, w.buf = w.end, nil
	w.Flushes++
	return nil
}

// crash drops the unflushed buffer and closes the file (power loss).
func (w *walWriter) crash() {
	w.buf, w.end = nil, w.flushed
	w.f.Close()
}

func (w *walWriter) close() error { return w.f.Close() }

// readWALFile returns every valid record of a WAL file (for tests and tools).
func readWALFile(path string) ([]wal.Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	recs, _, err := wal.ReadRecords(data, wal.FirstLSN)
	return recs, err
}

// ---------------------------------------------------------------- heap records

// heapOp says what a heap WAL record does to its page.
type heapOp uint8

const (
	opInsert heapOp = 1 // put Tuple into the page; it must land in Slot (XLOG_HEAP_INSERT)
	opDelete heapOp = 2 // set the xmax of the tuple in Slot to Xmax (XLOG_HEAP_DELETE)
	opPrune  heapOp = 3 // DeleteTuple every slot in Slots, then Compact (XLOG_HEAP2_PRUNE)
)

// heapRecord is the body of a RecUpdate record.
//
//	op u8 | rel u32 | page u32 | slot u16 | payload
//	  insert: payload = the tuple bytes
//	  delete: payload = xmax u32
//	  prune:  payload = n u16, then n × slot u16
//
// All integers little-endian.
type heapRecord struct {
	Op    heapOp
	Rel   uint32       // TableDef.RelID of the table
	Page  types.PageID // block the record applies to
	Slot  types.SlotID // insert / delete: the slot
	Tuple []byte       // insert: the full tuple (header included)
	Xmax  types.TxnID  // delete: the new xmax
	Slots []types.SlotID
}

func (r heapRecord) encode() []byte {
	b := make([]byte, 11, 11+len(r.Tuple)+4+2*len(r.Slots))
	b[0] = byte(r.Op)
	binary.LittleEndian.PutUint32(b[1:], r.Rel)
	binary.LittleEndian.PutUint32(b[5:], uint32(r.Page))
	binary.LittleEndian.PutUint16(b[9:], uint16(r.Slot))
	switch r.Op {
	case opInsert:
		b = append(b, r.Tuple...)
	case opDelete:
		b = binary.LittleEndian.AppendUint32(b, uint32(r.Xmax))
	case opPrune:
		b = binary.LittleEndian.AppendUint16(b, uint16(len(r.Slots)))
		for _, s := range r.Slots {
			b = binary.LittleEndian.AppendUint16(b, uint16(s))
		}
	}
	return b
}

func decodeHeapRecord(b []byte) (heapRecord, error) {
	if len(b) < 11 {
		return heapRecord{}, ErrCorruptWAL
	}
	r := heapRecord{
		Op:   heapOp(b[0]),
		Rel:  binary.LittleEndian.Uint32(b[1:]),
		Page: types.PageID(binary.LittleEndian.Uint32(b[5:])),
		Slot: types.SlotID(binary.LittleEndian.Uint16(b[9:])),
	}
	p := b[11:]
	switch r.Op {
	case opInsert:
		if len(p) == 0 {
			return heapRecord{}, ErrCorruptWAL
		}
		r.Tuple = append([]byte(nil), p...)
	case opDelete:
		if len(p) != 4 {
			return heapRecord{}, ErrCorruptWAL
		}
		r.Xmax = types.TxnID(binary.LittleEndian.Uint32(p))
	case opPrune:
		if len(p) < 2 {
			return heapRecord{}, ErrCorruptWAL
		}
		n := int(binary.LittleEndian.Uint16(p))
		if len(p) != 2+2*n {
			return heapRecord{}, ErrCorruptWAL
		}
		for i := 0; i < n; i++ {
			r.Slots = append(r.Slots, types.SlotID(binary.LittleEndian.Uint16(p[2+2*i:])))
		}
	default:
		return heapRecord{}, ErrCorruptWAL
	}
	return r, nil
}

func encodeCheckpoint(nextXid types.TxnID) []byte {
	return binary.LittleEndian.AppendUint32(nil, uint32(nextXid))
}

func decodeCheckpoint(b []byte) (types.TxnID, error) {
	if len(b) != 4 {
		return 0, ErrCorruptWAL
	}
	return types.TxnID(binary.LittleEndian.Uint32(b)), nil
}

// ---------------------------------------------------------------- pg_xact

// saveClog writes every clog page to path with WriteFileAtomic (Problem 11.1):
// for each page in ascending page number: pageNo u32 LE, then ClogPageSize bytes.
func saveClog(path string, c *txn.Clog, sync bool) error {
	nums := make([]uint32, 0, len(c.Pages))
	for n := range c.Pages {
		nums = append(nums, n)
	}
	slices.Sort(nums)
	var out []byte
	for _, n := range nums {
		out = binary.LittleEndian.AppendUint32(out, n)
		out = append(out, c.Pages[n]...)
	}
	return WriteFileAtomic(path, out, sync)
}

// loadClog reads a file written by saveClog. A missing file is an empty clog.
func loadClog(path string) (*txn.Clog, error) {
	c := txn.NewClog()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	const rec = 4 + txn.ClogPageSize
	if len(b)%rec != 0 {
		return nil, fmt.Errorf("engine: pg_xact has %d bytes, not a multiple of %d", len(b), rec)
	}
	for off := 0; off < len(b); off += rec {
		n := binary.LittleEndian.Uint32(b[off:])
		c.Pages[n] = append([]byte(nil), b[off+4:off+rec]...)
	}
	return c, nil
}
