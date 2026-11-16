package executor

import (
	"math"
	"testing"
)

// The example relation: 1000 heap pages, 100 000 tuples, 300 index leaf pages.
func costIn(s, corr float64) ScanCostInput {
	return ScanCostInput{RelPages: 1000, RelTuples: 100000, HasIndex: true, IndexPages: 300, Selectivity: s, Correlation: corr}
}

func TestP6_14_ChooseScan(t *testing.T) {
	inf := math.Inf(1)
	tests := []struct {
		name             string
		in               ScanCostInput
		wantPath         ScanPath
		wantCost         float64
		wantSeq, wantIdx float64 // -1 = don't check
		wantBitmap       float64 // -1 = don't check
	}{
		{"no index: seq scan only", ScanCostInput{RelPages: 1000, RelTuples: 100000, Selectivity: 0.0001}, PathSeqScan, 2250, 2250, inf, inf},
		{"10 rows, uncorrelated: bitmap", costIn(0.0001, 0), PathBitmapScan, 41.2, 2250, 44.175, 41.2},
		{"1 row: plain index scan", costIn(0.00001, 0), PathIndexScan, 8.0175, 2250, 8.0175, 8.02},
		{"1% of a perfectly correlated column: index scan", costIn(0.01, 1), PathIndexScan, 42.5, 2250, 42.5, -1},
		{"1% at correlation 0.5: bitmap", costIn(0.01, 0.5), PathBitmapScan, -1, 2250, 2033.75, -1},
		{"37.5% uncorrelated: bitmap still wins", costIn(0.375, 0), PathBitmapScan, 2202, 2250, -1, 2202},
		{"40.625% uncorrelated: seq scan", costIn(0.40625, 0), PathSeqScan, 2250, 2250, -1, 2300.5},
		{"50% correlated: index scan", costIn(0.5, 1), PathIndexScan, 1978, 2250, 1978, 2600},
		{"50% negatively correlated is the same", costIn(0.5, -1), PathIndexScan, 1978, 2250, 1978, 2600},
		{"62.5% correlated: seq scan", costIn(0.625, 1), PathSeqScan, 2250, 2250, 2473.75, -1},
		{"everything: seq scan", costIn(1, 0), PathSeqScan, 2250, 2250, 6950, 4200},
		{"zero selectivity: index wins the tie with bitmap", costIn(0, 0), PathIndexScan, 0, 2250, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ChooseScan(tc.in)
			if got.Path != tc.wantPath {
				t.Fatalf("Path = %s, want %s (seq %.4f, index %.4f, bitmap %.4f)", got.Path, tc.wantPath, got.SeqCost, got.IndexCost, got.BitmapCost)
			}
			check := func(what string, g, w float64) {
				if w != -1 && !near(g, w) {
					t.Fatalf("%s = %.6f, want %.6f", what, g, w)
				}
			}
			check("Cost", got.Cost, tc.wantCost)
			check("SeqCost", got.SeqCost, tc.wantSeq)
			check("IndexCost", got.IndexCost, tc.wantIdx)
			check("BitmapCost", got.BitmapCost, tc.wantBitmap)
		})
	}
}

// Sweep s = k/256: an uncorrelated column flips from Bitmap Heap Scan to
// Seq Scan exactly once, at k = 100 (s ≈ 0.39).
//
//	bitmap(s) = 4·ceil(300s) + 750s + 1000 + 1250s  (once every page is read)
//	k = 99  → 468 + 773.4375 + 1000            = 2241.4375 < 2250
//	k = 100 → 472 + 781.25   + 1000            = 2253.25   > 2250
func TestP6_14_ChooseScan_Crossover(t *testing.T) {
	for k := 1; k <= 256; k++ {
		s := float64(k) / 256
		got := ChooseScan(costIn(s, 0))
		want := PathBitmapScan
		if k >= 100 {
			want = PathSeqScan
		}
		if got.Path != want {
			t.Fatalf("s = %d/256: Path = %s, want %s (seq %.4f, index %.4f, bitmap %.4f)",
				k, got.Path, want, got.SeqCost, got.IndexCost, got.BitmapCost)
		}
	}
	if got := ChooseScan(costIn(99.0/256, 0)).BitmapCost; !near(got, 2241.4375) {
		t.Fatalf("bitmap cost at 99/256 = %v, want 2241.4375", got)
	}
	if got := ChooseScan(costIn(100.0/256, 0)).BitmapCost; !near(got, 2253.25) {
		t.Fatalf("bitmap cost at 100/256 = %v, want 2253.25", got)
	}
}

// With a perfectly correlated column the plain index scan wins all the way up
// to its crossover with the seq scan at index(s) ≈ 3950s + 3 = 2250 → s ≈ 0.569.
func TestP6_14_ChooseScan_CorrelatedCrossover(t *testing.T) {
	last := PathIndexScan
	flips := 0
	for k := 1; k <= 256; k++ {
		got := ChooseScan(costIn(float64(k)/256, 1)).Path
		if got == PathBitmapScan {
			t.Fatalf("s = %d/256: bitmap chosen for a perfectly correlated column", k)
		}
		if got != last {
			flips++
			s := float64(k) / 256
			if s < 0.55 || s > 0.59 {
				t.Fatalf("index→seq flip at s = %v, want ≈ 0.569", s)
			}
			last = got
		}
	}
	if flips != 1 {
		t.Fatalf("path flipped %d times, want exactly once", flips)
	}
}

func TestP6_14_ChooseScan_SmallTable(t *testing.T) {
	// A 10-page table: seq = 10 + 1000·0.0125 = 22.5,
	// index for one row = 1·4 + 0.0075 + 4 + 0.01 = 8.0175 → the index wins.
	in := ScanCostInput{RelPages: 10, RelTuples: 1000, HasIndex: true, IndexPages: 3, Selectivity: 0.001}
	got := ChooseScan(in)
	if got.Path != PathIndexScan || !near(got.SeqCost, 22.5) || !near(got.Cost, 8.0175) {
		t.Fatalf("got %+v, want Index Scan 8.0175 (seq 22.5)", got)
	}
	// 5% of it: T = 50, pages = 1000/70 ≈ 14.3 ≥ P → 10 pages.
	// bitmap = 4 + 0.375 + 10·1 + 0.625 = 15; seq 22.5; index = 4.375 + 40 + 0.5 = 44.875
	in.Selectivity = 0.05
	got = ChooseScan(in)
	if got.Path != PathBitmapScan || !near(got.BitmapCost, 15) || !near(got.IndexCost, 44.875) {
		t.Fatalf("got %+v, want Bitmap Heap Scan 15 (index 44.875)", got)
	}
}
