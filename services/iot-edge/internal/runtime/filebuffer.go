package runtime

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/domain"
)

// EventBuffer is the store-and-forward queue the runtime ships events from. domain.Buffer is the
// in-memory form; FileBuffer keeps the same queue across a restart.
type EventBuffer interface {
	Push(now time.Time, payload []byte) (evicted bool)
	Peek(now time.Time, n int) (payloads [][]byte, through uint64)
	Ack(through uint64)
	FillPct(now time.Time) float64
	Evicted() int
	Len() int
}

// FileBuffer is the persistent event buffer (FR-25.9): the domain ring plus an append-only journal. Every
// push and every acknowledgement is a line in the journal, so a connector that restarts (a rescheduled
// pod, a power cut) finds the events it had not yet delivered and sends them. A line the crash cut in
// half is ignored on the next start. The journal is rewritten from the live ring when it has grown to
// several times the live data, and at every start.
//
// Lines: "P <id> <unix nanos> <payload>" (or "B ..." with the payload in base64 when it holds a newline)
// and "A <id>" for everything up to and including id.
type FileBuffer struct {
	mu      sync.Mutex
	mem     *domain.Buffer
	path    string
	f       *os.File
	w       *bufio.Writer
	written int64 // journal bytes since the last rewrite
	live    int64 // bytes of the live payloads at the last rewrite
	synced  time.Time
	// WriteErrors counts journal writes that failed: those events still go out, but a restart would
	// lose them.
	writeErrors int64
	// SyncEvery bounds how long an acknowledged write can wait for fsync; zero syncs on every Ack.
	SyncEvery time.Duration
}

// OpenFileBuffer opens (or creates) the journal at path and returns the buffer holding what it had.
// maxAge and capacity are the ring's limits (see domain.NewBuffer); entries older than maxAge are not
// brought back.
func OpenFileBuffer(path string, maxAge time.Duration, capacity int, now time.Time) (*FileBuffer, error) {
	b := &FileBuffer{mem: domain.NewBuffer(maxAge, capacity), path: path}
	entries, next, err := readJournal(path)
	if err != nil {
		return nil, err
	}
	b.mem.Restore(entries, next)
	b.mem.Peek(now, 0) // expires what is too old now
	if err := b.rewriteLocked(); err != nil {
		return nil, err
	}
	return b, nil
}

func readJournal(path string) ([]domain.Entry, uint64, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 1, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("open event buffer: %w", err)
	}
	defer f.Close()
	var entries []domain.Entry
	var maxID uint64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) < 3 || line[1] != ' ' {
			continue
		}
		switch line[0] {
		case 'P', 'B':
			parts := bytes.SplitN(line[2:], []byte{' '}, 3)
			if len(parts) != 3 {
				continue
			}
			id, err1 := strconv.ParseUint(string(parts[0]), 10, 64)
			ns, err2 := strconv.ParseInt(string(parts[1]), 10, 64)
			if err1 != nil || err2 != nil {
				continue
			}
			payload := append([]byte(nil), parts[2]...)
			if line[0] == 'B' {
				var err error
				if payload, err = base64.StdEncoding.DecodeString(string(parts[2])); err != nil {
					continue
				}
			}
			entries = append(entries, domain.Entry{ID: id, At: time.Unix(0, ns), Payload: payload})
			maxID = max(maxID, id)
		case 'A':
			id, err := strconv.ParseUint(string(line[2:]), 10, 64)
			if err != nil {
				continue
			}
			cut := 0
			for cut < len(entries) && entries[cut].ID <= id {
				cut++
			}
			entries = entries[cut:]
			maxID = max(maxID, id)
		}
	}
	// A scanner error (a line over the limit) keeps what was read: losing the tail beats refusing to start.
	return entries, maxID + 1, nil
}

// rewriteLocked replaces the journal with the live ring through a temporary file and a rename.
func (b *FileBuffer) rewriteLocked() error {
	entries, _ := b.mem.Snapshot()
	tmp := filepath.Join(filepath.Dir(b.path), "."+filepath.Base(b.path)+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("rewrite event buffer: %w", err)
	}
	w := bufio.NewWriterSize(f, 256*1024)
	var live int64
	for _, e := range entries {
		n, err := writePush(w, e.ID, e.At, e.Payload)
		if err != nil {
			f.Close()
			return err
		}
		live += int64(n)
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, b.path); err != nil {
		return err
	}
	if b.f != nil {
		b.f.Close()
	}
	nf, err := os.OpenFile(b.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	b.f, b.w, b.written, b.live, b.synced = nf, bufio.NewWriterSize(nf, 64*1024), live, live, time.Now()
	return nil
}

func writePush(w *bufio.Writer, id uint64, at time.Time, payload []byte) (int, error) {
	kind, body := "P", payload
	if bytes.IndexByte(payload, '\n') >= 0 {
		kind, body = "B", []byte(base64.StdEncoding.EncodeToString(payload))
	}
	head := kind + " " + strconv.FormatUint(id, 10) + " " + strconv.FormatInt(at.UnixNano(), 10) + " "
	n, err := w.WriteString(head)
	if err != nil {
		return n, err
	}
	m, err := w.Write(body)
	if err != nil {
		return n + m, err
	}
	err = w.WriteByte('\n')
	return n + m + 1, err
}

// Push buffers the payload and journals it. A journal failure is counted and does not stop the event:
// it goes out normally and would only be lost by a restart.
func (b *FileBuffer) Push(now time.Time, payload []byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	evicted := b.mem.Push(now, payload)
	n, err := writePush(b.w, b.mem.LastID(), now, payload)
	if err == nil {
		// Hand the line to the kernel now: a crash of this process loses nothing; only a power cut can lose
		// what the next fsync has not covered.
		err = b.w.Flush()
	}
	if err != nil {
		b.writeErrors++
	}
	b.written += int64(n)
	return evicted
}

// Peek returns the oldest payloads without removing them.
func (b *FileBuffer) Peek(now time.Time, n int) ([][]byte, uint64) { return b.mem.Peek(now, n) }

// Ack removes the delivered payloads and journals the acknowledgement.
func (b *FileBuffer) Ack(through uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mem.Ack(through)
	if through == 0 {
		return
	}
	n, err := fmt.Fprintf(b.w, "A %d\n", through)
	if err == nil {
		err = b.w.Flush()
	}
	if err != nil {
		b.writeErrors++
	}
	b.written += int64(n)
	if b.SyncEvery <= 0 || time.Since(b.synced) >= b.SyncEvery {
		_ = b.f.Sync()
		b.synced = time.Now()
	}
	if b.written > 4*b.live+8<<20 {
		if err := b.rewriteLocked(); err != nil {
			b.writeErrors++
		}
	}
}

// FillPct, Evicted and Len report the ring.
func (b *FileBuffer) FillPct(now time.Time) float64 { return b.mem.FillPct(now) }

// Evicted counts the payloads lost to capacity.
func (b *FileBuffer) Evicted() int { return b.mem.Evicted() }

// Len is the number of buffered payloads.
func (b *FileBuffer) Len() int { return b.mem.Len() }

// WriteErrors is the number of journal writes that failed.
func (b *FileBuffer) WriteErrors() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writeErrors
}

// Close flushes and closes the journal.
func (b *FileBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.f == nil {
		return nil
	}
	_ = b.w.Flush()
	_ = b.f.Sync()
	err := b.f.Close()
	b.f = nil
	return err
}
