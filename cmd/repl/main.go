// Command repl is a tiny interactive shell over a trydb file.
//
//	go run ./cmd/repl data.trydb
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/SanjayM-2002/trydb"
)

const help = `commands:
  put <key> <value>   insert or overwrite (value may contain spaces)
  get <key>
  del <key>
  scan [prefix]       all keys starting with prefix, sorted
  tree                print the trie with file offsets
  stats               commit count, root offset, file size
  exit`

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: repl <file>")
		os.Exit(1)
	}
	db, err := trydb.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close()

	fmt.Println("trydb: type 'help' for commands")
	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !in.Scan() {
			return
		}
		parts := strings.SplitN(strings.TrimSpace(in.Text()), " ", 3)
		if parts[0] == "exit" || parts[0] == "quit" {
			return
		}
		if err := run(db, parts); err != nil {
			fmt.Println("error:", err)
		}
	}
}

func run(db *trydb.DB, args []string) error {
	switch {
	case args[0] == "":
		return nil

	case args[0] == "put" && len(args) == 3:
		if err := db.Put([]byte(args[1]), []byte(args[2])); err != nil {
			return err
		}
		fmt.Println("ok")

	case args[0] == "get" && len(args) == 2:
		v, ok, err := db.Get([]byte(args[1]))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("(not found)")
		} else {
			fmt.Println(string(v))
		}

	case args[0] == "del" && len(args) == 2:
		ok, err := db.Delete([]byte(args[1]))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("(not found)")
		} else {
			fmt.Println("ok")
		}

	case args[0] == "scan" && len(args) <= 2:
		prefix := ""
		if len(args) == 2 {
			prefix = args[1]
		}
		pairs, err := db.Scan([]byte(prefix))
		if err != nil {
			return err
		}
		for _, p := range pairs {
			fmt.Printf("%s = %s\n", p.Key, p.Value)
		}
		fmt.Printf("(%d keys)\n", len(pairs))

	case args[0] == "tree":
		return db.Tree(os.Stdout)

	case args[0] == "stats":
		s := db.Stats()
		fmt.Printf("commits: %d\nroot:    @%d\nfile:    %d bytes\n", s.TxID, s.Root, s.FileSize)

	default:
		fmt.Println(help)
	}
	return nil
}
