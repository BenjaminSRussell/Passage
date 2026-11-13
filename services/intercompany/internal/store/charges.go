package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	icevents "github.com/BenjaminSRussell/passage/services/intercompany/internal/events"
)

const chargeColumns = `id::text, source_id, version, lender, borrower, category, resource_role, transaction_date, quantity::text, unit_cost::text, currency,
	status, coalesce(method, ''), coalesce(cost::text, ''), coalesce(price::text, ''), coalesce(margin::text, ''), coalesce(journal_id::text, ''),
	coalesce(failure_code, ''), settlement_mode, tax_amount::text, tax_code, tax_recorded, created_by, created_at`

func scanCharge(row pgx.Row) (domain.ChargeRecord, error) {
	var c domain.ChargeRecord
	var status, qty, unit, cost, price, margin, tax string
	err := row.Scan(&c.ID, &c.SourceID, &c.Version, &c.Lender, &c.Borrower, &c.Category, &c.Role, &c.TransactionDate, &qty, &unit, &c.Currency,
		&status, &c.Method, &cost, &price, &margin, &c.JournalID, &c.FailureCode, &c.SettlementMode, &tax, &c.TaxCode, &c.TaxRecorded, &c.CreatedBy, &c.CreatedAt)
	if err != nil {
		return c, err
	}
	c.Status = domain.ChargeStatus(status)
	for _, f := range []struct {
		dst *decimal.Decimal
		raw string
	}{{&c.Quantity, qty}, {&c.UnitCost, unit}, {&c.Cost, cost}, {&c.Price, price}, {&c.Margin, margin}, {&c.TaxAmount, tax}} {
		if f.raw == "" {
			continue
		}
		if *f.dst, err = decimal.NewFromString(f.raw); err != nil {
			return c, fmt.Errorf("store: charge amount %q: %w", f.raw, err)
		}
	}
	return c, nil
}

func errChargeNotFound(id string) error {
	return &domain.Error{Code: domain.CodeChargeNotFound, Detail: "no intercompany charge with id " + id}
}

// BeginCharge stores a charge request as pending, or returns the one stored
// for the same source and version (existing=true). A source and version
// submitted again with a different request fails with IC-0841.
func (s *Postgres) BeginCharge(ctx context.Context, c domain.ChargeRecord, hash string) (domain.ChargeRecord, bool, error) {
	pr, by, err := principalOf(ctx)
	if err != nil {
		return domain.ChargeRecord{}, false, err
	}
	var (
		out      domain.ChargeRecord
		existing bool
	)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id := ids.New()
		tag, err := tx.Exec(ctx, `insert into ic_charge_request (tenant_id, id, source_id, version, request_hash, lender, borrower, category, resource_role,
				transaction_date, quantity, unit_cost, currency, status, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::numeric, $12::numeric, $13, 'pending', $14)
			on conflict (tenant_id, source_id, version) do nothing`,
			pr.TenantID, id, c.SourceID, c.Version, hash, c.Lender, c.Borrower, c.Category, c.Role, c.TransactionDate, c.Quantity.String(), c.UnitCost.String(), c.Currency, by)
		if err != nil {
			return err
		}
		where := id
		if tag.RowsAffected() == 0 {
			var stored string
			if err := tx.QueryRow(ctx, `select request_hash, id::text from ic_charge_request where tenant_id = $1 and source_id = $2 and version = $3`,
				pr.TenantID, c.SourceID, c.Version).Scan(&stored, &where); err != nil {
				return err
			}
			if stored != hash {
				return &domain.Error{Code: domain.CodeIdempotencyReuse, Detail: fmt.Sprintf("charge %s version %d was already submitted with a different request; send the next version", c.SourceID, c.Version)}
			}
			existing = true
		}
		out, err = scanCharge(tx.QueryRow(ctx, `select `+chargeColumns+` from ic_charge_request where tenant_id = $1 and id = $2::uuid`, pr.TenantID, where))
		return err
	})
	if err != nil {
		return domain.ChargeRecord{}, false, wrap("begin intercompany charge", err)
	}
	return out, existing, nil
}

// FinishCharge records the outcome of a pending charge: posted with its price
// and journal, compensated when the journal was reversed, or on suspense when
// no transfer price applied. It publishes charge.posted.v1 for a posted charge.
// A charge that is no longer pending is returned unchanged.
func (s *Postgres) FinishCharge(ctx context.Context, id string, status domain.ChargeStatus, method string, p domain.ChargePricing, journalID, failureCode string, extra domain.ChargeExtra) (domain.ChargeRecord, error) {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return domain.ChargeRecord{}, err
	}
	var out domain.ChargeRecord
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var cost, price, margin any
		if status != domain.ChargeSuspense {
			cost, price, margin = p.Cost.Value.String(), p.Price.Value.String(), p.Margin.Value.String()
		}
		var journal any
		if journalID != "" {
			journal = journalID
		}
		mode := extra.SettlementMode
		if mode == "" {
			mode = domain.SettlementJournal
		}
		tag, err := tx.Exec(ctx, `update ic_charge_request set status = $3, method = nullif($4, ''), cost = $5::numeric, price = $6::numeric, margin = $7::numeric,
				journal_id = $8::uuid, failure_code = nullif($9, ''), settlement_mode = $10, tax_amount = $11::numeric, tax_code = $12, updated_at = now()
			where tenant_id = $1 and id = $2::uuid and status = 'pending'`, pr.TenantID, id, string(status), method, cost, price, margin, journal, failureCode,
			mode, extra.TaxAmount.String(), extra.TaxCode)
		if err != nil {
			return err
		}
		out, err = scanCharge(tx.QueryRow(ctx, `select `+chargeColumns+` from ic_charge_request where tenant_id = $1 and id = $2::uuid`, pr.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return errChargeNotFound(id)
		}
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if status == domain.ChargeInvoiceRequested {
			data := icevents.ChargeInvoiceRequestedData{
				ID: out.ID, SourceID: out.SourceID, Version: out.Version, Lender: out.Lender, Borrower: out.Borrower, Category: out.Category, Role: out.Role,
				Date: out.TransactionDate.Format("2006-01-02"), Currency: out.Currency, Cost: out.Cost.String(), Price: out.Price.String(), Margin: out.Margin.String(),
				Tax: out.TaxAmount.String(), TaxCode: out.TaxCode,
			}
			// The invoice documents are raised in the two legal entities for the pairing's customer and vendor accounts (#40).
			if err := tx.QueryRow(ctx, `select coalesce((select legal_entity_id::text from ic_entity where entity = $1), ''),
					coalesce((select legal_entity_id::text from ic_entity where entity = $2), ''),
					coalesce((select customer_account from intercompany_partner where seller_entity = $1 and buyer_entity = $2 and active order by version desc limit 1), ''),
					coalesce((select vendor_account from intercompany_partner where seller_entity = $1 and buyer_entity = $2 and active order by version desc limit 1), '')`,
				out.Lender, out.Borrower).Scan(&data.LenderLegalEntityID, &data.BorrowerLegalEntityID, &data.CustomerAccount, &data.VendorAccount); err != nil {
				return err
			}
			return enqueue(ctx, tx, "ic_charge", out.ID, icevents.ChargeInvoiceRequested, pr.TenantID, data)
		}
		if status != domain.ChargePosted {
			return nil
		}
		return enqueue(ctx, tx, "ic_charge", out.ID, icevents.ChargePosted, pr.TenantID, icevents.ChargePostedData{
			ID: out.ID, SourceID: out.SourceID, Version: out.Version, Lender: out.Lender, Borrower: out.Borrower, Currency: out.Currency,
			Cost: out.Cost.String(), Price: out.Price.String(), Margin: out.Margin.String(), JournalID: out.JournalID,
		})
	})
	if err != nil {
		return domain.ChargeRecord{}, wrap("finish intercompany charge", err)
	}
	return out, nil
}

// GetCharge returns one charge, or IC-0848.
func (s *Postgres) GetCharge(ctx context.Context, id string) (domain.ChargeRecord, error) {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return domain.ChargeRecord{}, err
	}
	var out domain.ChargeRecord
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanCharge(tx.QueryRow(ctx, `select `+chargeColumns+` from ic_charge_request where tenant_id = $1 and id = $2::uuid`, pr.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return errChargeNotFound(id)
		}
		return err
	})
	if err != nil {
		return domain.ChargeRecord{}, wrap("get intercompany charge", err)
	}
	return out, nil
}

// ChargeFilter narrows a charge listing; empty fields match everything.
type ChargeFilter struct {
	Status   string
	SourceID string
}

// ChargePage is one page of charges, newest first.
type ChargePage struct {
	Items []domain.ChargeRecord
	Next  string
}

// ListCharges returns one page of charges, newest first, after the cursor (the id of the last charge of the previous page).
func (s *Postgres) ListCharges(ctx context.Context, f ChargeFilter, after string, limit int) (ChargePage, error) {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return ChargePage{}, err
	}
	page := ChargePage{Items: []domain.ChargeRecord{}}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+chargeColumns+` from ic_charge_request
			where tenant_id = $1
			  and ($2::text = '' or status = $2)
			  and ($3::text = '' or source_id = $3)
			  and ($4::text = '' or (created_at, id) < (select created_at, id from ic_charge_request where tenant_id = $1 and id = $4::uuid))
			order by created_at desc, id desc limit $5`, pr.TenantID, f.Status, f.SourceID, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCharge(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, c)
		}
		return rows.Err()
	})
	if err != nil {
		return ChargePage{}, wrap("list intercompany charges", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.Next = page.Items[limit-1].ID
	}
	return page, nil
}

// MarkTaxRecorded notes that the tax service has the transaction of a posted cross-border charge.
func (s *Postgres) MarkTaxRecorded(ctx context.Context, id string) error {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update ic_charge_request set tax_recorded = true, updated_at = now() where tenant_id = $1 and id = $2::uuid`, pr.TenantID, id)
		return err
	})
	return wrap("mark intercompany charge tax recorded", err)
}
