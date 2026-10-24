// Package domain is the number-sequence kernel: format rendering, mode rules
// and a mutex allocator used by the Jepsen-style concurrency proof.
package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	ModeGapFree       = "gap_free"
	ModeContinuous    = "continuous"
	ModeNonContinuous = "non_continuous"
	CodeInvalid       = "NUM-0001"
	CodeExhausted     = "NUM-0002"
	CodeUnknown       = "NUM-0003"
	maxName           = 120
	maxValue          = int64(1_000_000_000_000)
)

var (
	codeRe    = regexp.MustCompile(`^[A-Z][A-Z0-9_-]{1,31}$`)
	tokenRe   = regexp.MustCompile(`\{([#0]+)\}`)
	validMode = map[string]bool{ModeGapFree: true, ModeContinuous: true, ModeNonContinuous: true}
)

// Error is a stable domain failure.
type Error struct{ Code, Detail string }

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// Definition is a per-legal-entity sequence.
type Definition struct {
	Code          string
	Name          string
	Format        string
	LegalEntityID string
	Mode          string
	StartFrom     int64
}

// Allocation is one issued number.
type Allocation struct {
	Value     int64
	Formatted string
}

// Validate reports field errors for a definition.
func Validate(d Definition) map[string]string {
	fields := map[string]string{}
	if !codeRe.MatchString(d.Code) {
		fields["code"] = "must be 2 to 32 characters of A-Z, 0-9, _ or - starting with a letter"
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(d.Name)); n == 0 || n > maxName {
		fields["name"] = fmt.Sprintf("must be 1 to %d characters", maxName)
	}
	if _, _, _, err := parseFormat(d.Format); err != nil {
		fields["format"] = err.Error()
	}
	if !validMode[d.Mode] {
		fields["mode"] = "must be gap_free, continuous or non_continuous"
	}
	if d.StartFrom < 1 {
		fields["startFrom"] = "must be at least 1"
	}
	if d.LegalEntityID == "" {
		fields["legalEntityId"] = "is required"
	}
	return fields
}

// FormatValue renders n into format. The token is {######} or {000000}.
func FormatValue(format string, n int64) (string, error) {
	prefix, width, suffix, err := parseFormat(format)
	if err != nil {
		return "", err
	}
	if n < 1 || n > maxValue {
		return "", &Error{Code: CodeInvalid, Detail: "value is out of range"}
	}
	raw := strconv.FormatInt(n, 10)
	if len(raw) > width {
		return "", &Error{Code: CodeExhausted, Detail: "sequence padded width is exhausted"}
	}
	return prefix + strings.Repeat("0", width-len(raw)) + raw + suffix, nil
}

func parseFormat(format string) (prefix string, width int, suffix string, err error) {
	if format == "" {
		return "", 0, "", &Error{Code: CodeInvalid, Detail: "format is required"}
	}
	locs := tokenRe.FindAllStringSubmatchIndex(format, -1)
	if len(locs) != 1 {
		return "", 0, "", &Error{Code: CodeInvalid, Detail: "format must contain exactly one {######} token"}
	}
	width = locs[0][3] - locs[0][2]
	if width < 1 || width > 12 {
		return "", 0, "", &Error{Code: CodeInvalid, Detail: "token width must be 1 to 12"}
	}
	return format[:locs[0][0]], width, format[locs[0][1]:], nil
}

type allocKey struct{ Tenant, LegalEntity, Code string }

// MemoryAllocator is a process-local gap-free allocator. It is the oracle the
// Jepsen-style test drives; the Postgres store uses the same increment rule
// under SELECT FOR UPDATE.
type MemoryAllocator struct {
	mu   sync.Mutex
	next map[allocKey]int64
	seen map[allocKey]map[int64]struct{}
}

// NewMemoryAllocator returns an empty allocator.
func NewMemoryAllocator() *MemoryAllocator {
	return &MemoryAllocator{next: map[allocKey]int64{}, seen: map[allocKey]map[int64]struct{}{}}
}

// Allocate issues the next value for d. Concurrent callers serialize on mu.
func (a *MemoryAllocator) Allocate(tenantID string, d Definition) (Allocation, error) {
	if fields := Validate(d); len(fields) > 0 {
		return Allocation{}, &Error{Code: CodeInvalid, Detail: "invalid definition"}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	k := allocKey{tenantID, d.LegalEntityID, d.Code}
	n := a.next[k]
	if n == 0 {
		n = d.StartFrom
	}
	formatted, err := FormatValue(d.Format, n)
	if err != nil {
		return Allocation{}, err
	}
	if a.seen[k] == nil {
		a.seen[k] = map[int64]struct{}{}
	}
	if _, dup := a.seen[k][n]; dup {
		return Allocation{}, &Error{Code: CodeInvalid, Detail: "duplicate allocation"}
	}
	a.seen[k][n] = struct{}{}
	a.next[k] = n + 1
	return Allocation{Value: n, Formatted: formatted}, nil
}
