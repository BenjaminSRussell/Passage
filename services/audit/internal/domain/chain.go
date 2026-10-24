// Package domain is the audit kernel: field-level change records, an
// append-only hash chain, alert matching, standard-text expansion and the
// document checkout state machine.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	CodeInvalid = "AUD-0001"
	Genesis     = "0000000000000000000000000000000000000000000000000000000000000000"
)

// Error is a stable domain failure.
type Error struct{ Code, Detail string }

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// Change is one field mutation, as emitted by a CDC adapter (wave 4).
type Change struct {
	Entity     string
	RecordID   string
	Field      string
	OldValue   string
	NewValue   string
	UserID     string
	TraceID    string
	OccurredAt time.Time
}

// Entry is an immutable audit row.
type Entry struct {
	Change
	PrevHash string
	Hash     string
}

// Validate reports missing change fields.
func Validate(c Change) map[string]string {
	fields := map[string]string{}
	if c.Entity == "" {
		fields["entity"] = "is required"
	}
	if c.RecordID == "" {
		fields["recordId"] = "is required"
	}
	if c.Field == "" {
		fields["field"] = "is required"
	}
	if c.UserID == "" {
		fields["userId"] = "is required"
	}
	if c.TraceID == "" {
		fields["traceId"] = "is required"
	}
	if c.OccurredAt.IsZero() {
		fields["occurredAt"] = "is required"
	}
	return fields
}

// Hash links c to prevHash (64 hex zeros for the first row).
func Hash(prevHash string, c Change) string {
	if prevHash == "" {
		prevHash = Genesis
	}
	payload := strings.Join([]string{
		prevHash, c.Entity, c.RecordID, c.Field, c.OldValue, c.NewValue, c.UserID, c.TraceID, c.OccurredAt.UTC().Format(time.RFC3339Nano),
	}, "|")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// Link builds the next chain entry.
func Link(prevHash string, c Change) (Entry, error) {
	if fields := Validate(c); len(fields) > 0 {
		return Entry{}, &Error{Code: CodeInvalid, Detail: "invalid change"}
	}
	if prevHash == "" {
		prevHash = Genesis
	}
	return Entry{Change: c, PrevHash: prevHash, Hash: Hash(prevHash, c)}, nil
}

// Verify reports whether entries form a contiguous hash chain from Genesis.
func Verify(entries []Entry) error {
	prev := Genesis
	for i, e := range entries {
		want := Hash(prev, e.Change)
		if e.PrevHash != prev || e.Hash != want {
			return fmt.Errorf("chain break at %d", i)
		}
		prev = e.Hash
	}
	return nil
}
