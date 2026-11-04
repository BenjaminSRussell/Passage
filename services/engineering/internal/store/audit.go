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

type AuditJob struct {
	ID            string
	TenantID      string
	UserID        string
	LegalEntityID string
	Attempts      int
	Signature     domain.Signature
}

func enqueueAudit(ctx context.Context, tx pgx.Tx, p tenant.Principal, sig domain.Signature) error {
	payload, err := json.Marshal(sig)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into audit_outbox (id, tenant_id, user_id, legal_entity_id, payload) values ($1::uuid, $2::uuid, $3, $4, $5)`,
		sig.ID, p.TenantID, p.UserID, p.LegalEntityID, payload)
	return err
}

func (s *Postgres) ClaimAudit(ctx context.Context, limit int, lease time.Duration) ([]AuditJob, error) {
	rows, err := s.pool.Query(ctx, `with due as (
			select id from audit_outbox where delivered_at is null and next_attempt_at <= now() order by next_attempt_at limit $1 for update skip locked)
		update audit_outbox o set next_attempt_at = now() + make_interval(secs => $2::double precision)
		from due where o.id = due.id
		returning o.id::text, o.tenant_id::text, o.user_id, o.legal_entity_id, o.attempts, o.payload`, limit, lease.Seconds())
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

func (s *Postgres) CompleteAudit(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `update audit_outbox set delivered_at = now(), last_error = '' where id = $1::uuid`, id)
	return err
}

func (s *Postgres) FailAudit(ctx context.Context, id string, attempts int, cause error, retryAfter time.Duration) error {
	message := cause.Error()
	if len(message) > maxAuditError {
		message = message[:maxAuditError]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	_, err := s.pool.Exec(ctx, `update audit_outbox set attempts = $2, last_error = $3, next_attempt_at = now() + make_interval(secs => $4::double precision) where id = $1::uuid`,
		id, attempts, message, retryAfter.Seconds())
	return err
}
