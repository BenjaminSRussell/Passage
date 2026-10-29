// Package ids issues UUIDv7 identifiers: time-ordered, 128-bit, stored in
// Postgres uuid columns. Every Passage primary key uses New(). (The
// architecture's identifier decision: UUIDv7, not ULID.)
package ids

import "github.com/google/uuid"

// New returns a new UUIDv7 string. UUIDv7 embeds a millisecond timestamp so
// ids sort in issue order, which keeps btree inserts append-mostly.
func New() string {
	id, err := uuid.NewV7()
	if err != nil {
		// NewV7 fails only if the OS entropy source fails; a process without
		// entropy cannot safely continue issuing keys.
		panic("ids: " + err.Error())
	}
	return id.String()
}

// Valid reports whether s parses as a UUID of any version.
func Valid(s string) bool { _, err := uuid.Parse(s); return err == nil }

// Canonical returns the lowercase hyphenated form of a UUID string, the form
// whose byte order matches Postgres uuid ordering.
func Canonical(s string) (string, bool) {
	id, err := uuid.Parse(s)
	if err != nil {
		return "", false
	}
	return id.String(), true
}

// Nil is the all-zero UUID string, used for "not set" in tests and fixtures.
const Nil = "00000000-0000-0000-0000-000000000000"
