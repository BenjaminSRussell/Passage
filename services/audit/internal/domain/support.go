package domain

import (
	"strings"
	"time"
)

const (
	AlertChange  = "change"
	AlertDueDate = "due_date"
	DocDraft     = "draft"
	DocOut       = "checked_out"
	DocCurrent   = "current"
	DocHeld      = "held"
)

// AlertRule matches a field change or a due date.
type AlertRule struct {
	ID     string
	Entity string
	Field  string
	Kind   string
}

// Match reports whether rule fires for c.
func Match(rule AlertRule, c Change) bool {
	if rule.Kind != AlertChange {
		return false
	}
	return rule.Entity == c.Entity && (rule.Field == "" || rule.Field == c.Field) && c.OldValue != c.NewValue
}

// Due reports whether a due-date rule is open at now.
func Due(rule AlertRule, now, due time.Time) bool {
	return rule.Kind == AlertDueDate && !now.Before(due)
}

// Alert is an inbox row.
type Alert struct {
	RuleID   string
	Entity   string
	RecordID string
	Field    string
	Message  string
}

// Expand replaces {name} placeholders. Unknown tokens are left intact.
func Expand(body string, vars map[string]string) string {
	out := body
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}

// Document is a versioned attachment.
type Document struct {
	ID        string
	Type      string
	Location  string
	Status    string
	Version   int
	Holder    string
	LegalHold bool
}

// Checkout locks d for user.
func Checkout(d Document, user string) (Document, error) {
	if d.LegalHold || d.Status == DocOut || d.Status == DocHeld {
		return Document{}, &Error{Code: CodeInvalid, Detail: "document cannot be checked out"}
	}
	if user == "" {
		return Document{}, &Error{Code: CodeInvalid, Detail: "holder is required"}
	}
	d.Status = DocOut
	d.Holder = user
	return d, nil
}

// Checkin records a new version.
func Checkin(d Document, user string) (Document, error) {
	if d.Status != DocOut || d.Holder != user {
		return Document{}, &Error{Code: CodeInvalid, Detail: "document is not checked out by this user"}
	}
	d.Status = DocCurrent
	d.Holder = ""
	d.Version++
	return d, nil
}

// Hold places a legal hold that blocks purge.
func Hold(d Document) Document {
	d.LegalHold = true
	d.Status = DocHeld
	return d
}

// CanPurge is false under legal hold or before retainUntil.
func CanPurge(d Document, now, retainUntil time.Time) bool {
	return !d.LegalHold && now.After(retainUntil)
}

// BlobStore is the S3-compatible port (wave 5 ships an in-package fake).
type BlobStore interface {
	Put(key string, body []byte) error
	Get(key string) ([]byte, error)
}

// Scanner is the malware-scan port.
type Scanner interface {
	Scan(body []byte) error
}

// MemoryBlob is an in-process object store.
type MemoryBlob struct{ objects map[string][]byte }

// NewMemoryBlob returns an empty store.
func NewMemoryBlob() *MemoryBlob { return &MemoryBlob{objects: map[string][]byte{}} }

// Put writes an object.
func (m *MemoryBlob) Put(key string, body []byte) error {
	cp := make([]byte, len(body))
	copy(cp, body)
	m.objects[key] = cp
	return nil
}

// Get reads an object.
func (m *MemoryBlob) Get(key string) ([]byte, error) {
	body, ok := m.objects[key]
	if !ok {
		return nil, &Error{Code: CodeInvalid, Detail: "object not found"}
	}
	return body, nil
}

// CleanScanner accepts every payload.
type CleanScanner struct{}

// Scan implements Scanner.
func (CleanScanner) Scan([]byte) error { return nil }
