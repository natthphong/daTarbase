package executor

import (
	"math"
)

// Planner cost constants, Postgres defaults (postgresql.conf).
const (
	SeqPageCost       = 1.0    // seq_page_cost: one sequentially-read page
	RandomPageCost    = 4.0    // random_page_cost: one randomly-read page
	CPUTupleCost      = 0.01   // cpu_tuple_cost: processing one heap tuple
	CPUIndexTupleCost = 0.005  // cpu_index_tuple_cost: processing one index entry
	CPUOperatorCost   = 0.0025 // cpu_operator_cost: evaluating one operator (e.g. "=")
)

// ScanPath is an access path for a single-table scan.
type ScanPath uint8

const (
	PathSeqScan    ScanPath = iota // read the whole heap
	PathIndexScan                  // walk the index, fetch heap rows by TID
	PathBitmapScan                 // bitmap index scan + bitmap heap scan
)

func (p ScanPath) String() string {
	return [...]string{"Seq Scan", "Index Scan", "Bitmap Heap Scan"}[p]
}

// ScanCostInput describes the query "SELECT * FROM t WHERE col <op> const"
// (one qual, one candidate index on col).
type ScanCostInput struct {
	RelPages    float64 // P: heap pages (pg_class.relpages)
	RelTuples   float64 // N: heap tuples (pg_class.reltuples)
	HasIndex    bool    // is there a btree index on col?
	IndexPages  float64 // IP: index leaf pages
	Selectivity float64 // s: estimated fraction of rows matching the qual (0..1)
	Correlation float64 // c: pg_stats.correlation of col, -1..1 (1 = heap is in index order)
}

// ScanChoice is the planner's answer.
type ScanChoice struct {
	Path       ScanPath // the cheapest path
	Cost       float64  // its total cost
	SeqCost    float64  // total cost of each candidate;
	IndexCost  float64  // +Inf when !HasIndex
	BitmapCost float64  // +Inf when !HasIndex
}

// Problem 6.14 — ChooseScan [Medium]
//
// Cost a Seq Scan, an Index Scan and a Bitmap Heap Scan for one qual and pick the cheapest, Postgres-style.
//
// Let P = RelPages, N = RelTuples, IP = IndexPages, s = Selectivity,
// c = Correlation, T = s·N (tuples fetched). All arithmetic in float64.
//
//	Seq Scan:     P·seq_page_cost + N·(cpu_tuple_cost + cpu_operator_cost)
//
//	index part (shared by Index Scan and Bitmap Scan):
//	              ceil(s·IP)·random_page_cost + T·(cpu_index_tuple_cost + cpu_operator_cost)
//
//	heap pages fetched (Mackert–Lohman, table fits in cache):
//	              pages = 2·P·T / (2·P + T);  if pages ≥ P → pages = P, else pages = ceil(pages)
//	              (T = 0 → pages = 0)
//
//	Index Scan:   index part + IO + T·cpu_tuple_cost, where
//	              maxIO = pages · random_page_cost              (heap in random order)
//	              k     = ceil(s·P)
//	              minIO = k > 0 ? random_page_cost + (k-1)·seq_page_cost : 0   (heap in index order)
//	              IO    = maxIO + c²·(minIO - maxIO)
//
//	Bitmap Scan:  index part + pages·costPerPage + T·(cpu_tuple_cost + cpu_operator_cost), where
//	              costPerPage = pages ≥ 2 ? random_page_cost - (random_page_cost - seq_page_cost)·sqrt(pages/P)
//	                                      : random_page_cost
//	              (the more of the table we read, the more sequential the reads become;
//	              the extra cpu_operator_cost is the recheck of the condition)
//
// Rules:
//  1. Without an index only the Seq Scan exists; IndexCost and BitmapCost are +Inf.
//  2. Choose the smallest cost; ties prefer Seq Scan, then Index Scan, then Bitmap Scan.
//  3. Fill all of SeqCost / IndexCost / BitmapCost, plus Path and Cost of the winner.
//
// Example: P = 1000, N = 100000, IP = 300 (Seq Scan = 1000 + 1250 = 2250)
//
//	s = 0.0001, c = 0: T = 10, pages = ceil(9.95) = 10
//	    index part = 1·4 + 10·0.0075 = 4.075
//	    Index  = 4.075 + 40 + 0.1                      = 44.175
//	    Bitmap = 4.075 + 10·(4 - 3·sqrt(0.01)) + 0.125 = 41.2     → Bitmap Heap Scan
//	s = 0.00001, c = 0 (one row): Index 8.0175 < Bitmap 8.02  → Index Scan
//	s = 0.01, c = 1: Index = 12 + 7.5 + 13 + 10 = 42.5          → Index Scan
//	s = 0.3 → Bitmap (1960);  s = 0.4 → Seq Scan (Bitmap 2280 > 2250)
//
// Constraints: O(1).
// Postgres reference: cost_seqscan(), cost_index(), cost_bitmap_heap_scan(), index_pages_fetched()
// in src/backend/optimizer/path/costsize.c; genericcostestimate() in selfuncs.c
func ChooseScan(in ScanCostInput) ScanChoice {
	P, N, s := in.RelPages, in.RelTuples, in.Selectivity
	seq := P*SeqPageCost + N*(CPUTupleCost+CPUOperatorCost)
	out := ScanChoice{Path: PathSeqScan, Cost: seq, SeqCost: seq, IndexCost: math.Inf(1), BitmapCost: math.Inf(1)}
	if !in.HasIndex {
		return out
	}
	T := s * N
	indexPart := math.Ceil(s*in.IndexPages)*RandomPageCost + T*(CPUIndexTupleCost+CPUOperatorCost)
	pages := 0.0
	if T > 0 {
		pages = 2 * P * T / (2*P + T)
		if pages >= P {
			pages = P
		} else {
			pages = math.Ceil(pages)
		}
	}
	maxIO := pages * RandomPageCost
	minIO := 0.0
	if k := math.Ceil(s * P); k > 0 {
		minIO = RandomPageCost + (k-1)*SeqPageCost
	}
	io := maxIO + in.Correlation*in.Correlation*(minIO-maxIO)
	out.IndexCost = indexPart + io + T*CPUTupleCost

	costPerPage := RandomPageCost
	if pages >= 2 {
		costPerPage = RandomPageCost - (RandomPageCost-SeqPageCost)*math.Sqrt(pages/P)
	}
	out.BitmapCost = indexPart + pages*costPerPage + T*(CPUTupleCost+CPUOperatorCost)

	if out.IndexCost < out.Cost {
		out.Path, out.Cost = PathIndexScan, out.IndexCost
	}
	if out.BitmapCost < out.Cost {
		out.Path, out.Cost = PathBitmapScan, out.BitmapCost
	}
	return out
}
