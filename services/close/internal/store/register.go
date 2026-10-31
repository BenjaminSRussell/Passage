package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

func (s *Postgres) RegisterPeriod(ctx context.Context, in domain.PeriodRegistration) (bool, error) {
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, in.LedgerID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into ledger_owner(tenant_id, ledger_id, legal_entity_id) values(current_setting('passage.tenant_id')::uuid, $1, $2) on conflict do nothing`, in.LedgerID, in.LegalEntityID); err != nil {
			return err
		}
		var owner string
		if err := tx.QueryRow(ctx, `select legal_entity_id::text from ledger_owner where ledger_id = $1`, in.LedgerID).Scan(&owner); err != nil {
			return err
		}
		if !strings.EqualFold(owner, in.LegalEntityID) {
			return httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the ledger belongs to another legal entity")
		}
		var overlapping int
		if err := tx.QueryRow(ctx, `select count(distinct fiscal_period_id) from period_status where legal_entity_id = $1 and ledger_id = $2 and fiscal_period_id <> $3 and start_date <= $5 and end_date >= $4`,
			in.LegalEntityID, in.LedgerID, in.FiscalPeriodID, in.Start, in.End).Scan(&overlapping); err != nil {
			return err
		}
		if overlapping > 0 {
			return &domain.Error{Code: domain.CodeInvalidTransition, Detail: "the dates overlap another registered period of this ledger"}
		}
		var rows, moved int
		if err := tx.QueryRow(ctx, `select count(*), count(*) filter (where start_date is not null and (start_date <> $4 or end_date <> $5)) from period_status where legal_entity_id = $1 and ledger_id = $2 and fiscal_period_id = $3`,
			in.LegalEntityID, in.LedgerID, in.FiscalPeriodID, in.Start, in.End).Scan(&rows, &moved); err != nil {
			return err
		}
		switch {
		case rows == 0:
			created = true
			_, err := tx.Exec(ctx, `insert into period_status(tenant_id, legal_entity_id, ledger_id, fiscal_period_id, module, status, start_date, end_date, changed_by) values(current_setting('passage.tenant_id')::uuid, $1, $2, $3, 'all', 'open', $4, $5, $6)`,
				in.LegalEntityID, in.LedgerID, in.FiscalPeriodID, in.Start, in.End, in.Actor)
			return err
		case moved > 0:
			return &domain.Error{Code: domain.CodeInvalidTransition, Detail: "the period is registered with other dates"}
		default:
			_, err := tx.Exec(ctx, `update period_status set start_date = $4, end_date = $5 where legal_entity_id = $1 and ledger_id = $2 and fiscal_period_id = $3 and start_date is null`,
				in.LegalEntityID, in.LedgerID, in.FiscalPeriodID, in.Start, in.End)
			return err
		}
	})
	if err != nil {
		var de *domain.Error
		var p *httpx.Problem
		var pg *pgconn.PgError
		switch {
		case errors.As(err, &de), errors.As(err, &p):
			return false, err
		case errors.As(err, &pg) && pg.Code == "23505":
			return false, &domain.Error{Code: domain.CodeInvalidTransition, Detail: "the period is already registered"}
		}
		return false, fmt.Errorf("register period: %w", err)
	}
	return created, nil
}
