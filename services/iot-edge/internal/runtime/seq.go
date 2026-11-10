package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// SeqState keeps the per-machine sequence high-water marks across restarts. The server drops an
// event whose seq is not above the last it stored (BR-25.4), so a connector that restarted from 1
// would have every new event dropped as a duplicate.
type SeqState interface {
	Load() (map[string]int64, error)
	Save(map[string]int64) error
}

// MemorySeqState is the state for tests and the simulator: it survives nothing.
type MemorySeqState struct {
	mu sync.Mutex
	m  map[string]int64
}

// Load returns a copy of the saved marks.
func (s *MemorySeqState) Load() (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int64, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out, nil
}

// Save replaces the marks.
func (s *MemorySeqState) Save(m map[string]int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m = make(map[string]int64, len(m))
	for k, v := range m {
		s.m[k] = v
	}
	return nil
}

// FileSeqState keeps the marks in a JSON file, replaced atomically.
type FileSeqState struct{ Path string }

// Load reads the file; a missing file is an empty state.
func (s FileSeqState) Load() (map[string]int64, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]int64{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]int64{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s is not a sequence state file: %w", s.Path, err)
	}
	return m, nil
}

// Save writes the file through a temporary file and a rename, so a crash leaves the old or the new
// state and never half of one.
func (s FileSeqState) Save(m map[string]int64) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(s.Path), "."+filepath.Base(s.Path)+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// seqAllocator hands out the next seq per machine. It saves a reservation ahead of the numbers it
// issues (a block at a time), so after a crash it resumes above everything it ever issued. The
// price of a crash is a gap of at most one block, which the server reports (SF-0008), and never a
// duplicate that would be dropped.
type seqAllocator struct {
	mu       sync.Mutex
	state    SeqState
	block    int64
	issued   map[string]int64 // last issued
	reserved map[string]int64 // saved high-water mark
}

func newSeqAllocator(state SeqState, block int64) (*seqAllocator, error) {
	saved, err := state.Load()
	if err != nil {
		return nil, fmt.Errorf("load sequence state: %w", err)
	}
	a := &seqAllocator{state: state, block: block, issued: map[string]int64{}, reserved: saved}
	for m, v := range saved {
		a.issued[m] = v // resume above the whole reserved block
	}
	return a, nil
}

func (a *seqAllocator) next(machine string) (int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := a.issued[machine] + 1
	if n > a.reserved[machine] {
		a.reserved[machine] = n + a.block - 1
		if err := a.state.Save(a.reserved); err != nil {
			a.reserved[machine] = n - 1
			return 0, err
		}
	}
	a.issued[machine] = n
	return n, nil
}
