package page

// Problem 2.15 — Dump [Easy]
//
// Render the page header and every line pointer as a human-readable table, like pageinspect.
//
// When a page looks wrong the first thing a Postgres hacker does is
//
//	SELECT lp, lp_off, lp_flags, lp_len, t_data
//	FROM heap_page_items(get_raw_page('t', 0));
//
// Dump is our version of that: a fixed-format text report that tests and
// debugging sessions can compare byte-for-byte.
//
// Rules — the output is the concatenation of these lines, each ending in "\n":
//  1. Summary line: fmt.Sprintf("lower=%d upper=%d free=%d slots=%d\n",
//     Lower(), Upper(), FreeSpace(), NumSlots()) — FreeSpace from Problem 2.3.
//  2. Column header line, exactly: " lp | lp_off | lp_flags   | lp_len | t_data\n"
//  3. One line per slot 0..NumSlots()-1 in ascending order, ALL states included:
//     fmt.Sprintf("%3d | %6d | %d %-8s | %6d", slot, off, flags, flags.String(), len)
//     followed, for LPNormal slots only, by " | " and t_data, then "\n".
//     Non-normal slots have no t_data column at all (no trailing " | ").
//  4. t_data is the tuple bytes in lowercase hex (encoding/hex style). If the
//     tuple is longer than 8 bytes only the first 8 bytes are shown, followed by "..".
//  5. The page is not modified.
//
// Example (insert "aaaa", "bbbbbbbb", "cc", "ddd", "hello world!" → slots 0..4;
// DeleteTuple(3); Compact(); DeleteTuple(1)):
//
//	lower=28 upper=8166 free=8138 slots=5
//	 lp | lp_off | lp_flags   | lp_len | t_data
//	  0 |   8188 | 1 NORMAL   |      4 | 61616161
//	  1 |   8180 | 3 DEAD     |      8
//	  2 |   8178 | 1 NORMAL   |      2 | 6363
//	  3 |      0 | 0 UNUSED   |      0
//	  4 |   8166 | 1 NORMAL   |     12 | 68656c6c6f20776f..
//
// An empty page dumps as the summary "lower=8 upper=8192 free=8180 slots=0"
// plus the column header and nothing else.
//
// Constraints: O(PageSize).
// Postgres reference: heap_page_items() in contrib/pageinspect/heapfuncs.c
func (p *Page) Dump() string {
	panic("TODO: Problem 2.15")
}
