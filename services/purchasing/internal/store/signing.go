package store

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// Problem codes raised by the signing limit store (FR-17.17).
const (
	CodeSigningLimitNotFound   = "PRC-SL-LIMIT-NOT-FOUND"
	CodeSigningRequestNotFound = "PRC-SL-REQUEST-NOT-FOUND"
	// CodeSigningRequestDecided: the request was already approved or rejected.
	CodeSigningRequestDecided = "PRC-SL-004"
	// CodeSigningStale: If-Match is not the request's or limit's version.
	CodeSigningStale = "PRC-SL-005"
)

// Signing limit decisions.
const (
	SigningApprove = "approve"
	SigningReject  = "reject"
)

// SigningLimitFilter narrows ListSigningLimits; empty fields do not filter. Expired keeps
// the limits past their recertification date on Today.
type SigningLimitFilter struct {
	LegalEntityID string
	WorkerID      string
	DocumentType  string
	Expired       bool
	Today         domain.Day
}

// SigningRequestFilter narrows ListSigningLimitRequests; empty fields do not filter.
type SigningRequestFilter struct {
	LegalEntityID string
	WorkerID      string
	Status        string
}

const signingLimitColumns = `id, legal_entity_id, worker_id, document_type, currency, spending_limit, approval_limit, recertify_by,
	last_recertified_on, row_version, coalesce(created_by::text, ''), created_at, coalesce(updated_by::text, ''), updated_at`

func scanSigningLimit(row pgx.Row, l *domain.SigningLimit) error {
	var by time.Time
	var last *time.Time
	if err := row.Scan(&l.ID, &l.LegalEntityID, &l.WorkerID, &l.DocumentType, &l.Currency, &l.SpendingLimit, &l.ApprovalLimit, &by, &last,
		&l.Version, &l.CreatedBy, &l.CreatedAt, &l.UpdatedBy, &l.UpdatedAt); err != nil {
		return err
	}
	l.RecertifyBy, l.LastRecertifiedOn = day(by), dayOrNil(last)
	return nil
}

func loadSigningLimit(ctx context.Context, tx pgx.Tx, id string, lock bool) (domain.SigningLimit, error) {
	p, _ := tenant.FromContext(ctx)
	q := `select ` + signingLimitColumns + ` from signing_limit where tenant_id = $1 and id = $2`
	if lock {
		q += ` for update`
	}
	var l domain.SigningLimit
	err := scanSigningLimit(tx.QueryRow(ctx, q, p.TenantID, id), &l)
	return l, notFound(err, CodeSigningLimitNotFound, "signing limit not found")
}

const signingRequestColumns = `id, legal_entity_id, worker_id, document_type, currency, spending_limit, approval_limit, reason, status,
	requested_by::text, requested_at, coalesce(decided_by::text, ''), decided_at, decision_note, coalesce(signing_limit_id::text, ''), row_version`

func scanSigningRequest(row pgx.Row, r *domain.SigningLimitRequest) error {
	return row.Scan(&r.ID, &r.LegalEntityID, &r.WorkerID, &r.DocumentType, &r.Currency, &r.SpendingLimit, &r.ApprovalLimit, &r.Reason,
		&r.Status, &r.RequestedBy, &r.RequestedAt, &r.DecidedBy, &r.DecidedAt, &r.DecisionNote, &r.SigningLimitID, &r.Version)
}

func loadSigningRequest(ctx context.Context, tx pgx.Tx, id string, lock bool) (domain.SigningLimitRequest, error) {
	p, _ := tenant.FromContext(ctx)
	q := `select ` + signingRequestColumns + ` from signing_limit_request where tenant_id = $1 and id = $2`
	if lock {
		q += ` for update`
	}
	var r domain.SigningLimitRequest
	err := scanSigningRequest(tx.QueryRow(ctx, q, p.TenantID, id), &r)
	return r, notFound(err, CodeSigningRequestNotFound, "signing limit request not found")
}

func signingLimitData(l domain.SigningLimit, reason, by string) purchasingevents.SigningLimitData {
	return purchasingevents.SigningLimitData{LimitID: l.ID, LegalEntityID: l.LegalEntityID, WorkerID: l.WorkerID, DocumentType: l.DocumentType,
		Currency: l.Currency, SpendingLimit: l.SpendingLimit.String(), ApprovalLimit: l.ApprovalLimit.String(), RecertifyBy: l.RecertifyBy.String(),
		Reason: reason, Version: l.Version, ChangedBy: by}
}

// CreateSigningLimitRequest records a PENDING request to set a worker's limit. A replayed
// Idempotency-Key returns the first request.
func (s *Postgres) CreateSigningLimitRequest(ctx context.Context, in domain.SigningLimitRequest, idem Idem) (domain.SigningLimitRequest, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	var out domain.SigningLimitRequest
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, in.ID)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadSigningRequest(ctx, tx, existing, false)
			return err
		}
		if _, err := tx.Exec(ctx, `insert into signing_limit_request (tenant_id, id, legal_entity_id, worker_id, document_type, currency,
			spending_limit, approval_limit, reason, requested_by) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			p.TenantID, in.ID, in.LegalEntityID, in.WorkerID, in.DocumentType, in.Currency, in.SpendingLimit, in.ApprovalLimit, in.Reason,
			p.UserID); err != nil {
			return err
		}
		out, err = loadSigningRequest(ctx, tx, in.ID, false)
		return err
	})
	return out, err
}

// GetSigningLimitRequest returns one request.
func (s *Postgres) GetSigningLimitRequest(ctx context.Context, id string) (domain.SigningLimitRequest, error) {
	var out domain.SigningLimitRequest
	err := s.withTx(ctx, func(tx pgx.Tx) (err error) {
		out, err = loadSigningRequest(ctx, tx, id, false)
		return err
	})
	return out, err
}

// ListSigningLimitRequests returns requests newest first, after the cursor id.
func (s *Postgres) ListSigningLimitRequests(ctx context.Context, f SigningRequestFilter, after string, limit int) (Page[domain.SigningLimitRequest], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	items := []domain.SigningLimitRequest{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+signingRequestColumns+` from signing_limit_request
			where tenant_id = $1 and ($2::uuid is null or legal_entity_id = $2::uuid) and ($3::uuid is null or worker_id = $3::uuid)
			and ($4 = '' or status = $4) and ($5::uuid is null or id < $5::uuid) order by id desc limit $6`,
			p.TenantID, nullable(f.LegalEntityID), nullable(f.WorkerID), f.Status, nullable(after), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r domain.SigningLimitRequest
			if err := scanSigningRequest(rows, &r); err != nil {
				return err
			}
			items = append(items, r)
		}
		return rows.Err()
	})
	if err != nil {
		return Page[domain.SigningLimitRequest]{}, err
	}
	return pageOf(items, limit, func(r domain.SigningLimitRequest) string { return r.ID }), nil
}

func signingStale(kind string, cur, want int) error {
	return httpx.NewProblem(http.StatusPreconditionFailed, CodeSigningStale,
		fmt.Sprintf("%s is at version %d, not %d; reload and retry", kind, cur, want))
}

// DecideSigningLimitRequest approves or rejects a PENDING request under If-Match. The
// requester and the worker cannot decide it (PRC-SL-003, 'with own SigningLimitRequest').
// Approving writes the worker's limit for the document type and currency (a new limit, or the
// existing one at the next version), sets the next recertification date from today and
// writes signinglimit.changed.v1 in the same transaction. A decided request is 409 PRC-SL-004.
func (s *Postgres) DecideSigningLimitRequest(ctx context.Context, id string, ifMatch int, action, note string, today domain.Day, idem Idem) (domain.SigningLimitRequest, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.SigningLimitRequest
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadSigningRequest(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if _, replay, err := claimKey(ctx, tx, idem, id); err != nil {
			return err
		} else if replay {
			out = cur
			return nil
		}
		if cur.Version != ifMatch {
			return signingStale("signing limit request", cur.Version, ifMatch)
		}
		if cur.Status != domain.SigningPending {
			return httpx.NewProblem(http.StatusConflict, CodeSigningRequestDecided, "the request was already "+cur.Status)
		}
		if p.UserID == cur.RequestedBy || p.UserID == cur.WorkerID {
			return &domain.Error{Code: domain.CodeSigningSelfDecision, Detail: "a worker cannot decide a signing limit request they made or that is for them"}
		}
		status, limitID := domain.SigningRejected, ""
		if action == SigningApprove {
			status = domain.SigningApproved
			var l domain.SigningLimit
			err := scanSigningLimit(tx.QueryRow(ctx, `insert into signing_limit as sl (tenant_id, id, legal_entity_id, worker_id, document_type, currency,
				spending_limit, approval_limit, recertify_by, last_recertified_on, created_by, updated_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)
				on conflict (tenant_id, legal_entity_id, worker_id, document_type, currency) do update set
				spending_limit = excluded.spending_limit, approval_limit = excluded.approval_limit, recertify_by = excluded.recertify_by,
				last_recertified_on = excluded.last_recertified_on, row_version = sl.row_version + 1, updated_by = excluded.updated_by, updated_at = now()
				returning `+signingLimitColumns, p.TenantID, ids.New(), cur.LegalEntityID, cur.WorkerID, cur.DocumentType, cur.Currency,
				cur.SpendingLimit, cur.ApprovalLimit, domain.RecertifyDate(today).Time, today.Time, userOrNil(p.UserID)), &l)
			if err != nil {
				return err
			}
			limitID = l.ID
			if err := enqueue(ctx, tx, "signinglimit", l.ID, purchasingevents.SigningLimitChanged, signingLimitData(l, "REQUEST_APPROVED", p.UserID)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update signing_limit_request set status = $3, decided_by = $4, decided_at = now(), decision_note = $5,
			signing_limit_id = $6, row_version = row_version + 1 where tenant_id = $1 and id = $2`,
			p.TenantID, id, status, p.UserID, note, nullable(limitID)); err != nil {
			return err
		}
		out, err = loadSigningRequest(ctx, tx, id, false)
		return err
	})
	return out, err
}

// GetSigningLimit returns one limit.
func (s *Postgres) GetSigningLimit(ctx context.Context, id string) (domain.SigningLimit, error) {
	var out domain.SigningLimit
	err := s.withTx(ctx, func(tx pgx.Tx) (err error) {
		out, err = loadSigningLimit(ctx, tx, id, false)
		return err
	})
	return out, err
}

// ListSigningLimits returns limits newest first, after the cursor id.
func (s *Postgres) ListSigningLimits(ctx context.Context, f SigningLimitFilter, after string, limit int) (Page[domain.SigningLimit], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	items := []domain.SigningLimit{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+signingLimitColumns+` from signing_limit
			where tenant_id = $1 and ($2::uuid is null or legal_entity_id = $2::uuid) and ($3::uuid is null or worker_id = $3::uuid)
			and ($4 = '' or document_type = $4) and (not $5 or recertify_by < $6::date) and ($7::uuid is null or id < $7::uuid)
			order by id desc limit $8`, p.TenantID, nullable(f.LegalEntityID), nullable(f.WorkerID), f.DocumentType, f.Expired,
			f.Today.Time, nullable(after), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l domain.SigningLimit
			if err := scanSigningLimit(rows, &l); err != nil {
				return err
			}
			items = append(items, l)
		}
		return rows.Err()
	})
	if err != nil {
		return Page[domain.SigningLimit]{}, err
	}
	return pageOf(items, limit, func(l domain.SigningLimit) string { return l.ID }), nil
}

// SigningLimitsFor returns the worker's limits for a document type in a legal entity (one per
// currency), for the signing limit rule.
func (s *Postgres) SigningLimitsFor(ctx context.Context, legalEntityID, workerID, documentType string) ([]domain.SigningLimit, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.SigningLimit{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+signingLimitColumns+` from signing_limit
			where tenant_id = $1 and legal_entity_id = $2 and worker_id = $3 and document_type = $4 order by currency`,
			p.TenantID, legalEntityID, workerID, documentType)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l domain.SigningLimit
			if err := scanSigningLimit(rows, &l); err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	return out, err
}

// RecertifySigningLimit moves the recertification date of a limit 12 months past today under
// If-Match and writes signinglimit.changed.v1. The worker cannot recertify their own limit
// (PRC-SL-003).
func (s *Postgres) RecertifySigningLimit(ctx context.Context, id string, ifMatch int, today domain.Day, idem Idem) (domain.SigningLimit, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.SigningLimit
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadSigningLimit(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if _, replay, err := claimKey(ctx, tx, idem, id); err != nil {
			return err
		} else if replay {
			out = cur
			return nil
		}
		if cur.Version != ifMatch {
			return signingStale("signing limit", cur.Version, ifMatch)
		}
		if p.UserID == cur.WorkerID {
			return &domain.Error{Code: domain.CodeSigningSelfDecision, Detail: "a worker cannot recertify their own signing limit"}
		}
		if _, err := tx.Exec(ctx, `update signing_limit set recertify_by = $3, last_recertified_on = $4, updated_by = $5, updated_at = now(),
			row_version = row_version + 1 where tenant_id = $1 and id = $2`, p.TenantID, id, domain.RecertifyDate(today).Time, today.Time, userOrNil(p.UserID)); err != nil {
			return err
		}
		if out, err = loadSigningLimit(ctx, tx, id, false); err != nil {
			return err
		}
		return enqueue(ctx, tx, "signinglimit", id, purchasingevents.SigningLimitChanged, signingLimitData(out, "RECERTIFIED", p.UserID))
	})
	return out, err
}
