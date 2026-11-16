// Package types holds the shared vocabulary used by every phase:
// SQL values, rows, schemas and the IDs that address data on disk.
//
// This package is PROVIDED — it is not a problem. Read it, don't change it.
package types

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// PageSize is the size of every on-disk page / block, same as Postgres (BLCKSZ).
const PageSize = 8192

// PageID is a block number inside a relation file (Postgres: BlockNumber).
type PageID uint32

// InvalidPageID marks "no page" (e.g. end of a sibling chain).
const InvalidPageID PageID = math.MaxUint32

// SlotID is an index into a page's line-pointer array (Postgres: OffsetNumber).
type SlotID uint16

// TID addresses one tuple version on disk (Postgres: ItemPointer / ctid).
type TID struct {
	Page PageID
	Slot SlotID
}

func (t TID) String() string { return fmt.Sprintf("(%d,%d)", t.Page, t.Slot) }

// Less orders TIDs by physical position (page, then slot).
func (t TID) Less(o TID) bool {
	if t.Page != o.Page {
		return t.Page < o.Page
	}
	return t.Slot < o.Slot
}

// TxnID is a transaction id (Postgres: TransactionId / xid). 0 is invalid.
type TxnID uint32

// InvalidTxnID means "no transaction" (e.g. xmax of a live tuple).
const InvalidTxnID TxnID = 0

// LSN is a log sequence number: the byte position of a WAL record.
type LSN uint64

// Kind is the data type of a Value.
type Kind uint8

const (
	KindNull Kind = iota
	KindBool
	KindInt32
	KindInt64
	KindFloat64
	KindText
)

func (k Kind) String() string {
	switch k {
	case KindNull:
		return "null"
	case KindBool:
		return "bool"
	case KindInt32:
		return "int4"
	case KindInt64:
		return "int8"
	case KindFloat64:
		return "float8"
	case KindText:
		return "text"
	}
	return fmt.Sprintf("kind(%d)", uint8(k))
}

// Value is one SQL value. Only the field matching Kind is meaningful:
// Bool→B, Int32/Int64→I, Float64→F, Text→S. KindNull is SQL NULL.
type Value struct {
	Kind Kind
	B    bool
	I    int64
	F    float64
	S    string
}

func Null() Value             { return Value{Kind: KindNull} }
func Bool(b bool) Value       { return Value{Kind: KindBool, B: b} }
func Int32(v int32) Value     { return Value{Kind: KindInt32, I: int64(v)} }
func Int64(v int64) Value     { return Value{Kind: KindInt64, I: v} }
func Float64(v float64) Value { return Value{Kind: KindFloat64, F: v} }
func Text(s string) Value     { return Value{Kind: KindText, S: s} }
func (v Value) IsNull() bool  { return v.Kind == KindNull }
func (v Value) isNumeric() bool {
	return v.Kind == KindInt32 || v.Kind == KindInt64 || v.Kind == KindFloat64
}

func (v Value) String() string {
	switch v.Kind {
	case KindNull:
		return "NULL"
	case KindBool:
		return strconv.FormatBool(v.B)
	case KindInt32, KindInt64:
		return strconv.FormatInt(v.I, 10)
	case KindFloat64:
		return strconv.FormatFloat(v.F, 'g', -1, 64)
	case KindText:
		return "'" + v.S + "'"
	}
	return "?"
}

// Compare orders two NON-NULL values: -1 if a<b, 0 if equal, +1 if a>b.
// Numeric kinds compare with each other by value (int4 vs float8 is fine).
// Bool: false < true. Text: byte-wise (like COLLATE "C").
// It panics on NULL or on incomparable kinds — NULL ordering is the
// caller's job (see ORDER BY ... NULLS FIRST/LAST in phase 7).
func Compare(a, b Value) int {
	if a.IsNull() || b.IsNull() {
		panic("types.Compare: NULL has no order; handle NULL before calling Compare")
	}
	switch {
	case a.isNumeric() && b.isNumeric():
		if a.Kind != KindFloat64 && b.Kind != KindFloat64 {
			return cmp3(a.I < b.I, a.I > b.I)
		}
		af, bf := a.asFloat(), b.asFloat()
		return cmp3(af < bf, af > bf)
	case a.Kind == KindText && b.Kind == KindText:
		return strings.Compare(a.S, b.S)
	case a.Kind == KindBool && b.Kind == KindBool:
		return cmp3(!a.B && b.B, a.B && !b.B)
	}
	panic(fmt.Sprintf("types.Compare: cannot compare %s with %s", a.Kind, b.Kind))
}

func (v Value) asFloat() float64 {
	if v.Kind == KindFloat64 {
		return v.F
	}
	return float64(v.I)
}

func cmp3(less, greater bool) int {
	if less {
		return -1
	}
	if greater {
		return 1
	}
	return 0
}

// Identical reports whether two values are the same, treating NULL as equal
// to NULL. Use it in tests and hash tables — NOT for SQL "=" semantics.
func Identical(a, b Value) bool {
	if a.IsNull() || b.IsNull() {
		return a.IsNull() && b.IsNull()
	}
	if a.isNumeric() && b.isNumeric() {
		return Compare(a, b) == 0
	}
	if a.Kind != b.Kind {
		return false
	}
	return Compare(a, b) == 0
}

// HashKey returns a string usable as a Go map key for a value. NULL gets its
// own key; numeric kinds that compare equal share a key (1 == 1.0).
func (v Value) HashKey() string {
	switch v.Kind {
	case KindNull:
		return "N"
	case KindBool:
		return "B" + strconv.FormatBool(v.B)
	case KindInt32, KindInt64:
		return "#" + strconv.FormatFloat(float64(v.I), 'g', -1, 64) + "|" + strconv.FormatInt(v.I, 10)
	case KindFloat64:
		if v.F == math.Trunc(v.F) && math.Abs(v.F) < 1<<53 {
			return "#" + strconv.FormatFloat(v.F, 'g', -1, 64) + "|" + strconv.FormatInt(int64(v.F), 10)
		}
		return "#" + strconv.FormatFloat(v.F, 'g', -1, 64)
	case KindText:
		return "T" + v.S
	}
	return "?"
}

// Row is one tuple of values, in schema column order.
type Row []Value

func (r Row) String() string {
	parts := make([]string, len(r))
	for i, v := range r {
		parts[i] = v.String()
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// HashKey returns a map key for the whole row (see Value.HashKey).
func (r Row) HashKey() string {
	parts := make([]string, len(r))
	for i, v := range r {
		parts[i] = v.HashKey()
	}
	return strings.Join(parts, "\x00")
}

// Column describes one attribute of a table.
type Column struct {
	Name    string
	Kind    Kind
	NotNull bool
}

// Schema is an ordered list of columns.
type Schema struct {
	Columns []Column
}

// NewSchema is a shorthand: NewSchema("id", KindInt32, "name", KindText).
func NewSchema(pairs ...any) Schema {
	if len(pairs)%2 != 0 {
		panic("types.NewSchema: want name/kind pairs")
	}
	var s Schema
	for i := 0; i < len(pairs); i += 2 {
		s.Columns = append(s.Columns, Column{Name: pairs[i].(string), Kind: pairs[i+1].(Kind)})
	}
	return s
}

// Index returns the position of the named column, or -1.
func (s Schema) Index(name string) int {
	for i, c := range s.Columns {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// Len is the number of columns.
func (s Schema) Len() int { return len(s.Columns) }
