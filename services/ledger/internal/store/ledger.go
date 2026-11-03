package store

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// ReconNone is the default reconciliation account type.
const ReconNone = "none"

const accountColumns = `id, chart_code, account_number, name, account_type, is_postable, suspended_from::text, reconciliation_account_type`

func scanAccount(row pgx.Row, extra ...any) (Account, error) {
	var a Account
	err := row.Scan(append([]any{&a.ID, &a.ChartCode, &a.Number, &a.Name, &a.Type, &a.Postable, &a.SuspendedFrom, &a.ReconType}, extra...)...)
	return a, err
}

// PutAccount creates or replaces the account number in chart. created is true
// when the row was inserted.
func (s *Postgres) PutAccount(ctx context.Context, chartCode, number string, in Account) (Account, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return Account{}, false, err
	}
	if in.ReconType == "" {
		in.ReconType = ReconNone
	}
	var out Account
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `insert into main_account (tenant_id, id, chart_code, account_number, name, account_type, is_postable, suspended_from, reconciliation_account_type)
			values ($1, $2, $3, $4, $5, $6, $7, nullif($8::text, '')::date, $9)
			on conflict (tenant_id, chart_code, account_number) do update set name = excluded.name, account_type = excluded.account_type,
				is_postable = excluded.is_postable, suspended_from = excluded.suspended_from,
				reconciliation_account_type = excluded.reconciliation_account_type, updated_at = now()
			returning `+accountColumns+`, (xmax = 0)`,
			p.TenantID, ids.New(), chartCode, number, in.Name, in.Type, in.Postable, derefString(in.SuspendedFrom), in.ReconType)
		var scanErr error
		out, scanErr = scanAccount(row, &created)
		return scanErr
	})
	if err != nil {
		return Account{}, false, err
	}
	return out, created, nil
}

// ListAccounts returns accounts of chart in account-number order after the cursor.
func (s *Postgres) ListAccounts(ctx context.Context, chartCode, afterNumber string, limit int) ([]Account, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	items := []Account{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+accountColumns+` from main_account
			where tenant_id = $1 and chart_code = $2 and account_number > $3 order by account_number limit $4`,
			p.TenantID, chartCode, afterNumber, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAccount(rows)
			if err != nil {
				return err
			}
			items = append(items, a)
		}
		return rows.Err()
	})
	return items, err
}

// CreateLedger inserts a ledger. A duplicate code is 409.
func (s *Postgres) CreateLedger(ctx context.Context, in Ledger) (Ledger, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return Ledger{}, err
	}
	in.ID = ids.New()
	if in.DimensionAttributes == nil {
		in.DimensionAttributes = []string{}
	}
	var out Ledger
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var scanErr error
		out, scanErr = scanLedger(tx.QueryRow(ctx, `insert into ledger (tenant_id, id, legal_entity_id, code, name, chart_code, currency, dimension_attributes,
			first_open_date, future_posting_days, reversal_reason_required, negative_posting_enabled)
			values ($1, $2, $3, $4, $5, $6, $7, $8, nullif($9::text, '')::date, $10, $11, $12)
			returning `+ledgerFields,
			p.TenantID, in.ID, in.LegalEntityID, in.Code, in.Name, in.ChartCode, in.Currency, in.DimensionAttributes,
			derefString(in.FirstOpenDate), in.FuturePostingDays, in.ReversalReasonRequired, in.NegativePostingEnabled))
		return scanErr
	})
	if isUnique(err) {
		return Ledger{}, httpx.NewProblem(http.StatusConflict, "LEDGER_CODE_DUPLICATE", "a ledger with this code already exists")
	}
	return out, err
}

// GetLedger returns one ledger visible to the caller.
func (s *Postgres) GetLedger(ctx context.Context, id string) (Ledger, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return Ledger{}, err
	}
	var l Ledger
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		l, err = loadLedger(ctx, tx, p.TenantID, id, false)
		return err
	})
	return l, err
}

// LedgerSettings are the optional accounting properties of a ledger (FR-12.7). A nil field keeps the
// stored value; an empty string clears it.
type LedgerSettings struct {
	CalendarCode        *string
	AccountingPrinciple *string
	ReportingCurrency   *string
	// AddDimensionAttributes appends dimension attributes the ledger does not
	// have yet (never removes or reorders one); at most 20 in all.
	AddDimensionAttributes []string
}

func (s *Postgres) UpdateLedgerSettings(ctx context.Context, id string, version int, in LedgerSettings) (Ledger, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return Ledger{}, err
	}
	var out Ledger
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := loadLedger(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if version > 0 && cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the ledger changed since it was read; reload it and try again")
		}
		added := 0
		for _, a := range in.AddDimensionAttributes {
			if !slices.Contains(cur.DimensionAttributes, a) {
				added++
			}
		}
		if len(cur.DimensionAttributes)+added > domain.MaxDimensionAttributes {
			return httpx.NewProblem(http.StatusBadRequest, "LEDGER_DIMENSION_LIMIT", fmt.Sprintf("a ledger holds at most %d dimension attributes", domain.MaxDimensionAttributes))
		}
		out, err = scanLedger(tx.QueryRow(ctx, `update ledger set calendar_code = case when $3::text is null then calendar_code else nullif($3, '') end,
				accounting_principle = case when $4::text is null then accounting_principle else nullif($4, '') end,
				reporting_currency = case when $5::text is null then reporting_currency else nullif($5, '') end,
				dimension_attributes = dimension_attributes || array(select a from unnest($6::text[]) with ordinality as t(a, n) where not a = any(dimension_attributes) group by a, n order by n),
				settings_version = settings_version + 1
			where tenant_id = $1 and id = $2 returning `+ledgerFields, p.TenantID, id, in.CalendarCode, in.AccountingPrinciple, in.ReportingCurrency, in.AddDimensionAttributes))
		if err != nil {
			return err
		}
		return enqueueFor(ctx, tx, "ledger", id, ledgerevents.RulesChanged, p.TenantID, ledgerevents.RulesChangedData{
			LedgerID: id, Kind: "ledger_settings", ID: id, Version: out.Version, Actor: p.UserID})
	})
	return out, err
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
