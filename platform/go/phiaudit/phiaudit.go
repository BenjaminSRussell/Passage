// Package phiaudit records reads of protected health information in the audit service (#606, HIPAA audit controls).
//
// A service that returns a record with phi-tagged fields calls Recorder.RecordRead once per read. The HTTP recorder
// posts to POST /v1/audit/reads with a service token that carries only the audit.read-log.write duty; the audit
// service takes the reader from that token's verified caller, so a Read has no user field. A read that cannot be
// recorded is an error: the caller decides, and the HR service refuses to return the data (fail closed).
package phiaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/cloudprofile"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	// EnvReadAudit switches read auditing for a service: "true" or "false" force it, anything else (including unset)
	// follows the cloud profile: on when PASSAGE_CLOUD_PROFILE includes the hipaa overlay.
	EnvReadAudit = "PASSAGE_PHI_READ_AUDIT"

	// DutyWrite is the service-only duty of POST /v1/audit/reads.
	DutyWrite = "audit.read-log.write"

	// OverlayHIPAA is the cloud profile overlay that turns read auditing on.
	OverlayHIPAA = "hipaa"

	audience    = "audit"
	readsPath   = "/v1/audit/reads"
	timeout     = 3 * time.Second
	maxResponse = 1 << 20
)

// BreakGlassPurpose is the reserved reason code of emergency access: a read whose purpose is "BREAK-GLASS", or starts
// with "BREAK-GLASS:" or "BREAK-GLASS " and the reason (any letter case), was made under a duty kept for emergencies.
// The audit service queues such reads for review by a second person (GET /v1/audit/reads:break-glass).
const BreakGlassPurpose = "BREAK-GLASS"

// IsBreakGlass reports whether purpose carries the break-glass reason code.
func IsBreakGlass(purpose string) bool {
	p := strings.ToUpper(strings.TrimSpace(purpose))
	if !strings.HasPrefix(p, BreakGlassPurpose) {
		return false
	}
	rest := p[len(BreakGlassPurpose):]
	return rest == "" || rest[0] == ':' || rest[0] == ' '
}

// Read is one read of protected health information. Who read it is not a field: the audit service takes the
// reader from the verified caller.
type Read struct {
	// Entity names the kind of record, for example "hr.worker.benefits".
	Entity string
	// RecordID identifies the record that was read.
	RecordID string
	// Fields are the phi fields (or paths) the response contained.
	Fields []string
	// Purpose is a reason code or a short reason for the read; the audit service requires it.
	Purpose string
	// TraceID defaults to the request id of the context.
	TraceID string
	// OccurredAt defaults to now.
	OccurredAt time.Time
	// LegalEntityID scopes the service token; it defaults to the first legal entity of the caller.
	LegalEntityID string
}

// Recorder records reads. Implementations are safe for concurrent use.
type Recorder interface {
	RecordRead(ctx context.Context, r Read) error
}

// Nop discards reads. It is what a service uses when read auditing is off.
type Nop struct{}

// RecordRead implements Recorder.
func (Nop) RecordRead(context.Context, Read) error { return nil }

// HTTP posts reads to the audit service.
type HTTP struct {
	baseURL string
	client  *http.Client
	signer  *tenant.ServiceSigner
}

// NewHTTP builds a recorder for the audit service at baseURL. The signer is the calling service's identity; without
// it every RecordRead fails, so a misconfigured service cannot read unrecorded.
func NewHTTP(baseURL string, signer *tenant.ServiceSigner) *HTTP {
	return &HTTP{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		signer:  signer,
	}
}

type body struct {
	Entity     string    `json:"entity"`
	RecordID   string    `json:"recordId"`
	Fields     []string  `json:"fields"`
	Purpose    string    `json:"purpose"`
	TraceID    string    `json:"traceId"`
	OccurredAt time.Time `json:"occurredAt"`
}

// RecordRead posts r on behalf of the caller in ctx.
func (h *HTTP) RecordRead(ctx context.Context, r Read) error {
	if h == nil || h.baseURL == "" {
		return errors.New("phiaudit: no audit service URL configured")
	}
	if h.signer == nil {
		return errors.New("phiaudit: no service signing key configured")
	}
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("phiaudit: no principal in context")
	}
	if r.TraceID == "" {
		r.TraceID = middleware.GetReqID(ctx)
	}
	if r.TraceID == "" {
		r.TraceID = "phiaudit-" + time.Now().UTC().Format("20060102T150405.000000000")
	}
	if r.OccurredAt.IsZero() {
		r.OccurredAt = time.Now().UTC()
	}
	payload, err := json.Marshal(body{Entity: r.Entity, RecordID: r.RecordID, Fields: r.Fields, Purpose: r.Purpose, TraceID: r.TraceID, OccurredAt: r.OccurredAt.UTC()})
	if err != nil {
		return fmt.Errorf("phiaudit: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+readsPath, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("phiaudit: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	legalEntity := r.LegalEntityID
	if legalEntity == "" {
		if scopes := tenant.LegalEntityScopeList(p); len(scopes) > 0 {
			legalEntity = scopes[0]
		}
	}
	if _, err := h.signer.ApplyDuties(req, p, audience, legalEntity, []string{DutyWrite}); err != nil {
		return fmt.Errorf("phiaudit: sign request: %w", err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("phiaudit: post %s: %w", readsPath, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponse))
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("phiaudit: post %s: unexpected status %d", readsPath, resp.StatusCode)
	}
	return nil
}

// Recorded is one read a Fake received, with the caller it saw in the context.
type Recorded struct {
	Read
	TenantID string
	UserID   string
}

// Fake is a Recorder for tests: it keeps what it was given and can be made to fail.
type Fake struct {
	mu    sync.Mutex
	reads []Recorded
	// Err, when set before use, is returned by every RecordRead and nothing is kept.
	Err error
}

// RecordRead implements Recorder.
func (f *Fake) RecordRead(ctx context.Context, r Read) error {
	if f.Err != nil {
		return f.Err
	}
	p, _ := tenant.FromContext(ctx)
	if r.OccurredAt.IsZero() {
		r.OccurredAt = time.Now().UTC() // what the HTTP recorder sends, so tests see "when"
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, Recorded{Read: r, TenantID: p.TenantID, UserID: p.UserID})
	return nil
}

// Reads returns a copy of what was recorded, oldest first.
func (f *Fake) Reads() []Recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Recorded(nil), f.reads...)
}

// Enabled reports whether read auditing is on: EnvReadAudit "true" or "false" decides; otherwise it is on exactly when
// PASSAGE_CLOUD_PROFILE includes the hipaa overlay. An unknown profile name is an error (the profile fails closed).
func Enabled(getenv func(string) string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(getenv(EnvReadAudit))) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	profile, err := cloudprofile.FromEnv(getenv)
	if err != nil {
		return false, err
	}
	for _, o := range profile.Overlays {
		if o == OverlayHIPAA {
			return true, nil
		}
	}
	return false, nil
}
