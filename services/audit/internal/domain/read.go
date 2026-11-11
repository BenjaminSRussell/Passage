package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// CodeReaderNamed rejects a read-log request that names the reader: the reader is the verified caller.
	CodeReaderNamed = "AUD-0002"
	// CodeReadQuery rejects an invalid read-log filter, cursor or export format.
	CodeReadQuery = "AUD-0003"

	MaxReadFields   = 64
	MaxReadPurpose  = 500
	maxReadName     = 128
	maxFutureSkew   = 5 * time.Minute
	readFieldFormat = `^[A-Za-z0-9][A-Za-z0-9_.\[\]-]*$`
)

var readFieldPattern = regexp.MustCompile(readFieldFormat)

// Read is one access to protected health information: who (UserID, the verified caller), which record and
// fields, when, and why (Purpose, a reason code or free text). Source is the service that recorded it.
type Read struct {
	Entity     string
	RecordID   string
	Fields     []string
	Purpose    string
	UserID     string
	Source     string
	TraceID    string
	OccurredAt time.Time
}

// ReadEntry is an immutable read-log row linked into the tenant's chain.
type ReadEntry struct {
	Read
	PrevHash string
	Hash     string
}

// Normalize returns r with the field list de-duplicated and sorted, the purpose trimmed and the time in UTC at
// microsecond precision, the precision Postgres stores, so a stored row hashes to the value it was linked with.
func (r Read) Normalize() Read {
	fields := slices.Clone(r.Fields)
	slices.Sort(fields)
	r.Fields = slices.Compact(fields)
	r.Purpose = strings.TrimSpace(r.Purpose)
	r.OccurredAt = r.OccurredAt.UTC().Truncate(time.Microsecond)
	return r
}

// ValidateRead reports every problem with r as field name to message; now bounds a future occurredAt.
func ValidateRead(r Read, now time.Time) map[string]string {
	problems := map[string]string{}
	if r.Entity == "" || len(r.Entity) > maxReadName {
		problems["entity"] = fmt.Sprintf("is required and at most %d characters", maxReadName)
	}
	if r.RecordID == "" || len(r.RecordID) > maxReadName {
		problems["recordId"] = fmt.Sprintf("is required and at most %d characters", maxReadName)
	}
	if len(r.Fields) == 0 || len(r.Fields) > MaxReadFields {
		problems["fields"] = fmt.Sprintf("needs 1 to %d field names", MaxReadFields)
	} else {
		for _, f := range r.Fields {
			if len(f) > maxReadName || !readFieldPattern.MatchString(f) {
				problems["fields"] = "names must be letters, digits, _ . - [ ] and at most 128 characters"
				break
			}
		}
	}
	if p := strings.TrimSpace(r.Purpose); p == "" {
		problems["purpose"] = "is required: a reason code or a short reason for reading protected health information"
	} else if len(p) > MaxReadPurpose {
		problems["purpose"] = fmt.Sprintf("is at most %d characters", MaxReadPurpose)
	}
	if r.UserID == "" {
		problems["userId"] = "the caller is unknown"
	}
	if r.TraceID == "" || len(r.TraceID) > maxReadName {
		problems["traceId"] = fmt.Sprintf("is required and at most %d characters", maxReadName)
	}
	if r.OccurredAt.IsZero() {
		problems["occurredAt"] = "is required"
	} else if r.OccurredAt.After(now.Add(maxFutureSkew)) {
		problems["occurredAt"] = "is in the future"
	}
	return problems
}

// ReadHash links r to prevHash (Genesis for the first row). The payload is a JSON array, so no value can
// shift into a neighbouring one.
func ReadHash(prevHash string, r Read) string {
	if prevHash == "" {
		prevHash = Genesis
	}
	r = r.Normalize()
	payload, _ := json.Marshal([]any{
		prevHash, r.Entity, r.RecordID, r.Fields, r.Purpose, r.UserID, r.Source, r.TraceID, r.OccurredAt.Format(time.RFC3339Nano),
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// LinkRead builds the next read-log entry after prevHash.
func LinkRead(prevHash string, r Read, now time.Time) (ReadEntry, error) {
	r = r.Normalize()
	if problems := ValidateRead(r, now); len(problems) > 0 {
		return ReadEntry{}, &Error{Code: CodeInvalid, Detail: "invalid read"}
	}
	if prevHash == "" {
		prevHash = Genesis
	}
	return ReadEntry{Read: r, PrevHash: prevHash, Hash: ReadHash(prevHash, r)}, nil
}

// ChainStatus is the result of walking a hash chain from Genesis.
type ChainStatus struct {
	Verified bool
	Entries  int64
	Head     string
	// BrokenAt is the 0-based position of the first row that does not link, or -1.
	BrokenAt int64
}

// ChainWalker verifies a read-log chain one row at a time, so a large tenant is never held in memory.
type ChainWalker struct{ status ChainStatus }

// NewChainWalker starts at Genesis.
func NewChainWalker() *ChainWalker {
	return &ChainWalker{status: ChainStatus{Verified: true, Head: Genesis, BrokenAt: -1}}
}

// Next checks e against the chain so far. After the first break it keeps counting rows but stays broken.
func (w *ChainWalker) Next(e ReadEntry) {
	if w.status.Verified && (e.PrevHash != w.status.Head || e.Hash != ReadHash(w.status.Head, e.Read)) {
		w.status.Verified = false
		w.status.BrokenAt = w.status.Entries
	}
	w.status.Entries++
	if w.status.Verified {
		w.status.Head = e.Hash
	}
}

// Status is the verification result so far.
func (w *ChainWalker) Status() ChainStatus { return w.status }

// VerifyReads reports whether entries form a contiguous read-log chain from Genesis.
func VerifyReads(entries []ReadEntry) ChainStatus {
	w := NewChainWalker()
	for _, e := range entries {
		w.Next(e)
	}
	return w.Status()
}
