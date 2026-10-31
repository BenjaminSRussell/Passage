package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
)

// ToleranceGroup is the write-off limit a manual clearing may use (FR-11.20,
// OBA0 semantics, BR-11.12): a debit residual up to DebitLimit, a credit
// residual up to CreditLimit and, when PercentLimit is above zero, up to that
// percentage of the selection's absolute total. The residual is posted to
// DifferenceAccount.
type ToleranceGroup struct {
	LedgerID          string    `json:"ledgerId"`
	Code              string    `json:"code"`
	Name              string    `json:"name"`
	DebitLimit        string    `json:"debitLimit"`
	CreditLimit       string    `json:"creditLimit"`
	PercentLimit      string    `json:"percentLimit"`
	DifferenceAccount string    `json:"differenceAccount"`
	Active            bool      `json:"active"`
	Version           int       `json:"version"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

const toleranceSelect = `select t.ledger_id::text, t.code, t.name, t.debit_limit::text, t.credit_limit::text, t.percent_limit::text, a.account_number,
		t.active, t.version, t.created_at, t.updated_at
	from tolerance_group t join main_account a on a.tenant_id = t.tenant_id and a.id = t.difference_account_id`

func scanTolerance(row pgx.Row) (ToleranceGroup, error) {
	var t ToleranceGroup
	err := row.Scan(&t.LedgerID, &t.Code, &t.Name, &t.DebitLimit, &t.CreditLimit, &t.PercentLimit, &t.DifferenceAccount, &t.Active, &t.Version,
		&t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func toleranceNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeToleranceNotFound, "tolerance group not found")
}

// ListToleranceGroups returns the tolerance groups of a ledger by code.
func (s *Postgres) ListToleranceGroups(ctx context.Context, ledgerID string) ([]ToleranceGroup, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []ToleranceGroup{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, toleranceSelect+` where t.tenant_id = $1 and t.ledger_id = $2 order by t.code`, p.TenantID, ledgerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTolerance(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// GetToleranceGroup returns one group or 404 LEDGER_TOLERANCE_GROUP_NOT_FOUND.
func (s *Postgres) GetToleranceGroup(ctx context.Context, ledgerID, code string) (ToleranceGroup, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return ToleranceGroup{}, err
	}
	var out ToleranceGroup
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		out, err = scanTolerance(tx.QueryRow(ctx, toleranceSelect+` where t.tenant_id = $1 and t.ledger_id = $2 and t.code = $3`, p.TenantID, ledgerID, code))
		if errors.Is(err, pgx.ErrNoRows) {
			return toleranceNotFound()
		}
		return err
	})
	return out, err
}

// PutToleranceGroup creates or replaces a group. A replace checks version when
// it is above zero (412 LEDGER_VERSION_CONFLICT). The difference account must
// exist in the ledger's chart and be postable.
func (s *Postgres) PutToleranceGroup(ctx context.Context, ledgerID, code string, version int, in ToleranceGroup) (ToleranceGroup, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return ToleranceGroup{}, false, err
	}
	var out ToleranceGroup
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false)
		if err != nil {
			return err
		}
		var accountID string
		var postable bool
		err = tx.QueryRow(ctx, `select id::text, is_postable from main_account where tenant_id = $1 and chart_code = $2 and account_number = $3`,
			p.TenantID, ledger.ChartCode, in.DifferenceAccount).Scan(&accountID, &postable)
		if errors.Is(err, pgx.ErrNoRows) {
			return oiViolation(CodeAccountNotFound, "differenceAccount", "account "+in.DifferenceAccount+" does not exist in chart "+ledger.ChartCode)
		}
		if err != nil {
			return err
		}
		if !postable {
			return oiViolation("LEDGER_ACCOUNT_NOT_POSTABLE", "differenceAccount", "account "+in.DifferenceAccount+" is not postable")
		}
		var current int
		err = tx.QueryRow(ctx, `select version from tolerance_group where tenant_id = $1 and ledger_id = $2 and code = $3 for update`,
			p.TenantID, ledgerID, code).Scan(&current)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			created = true
			_, err = tx.Exec(ctx, `insert into tolerance_group (tenant_id, legal_entity_id, ledger_id, code, name, debit_limit, credit_limit,
					percent_limit, difference_account_id, active)
				values ($1, $2, $3, $4, $5, $6::text::numeric, $7::text::numeric, $8::text::numeric, $9, $10)`,
				p.TenantID, ledger.LegalEntityID, ledgerID, code, in.Name, in.DebitLimit, in.CreditLimit, in.PercentLimit, accountID, in.Active)
		case err != nil:
			return err
		default:
			if version > 0 && version != current {
				return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
					"the tolerance group changed since it was read; reload and retry")
			}
			_, err = tx.Exec(ctx, `update tolerance_group set name = $4, debit_limit = $5::text::numeric, credit_limit = $6::text::numeric,
					percent_limit = $7::text::numeric, difference_account_id = $8, active = $9, version = version + 1, updated_at = now()
				where tenant_id = $1 and ledger_id = $2 and code = $3`,
				p.TenantID, ledgerID, code, in.Name, in.DebitLimit, in.CreditLimit, in.PercentLimit, accountID, in.Active)
		}
		if err != nil {
			return err
		}
		out, err = scanTolerance(tx.QueryRow(ctx, toleranceSelect+` where t.tenant_id = $1 and t.ledger_id = $2 and t.code = $3`, p.TenantID, ledgerID, code))
		return err
	})
	return out, created, err
}
