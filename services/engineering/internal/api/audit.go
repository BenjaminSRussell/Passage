package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
)

const (
	auditTimeout     = 3 * time.Second
	auditMaxResponse = 1 << 20
)

// Auditor records a Part 11 signature in the audit service (POST /v1/audit/changes).
type Auditor interface {
	RecordSignature(ctx context.Context, sig domain.Signature) error
}

// Reauthenticator is the step-up seam (21 CFR 11.200). Production can swap in a Keycloak check.
type Reauthenticator interface {
	Verify(ctx context.Context, actor string, auth domain.Reauth) error
}

// NopAuditor is used when AUDIT_URL is unset (local/dev).
type NopAuditor struct{}

func (NopAuditor) RecordSignature(context.Context, domain.Signature) error { return nil }

// AssertionReauth accepts a non-empty assertion whose user matches the acting user.
type AssertionReauth struct{}

func (AssertionReauth) Verify(_ context.Context, actor string, auth domain.Reauth) error {
	if auth.Assertion == "" || auth.UserID == "" || auth.UserID != actor {
		return &domain.Error{Code: domain.CodeDocReauth, Detail: "signature rejected: re-authentication failed"}
	}
	return nil
}

type auditChange struct {
	Entity     string    `json:"entity"`
	RecordID   string    `json:"recordId"`
	Field      string    `json:"field"`
	OldValue   string    `json:"oldValue"`
	NewValue   string    `json:"newValue"`
	UserID     string    `json:"userId"`
	TraceID    string    `json:"traceId"`
	OccurredAt time.Time `json:"occurredAt"`
}

// HTTPAuditor posts signature manifestations to the audit service.
type HTTPAuditor struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func NewHTTPAuditor(baseURL string, signer *tenant.ServiceSigner) *HTTPAuditor {
	return &HTTPAuditor{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: auditTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		signer:  signer,
	}
}

func (c *HTTPAuditor) RecordSignature(ctx context.Context, sig domain.Signature) error {
	if c == nil || c.baseURL == "" {
		return nil
	}
	traceID := middleware.GetReqID(ctx)
	if traceID == "" {
		traceID = sig.ID
	}
	payload, err := json.Marshal(auditChange{
		Entity: "document", RecordID: sig.RecordID, Field: "signature",
		NewValue: fmt.Sprintf("%s by %s at %s", sig.Meaning, sig.SignerID, sig.SignedAt.UTC().Format(time.RFC3339)),
		UserID:   sig.SignerID, TraceID: traceID, OccurredAt: sig.SignedAt.UTC(),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/audit/changes", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	p, _ := tenant.FromContext(ctx)
	if c.signer != nil {
		if _, err := c.signer.ApplyDuties(req, p, "audit", p.LegalEntityID, []string{"audit.write"}); err != nil {
			return err
		}
	}
	if req.Header.Get(tenant.HeaderServiceToken) == "" {
		req.Header.Set(tenant.HeaderTenant, p.TenantID)
		req.Header.Set(tenant.HeaderUser, p.UserID)
		req.Header.Set(tenant.HeaderDuties, "audit.write")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return httpx.NewProblem(http.StatusServiceUnavailable, "ENG-AUDIT-UNAVAILABLE", "audit service unavailable")
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.CopyN(io.Discard, res.Body, auditMaxResponse)
	if res.StatusCode >= 300 {
		return httpx.NewProblem(http.StatusServiceUnavailable, "ENG-AUDIT-UNAVAILABLE", "audit service refused the signature record")
	}
	return nil
}
