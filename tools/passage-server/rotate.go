package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// RotatingFile is an io.Writer that rolls name to name.1 ... name.N when it passes MaxBytes.
type RotatingFile struct {
	Path     string
	MaxBytes int64
	Keep     int
	mu       sync.Mutex
	f        *os.File
	size     int64
}

// Write appends p, rotating first when the file is full.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.MaxBytes > 0 && r.size+int64(len(p)) > r.MaxBytes && r.size > 0 {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// Close closes the file.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

func (r *RotatingFile) open() error {
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(r.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *RotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	r.f = nil
	keep := max(r.Keep, 1)
	_ = os.Remove(fmt.Sprintf("%s.%d", r.Path, keep))
	for i := keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.Path, i), fmt.Sprintf("%s.%d", r.Path, i+1))
	}
	if err := os.Rename(r.Path, r.Path+".1"); err != nil {
		return err
	}
	return r.open()
}
