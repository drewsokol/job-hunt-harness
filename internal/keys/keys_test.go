package keys

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	f := Open(path)
	if f.Has("himalayas") {
		t.Fatal("a key before any was saved")
	}
	if err := f.Remove("himalayas"); err != nil {
		t.Fatalf("removing a key that isn't there: %v", err)
	}
	if err := f.Set("himalayas", ""); err == nil {
		t.Error("an empty key was saved")
	}
	if err := f.Set("himalayas", "made-up-key"); err != nil {
		t.Fatal(err)
	}
	k, ok, err := f.Get("himalayas")
	if err != nil || !ok || k != "made-up-key" {
		t.Fatalf("Get = %q, %v, %v", k, ok, err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("key file mode %v, %v; want 0600", fi.Mode().Perm(), err)
		}
	}
	if !Open(path).Has("himalayas") {
		t.Error("the key wasn't saved to the file")
	}
	if err := f.Remove("himalayas"); err != nil {
		t.Fatal(err)
	}
	if f.Has("himalayas") {
		t.Error("the key is still there after removing it")
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

func TestBrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	if err := os.WriteFile(path, []byte(`{"himalayas": "made-up-secret"`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := Open(path)
	if _, _, err := f.Get("himalayas"); err == nil || strings.Contains(err.Error(), "made-up-secret") {
		t.Errorf("err = %v; want an error that doesn't quote the file", err)
	}
	if f.Has("himalayas") {
		t.Error("a broken file counts as a key")
	}
	// Saving a key again repairs the file.
	if err := f.Set("himalayas", "made-up-key"); err != nil || !f.Has("himalayas") {
		t.Errorf("repair: %v", err)
	}
	var none *File
	if none.Has("himalayas") || none.Remove("himalayas") != nil {
		t.Error("a nil key file holds no keys")
	}
}
