// Package testkit has helpers that make test data short and readable.
//
// This package is PROVIDED — it is not a problem.
package testkit

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

// V converts a Go literal to a types.Value:
//
//	nil → NULL, bool → bool, int/int64 → int8, int32 → int4,
//	float64 → float8, string → text, types.Value → itself.
func V(x any) types.Value {
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
	panic(fmt.Sprintf("testkit.V: unsupported %T", x))
}

// R builds a row: R(1, "alice", nil) → (1, 'alice', NULL).
func R(xs ...any) types.Row {
	row := make(types.Row, len(xs))
	for i, x := range xs {
		row[i] = V(x)
	}
	return row
}

// Rows builds many rows: Rows([]any{1,"a"}, []any{2,"b"}).
func Rows(rs ...[]any) []types.Row {
	out := make([]types.Row, len(rs))
	for i, r := range rs {
		out[i] = R(r...)
	}
	return out
}

func rowsEqual(a, b types.Row) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !types.Identical(a[i], b[i]) {
			return false
		}
	}
	return true
}

func format(rows []types.Row) string {
	var sb strings.Builder
	for _, r := range rows {
		sb.WriteString("\n    ")
		sb.WriteString(r.String())
	}
	if len(rows) == 0 {
		sb.WriteString("\n    (no rows)")
	}
	return sb.String()
}

// AssertRows fails unless got equals want in the same order.
func AssertRows(t testing.TB, got, want []types.Row) {
	t.Helper()
	ok := len(got) == len(want)
	for i := 0; ok && i < len(got); i++ {
		ok = rowsEqual(got[i], want[i])
	}
	if !ok {
		t.Fatalf("rows mismatch\n  got:%s\n  want:%s", format(got), format(want))
	}
}

// AssertRowsUnordered fails unless got and want hold the same multiset of rows.
func AssertRowsUnordered(t testing.TB, got, want []types.Row) {
	t.Helper()
	key := func(rs []types.Row) []string {
		ks := make([]string, len(rs))
		for i, r := range rs {
			ks[i] = r.HashKey()
		}
		sort.Strings(ks)
		return ks
	}
	g, w := key(got), key(want)
	ok := len(g) == len(w)
	for i := 0; ok && i < len(g); i++ {
		ok = g[i] == w[i]
	}
	if !ok {
		t.Fatalf("rows mismatch (order ignored)\n  got:%s\n  want:%s", format(got), format(want))
	}
}
