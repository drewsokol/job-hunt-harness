// Package keys keeps services' optional keys (Himalayas) on the user's
// machine, in keys.json in the data directory (docs/design.md, "Keys";
// decision 0009).
//
// Keys live in their own file, not in the database, so backups and exports
// of the database never contain one. A key is saved only from the
// dashboard and is never returned by the API, a tool or a log: the only
// reader is the source the key belongs to.
package keys

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// File is keys.json: one key per source name. Its zero value is not
// usable; call Open. A nil *File holds no keys.
type File struct {
	path string
	mu   sync.Mutex
}

// Open returns the key file at path. The file is created on the first Set.
func Open(path string) *File { return &File{path: path} }

// maxFile caps keys.json; a few keys are a few hundred bytes.
const maxFile = 64 << 10

// MaxKeyLen caps one key.
const MaxKeyLen = 500

func (f *File) read() (map[string]string, error) {
	b, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the key file: %w", err)
	}
	if len(b) > maxFile {
		return nil, errors.New("the key file is too large")
	}
	all := map[string]string{}
	if err := json.Unmarshal(b, &all); err != nil {
		// json's error can quote the file, which holds keys.
		return nil, errors.New("the key file is not valid JSON; remove the key on the dashboard and enter it again")
	}
	return all, nil
}

// Get returns the key saved for source.
func (f *File) Get(source string) (string, bool, error) {
	if f == nil {
		return "", false, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	all, err := f.read()
	if err != nil {
		return "", false, err
	}
	k := all[source]
	return k, k != "", nil
}

// Has reports whether a key is saved for source. A key file that can't be
// read counts as no key.
func (f *File) Has(source string) bool {
	_, ok, err := f.Get(source)
	return ok && err == nil
}

// Set saves source's key, replacing any it had.
func (f *File) Set(source, key string) error {
	if f == nil {
		return errors.New("jhh has no key file")
	}
	if key == "" || len(key) > MaxKeyLen {
		return fmt.Errorf("a key is 1 to %d characters", MaxKeyLen)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	all, err := f.read()
	if err != nil {
		// A broken file is replaced, so a new key can repair it.
		all = map[string]string{}
	}
	all[source] = key
	return f.write(all)
}

// Remove deletes source's key. Removing a key that isn't saved is fine.
func (f *File) Remove(source string) error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	all, err := f.read()
	if err != nil {
		all = map[string]string{}
	} else if _, ok := all[source]; !ok {
		return nil
	}
	delete(all, source)
	return f.write(all)
}

// write replaces the file through a temporary one, so a crash never leaves
// half a file. On Unix the file is mode 0600. On Windows the mode bits are
// ignored, and the file takes the data directory's ACL, which only the
// user (and administrators) can read.
func (f *File) write(all map[string]string) error {
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".keys-*.tmp")
	if err != nil {
		return fmt.Errorf("saving the key: %w", err)
	}
	defer os.Remove(tmp.Name()) // gone already after a successful rename
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		tmp.Close()
		return fmt.Errorf("saving the key: %w", err)
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("saving the key: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("saving the key: %w", err)
	}
	if err := os.Rename(tmp.Name(), f.path); err != nil {
		return fmt.Errorf("saving the key: %w", err)
	}
	return nil
}
