package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
)

const entityColumns = `entity, legal_entity_id::text, ledger_id, currency, country, fx_difference_account, output_tax_account, input_tax_account, tax_group`

func scanEntity(row pgx.Row) (domain.EntityRecord, error) {
	var e domain.EntityRecord
	err := row.Scan(&e.Entity, &e.LegalEntityID, &e.LedgerID, &e.Currency, &e.Country, &e.FXDifferenceAccount, &e.OutputTaxAccount, &e.InputTaxAccount, &e.TaxGroup)
	return e, err
}

// PutEntity stores or replaces the registry entry of an entity code.
func (s *Postgres) PutEntity(ctx context.Context, e domain.EntityRecord) error {
	if err := e.Validate(); err != nil {
		return err
	}
	pr, by, err := principalOf(ctx)
	if err != nil {
		return err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into ic_entity (tenant_id, entity, legal_entity_id, ledger_id, currency, country, fx_difference_account, output_tax_account, input_tax_account, tax_group, updated_by)
			values ($1, $2, $3::uuid, $4, $5, $6, $7, $8, $9, $10, $11)
			on conflict (tenant_id, entity) do update set legal_entity_id = excluded.legal_entity_id, ledger_id = excluded.ledger_id, currency = excluded.currency,
				country = excluded.country, fx_difference_account = excluded.fx_difference_account, output_tax_account = excluded.output_tax_account,
				input_tax_account = excluded.input_tax_account, tax_group = excluded.tax_group, updated_by = excluded.updated_by, updated_at = now()`,
			pr.TenantID, e.Entity, e.LegalEntityID, e.LedgerID, e.Currency, e.Country, e.FXDifferenceAccount, e.OutputTaxAccount, e.InputTaxAccount, e.TaxGroup, by)
		return err
	})
	return wrap("put intercompany entity", err)
}

// ListEntities returns the registry, ordered by entity code.
func (s *Postgres) ListEntities(ctx context.Context) ([]domain.EntityRecord, error) {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []domain.EntityRecord{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+entityColumns+` from ic_entity where tenant_id = $1 order by entity`, pr.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanEntity(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, wrap("list intercompany entities", err)
	}
	return out, nil
}

// ReplaceMatrix replaces the accounting matrix with the given rows in one transaction.
func (s *Postgres) ReplaceMatrix(ctx context.Context, m domain.Matrix) error {
	pr, by, err := principalOf(ctx)
	if err != nil {
		return err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		registered := map[string]bool{}
		rows, err := tx.Query(ctx, `select entity from ic_entity where tenant_id = $1`, pr.TenantID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var e string
			if err := rows.Scan(&e); err != nil {
				rows.Close()
				return err
			}
			registered[e] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if err := domain.ValidateMatrix(m, registered); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from ic_matrix_row where tenant_id = $1`, pr.TenantID); err != nil {
			return err
		}
		for _, r := range m {
			if _, err := tx.Exec(ctx, `insert into ic_matrix_row (tenant_id, entity_a, entity_b, due_from, due_to, updated_by) values ($1, $2, $3, $4, $5, $6)`,
				pr.TenantID, r.EntityA, r.EntityB, r.DueFrom, r.DueTo, by); err != nil {
				return err
			}
		}
		return nil
	})
	return wrap("replace intercompany matrix", err)
}

// GetMatrix returns the stored accounting matrix, ordered by entity pair.
func (s *Postgres) GetMatrix(ctx context.Context) (domain.Matrix, error) {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := domain.Matrix{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select entity_a, entity_b, due_from, due_to from ic_matrix_row where tenant_id = $1 order by entity_a, entity_b`, pr.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r domain.MatrixRow
			if err := rows.Scan(&r.EntityA, &r.EntityB, &r.DueFrom, &r.DueTo); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, wrap("get intercompany matrix", err)
	}
	return out, nil
}

const priceColumns = `id::text, lender, borrower, category, resource_role, method, markup_pct::text, unit_price::text, currency, effective_from, created_by, created_at`

func scanPrice(row pgx.Row) (domain.TransferPriceRecord, error) {
	var p domain.TransferPriceRecord
	var markup, unit string
	if err := row.Scan(&p.ID, &p.Lender, &p.Borrower, &p.Category, &p.Role, &p.Method, &markup, &unit, &p.Currency, &p.EffectiveFrom, &p.CreatedBy, &p.CreatedAt); err != nil {
		return p, err
	}
	var err error
	if p.MarkupPct, err = decimal.NewFromString(markup); err != nil {
		return p, fmt.Errorf("store: markup %q: %w", markup, err)
	}
	if p.UnitPrice, err = decimal.NewFromString(unit); err != nil {
		return p, fmt.Errorf("store: unit price %q: %w", unit, err)
	}
	return p, nil
}

// AddTransferPrice stores an effective-dated transfer price. A price for the same lender, borrower, category and role that is
// effective on the same date is IC-0854: prices are never edited, the next one takes over from its own date (BR-40.12).
func (s *Postgres) AddTransferPrice(ctx context.Context, p domain.TransferPrice) (domain.TransferPriceRecord, error) {
	if err := p.Validate(); err != nil {
		return domain.TransferPriceRecord{}, err
	}
	pr, by, err := principalOf(ctx)
	if err != nil {
		return domain.TransferPriceRecord{}, err
	}
	var out domain.TransferPriceRecord
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var missing string
		if err := tx.QueryRow(ctx, `select coalesce((select e from unnest($2::text[]) e where not exists (select 1 from ic_entity where tenant_id = $1 and entity = e) limit 1), '')`,
			pr.TenantID, []string{p.Lender, p.Borrower}).Scan(&missing); err != nil {
			return err
		}
		if missing != "" {
			return &domain.Error{Code: domain.CodeEntityUnknown, Detail: "entity " + missing + " is not in the registry"}
		}
		var err error
		out, err = scanPrice(tx.QueryRow(ctx, `insert into ic_transfer_price (tenant_id, id, lender, borrower, category, resource_role, method, markup_pct, unit_price, currency, effective_from, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9::numeric, $10, $11::date, $12) returning `+priceColumns,
			pr.TenantID, ids.New(), p.Lender, p.Borrower, p.Category, p.Role, p.Method, p.MarkupPct.String(), p.UnitPrice.String(), p.Currency, p.EffectiveFrom.Format(time.DateOnly), by))
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == uniqueViolation {
			return &domain.Error{Code: domain.CodeTransferPriceExist, Detail: fmt.Sprintf("a %s / %s price from %s to %s is already effective on %s; give the new price a later date",
				p.Category, p.Role, p.Lender, p.Borrower, p.EffectiveFrom.Format(time.DateOnly))}
		}
		return err
	})
	if err != nil {
		return domain.TransferPriceRecord{}, wrap("add intercompany transfer price", err)
	}
	return out, nil
}

// ListTransferPrices returns the stored prices, the newest effective date first; lender and borrower narrow the list when set.
func (s *Postgres) ListTransferPrices(ctx context.Context, lender, borrower string) ([]domain.TransferPriceRecord, error) {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []domain.TransferPriceRecord{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+priceColumns+` from ic_transfer_price
			where tenant_id = $1 and ($2::text = '' or lender = $2) and ($3::text = '' or borrower = $3)
			order by lender, borrower, category, resource_role, effective_from desc limit 2000`, pr.TenantID, lender, borrower)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPrice(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, wrap("list intercompany transfer prices", err)
	}
	return out, nil
}

// PricesFor returns every stored price of a lender and borrower, for ResolveTransferPrice.
func (s *Postgres) PricesFor(ctx context.Context, lender, borrower string) ([]domain.TransferPrice, error) {
	recs, err := s.ListTransferPrices(ctx, lender, borrower)
	if err != nil {
		return nil, err
	}
	out := make([]domain.TransferPrice, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.TransferPrice)
	}
	return out, nil
}

// GetSettings returns the tenant's charge settings, or the defaults when none were stored.
func (s *Postgres) GetSettings(ctx context.Context) (domain.Settings, error) {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return domain.Settings{}, err
	}
	out := domain.DefaultSettings
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var tol string
		err := tx.QueryRow(ctx, `select charge_settlement_mode, cross_border_tax, recon_currency, fx_tolerance_pct::text from ic_setting where tenant_id = $1`, pr.TenantID).
			Scan(&out.ChargeSettlementMode, &out.CrossBorderTax, &out.ReconCurrency, &tol)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		out.FXTolerancePct, err = decimal.NewFromString(tol)
		return err
	})
	if err != nil {
		return domain.Settings{}, wrap("get intercompany settings", err)
	}
	return out, nil
}

// PutSettings stores the tenant's charge settings.
func (s *Postgres) PutSettings(ctx context.Context, st domain.Settings) error {
	if err := st.Validate(); err != nil {
		return err
	}
	pr, by, err := principalOf(ctx)
	if err != nil {
		return err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into ic_setting (tenant_id, charge_settlement_mode, cross_border_tax, recon_currency, fx_tolerance_pct, updated_by) values ($1, $2, $3, $4, $5::numeric, $6)
			on conflict (tenant_id) do update set charge_settlement_mode = excluded.charge_settlement_mode, cross_border_tax = excluded.cross_border_tax,
				recon_currency = excluded.recon_currency, fx_tolerance_pct = excluded.fx_tolerance_pct, updated_by = excluded.updated_by, updated_at = now()`,
			pr.TenantID, st.ChargeSettlementMode, st.CrossBorderTax, st.ReconCurrency, st.FXTolerancePct.String(), by)
		return err
	})
	return wrap("put intercompany settings", err)
}
