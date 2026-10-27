package trydb

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func openTemp(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.trydb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return db, path
}

func mustGet(t *testing.T, db *DB, key string) (string, bool) {
	t.Helper()
	v, ok, err := db.Get([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	return string(v), ok
}

func TestPutGetDelete(t *testing.T) {
	db, _ := openTemp(t)
	defer db.Close()

	for _, kv := range [][2]string{{"cat", "1"}, {"car", "2"}, {"ca", "3"}, {"cart", "4"}, {"", "empty"}} {
		if err := db.Put([]byte(kv[0]), []byte(kv[1])); err != nil {
			t.Fatal(err)
		}
	}
	db.Put([]byte("cat"), []byte("one")) // overwrite

	for key, want := range map[string]string{"cat": "one", "car": "2", "ca": "3", "cart": "4", "": "empty"} {
		if got, ok := mustGet(t, db, key); !ok || got != want {
			t.Errorf("get %q = %q, %v; want %q", key, got, ok, want)
		}
	}
	if _, ok := mustGet(t, db, "c"); ok {
		t.Error(`"c" should not exist`)
	}

	if found, _ := db.Delete([]byte("ca")); !found {
		t.Error(`delete "ca" should find it`)
	}
	if found, _ := db.Delete([]byte("ca")); found {
		t.Error(`second delete "ca" should not find it`)
	}
	if _, ok := mustGet(t, db, "ca"); ok {
		t.Error(`"ca" should be gone`)
	}
	if got, _ := mustGet(t, db, "cart"); got != "4" {
		t.Errorf(`"cart" = %q after deleting "ca"`, got)
	}
}

func TestScan(t *testing.T) {
	db, _ := openTemp(t)
	defer db.Close()

	for _, k := range []string{"user/2", "user/10", "team/1", "user/1", "use"} {
		db.Put([]byte(k), []byte("v"))
	}
	pairs, err := db.Scan([]byte("user/"))
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, p := range pairs {
		keys = append(keys, string(p.Key))
	}
	want := []string{"user/1", "user/10", "user/2"} // byte order
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Errorf("scan = %v, want %v", keys, want)
	}
}

// Compare against a Go map through thousands of random operations,
// then reopen the file and compare again.
func TestRandomAgainstMap(t *testing.T) {
	db, path := openTemp(t)
	rng := rand.New(rand.NewSource(1))
	model := map[string]string{}

	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("k%d", rng.Intn(100)) // small key space -> lots of shared prefixes
		if rng.Intn(3) == 0 {
			found, err := db.Delete([]byte(key))
			if err != nil {
				t.Fatal(err)
			}
			if _, inModel := model[key]; found != inModel {
				t.Fatalf("delete %q found=%v, model has=%v", key, found, inModel)
			}
			delete(model, key)
		} else {
			val := fmt.Sprint(i)
			if err := db.Put([]byte(key), []byte(val)); err != nil {
				t.Fatal(err)
			}
			model[key] = val
		}
	}

	check := func(db *DB) {
		t.Helper()
		pairs, err := db.Scan(nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(pairs) != len(model) {
			t.Fatalf("scan has %d keys, model has %d", len(pairs), len(model))
		}
		if !sort.SliceIsSorted(pairs, func(i, j int) bool { return string(pairs[i].Key) < string(pairs[j].Key) }) {
			t.Fatal("scan is not sorted")
		}
		for _, p := range pairs {
			if model[string(p.Key)] != string(p.Value) {
				t.Fatalf("%q = %q, model has %q", p.Key, p.Value, model[string(p.Key)])
			}
		}
	}

	check(db)
	db.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	check(db)
}

// Simulate a crash during the last commit by corrupting the newest meta
// slot. The DB must open at the previous commit.
func TestCrashFallsBackToPreviousCommit(t *testing.T) {
	db, path := openTemp(t)
	db.Put([]byte("a"), []byte("1")) // txid 1
	db.Put([]byte("b"), []byte("2")) // txid 2, written to slot 0
	db.Close()

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteAt([]byte("garbage!"), 8) // tear meta slot 0
	f.Close()

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if got, ok := mustGet(t, db, "a"); !ok || got != "1" {
		t.Errorf(`"a" = %q, %v; want "1"`, got, ok)
	}
	if _, ok := mustGet(t, db, "b"); ok {
		t.Error(`"b" was in the torn commit and should not be visible`)
	}
	if db.Stats().TxID != 1 {
		t.Errorf("txid = %d, want 1", db.Stats().TxID)
	}
}
