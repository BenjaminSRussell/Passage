package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

// RiskFilter narrows the register of one project (§8.41.14 "Risks and issues").
// ProjectID is required; Kind and Status are optional.
type RiskFilter struct {
	ProjectID string
	Kind      domain.RiskKind
	Status    domain.RiskStatus
}

// riskKeyConstraint is the default name of UNIQUE (tenant_id, idempotency_key).
const riskKeyConstraint = "project_risk_issue_tenant_id_idempotency_key_key"

const riskColumns = `id::text, project_id, kind, title, description, probability, impact, owner_worker_id::text,
	mitigation, due_date::text, status, version, created_at, updated_at`

func scanRisk(row pgx.Row) (domain.RiskIssue, error) {
	var r domain.RiskIssue
	var kind, status string
	err := row.Scan(&r.ID, &r.ProjectID, &kind, &r.Title, &r.Description, &r.Probability, &r.Impact, &r.OwnerWorkerID,
		&r.Mitigation, &r.DueDate, &status, &r.Version, &r.CreatedAt, &r.UpdatedAt)
	r.Kind = domain.RiskKind(kind)
	r.Status = domain.RiskStatus(status)
	r.Score = domain.RiskScore(r.Probability, r.Impact)
	return r, err
}

// SameRisk reports whether a replayed Idempotency-Key carries the same request
// as the stored entry.
func SameRisk(stored, in domain.RiskIssue) bool {
	return stored.ProjectID == in.ProjectID && stored.Kind == in.Kind && stored.Title == in.Title && stored.Description == in.Description &&
		stored.Probability == in.Probability && stored.Impact == in.Impact && eqPtr(stored.OwnerWorkerID, in.OwnerWorkerID) &&
		stored.Mitigation == in.Mitigation && eqPtr(stored.DueDate, in.DueDate)
}

// CreateRisk adds an OPEN risk or issue to a project's register. The project
// row is locked, so a concurrent request with the same key waits and replays.
// A replayed Idempotency-Key returns the stored entry with created=false; a
// different body under the key is idempotency_key_reuse.
func (s *Postgres) CreateRisk(ctx context.Context, in domain.RiskIssue, idempotencyKey string) (domain.RiskIssue, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RiskIssue
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockProject(ctx, tx, p.TenantID, in.ProjectID, true); err != nil {
			return err
		}
		stored, err := scanRisk(tx.QueryRow(ctx, `select `+riskColumns+` from project_risk_issue where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idempotencyKey))
		switch {
		case err == nil:
			if !SameRisk(stored, in) {
				return idempotencyReuse()
			}
			out = stored
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		out, err = scanRisk(tx.QueryRow(ctx, `insert into project_risk_issue (tenant_id, id, project_id, kind, title, description,
				probability, impact, owner_worker_id, mitigation, due_date, status, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::text::uuid, $10, $11::text::date, 'OPEN', $12)
			returning `+riskColumns,
			p.TenantID, ids.New(), in.ProjectID, string(in.Kind), in.Title, in.Description, in.Probability, in.Impact,
			in.OwnerWorkerID, in.Mitigation, in.DueDate, idempotencyKey))
		if err != nil {
			return err
		}
		created = true
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == riskKeyConstraint {
		return domain.RiskIssue{}, false, idempotencyReuse()
	}
	return out, created, err
}

// ListRisks returns the register of one project, highest score first, then
// oldest first. A project that is not visible is 404.
func (s *Postgres) ListRisks(ctx context.Context, f RiskFilter) ([]domain.RiskIssue, error) {
	p, _ := tenant.FromContext(ctx)
	items := []domain.RiskIssue{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockProject(ctx, tx, p.TenantID, f.ProjectID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+riskColumns+` from project_risk_issue
			where tenant_id = $1 and project_id = $2 and ($3::text = '' or kind = $3) and ($4::text = '' or status = $4)
			order by probability * impact desc, created_at, id`, p.TenantID, f.ProjectID, string(f.Kind), string(f.Status))
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.RiskIssue, error) { return scanRisk(r) })
		return err
	})
	return items, err
}

// UpdateRisk replaces the editable fields and status of the entry in.ID of
// project in.ProjectID when expected matches its version (412 otherwise). The
// status move must be allowed (PROJ-RISK-001); a CLOSED entry is final.
func (s *Postgres) UpdateRisk(ctx context.Context, expected int, in domain.RiskIssue) (domain.RiskIssue, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RiskIssue
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanRisk(tx.QueryRow(ctx, `select `+riskColumns+` from project_risk_issue
			where tenant_id = $1 and project_id = $2 and id = $3::uuid for update`, p.TenantID, in.ProjectID, in.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("risk or issue")
		}
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the risk or issue changed since it was read; re-read and retry")
		}
		if err := domain.CheckRiskMove(cur.Status, in.Status); err != nil {
			return err
		}
		out, err = scanRisk(tx.QueryRow(ctx, `update project_risk_issue set kind = $3, title = $4, description = $5,
			probability = $6, impact = $7, owner_worker_id = $8::text::uuid, mitigation = $9, due_date = $10::text::date, status = $11,
			version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2::uuid returning `+riskColumns,
			p.TenantID, in.ID, string(in.Kind), in.Title, in.Description, in.Probability, in.Impact,
			in.OwnerWorkerID, in.Mitigation, in.DueDate, string(in.Status)))
		return err
	})
	return out, err
}
