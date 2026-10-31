package domain

import (
	"sync"
	"time"
)

// Buffer is the store-and-forward ring (FR-25.9): payloads are kept until they
// are drained to Kafka, dropped after maxAge, and evicted oldest-first when the
// capacity is reached. The production connector backs this with SQLite so it
// survives restarts (see NOT-IMPLEMENTED.md); this in-memory form defines the rules.
type Buffer struct {
	mu       sync.Mutex
	maxAge   time.Duration
	capacity int
	items    []entry
	evicted  int
	next     uint64
}

type entry struct {
	id      uint64
	at      time.Time
	payload []byte
}

// NewBuffer returns a ring that keeps at most capacity payloads for maxAge each.
func NewBuffer(maxAge time.Duration, capacity int) *Buffer {
	return &Buffer{maxAge: maxAge, capacity: capacity}
}

// Push stores one payload enqueued at time at. It returns true when an older
// payload was evicted to make room (the caller counts these as lost).
func (b *Buffer) Push(now time.Time, payload []byte) (evicted bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire(now)
	if b.capacity <= 0 {
		b.evicted++
		return true
	}
	if len(b.items) >= b.capacity {
		b.items = b.items[1:]
		b.evicted++
		evicted = true
	}
	b.next++
	b.items = append(b.items, entry{id: b.next, at: now, payload: payload})
	return evicted
}

// Peek returns up to n of the oldest payloads still within maxAge without
// removing them, and the position to pass to Ack once the broker acknowledged
// them (0 when nothing is buffered). A publisher that loses its broker before
// the acknowledgement keeps the payloads and sends them again, so a broker
// restart costs duplicates (dropped downstream by seq) and never loses events,
// which Drain cannot promise.
func (b *Buffer) Peek(now time.Time, n int) (payloads [][]byte, through uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire(now)
	n = min(n, len(b.items))
	payloads = make([][]byte, 0, n)
	for _, e := range b.items[:n] {
		payloads = append(payloads, e.payload)
		through = e.id
	}
	return payloads, through
}

// Ack removes the payloads up to and including position through, as returned
// by Peek. Payloads evicted or expired since the Peek are already gone, and
// payloads pushed since stay.
func (b *Buffer) Ack(through uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cut := 0
	for cut < len(b.items) && b.items[cut].id <= through {
		cut++
	}
	b.items = b.items[cut:]
}

// Drain removes and returns up to n of the oldest payloads that are still within maxAge.
func (b *Buffer) Drain(now time.Time, n int) [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire(now)
	if n > len(b.items) {
		n = len(b.items)
	}
	out := make([][]byte, 0, n)
	for _, e := range b.items[:n] {
		out = append(out, e.payload)
	}
	b.items = b.items[n:]
	return out
}

// FillPct returns the buffer fill as a percentage of capacity (BR: edge buffer never exceeds 80%).
func (b *Buffer) FillPct(now time.Time) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire(now)
	if b.capacity <= 0 {
		return 100
	}
	return float64(len(b.items)) * 100 / float64(b.capacity)
}

// Evicted returns how many payloads were lost to capacity pressure since start.
func (b *Buffer) Evicted() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.evicted
}

// Len returns the number of buffered payloads.
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.items)
}

// expire drops payloads older than maxAge. Callers hold b.mu.
func (b *Buffer) expire(now time.Time) {
	cut := 0
	for cut < len(b.items) && now.Sub(b.items[cut].at) > b.maxAge {
		cut++
	}
	b.items = b.items[cut:]
}
