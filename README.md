# trydb

A tiny trie-based key-value database, with a single file, copy-on-write, and no write-ahead log.

```bash
go run ./cmd/repl data.trydb
```

```
> put cat 1
> put car 2
> tree
"ca"                                     @123
  "r" = "2"                              @104
  "t" = "1"                              @85
> put ca 3
> tree
"ca" = "3"                               @160   <- only the root was rewritten
  "r" = "2"                              @104   <- shared with the old trie
  "t" = "1"                              @85
> stats
```

Inspect the raw bytes with `xxd data.trydb | head`.

Or use it as a library:

```go
import "github.com/SanjayM-2002/trydb"

db, err := trydb.Open("data.trydb")
defer db.Close()
db.Put([]byte("user/1"), []byte("alice"))
v, ok, err := db.Get([]byte("user/1"))
pairs, err := db.Scan([]byte("user/"))
```

## Operations

| Go API | REPL | What it does |
|---|---|---|
| `Open(path)` | (startup) | Open or create the file |
| `Put(key, value)` | `put k v` | Insert or overwrite; one commit |
| `Get(key)` | `get k` | Look up a key |
| `Delete(key)` | `del k` | Remove a key; one commit |
| `Scan(prefix)` | `scan p` | All keys starting with `p`, sorted |
| `Tree(w)` | `tree` | Print the trie with each node's file offset |
| `Stats()` | `stats` | Commit count, root offset, file size |

## Code map

| File | Responsibility |
|---|---|
| `node.go` | Node struct, encode/decode to bytes |
| `db.go` | The file: meta slots, reading/writing nodes, commit |
| `trie.go` | Trie algorithms: get, insert, remove, scan |
| `cmd/repl` | Interactive shell (the engine never parses input or prints) |

## How it works

**Copy-on-write.** Nodes on disk are never changed. A write copies the path
from the root down to the changed key and appends those copies to the end of
the file. Untouched subtrees are shared between the old and new trie.

**Commit = flip a pointer.** After appending new nodes, the DB writes a small
meta record pointing at the new root:

1. append nodes, `fsync`
2. write meta (txid+1, new root), `fsync`

If the process dies before step 2, the old meta still points at the old root,
so the DB opens in its previous state. No write-ahead log needed.

## File format

One custom binary file. All integers little-endian.

```
offset 0        32       64
       | meta A | meta B | node | node | node | ... (append-only)
```

**Meta** (32 bytes, two slots, commits alternate between them):

```
magic "TRIE" [4] | txid u64 | root u64 | end u64 | crc32 u32
```

On open, both slots are read; the valid one (magic + crc) with the higher txid
wins. `root = 0` means empty.

**Node** (variable size):

```
len u32 | crc32 u32 | body
body = prefixLen u16 | prefix
     | hasValue u8 | valueLen u32 | value
     | numChildren u16 | (edge u8, offset u64) * numChildren
```

Children point at other nodes by **file offset**, the on-disk version of a pointer.

