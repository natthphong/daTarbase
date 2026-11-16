// Package exec defines the Volcano / iterator model shared by every query
// operator (phases 6, 7, 8): each operator pulls rows from its children one
// at a time via Next().
//
// This package is PROVIDED — it is not a problem.
package exec

import (
	"errors"

	"github.com/natthphong/daTarbase/pkg/types"
)

// Iterator is a pull-based operator (Postgres: ExecProcNode).
//
//	Open()  — prepare state, open children.
//	Next()  — return the next row; ok=false means "no more rows".
//	Close() — release resources, close children.
//
// Calling Next before Open, or after Close, is a bug in the caller.
type Iterator interface {
	Open() error
	Next() (row types.Row, ok bool, err error)
	Close() error
}

// ErrNotOpen is returned by provided iterators used incorrectly.
var ErrNotOpen = errors.New("exec: iterator not open")

// ValuesIter is an in-memory source of rows (Postgres: Values Scan).
// It also records how it was used so tests can check operators open/close
// their children correctly and don't read more rows than needed.
type ValuesIter struct {
	rows []types.Row
	pos  int
	open bool

	Opens  int // times Open was called
	Closes int // times Close was called
	Pulled int // rows returned by Next
}

// Values returns an iterator over the given rows.
func Values(rows ...types.Row) *ValuesIter { return &ValuesIter{rows: rows} }

func (v *ValuesIter) Open() error {
	v.open, v.pos = true, 0
	v.Opens++
	return nil
}

func (v *ValuesIter) Next() (types.Row, bool, error) {
	if !v.open {
		return nil, false, ErrNotOpen
	}
	if v.pos >= len(v.rows) {
		return nil, false, nil
	}
	r := v.rows[v.pos]
	v.pos++
	v.Pulled++
	return r, true, nil
}

func (v *ValuesIter) Close() error {
	v.open = false
	v.Closes++
	return nil
}

// Collect opens it, drains it and closes it.
func Collect(it Iterator) ([]types.Row, error) {
	if err := it.Open(); err != nil {
		return nil, err
	}
	var out []types.Row
	for {
		r, ok, err := it.Next()
		if err != nil {
			it.Close()
			return out, err
		}
		if !ok {
			break
		}
		out = append(out, r)
	}
	return out, it.Close()
}
