package index

import (
	"os"
	"sync/atomic"
)

var current atomic.Pointer[Vocabulary]

// Configure installs an immutable artifact at process startup. No online
// request edits vocabulary or starts an unreviewed taxonomy rebuild.
func Configure(path string) (*Vocabulary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	v, err := Load(f)
	if err != nil {
		return nil, err
	}
	current.Store(v)
	return v, nil
}
func Current() *Vocabulary { return current.Load() }
