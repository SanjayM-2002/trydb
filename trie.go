package trydb

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

// Get walks from the root, consuming each node's prefix from the key.
func (db *DB) Get(key []byte) ([]byte, bool, error) {
	off := db.meta.root
	for off != 0 {
		n, err := db.readNode(off)
		if err != nil {
			return nil, false, err
		}
		if !bytes.HasPrefix(key, n.prefix) {
			return nil, false, nil
		}
		key = key[len(n.prefix):]
		if len(key) == 0 {
			return n.value, n.hasValue, nil
		}
		i := n.findChild(key[0])
		if i < 0 {
			return nil, false, nil
		}
		off = n.children[i].offset
	}
	return nil, false, nil
}

// Put inserts or overwrites key. It is one commit.
func (db *DB) Put(key, value []byte) error {
	root, err := db.insert(db.meta.root, key, value)
	if err != nil {
		db.pending = nil
		return err
	}
	return db.commit(root)
}

// insert returns the offset of a new copy of the subtree at off that also
// contains key. Nothing is modified in place: every node on the path from the
// root to the key is rewritten, and every other subtree is shared as is.
func (db *DB) insert(off uint64, key, value []byte) (uint64, error) {
	if off == 0 {
		return db.writeNode(&node{prefix: key, hasValue: true, value: value}), nil
	}
	n, err := db.readNode(off)
	if err != nil {
		return 0, err
	}

	common := commonPrefixLen(n.prefix, key)

	// Case 1: the key diverges inside this node's prefix, so split it.
	//
	//	"cat" + put "car"  ->  "ca" ─┬─ "r" (new)
	//	                             └─ "t" (old node, prefix shortened)
	if common < len(n.prefix) {
		parent := &node{prefix: key[:common]}

		n.prefix = n.prefix[common:]
		parent.setChild(n.prefix[0], db.writeNode(n))

		if common == len(key) {
			parent.hasValue, parent.value = true, value // key ends at the split point
		} else {
			rest := key[common:]
			parent.setChild(rest[0], db.writeNode(&node{prefix: rest, hasValue: true, value: value}))
		}
		return db.writeNode(parent), nil
	}

	// Case 2: this node's whole prefix matched.
	rest := key[common:]
	if len(rest) == 0 {
		n.hasValue, n.value = true, value // key ends here: set or overwrite
		return db.writeNode(n), nil
	}

	// Case 3: descend into the child for the next byte (or create it).
	childOff := uint64(0)
	if i := n.findChild(rest[0]); i >= 0 {
		childOff = n.children[i].offset
	}
	newChild, err := db.insert(childOff, rest, value)
	if err != nil {
		return 0, err
	}
	n.setChild(rest[0], newChild)
	return db.writeNode(n), nil
}

// Delete removes key and reports whether it existed. It is one commit.
func (db *DB) Delete(key []byte) (bool, error) {
	root, found, err := db.remove(db.meta.root, key)
	if err != nil || !found {
		db.pending = nil
		return false, err
	}
	return true, db.commit(root)
}

// remove returns the offset of a new copy of the subtree at off without key.
// An offset of 0 means the subtree became empty.
func (db *DB) remove(off uint64, key []byte) (uint64, bool, error) {
	if off == 0 {
		return 0, false, nil
	}
	n, err := db.readNode(off)
	if err != nil {
		return 0, false, err
	}
	if !bytes.HasPrefix(key, n.prefix) {
		return off, false, nil
	}

	rest := key[len(n.prefix):]
	if len(rest) == 0 {
		if !n.hasValue {
			return off, false, nil
		}
		n.hasValue, n.value = false, nil
	} else {
		i := n.findChild(rest[0])
		if i < 0 {
			return off, false, nil
		}
		newChild, found, err := db.remove(n.children[i].offset, rest)
		if err != nil || !found {
			return off, found, err
		}
		if newChild == 0 {
			n.removeChild(rest[0])
		} else {
			n.children[i].offset = newChild
		}
	}

	// Clean up so the trie stays compact.
	switch {
	case !n.hasValue && len(n.children) == 0:
		// Nothing left here at all.
		return 0, true, nil

	case !n.hasValue && len(n.children) == 1:
		// A node with no value and one child is pointless: merge them.
		//	"ca" ── "r"  ->  "car"
		only, err := db.readNode(n.children[0].offset)
		if err != nil {
			return 0, false, err
		}
		only.prefix = append(n.prefix, only.prefix...)
		return db.writeNode(only), true, nil
	}
	return db.writeNode(n), true, nil
}

type Pair struct {
	Key, Value []byte
}

// Scan returns every pair whose key starts with prefix, in sorted key order.
func (db *DB) Scan(prefix []byte) ([]Pair, error) {
	var out []Pair
	if db.meta.root == 0 {
		return out, nil
	}
	err := db.scan(db.meta.root, nil, prefix, &out)
	return out, err
}

// scan visits the subtree at off. path is the key spelled by all ancestors.
func (db *DB) scan(off uint64, path, prefix []byte, out *[]Pair) error {
	n, err := db.readNode(off)
	if err != nil {
		return err
	}
	path = append(path[:len(path):len(path)], n.prefix...) // copy, siblings share path

	// Only go down here if path and prefix agree so far: either we are still
	// spelling out the prefix, or we are already inside it.
	if !bytes.HasPrefix(path, prefix) && !bytes.HasPrefix(prefix, path) {
		return nil
	}
	if n.hasValue && bytes.HasPrefix(path, prefix) {
		*out = append(*out, Pair{Key: path, Value: n.value})
	}
	for _, c := range n.children {
		if err := db.scan(c.offset, path, prefix, out); err != nil {
			return err
		}
	}
	return nil
}

// ---- learning helpers ----

type Stats struct {
	TxID     uint64 // number of commits so far
	Root     uint64 // offset of the root node
	FileSize uint64 // grows on every write: old nodes are never reused
}

func (db *DB) Stats() Stats {
	return Stats{TxID: db.meta.txid, Root: db.meta.root, FileSize: db.meta.end}
}

// Tree prints the trie, one node per line, with each node's file offset.
func (db *DB) Tree(w io.Writer) error {
	if db.meta.root == 0 {
		fmt.Fprintln(w, "(empty)")
		return nil
	}
	return db.tree(w, db.meta.root, 0)
}

func (db *DB) tree(w io.Writer, off uint64, depth int) error {
	n, err := db.readNode(off)
	if err != nil {
		return err
	}
	line := fmt.Sprintf("%s%q", strings.Repeat("  ", depth), n.prefix)
	if n.hasValue {
		line += fmt.Sprintf(" = %q", n.value)
	}
	fmt.Fprintf(w, "%-40s @%d\n", line, off)
	for _, c := range n.children {
		if err := db.tree(w, c.offset, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func commonPrefixLen(a, b []byte) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}
