package store

import (
	"context"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
)

const maxAuditError = 500

// AuditJob is one queued signature record. LegalEntityID is the signer's legal entity, used to mint the
// audit.write service token on the signer's behalf.
type AuditJob struct {
	ID            string
	TenantID      string
	UserID        string
	LegalEntityID string
	Attempts      int
	Signature     domain.Signature
}

// SignerLegalEntity is the legal entity a signature's audit record is delivered under: the signer's first
// legal-entity scope, or "" when the signer has none (the API refuses such a signer before signing).
func SignerLegalEntity(p tenant.Principal) string {
	if scopes := tenant.LegalEntityScopeList(p); len(scopes) > 0 {
		return scopes[0]
	}
	return ""
}

func enqueueAudit(ctx context.Context, tx pgx.Tx, p tenant.Principal, sig domain.Signature) error {
	payload, err := json.Marshal(sig)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into audit_outbox (id, tenant_id, user_id, actor_legal_entity, payload) values ($1::uuid, $2::uuid, $3, $4, $5)`,
		sig.ID, p.TenantID, p.UserID, SignerLegalEntity(p), payload)
	return err
}

// CanScanAudit reports whether engineering_audit_claim can see every tenant's records (its owner bypasses
// row-level security); when it cannot, the relay would deliver nothing.
func (s *Postgres) CanScanAudit(ctx context.Context) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `select engineering_audit_can_scan()`).Scan(&ok)
	return ok, err
}

// ClaimAudit leases due records across tenants through the SECURITY DEFINER engineering_audit_claim;
// audit_outbox itself is tenant-scoped under forced row-level security.
func (s *Postgres) ClaimAudit(ctx context.Context, limit int, lease time.Duration) ([]AuditJob, error) {
	rows, err := s.pool.Query(ctx, `select id::text, tenant_id::text, user_id, actor_legal_entity, attempts, payload from engineering_audit_claim($1, $2::double precision)`, limit, lease.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []AuditJob
	for rows.Next() {
		var job AuditJob
		var payload []byte
		if err := rows.Scan(&job.ID, &job.TenantID, &job.UserID, &job.LegalEntityID, &job.Attempts, &payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &job.Signature); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// CompleteAudit marks a record delivered; ctx carries the record's tenant.
func (s *Postgres) CompleteAudit(ctx context.Context, id string) error {
	return withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) error {
		_, err := tx.Exec(ctx, `update audit_outbox set delivered_at = now(), last_error = '' where tenant_id = $1 and id = $2::uuid`, tenantID, id)
		return err
	})
}

// FailAudit records a failed delivery and when to retry it; ctx carries the record's tenant.
func (s *Postgres) FailAudit(ctx context.Context, id string, attempts int, cause error, retryAfter time.Duration) error {
	message := cause.Error()
	if len(message) > maxAuditError {
		message = message[:maxAuditError]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	return withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) error {
		_, err := tx.Exec(ctx, `update audit_outbox set attempts = $3, last_error = $4, next_attempt_at = now() + make_interval(secs => $5::double precision) where tenant_id = $1 and id = $2::uuid`,
			tenantID, id, attempts, message, retryAfter.Seconds())
		return err
	})
}
