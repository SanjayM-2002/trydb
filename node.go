package trydb

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
)

// node is one trie node.
//
// In memory it is a plain struct. On disk, children are not pointers but
// file offsets: the byte position where the child node starts.
type node struct {
	prefix   []byte // the bytes this node consumes from the key
	hasValue bool   // a key ends here (needed because an empty value is valid)
	value    []byte
	children []child // sorted by edge, so scans come out in key order
}

type child struct {
	edge   byte   // first byte of the child's prefix, so we can pick a child without reading it
	offset uint64 // where the child node starts in the file
}

// Every child has a non-empty prefix (its first byte is the edge).
// Only the root may have an empty prefix.

func (n *node) findChild(edge byte) int {
	for i, c := range n.children {
		if c.edge == edge {
			return i
		}
	}
	return -1
}

// setChild points edge at offset, adding the child if it does not exist.
func (n *node) setChild(edge byte, offset uint64) {
	if i := n.findChild(edge); i >= 0 {
		n.children[i].offset = offset
		return
	}
	i := 0
	for i < len(n.children) && n.children[i].edge < edge {
		i++
	}
	n.children = append(n.children, child{})
	copy(n.children[i+1:], n.children[i:])
	n.children[i] = child{edge: edge, offset: offset}
}

func (n *node) removeChild(edge byte) {
	if i := n.findChild(edge); i >= 0 {
		n.children = append(n.children[:i], n.children[i+1:]...)
	}
}

// ---- encoding ----
//
// A node record on disk:
//
//	len u32 | crc u32 | body
//
//	body = prefixLen u16 | prefix
//	     | hasValue u8 | valueLen u32 | value
//	     | numChildren u16 | (edge u8, offset u64) * numChildren
//
// All integers are little-endian.

const nodeHeaderSize = 8 // len + crc

var errCorrupt = errors.New("trydb: corrupt node")

func encodeNode(n *node) []byte {
	var body []byte
	body = binary.LittleEndian.AppendUint16(body, uint16(len(n.prefix)))
	body = append(body, n.prefix...)
	if n.hasValue {
		body = append(body, 1)
	} else {
		body = append(body, 0)
	}
	body = binary.LittleEndian.AppendUint32(body, uint32(len(n.value)))
	body = append(body, n.value...)
	body = binary.LittleEndian.AppendUint16(body, uint16(len(n.children)))
	for _, c := range n.children {
		body = append(body, c.edge)
		body = binary.LittleEndian.AppendUint64(body, c.offset)
	}

	out := binary.LittleEndian.AppendUint32(nil, uint32(len(body)))
	out = binary.LittleEndian.AppendUint32(out, crc32.ChecksumIEEE(body))
	return append(out, body...)
}

// decodeBody turns a node body (without the len/crc header) back into a node.
// It copies bytes out of buf so the node never aliases the read buffer.
func decodeBody(buf []byte) (*node, error) {
	r := reader{buf: buf}
	n := &node{}
	n.prefix = r.bytes(int(r.u16()))
	n.hasValue = r.u8() == 1
	n.value = r.bytes(int(r.u32()))
	count := int(r.u16())
	for i := 0; i < count; i++ {
		n.children = append(n.children, child{edge: r.u8(), offset: r.u64()})
	}
	if r.err {
		return nil, errCorrupt
	}
	return n, nil
}

// reader walks a byte slice field by field. If it runs out of bytes it
// sets err and returns zeros, so decodeBody only has to check once.
type reader struct {
	buf []byte
	err bool
}

func (r *reader) take(k int) []byte {
	if r.err || len(r.buf) < k {
		r.err = true
		return make([]byte, min(k, 8)) // enough zeros for any integer field
	}
	b := r.buf[:k]
	r.buf = r.buf[k:]
	return b
}

func (r *reader) u8() byte    { return r.take(1)[0] }
func (r *reader) u16() uint16 { return binary.LittleEndian.Uint16(r.take(2)) }
func (r *reader) u32() uint32 { return binary.LittleEndian.Uint32(r.take(4)) }
func (r *reader) u64() uint64 { return binary.LittleEndian.Uint64(r.take(8)) }
func (r *reader) bytes(k int) []byte {
	return append([]byte(nil), r.take(k)...)
}
