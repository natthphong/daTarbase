package join

import (
	"errors"
	"testing"

	"github.com/natthphong/daTarbase/pkg/exec"
	"github.com/natthphong/daTarbase/pkg/testkit"
	"github.com/natthphong/daTarbase/pkg/types"
)

// runHash runs a hash join (left = probe, right = build) and checks each
// input was opened at most once and every Open was closed.
func runHash(t *testing.T, fn iterJoin, spec JoinSpec, left, right []types.Row) ([]types.Row, *exec.ValuesIter, *exec.ValuesIter) {
	t.Helper()
	got, l, r := runJoin(t, fn, spec, left, right)
	if r.Opens != 1 {
		t.Fatalf("build side opened %d times, want 1", r.Opens)
	}
	if l.Opens > 1 {
		t.Fatalf("probe side opened %d times, want at most 1", l.Opens)
	}
	if r.Pulled != len(right) || l.Pulled != l.Opens*len(left) {
		t.Fatalf("pulled probe=%d build=%d, want each input read once (%d, %d)", l.Pulled, r.Pulled, l.Opens*len(left), len(right))
	}
	return got, l, r
}

// ---------------------------------------------------------------- 8.7

func TestP8_7_HashJoinInner(t *testing.T) {
	tests := []struct {
		name        string
		spec        JoinSpec
		left, right []types.Row
		want        []types.Row
		probeOpens  int
	}{
		{"employees ⨝ departments", empDept(Inner), employees(), departments(), innerRowsWant(), 1},
		{"extra is applied after the hash match", withExtra(empDept(Inner), notEmea), employees(), departments(), testkit.Rows(
			[]any{1, "alice", 10, 10, "eng"},
			[]any{2, "bob", 20, 20, "sales"},
			[]any{3, "carol", 10, 10, "eng"},
			[]any{6, "frank", 30, 30, "ops"},
			[]any{7, "grace", 20, 20, "sales"},
		), 1},
		{"empty build side skips the probe", empDept(Inner), employees(), nil, nil, 0},
		{"only NULL keys on the build side skips the probe", empDept(Inner), employees(), departments()[5:], nil, 0},
		{"empty probe side", empDept(Inner), nil, departments(), nil, 1},
		{"int8 probe finds float8 build key", empDept(Inner), employees()[:1], testkit.Rows([]any{10.0, "eng"}),
			testkit.Rows([]any{1, "alice", 10, 10.0, "eng"}), 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, l, _ := runHash(t, HashJoinInner, tc.spec, tc.left, tc.right)
			testkit.AssertRows(t, got, tc.want)
			if l.Opens != tc.probeOpens {
				t.Fatalf("probe opened %d times, want %d", l.Opens, tc.probeOpens)
			}
		})
	}
}

func TestP8_7_HashJoinInner_MultiKey(t *testing.T) {
	// orders (id, cust, region) ⨝ customers (region, cust, name) ON cust = cust AND region = region
	orders := testkit.Rows(
		[]any{1, 7, "eu"},
		[]any{2, 7, "us"},
		[]any{3, 8, "eu"},
		[]any{4, 7, nil},
	)
	customers := testkit.Rows(
		[]any{"eu", 7, "anna"},
		[]any{"us", 7, "ben"},
		[]any{"us", 8, "cara"},
		[]any{nil, 7, "dan"},
	)
	spec := JoinSpec{Type: Inner, Keys: []KeyPair{{Left: 1, Right: 1}, {Left: 2, Right: 0}}, LeftWidth: 3, RightWidth: 3}
	got, _, _ := runHash(t, HashJoinInner, spec, orders, customers)
	testkit.AssertRows(t, got, testkit.Rows(
		[]any{1, 7, "eu", "eu", 7, "anna"},
		[]any{2, 7, "us", "us", 7, "ben"},
	))
}

func TestP8_7_HashJoinInner_Errors(t *testing.T) {
	checkUnsupported(t, HashJoinInner, Left, Right, Full, Semi, Anti)
	checkErrors(t, HashJoinInner, Inner)
}

func TestP8_7_HashJoinInner_Random1000x1000(t *testing.T) {
	left, right := randData(1)
	for _, extra := range []bool{false, true} {
		got, _, _ := runHash(t, HashJoinInner, randSpec(Inner, extra), left, right)
		testkit.AssertRows(t, got, randOracle(Inner, extra))
	}
}

// ---------------------------------------------------------------- 8.8

func TestP8_8_HashJoinOuter(t *testing.T) {
	tests := []struct {
		name        string
		spec        JoinSpec
		left, right []types.Row
		want        []types.Row
		probeOpens  int
	}{
		{"LEFT", empDept(Left), employees(), departments(), leftRowsWant(), 1},
		{"RIGHT keeps the NULL-key department", empDept(Right), employees(), departments(), rightRowsWant(), 1},
		{"FULL", empDept(Full), employees(), departments(), fullRowsWant(), 1},
		{"FULL with extra e.id <> 6", withExtra(empDept(Full), notFrank), employees(), departments(), fullNotFrankWant(), 1},
		{"RIGHT with empty build skips the probe", empDept(Right), employees(), nil, nil, 0},
		{"LEFT with empty build pads every probe row", empDept(Left), employees()[3:5], nil, testkit.Rows(
			[]any{4, "dave", nil, nil, nil},
			[]any{5, "erin", 40, nil, nil},
		), 1},
		{"FULL with empty build", empDept(Full), employees()[:1], nil, testkit.Rows([]any{1, "alice", 10, nil, nil}), 1},
		{"RIGHT with only a NULL-key build row", empDept(Right), employees(), departments()[5:],
			testkit.Rows([]any{nil, nil, nil, nil, "unassigned"}), 1},
		{"FULL with empty probe", empDept(Full), nil, departments()[3:], testkit.Rows(
			[]any{nil, nil, nil, 30, "ops"},
			[]any{nil, nil, nil, 50, "legal"},
			[]any{nil, nil, nil, nil, "unassigned"},
		), 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, l, _ := runHash(t, HashJoinOuter, tc.spec, tc.left, tc.right)
			testkit.AssertRows(t, got, tc.want)
			if l.Opens != tc.probeOpens {
				t.Fatalf("probe opened %d times, want %d", l.Opens, tc.probeOpens)
			}
		})
	}
}

// An unmatched build row stays unmatched even when its key appears on the
// probe side, if Extra rejects every pair.
func TestP8_8_HashJoinOuter_MatchedFlagNeedsExtra(t *testing.T) {
	spec := withExtra(empDept(Right), notEmea)
	got, _, _ := runHash(t, HashJoinOuter, spec, employees(), departments())
	testkit.AssertRows(t, got, testkit.Rows(
		[]any{1, "alice", 10, 10, "eng"},
		[]any{2, "bob", 20, 20, "sales"},
		[]any{3, "carol", 10, 10, "eng"},
		[]any{6, "frank", 30, 30, "ops"},
		[]any{7, "grace", 20, 20, "sales"},
		[]any{nil, nil, nil, 20, "sales-emea"},
		[]any{nil, nil, nil, 50, "legal"},
		[]any{nil, nil, nil, nil, "unassigned"},
	))
}

func TestP8_8_HashJoinOuter_Errors(t *testing.T) {
	checkUnsupported(t, HashJoinOuter, Inner, Semi, Anti)
	for _, jt := range []JoinType{Left, Right, Full} {
		checkErrors(t, HashJoinOuter, jt)
	}
}

func TestP8_8_HashJoinOuter_Random1000x1000(t *testing.T) {
	left, right := randData(1)
	for _, jt := range []JoinType{Left, Right, Full} {
		for _, extra := range []bool{false, true} {
			got, _, _ := runHash(t, HashJoinOuter, randSpec(jt, extra), left, right)
			testkit.AssertRows(t, got, randOracle(jt, extra))
		}
	}
}

// ---------------------------------------------------------------- 8.9

func TestP8_9_GraceHashJoin(t *testing.T) {
	cfg := GraceConfig{MemRows: 10, Fanout: 4, MaxDepth: 3}
	got, st, err := GraceHashJoin(employees(), departments(), empDept(Inner), cfg)
	if err != nil {
		t.Fatal(err)
	}
	testkit.AssertRowsUnordered(t, got, innerRowsWant())
	if st.Partitionings != 0 || st.Overflows != 0 || st.Deepest != 0 || len(st.BuildSizes) != 1 || st.BuildSizes[0] != 5 {
		t.Fatalf("stats = %+v, want no partitioning and BuildSizes [5] (NULL key dropped)", st)
	}
}

func TestP8_9_GraceHashJoin_Partitioned(t *testing.T) {
	for _, extra := range []bool{false, true} {
		spec := empDept(Inner)
		want := innerRowsWant()
		if extra {
			spec = withExtra(spec, notEmea)
			want = oracleJoin(spec, employees(), departments())
		}
		cfg := GraceConfig{MemRows: 1, Fanout: 2, MaxDepth: 8}
		got, st, err := GraceHashJoin(employees(), departments(), spec, cfg)
		if err != nil {
			t.Fatal(err)
		}
		testkit.AssertRowsUnordered(t, got, want)
		if st.Partitionings < 1 {
			t.Fatalf("Partitionings = %d, want >= 1 (5 build rows > MemRows 1)", st.Partitionings)
		}
		// Key 20 has two build rows and can never be split: it must overflow at MaxDepth.
		if st.Overflows != 1 || st.Deepest != 8 {
			t.Fatalf("Overflows = %d, Deepest = %d, want 1 and 8 (key 20 has 2 rows)", st.Overflows, st.Deepest)
		}
		sum := 0
		for _, n := range st.BuildSizes {
			if n > 2 {
				t.Fatalf("BuildSizes = %v, no in-memory join may exceed 2 rows here", st.BuildSizes)
			}
			sum += n
		}
		// keys 10, 20, 20, 30 have probe rows; 50 has none, so its partition may be skipped.
		if sum < 4 || sum > 5 {
			t.Fatalf("sum(BuildSizes) = %d, want 4 or 5", sum)
		}
	}
}

func TestP8_9_GraceHashJoin_Skew(t *testing.T) {
	var left, right []types.Row
	for i := range 30 {
		left = append(left, testkit.R(i, 7))
	}
	for i := range 12 {
		right = append(right, testkit.R(7, 100+i))
	}
	spec := JoinSpec{Type: Inner, Keys: []KeyPair{{Left: 1, Right: 0}}, LeftWidth: 2, RightWidth: 2}
	got, st, err := GraceHashJoin(left, right, spec, GraceConfig{MemRows: 5, Fanout: 4, MaxDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 360 {
		t.Fatalf("len(rows) = %d, want 30*12 = 360", len(got))
	}
	if st.Partitionings != 2 || st.Overflows != 1 || st.Deepest != 2 || len(st.BuildSizes) != 1 || st.BuildSizes[0] != 12 {
		t.Fatalf("stats = %+v, want Partitionings 2, Overflows 1, Deepest 2, BuildSizes [12]", st)
	}
}

func TestP8_9_GraceHashJoin_EmptySides(t *testing.T) {
	cfg := GraceConfig{MemRows: 1, Fanout: 2, MaxDepth: 3}
	for name, in := range map[string][2][]types.Row{
		"empty left":        {nil, departments()},
		"empty right":       {employees(), nil},
		"right only NULL":   {employees(), departments()[5:]},
		"left only NULL":    {employees()[3:4], departments()},
		"keys never shared": {employees()[4:5], departments()},
	} {
		got, st, err := GraceHashJoin(in[0], in[1], empDept(Inner), cfg)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != 0 {
			t.Fatalf("%s: rows = %v, want none", name, got)
		}
		if name != "keys never shared" && (st.Partitionings != 0 || len(st.BuildSizes) != 0) {
			t.Fatalf("%s: stats = %+v, want nothing done", name, st)
		}
	}
}

func TestP8_9_GraceHashJoin_Errors(t *testing.T) {
	good := GraceConfig{MemRows: 4, Fanout: 2, MaxDepth: 2}
	if _, _, err := GraceHashJoin(employees(), departments(), empDept(Left), good); !errors.Is(err, ErrUnsupportedJoin) {
		t.Fatalf("LEFT: err = %v, want ErrUnsupportedJoin", err)
	}
	if _, _, err := GraceHashJoin(employees(), departments(), empDept(Left), GraceConfig{}); !errors.Is(err, ErrUnsupportedJoin) {
		t.Fatalf("LEFT + bad config: err = %v, want ErrUnsupportedJoin (type is checked first)", err)
	}
	for _, cfg := range []GraceConfig{
		{MemRows: 0, Fanout: 2, MaxDepth: 2},
		{MemRows: 4, Fanout: 1, MaxDepth: 2},
		{MemRows: 4, Fanout: 2, MaxDepth: -1},
	} {
		if _, _, err := GraceHashJoin(employees(), departments(), empDept(Inner), cfg); !errors.Is(err, ErrBadConfig) {
			t.Fatalf("%+v: err = %v, want ErrBadConfig", cfg, err)
		}
	}
	rows, st, err := GraceHashJoin(employees(), departments(), withExtra(empDept(Inner), failOnBob), good)
	if !errors.Is(err, errBoom) || rows != nil || st.Partitionings != 0 || st.BuildSizes != nil {
		t.Fatalf("extra error: rows=%v stats=%+v err=%v, want nil, zero stats, errBoom", rows, st, err)
	}
}

func TestP8_9_GraceHashJoin_Random1000x1000(t *testing.T) {
	left, right := randData(1)
	for _, extra := range []bool{false, true} {
		cfg := GraceConfig{MemRows: 50, Fanout: 4, MaxDepth: 4}
		got, st, err := GraceHashJoin(left, right, randSpec(Inner, extra), cfg)
		if err != nil {
			t.Fatal(err)
		}
		testkit.AssertRowsUnordered(t, got, randOracle(Inner, extra))
		if st.Partitionings < 1+4 {
			t.Fatalf("Partitionings = %d, want >= 5 (≈950 build rows / 4 partitions still exceed 50)", st.Partitionings)
		}
		sum := 0
		for _, n := range st.BuildSizes {
			if n > cfg.MemRows && st.Overflows == 0 {
				t.Fatalf("in-memory join with %d build rows > MemRows %d and no overflow", n, cfg.MemRows)
			}
			sum += n
		}
		nonNull := 0
		for _, r := range right {
			if !r[1].IsNull() {
				nonNull++
			}
		}
		if sum > nonNull {
			t.Fatalf("sum(BuildSizes) = %d > %d non-NULL build rows", sum, nonNull)
		}
	}
}
