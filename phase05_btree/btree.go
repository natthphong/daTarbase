// Package btree is a B+Tree index in the style of Postgres "nbtree".
//
// Keys are []byte compared with bytes.Compare (memcomparable encodings,
// see EncodeInt / EncodeText), values are heap TIDs. Every entry is made
// unique by ordering on (Key, TID) — the Postgres 12+ "heapkeyspace" rule —
// so even millions of duplicates of one key have a single exact position.
//
// Nodes live in a NodeStore, an in-memory stand-in for the buffer manager
// that hands out COPIES of nodes and counts reads, writes, allocs and frees.
// A change to a node is only visible to the tree after you Put it back.
package btree

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/natthphong/daTarbase/pkg/types"
)

// Errors returned by the tree.
var (
	ErrDuplicateEntry = errors.New("btree: entry (key, tid) already exists")
	ErrDuplicateKey   = errors.New("btree: duplicate key value violates unique constraint")
	ErrNotFound       = errors.New("btree: entry not found")
	ErrNodeFull       = errors.New("btree: node is full")
	ErrUnsorted       = errors.New("btree: bulk-load input is not strictly sorted")
	ErrBadFillfactor  = errors.New("btree: fillfactor must be 0 (default) or in [50, 100]")
	ErrNodeTooLarge   = errors.New("btree: node does not fit in one page")
	ErrCorruptPage    = errors.New("btree: corrupt page")
	ErrInvalidTree    = errors.New("btree: invariant violated")
)

// MinTID and MaxTID bracket every real TID. Use them to build search probes:
// (key, MinTID) sorts before every entry with that key, (key, MaxTID) after.
// MaxTID never appears as the TID of a real entry.
var (
	MinTID = types.TID{Page: 0, Slot: 0}
	MaxTID = types.TID{Page: types.InvalidPageID, Slot: math.MaxUint16}
)

// DefaultFillfactor is the leaf fillfactor used by BulkLoad when 0 is passed
// (Postgres: BTREE_DEFAULT_FILLFACTOR = 90).
const DefaultFillfactor = 90

// Page layout constants used by EncodePage / DecodePage.
const (
	PageHeaderSize  = 16 // flags, nitems, lower, upper, prev, next
	LinePointerSize = 4  // offset uint16 + length uint16
)

// Item is one index tuple.
//
// In a leaf it is an index entry: Key + the heap TID it points to + an
// optional INCLUDE payload. In an internal node it is a separator ("pivot"):
// only Key and TID matter there and Payload is always nil.
type Item struct {
	Key     []byte    // index key, compared with bytes.Compare
	TID     types.TID // heap tuple pointer; tie-breaker for equal keys
	Payload []byte    // INCLUDE columns: leaf only, never compared
}

// Node is one B+Tree page.
//
//	internal: Children[0] Items[0] Children[1] Items[1] ... Items[k-1] Children[k]
//	          child i holds entries x with Items[i-1] <= x < Items[i]
//	leaf:     sorted Items, linked to its neighbours with Prev / Next
type Node struct {
	ID       types.PageID   // where the node lives in the store
	Leaf     bool           // true for leaf nodes
	Items    []Item         // leaf: entries; internal: separators (len == len(Children)-1)
	Children []types.PageID // internal only
	Prev     types.PageID   // leaf only: left sibling or InvalidPageID
	Next     types.PageID   // leaf only: right sibling or InvalidPageID
}

func (n *Node) clone() *Node {
	c := *n
	c.Items = append([]Item(nil), n.Items...)
	c.Children = append([]types.PageID(nil), n.Children...)
	return &c
}

// NodeStore is an in-memory page store with I/O counters.
// Get returns a copy; modifications must be written back with Put.
// IDs are handed out sequentially from 0 and never reused.
type NodeStore struct {
	nodes  map[types.PageID]*Node
	nextID types.PageID

	Reads  int // Get calls that found a node
	Writes int // Put calls
	Allocs int // Alloc calls
	Frees  int // Free calls
}

// NewNodeStore returns an empty store.
func NewNodeStore() *NodeStore { return &NodeStore{nodes: map[types.PageID]*Node{}} }

// Alloc creates and stores a new empty node with the next free ID and returns
// a copy of it. Prev and Next are InvalidPageID. Counts as one alloc, no write.
func (s *NodeStore) Alloc(leaf bool) *Node {
	n := &Node{ID: s.nextID, Leaf: leaf, Prev: types.InvalidPageID, Next: types.InvalidPageID}
	s.nextID++
	s.nodes[n.ID] = n
	s.Allocs++
	return n.clone()
}

// Get returns a copy of node id (one read), or nil if no such node exists.
func (s *NodeStore) Get(id types.PageID) *Node {
	n, ok := s.nodes[id]
	if !ok {
		return nil
	}
	s.Reads++
	return n.clone()
}

// Put stores a copy of n under n.ID (one write). It panics if n.ID was never
// allocated or has been freed.
func (s *NodeStore) Put(n *Node) {
	if _, ok := s.nodes[n.ID]; !ok {
		panic("btree: Put of unallocated node")
	}
	s.Writes++
	s.nodes[n.ID] = n.clone()
}

// Free deletes node id. It panics if the node does not exist.
func (s *NodeStore) Free(id types.PageID) {
	if _, ok := s.nodes[id]; !ok {
		panic("btree: Free of unallocated node")
	}
	s.Frees++
	delete(s.nodes, id)
}

// Peek is Get without counting a read (for tests and debugging).
func (s *NodeStore) Peek(id types.PageID) *Node {
	n, ok := s.nodes[id]
	if !ok {
		return nil
	}
	return n.clone()
}

// Len is the number of live nodes.
func (s *NodeStore) Len() int { return len(s.nodes) }

// ResetCounters zeroes Reads, Writes, Allocs and Frees.
func (s *NodeStore) ResetCounters() { s.Reads, s.Writes, s.Allocs, s.Frees = 0, 0, 0, 0 }

// Tree is a B+Tree index.
type Tree struct {
	Store    *NodeStore   // where the nodes live
	Root     types.PageID // root node
	Height   int          // number of levels; 1 means the root is a leaf
	MaxItems int          // max items per node (leaf entries or internal separators), >= 3
}

// New creates an empty tree: one empty root leaf (1 alloc, no writes).
// It panics if maxItems < 3.
func New(store *NodeStore, maxItems int) *Tree {
	if maxItems < 3 {
		panic("btree: maxItems must be >= 3")
	}
	root := store.Alloc(true)
	return &Tree{Store: store, Root: root.ID, Height: 1, MaxItems: maxItems}
}

// MinItems is the minimum number of items in every non-root node: MaxItems/2
// (rounded down).
func (t *Tree) MinItems() int { return t.MaxItems / 2 }

// Step is one level of a root-to-leaf descent (Postgres: BTStack entry).
type Step struct {
	Node *Node // the node read at this level
	Pos  int   // internal: index of the child followed; leaf: position of the probe
}

// Bound is one end of a range scan.
type Bound struct {
	Key       []byte // the bound key (ignored when Unbounded)
	Inclusive bool   // true: key itself is in range ( <= / >= ); false: < / >
	Unbounded bool   // true: no bound on this side (-inf / +inf)
}

// Incl is an inclusive bound on key.
func Incl(key []byte) Bound { return Bound{Key: key, Inclusive: true} }

// Excl is an exclusive bound on key.
func Excl(key []byte) Bound { return Bound{Key: key} }

// NoBound is an open end (-inf for lo, +inf for hi).
func NoBound() Bound { return Bound{Unbounded: true} }

// Cursor walks the entries of a range scan, one Next call at a time
// (Postgres: an index scan's BTScanOpaque). It is positioned lazily by the
// first Next call.
type Cursor struct {
	tree    *Tree
	lo, hi  Bound
	desc    bool  // false: ascending (lo -> hi), true: descending (hi -> lo)
	started bool  // the first Next call has positioned the cursor
	done    bool  // no more entries; Next keeps returning false
	leaf    *Node // current leaf
	pos     int   // position of the next entry to look at in leaf.Items
}

// Scan returns a cursor over the entries whose KEY lies between lo and hi.
// desc=true walks from hi down to lo (ORDER BY key DESC). No node is read
// until the first Next call.
func (t *Tree) Scan(lo, hi Bound, desc bool) *Cursor {
	return &Cursor{tree: t, lo: lo, hi: hi, desc: desc}
}

// ---------------------------------------------------------------- key encoding

// EncodeInt encodes a signed integer so that bytes.Compare orders encodings
// like the numbers: 8 bytes big-endian with the sign bit flipped.
// EncodeInt(-1) = 7f ff ff ff ff ff ff ff, EncodeInt(7) = 80 00 00 00 00 00 00 07.
func EncodeInt(v int64) []byte {
	return binary.BigEndian.AppendUint64(nil, uint64(v)^(1<<63))
}

// EncodeText encodes a string so that bytes.Compare orders encodings like the
// strings AND a shorter string sorts before any longer string sharing its
// prefix, even when another column follows: every 0x00 byte becomes 00 ff
// and the value ends with the terminator 00 01.
// EncodeText("ab") = 61 62 00 01.
func EncodeText(s string) []byte {
	out := make([]byte, 0, len(s)+2)
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			out = append(out, 0x00, 0xFF)
		} else {
			out = append(out, s[i])
		}
	}
	return append(out, 0x00, 0x01)
}

// Key concatenates encoded columns into one composite key.
func Key(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
