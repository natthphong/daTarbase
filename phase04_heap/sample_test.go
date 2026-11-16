package heap

import (
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/natthphong/daTarbase/pkg/types"
)

// sampleStore: 10 pages, page p holds "p<p>a" and "p<p>b".
func sampleStore() *PageStore {
	var pages [][]string
	for p := 0; p < 10; p++ {
		pages = append(pages, []string{fmt.Sprintf("p%da", p), fmt.Sprintf("p%db", p)})
	}
	return mkStore(pages...)
}

func pagesOf(ts []Tuple) []types.PageID {
	var out []types.PageID
	for _, tu := range ts {
		if len(out) == 0 || out[len(out)-1] != tu.TID.Page {
			out = append(out, tu.TID.Page)
		}
	}
	return out
}

func TestP4_15_SampleSystem(t *testing.T) {
	tests := []struct {
		name    string
		percent float64
		seed    uint64
		pages   string
		reads   int
	}{
		{"30% with seed 42", 30, 42, "[0 2 3 5 6 8]", 6},
		{"another seed, another sample", 30, 7, "[2 3 5 6]", 4},
		{"50% is a superset with the same seed", 50, 42, "[0 2 3 5 6 8 9]", 7},
		{"0% picks nothing", 0, 42, "[]", 0},
		{"100% picks every page", 100, 42, "[0 1 2 3 4 5 6 7 8 9]", 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := sampleStore()
			got, err := SampleSystem(s, tc.percent, tc.seed)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil {
				t.Fatalf("result must be non-nil")
			}
			if p := fmt.Sprint(pagesOf(got)); p != tc.pages {
				t.Fatalf("picked pages = %s, want %s", p, tc.pages)
			}
			if len(got) != 2*tc.reads || s.Reads != tc.reads {
				t.Fatalf("got %d tuples with %d reads, want %d tuples with %d reads", len(got), s.Reads, 2*tc.reads, tc.reads)
			}
			for _, tu := range got {
				want := fmt.Sprintf("p%d%c", tu.TID.Page, 'a'+rune(tu.TID.Slot))
				if string(tu.Data) != want {
					t.Fatalf("tuple %v = %q, want %q", tu.TID, tu.Data, want)
				}
			}
		})
	}
}

func TestP4_15_SampleSystem_Repeatable(t *testing.T) {
	s := sampleStore()
	a, _ := SampleSystem(s, 30, 42)
	b, _ := SampleSystem(s, 30, 42)
	if fmtTuples(a) != fmtTuples(b) {
		t.Fatalf("same seed gave different samples:\n %s\n %s", fmtTuples(a), fmtTuples(b))
	}
}

func TestP4_15_SampleSystem_SkipsDeadAndEmpty(t *testing.T) {
	s := sampleStore()
	kill(s, tid(2, 0))
	got, _ := SampleSystem(s, 30, 42)
	assertTuples(t, got[:3], "(0,0)=p0a (0,1)=p0b (2,1)=p2b")

	empty := NewPageStore()
	got, err := SampleSystem(empty, 100, 1)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty relation: %v, %v", got, err)
	}
}

func TestP4_15_SampleSystem_BadPercent(t *testing.T) {
	for _, pct := range []float64{-0.5, 100.01, 101, math.NaN(), math.Inf(1)} {
		s := sampleStore()
		got, err := SampleSystem(s, pct, 42)
		if !errors.Is(err, ErrBadPercent) || got != nil || s.Reads != 0 {
			t.Fatalf("percent %v: got %v, err %v, reads %d; want nil, ErrBadPercent, 0", pct, got, err, s.Reads)
		}
	}
}
