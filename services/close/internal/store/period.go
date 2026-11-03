package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

func (s *Postgres) PeriodMatrix(ctx context.Context, legalEntityID, ledgerID string) ([]domain.PeriodStatus, error) {
	var out []domain.PeriodStatus
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select legal_entity_id::text, ledger_id::text, fiscal_period_id::text, module, status, coalesce(allowed_group_id,''), reopen_count, reopen_expires_at, changed_by, coalesce(start_date::text,''), coalesce(end_date::text,'')
			from period_status where legal_entity_id = $1 and ledger_id = $2 order by start_date, fiscal_period_id, module`, legalEntityID, ledgerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p domain.PeriodStatus
			if err := rows.Scan(&p.LegalEntityID, &p.LedgerID, &p.FiscalPeriodID, &p.Module, &p.Status, &p.AllowedGroupID, &p.ReopenCount, &p.ReopenExpiresAt, &p.ChangedBy, &p.StartDate, &p.EndDate); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("read period matrix: %w", err)
	}
	return out, nil
}

func (s *Postgres) TransitionPeriod(ctx context.Context, in domain.PeriodTransition) (domain.PeriodStatus, error) {
	var out domain.PeriodStatus
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return out, errors.New("transition period: no principal")
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		wait := in.LockWait
		if wait <= 0 {
			wait = 5 * time.Second
		}
		if _, err := tx.Exec(ctx, `select set_config('lock_timeout', $1, true)`, fmt.Sprintf("%dms", wait.Milliseconds())); err != nil {
			return err
		}
		found, err := tx.Query(ctx, `select module, status, coalesce(allowed_group_id,'') from period_status where legal_entity_id = $1 and ledger_id = $2 and fiscal_period_id = $3 for update`,
			in.LegalEntityID, in.LedgerID, in.FiscalPeriodID)
		if err != nil {
			return lockErr(err)
		}
		var rows []domain.Row
		for found.Next() {
			var r domain.Row
			if err := found.Scan(&r.Module, &r.Status, &r.AllowedGroupID); err != nil {
				found.Close()
				return err
			}
			rows = append(rows, r)
		}
		found.Close()
		if err := found.Err(); err != nil {
			return lockErr(err)
		}
		if in.To == domain.StatusOnHold && in.AllowedGroupID == "" {
			return &domain.Error{Code: domain.CodeValidation, Detail: "allowedGroupId is required when holding a period"}
		}
		if err := domain.ValidateTransition(rows, in.To, in.Module); err != nil {
			return err
		}
		from := domain.StatusNeverOpened
		for _, r := range rows {
			if r.Module == in.Module {
				from = r.Status
			}
		}
		group := in.AllowedGroupID
		if in.To != domain.StatusOnHold {
			group = ""
		}
		if from == domain.StatusNeverOpened {
			tag, err := tx.Exec(ctx, `insert into period_status(tenant_id, legal_entity_id, ledger_id, fiscal_period_id, module, status, allowed_group_id, start_date, end_date, changed_by)
				select current_setting('passage.tenant_id')::uuid, legal_entity_id, ledger_id, fiscal_period_id, $4, $5, nullif($6,''), start_date, end_date, $7
				from period_status where legal_entity_id = $1 and ledger_id = $2 and fiscal_period_id = $3 limit 1`,
				in.LegalEntityID, in.LedgerID, in.FiscalPeriodID, in.Module, in.To, group, in.Actor)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return &domain.Error{Code: domain.CodeNoPeriod, Detail: "period is not registered"}
			}
		} else {
			if _, err := tx.Exec(ctx, `update period_status set status = $4, allowed_group_id = nullif($5,''), reopen_expires_at = null, changed_by = $6, changed_at = now()
				where legal_entity_id = $1 and ledger_id = $2 and fiscal_period_id = $3 and module = $7`,
				in.LegalEntityID, in.LedgerID, in.FiscalPeriodID, in.To, group, in.Actor, in.Module); err != nil {
				return err
			}
		}
		return writePeriodChange(ctx, tx, p.TenantID, in.LegalEntityID, in.LedgerID, in.FiscalPeriodID, in.Module, from, in.To, in.Actor, in.Reason, nil)
	})
	if err != nil {
		return out, err
	}
	return s.onePeriod(ctx, in.LegalEntityID, in.LedgerID, in.FiscalPeriodID, in.Module)
}

func (s *Postgres) onePeriod(ctx context.Context, entity, ledger, period string, module domain.Module) (domain.PeriodStatus, error) {
	var out domain.PeriodStatus
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select legal_entity_id::text, ledger_id::text, fiscal_period_id::text, module, status, coalesce(allowed_group_id,''), reopen_count, reopen_expires_at, changed_by, coalesce(start_date::text,''), coalesce(end_date::text,'')
			from period_status where legal_entity_id = $1 and ledger_id = $2 and fiscal_period_id = $3 and module = $4`, entity, ledger, period, module).
			Scan(&out.LegalEntityID, &out.LedgerID, &out.FiscalPeriodID, &out.Module, &out.Status, &out.AllowedGroupID, &out.ReopenCount, &out.ReopenExpiresAt, &out.ChangedBy, &out.StartDate, &out.EndDate)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, &domain.Error{Code: domain.CodeNoPeriod, Detail: "period is not registered"}
	}
	if err != nil {
		return out, fmt.Errorf("read period: %w", err)
	}
	return out, nil
}

func lockErr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "55P03" || pg.Code == "57014") {
		return &domain.Error{Code: domain.CodeBusy, Detail: "the period is busy; retry the close"}
	}
	return err
}

func writePeriodChange(ctx context.Context, tx pgx.Tx, tenantID, entity, ledger, period string, module domain.Module, from, to domain.Status, actor, reason string, approvals []domain.ReopenApproval) error {
	if approvals == nil {
		approvals = []domain.ReopenApproval{}
	}
	raw, err := json.Marshal(approvals)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into period_status_change(tenant_id, id, legal_entity_id, ledger_id, fiscal_period_id, module, from_status, to_status, actor, reason, approvals)
		values(current_setting('passage.tenant_id')::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)`,
		ids.New(), entity, ledger, period, module, from, to, actor, reason, raw); err != nil {
		return err
	}
	evt, err := events.New(domain.EventPeriodStatusChanged, eventSource, period, tenantID, domain.PeriodChanged{
		LedgerID: ledger, FiscalPeriodID: period, Module: module, PreviousStatus: from, NewStatus: to, ChangedBy: actor, Reason: reason, Approvals: approvals,
	})
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "period_status", period, evt)
}

func (s *Postgres) RequestReopen(ctx context.Context, in domain.StoredReopen) (domain.StoredReopen, error) {
	if err := domain.ValidateReopenReason(in.Reason); err != nil {
		return domain.StoredReopen{}, err
	}
	if in.Required < 2 {
		in.Required = 2
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status domain.Status
		err := tx.QueryRow(ctx, `select status from period_status where legal_entity_id = $1 and ledger_id = $2 and fiscal_period_id = $3 and module = $4`,
			in.LegalEntityID, in.LedgerID, in.FiscalPeriodID, in.Module).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeNoPeriod, Detail: "period is not registered"}
		}
		if err != nil {
			return err
		}
		if status != domain.StatusClosed {
			return &domain.Error{Code: domain.CodeInvalidTransition, Detail: "only a closed period can be reopened"}
		}
		in.ID = ids.New()
		in.Status = "pending"
		in.Approvals = []domain.ReopenApproval{}
		raw, _ := json.Marshal(in.Approvals)
		_, err = tx.Exec(ctx, `insert into period_reopen_request(tenant_id, id, legal_entity_id, ledger_id, fiscal_period_id, module, reason, requested_by, approvals, required_approvals, status, expires_days)
			values(current_setting('passage.tenant_id')::uuid, $1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, 'pending', $10)`,
			in.ID, in.LegalEntityID, in.LedgerID, in.FiscalPeriodID, in.Module, in.Reason, in.RequestedBy, raw, in.Required, in.ExpiresDays)
		return uniquePending(err)
	})
	if err != nil {
		return domain.StoredReopen{}, err
	}
	return in, nil
}

func uniquePending(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return &domain.Error{Code: domain.CodeInvalidTransition, Detail: "a reopen request is already pending for this period and module"}
	}
	return err
}

func (s *Postgres) ApproveReopen(ctx context.Context, id, actor string) (domain.StoredReopen, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return domain.StoredReopen{}, errors.New("approve reopen: no principal")
	}
	var out domain.StoredReopen
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		req, err := scanReopen(tx.QueryRow(ctx, reopenSelect+` where id = $1 for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeNoPeriod, Detail: "reopen request not found"}
		}
		if err != nil {
			return err
		}
		if req.Status != "pending" {
			return &domain.Error{Code: domain.CodeInvalidTransition, Detail: "reopen request is " + req.Status}
		}
		updated, done, err := domain.ApproveReopen(domain.ReopenRequest{RequestedBy: req.RequestedBy, Required: req.Required, Reason: req.Reason, Approvals: req.Approvals}, actor)
		if err != nil {
			return err
		}
		req.Approvals = updated.Approvals
		raw, _ := json.Marshal(req.Approvals)
		if !done {
			_, err = tx.Exec(ctx, `update period_reopen_request set approvals = $2::jsonb where id = $1`, id, raw)
			out = req
			return err
		}
		req.Status = "approved"
		if _, err := tx.Exec(ctx, `update period_reopen_request set approvals = $2::jsonb, status = 'approved', decided_at = now() where id = $1`, id, raw); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update period_status set status = 'open', reopen_count = reopen_count + 1, reopen_expires_at = now() + make_interval(days => $5), approvals = $2::jsonb, changed_by = $6, changed_at = now()
			where legal_entity_id = $1 and ledger_id = $3 and fiscal_period_id = $4 and module = $7 and status = 'closed'`,
			req.LegalEntityID, raw, req.LedgerID, req.FiscalPeriodID, req.ExpiresDays, actor, req.Module)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return &domain.Error{Code: domain.CodeInvalidTransition, Detail: "period is no longer closed"}
		}
		out = req
		return writePeriodChange(ctx, tx, p.TenantID, req.LegalEntityID, req.LedgerID, req.FiscalPeriodID, req.Module, domain.StatusClosed, domain.StatusOpen, actor, req.Reason, req.Approvals)
	})
	return out, err
}

func (s *Postgres) RejectReopen(ctx context.Context, id, actor string) (domain.StoredReopen, error) {
	var out domain.StoredReopen
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		req, err := scanReopen(tx.QueryRow(ctx, reopenSelect+` where id = $1 for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeNoPeriod, Detail: "reopen request not found"}
		}
		if err != nil {
			return err
		}
		if req.Status != "pending" {
			return &domain.Error{Code: domain.CodeInvalidTransition, Detail: "reopen request is " + req.Status}
		}
		if _, err := tx.Exec(ctx, `update period_reopen_request set status = 'rejected', decided_at = now() where id = $1`, id); err != nil {
			return err
		}
		req.Status = "rejected"
		out = req
		return nil
	})
	return out, err
}

const reopenSelect = `select id::text, legal_entity_id::text, ledger_id::text, fiscal_period_id::text, module, reason, requested_by, approvals, required_approvals, status, expires_days from period_reopen_request`

func scanReopen(row pgx.Row) (domain.StoredReopen, error) {
	var r domain.StoredReopen
	var raw []byte
	err := row.Scan(&r.ID, &r.LegalEntityID, &r.LedgerID, &r.FiscalPeriodID, &r.Module, &r.Reason, &r.RequestedBy, &raw, &r.Required, &r.Status, &r.ExpiresDays)
	if err != nil {
		return r, err
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &r.Approvals)
	}
	return r, nil
}

func (s *Postgres) ExpireReopened(ctx context.Context) (int, error) {
	admin, err := s.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer admin.Release()
	rows, err := admin.Query(ctx, `select close_reopen_due_tenants()`)
	if err != nil {
		return 0, fmt.Errorf("list due reopen tenants: %w", err)
	}
	var tenants []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		tenants = append(tenants, id)
	}
	rows.Close()
	closed := 0
	for _, tenantID := range tenants {
		n, err := s.expireTenant(ctx, tenantID)
		if err != nil {
			return closed, err
		}
		closed += n
	}
	return closed, nil
}

func (s *Postgres) expireTenant(ctx context.Context, tenantID string) (int, error) {
	ctx = tenant.With(ctx, tenant.Principal{TenantID: tenantID, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	n := 0
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select legal_entity_id::text, ledger_id::text, fiscal_period_id::text, module from period_status where reopen_expires_at is not null and reopen_expires_at <= now() and status in ('open', 'on_hold') for update`)
		if err != nil {
			return err
		}
		type key struct{ entity, ledger, period, module string }
		var due []key
		for rows.Next() {
			var k key
			if err := rows.Scan(&k.entity, &k.ledger, &k.period, &k.module); err != nil {
				rows.Close()
				return err
			}
			due = append(due, k)
		}
		rows.Close()
		for _, k := range due {
			if _, err := tx.Exec(ctx, `update period_status set status = 'closed', reopen_expires_at = null, changed_by = 'reopen-expiry', changed_at = now()
				where legal_entity_id = $1 and ledger_id = $2 and fiscal_period_id = $3 and module = $4`, k.entity, k.ledger, k.period, k.module); err != nil {
				return err
			}
			if err := writePeriodChange(ctx, tx, tenantID, k.entity, k.ledger, k.period, domain.Module(k.module), domain.StatusOpen, domain.StatusClosed, "reopen-expiry", "reopen expired", nil); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
