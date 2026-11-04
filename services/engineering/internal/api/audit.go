package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

const (
	auditTimeout     = 3 * time.Second
	auditMaxResponse = 1 << 20
	auditBatch       = 50
	auditLease       = time.Minute
	auditRetryBase   = 5 * time.Second
	auditRetryMax    = 15 * time.Minute
)

// Auditor records a Part 11 signature in the audit service (POST /v1/audit/changes).
type Auditor interface {
	RecordSignature(ctx context.Context, sig domain.Signature) error
}

// Reauthenticator is the step-up seam (21 CFR 11.200): it proves the actor just re-authenticated,
// with a proof bound to the document and record hash in auth.
type Reauthenticator interface {
	Verify(ctx context.Context, actor string, auth domain.Reauth) error
}

// NopAuditor discards records; it is bound only when PASSAGE_ENV=dev and AUDIT_URL is unset.
type NopAuditor struct{}

func (NopAuditor) RecordSignature(context.Context, domain.Signature) error { return nil }

type FailClosedReauth struct{}

func (FailClosedReauth) Verify(context.Context, string, domain.Reauth) error {
	return httpx.NewProblem(http.StatusServiceUnavailable, domain.CodeDocReauthUnavailable, "signing is unavailable: no re-authentication service is configured")
}

type DevReauth struct{}

func (DevReauth) Verify(_ context.Context, actor string, auth domain.Reauth) error {
	if actor == "" || auth.UserID != actor {
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
	if c == nil || c.baseURL == "" || c.signer == nil {
		return httpx.NewProblem(http.StatusServiceUnavailable, "ENG-AUDIT-UNAVAILABLE", "audit service is not configured")
	}
	traceID := middleware.GetReqID(ctx)
	if traceID == "" {
		traceID = sig.ID
	}
	payload, err := json.Marshal(auditChange{
		Entity: "document", RecordID: sig.RecordID, Field: "signature",
		NewValue: fmt.Sprintf("%s: %s (%s) signed %s %s to %s at %s; record hash %s", sig.Meaning, sig.PrintedName, sig.SignerID, sig.DocumentNumber, sig.DocumentVersion, sig.Transition, sig.SignedAt.UTC().Format(time.RFC3339), sig.OriginalsHash),
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
	legalEntity := ""
	if scopes := tenant.LegalEntityScopeList(p); len(scopes) > 0 {
		legalEntity = scopes[0]
	}
	if _, err := c.signer.ApplyDuties(req, p, "audit", legalEntity, []string{"audit.write"}); err != nil {
		return err
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

type AuditRelay struct {
	store   store.Store
	auditor Auditor
}

func NewAuditRelay(s store.Store, a Auditor) *AuditRelay {
	return &AuditRelay{store: s, auditor: a}
}

func auditBackoff(attempts int) time.Duration {
	return min(auditRetryBase<<min(attempts, 10), auditRetryMax)
}

func (r *AuditRelay) Drain(ctx context.Context) (int, error) {
	jobs, err := r.store.ClaimAudit(ctx, auditBatch, auditLease)
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, job := range jobs {
		callCtx := tenant.With(ctx, tenant.Principal{TenantID: job.TenantID, UserID: job.UserID, LegalEntityID: job.LegalEntityID})
		if err := r.auditor.RecordSignature(callCtx, job.Signature); err != nil {
			slog.ErrorContext(ctx, "audit delivery failed", "signature", job.ID, "attempts", job.Attempts+1, "err", err)
			if failErr := r.store.FailAudit(ctx, job.ID, job.Attempts+1, err, auditBackoff(job.Attempts+1)); failErr != nil {
				return delivered, failErr
			}
			continue
		}
		if err := r.store.CompleteAudit(ctx, job.ID); err != nil {
			return delivered, err
		}
		delivered++
	}
	return delivered, nil
}

func (r *AuditRelay) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		if _, err := r.Drain(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "audit relay pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
