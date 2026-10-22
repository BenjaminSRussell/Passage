// Package ids issues ULIDs: time-ordered, 26-char, URL-safe identifiers used
// for every Passage primary key.
package ids

import (
	"crypto/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

var (
	mu      sync.Mutex
	entropy = ulid.Monotonic(rand.Reader, 0)
)

// New returns a new ULID string. Monotonic within a process so ids issued in
// the same millisecond still sort in issue order.
func New() string {
	mu.Lock()
	defer mu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
}

// Valid reports whether s parses as a ULID.
func Valid(s string) bool { _, err := ulid.ParseStrict(s); return err == nil }
