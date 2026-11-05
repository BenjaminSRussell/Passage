// Package transport moves serialized EDI interchanges to and from trading
// partners (FR-35.5): AS2 (RFC 4130) with signed messages and synchronous
// MDNs (BR-35.11), SFTP with an atomic upload and an inbound pull, and an
// in-memory loopback for tests and the partner simulator. The EDI domain
// stays free of I/O; this package knows nothing about X12 or EDIFACT beyond
// the content type.
package transport

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
)

// Content types for the payloads (RFC 1767 and AS2 practice).
const (
	ContentX12     = "application/edi-x12"
	ContentEdifact = "application/edifact"
	ContentXML     = "application/xml"
)

// Message is one interchange to send. ID is the unique message id (the AS2
// Message-ID without angle brackets; the SFTP file name stem when Filename is
// empty). From and To are the AS2 identities; Payload is the serialized
// interchange, sent byte for byte.
type Message struct {
	ID          string
	From        string
	To          string
	Filename    string
	ContentType string
	Payload     []byte
}

// Receipt is the proof of delivery the transport obtained. For AS2 it is the
// MDN: Disposition, the Received-Content-MIC the partner computed and whether
// the MDN was signed. For SFTP it is the remote path after the rename.
type Receipt struct {
	Transport   string
	MessageID   string
	Reference   string
	MIC         string
	Disposition string
	SignedMDN   bool
	At          time.Time
}

// Sender delivers a message to one partner.
type Sender interface {
	Send(ctx context.Context, m Message) (Receipt, error)
}

// ErrRejected marks a message the partner received and refused (an MDN with
// processed/error or failed, or a loopback rejection). It is not retried.
var ErrRejected = errors.New("transport: rejected by partner")

// ErrInvalid marks a message that cannot be sent as given.
var ErrInvalid = errors.New("transport: invalid message")

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,200}$`)

func (m Message) validate() error {
	if !safeName.MatchString(m.ID) {
		return fmt.Errorf("%w: message id %q must be 1 to 201 of A-Z a-z 0-9 . _ @ -", ErrInvalid, m.ID)
	}
	if m.Filename != "" && !safeName.MatchString(m.Filename) {
		return fmt.Errorf("%w: file name %q must be a plain name", ErrInvalid, m.Filename)
	}
	if len(m.Payload) == 0 {
		return fmt.Errorf("%w: payload is empty", ErrInvalid)
	}
	switch m.ContentType {
	case ContentX12, ContentEdifact, ContentXML:
	default:
		return fmt.Errorf("%w: content type %q is not %s, %s or %s", ErrInvalid, m.ContentType, ContentX12, ContentEdifact, ContentXML)
	}
	return nil
}

func (m Message) filename() string {
	if m.Filename != "" {
		return m.Filename
	}
	ext := ".edi"
	switch m.ContentType {
	case ContentX12:
		ext = ".x12"
	case ContentXML:
		ext = ".xml"
	}
	return m.ID + ext
}

// Loopback is an in-memory Sender: it records every message and, when Reject
// is set, asks it whether the "partner" refuses one. It stands in for a
// partner in tests and in the simulator.
type Loopback struct {
	Reject func(Message) error
	Now    func() time.Time

	mu   sync.Mutex
	sent []Message
}

// Send records the message and returns a receipt referencing its position.
func (l *Loopback) Send(ctx context.Context, m Message) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if err := m.validate(); err != nil {
		return Receipt{}, err
	}
	if l.Reject != nil {
		if err := l.Reject(m); err != nil {
			return Receipt{}, fmt.Errorf("%w: %v", ErrRejected, err)
		}
	}
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	m.Payload = append([]byte(nil), m.Payload...)
	l.sent = append(l.sent, m)
	return Receipt{Transport: "loopback", MessageID: m.ID, Reference: fmt.Sprintf("loopback/%d/%s", len(l.sent), m.filename()), Disposition: "processed", At: now().UTC()}, nil
}

// Messages returns a copy of what was sent, in order.
func (l *Loopback) Messages() []Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Message(nil), l.sent...)
}
