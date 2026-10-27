package trydb

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
)

// File layout:
//
//	offset 0         32        64
//	       | meta A  | meta B  | node | node | node | ... (append-only)
//
// The two meta slots are the only bytes ever overwritten. Commits alternate
// between them, so a torn meta write always leaves the other slot intact.
const (
	metaSize  = 32
	dataStart = 2 * metaSize
	magic     = "TRIE"
)

// meta says where the current trie lives.
//
//	magic [4] | txid u64 | root u64 | end u64 | crc u32
type meta struct {
	txid uint64 // commit counter; the higher valid slot wins
	root uint64 // offset of the root node, 0 means the trie is empty
	end  uint64 // end of committed data; new nodes are appended here
}

func encodeMeta(m meta) []byte {
	buf := []byte(magic)
	buf = binary.LittleEndian.AppendUint64(buf, m.txid)
	buf = binary.LittleEndian.AppendUint64(buf, m.root)
	buf = binary.LittleEndian.AppendUint64(buf, m.end)
	return binary.LittleEndian.AppendUint32(buf, crc32.ChecksumIEEE(buf))
}

func decodeMeta(buf []byte) (meta, bool) {
	if string(buf[:4]) != magic {
		return meta{}, false
	}
	if crc32.ChecksumIEEE(buf[:28]) != binary.LittleEndian.Uint32(buf[28:32]) {
		return meta{}, false
	}
	return meta{
		txid: binary.LittleEndian.Uint64(buf[4:12]),
		root: binary.LittleEndian.Uint64(buf[12:20]),
		end:  binary.LittleEndian.Uint64(buf[20:28]),
	}, true
}

// DB is a single-file, copy-on-write trie. It is not safe for concurrent use.
type DB struct {
	f    *os.File
	meta meta

	// pending holds nodes written by the operation in progress. They are
	// appended to the file only on commit. Their offsets are already final:
	// meta.end + their position in pending.
	pending []byte
}

func Open(path string) (*DB, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	db := &DB{f: f}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}

	if info.Size() == 0 {
		// Brand new file: write an empty trie into both slots.
		db.meta = meta{end: dataStart}
		for slot := 0; slot < 2; slot++ {
			if _, err := f.WriteAt(encodeMeta(db.meta), int64(slot*metaSize)); err != nil {
				f.Close()
				return nil, err
			}
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, err
		}
		return db, nil
	}

	// Existing file: use the valid slot with the highest txid.
	found := false
	for slot := 0; slot < 2; slot++ {
		buf := make([]byte, metaSize)
		if _, err := f.ReadAt(buf, int64(slot*metaSize)); err != nil {
			continue
		}
		if m, ok := decodeMeta(buf); ok && (!found || m.txid > db.meta.txid) {
			db.meta = m
			found = true
		}
	}
	if !found {
		f.Close()
		return nil, errors.New("trydb: no valid meta slot")
	}
	return db, nil
}

func (db *DB) Close() error {
	return db.f.Close()
}

// writeNode stages n and returns the offset it will live at once committed.
func (db *DB) writeNode(n *node) uint64 {
	off := db.meta.end + uint64(len(db.pending))
	db.pending = append(db.pending, encodeNode(n)...)
	return off
}

// readNode loads the node at off, from the file or from pending.
func (db *DB) readNode(off uint64) (*node, error) {
	if off >= db.meta.end {
		buf := db.pending[off-db.meta.end:]
		size := binary.LittleEndian.Uint32(buf[0:4])
		return checkAndDecode(buf[4:8], buf[nodeHeaderSize:nodeHeaderSize+size])
	}

	header := make([]byte, nodeHeaderSize)
	if _, err := db.f.ReadAt(header, int64(off)); err != nil {
		return nil, err
	}
	body := make([]byte, binary.LittleEndian.Uint32(header[0:4]))
	if _, err := db.f.ReadAt(body, int64(off)+nodeHeaderSize); err != nil {
		return nil, err
	}
	return checkAndDecode(header[4:8], body)
}

func checkAndDecode(crc, body []byte) (*node, error) {
	if crc32.ChecksumIEEE(body) != binary.LittleEndian.Uint32(crc) {
		return nil, errCorrupt
	}
	return decodeBody(body)
}

// commit makes root the new current trie. The order is what makes it crash safe:
//
//  1. append the new nodes, fsync   -> nodes are durable but unreachable
//  2. write the new meta, fsync     -> now they are reachable
//
// A crash before step 2 finishes leaves the old meta pointing at the old root.
func (db *DB) commit(root uint64) error {
	defer func() { db.pending = nil }()

	if _, err := db.f.WriteAt(db.pending, int64(db.meta.end)); err != nil {
		return err
	}
	if err := db.f.Sync(); err != nil {
		return err
	}

	next := meta{
		txid: db.meta.txid + 1,
		root: root,
		end:  db.meta.end + uint64(len(db.pending)),
	}
	slot := int64(next.txid % 2) // alternate slots: never overwrite the current one
	if _, err := db.f.WriteAt(encodeMeta(next), slot*metaSize); err != nil {
		return err
	}
	if err := db.f.Sync(); err != nil {
		return err
	}

	db.meta = next
	return nil
}
