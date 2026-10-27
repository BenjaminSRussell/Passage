package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

const agreementColumns = `id, relation, account_code, coalesce(account_id::text, ''), item_code, coalesce(item_id::text, ''),
	currency, unit, from_qty, to_qty, price, price_unit, valid_from, valid_to, consignment, status`

// CodeTradeAgreementOverlap is BR-17.3: an ACTIVE row with the same account,
// item, currency, unit and valid_from whose quantity break overlaps.
const CodeTradeAgreementOverlap = "PRC-TA-002"

// PriceFilter selects the trade agreement rows that could price a line: the
// relation, currency and unit, valid on Date, for the vendor or one of its
// groups (or all vendors) and the product or one of its groups (or all products).
type PriceFilter struct {
	Relation        string
	Currency        string
	Unit            string
	Date            domain.Day
	VendorID        string
	VendorGroupIDs  []string
	ProductID       string
	ProductGroupIDs []string
}

// TradeAgreementFilter narrows ListTradeAgreements; empty fields do not filter.
// AccountID and ItemID match the row's account_id and item_id exactly.
type TradeAgreementFilter struct {
	AccountID string
	ItemID    string
	Status    string
}

// CreateTradeAgreement stores an ACTIVE price row after the BR-17.3 overlap
// check. A replayed Idempotency-Key returns the row created by the first request.
func (s *Postgres) CreateTradeAgreement(ctx context.Context, row domain.TradeAgreement, idem Idem) (domain.TradeAgreement, error) {
	p, _ := tenant.FromContext(ctx)
	row.ID = ids.New()
	var out domain.TradeAgreement
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, row.ID)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadTradeAgreement(ctx, tx, existing)
			return err
		}
		// Serialize activations of the same account/item key so two concurrent
		// creates cannot both pass the overlap check.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`,
			p.TenantID+"|"+row.Relation+"|"+row.AccountCode+"|"+row.AccountID+"|"+row.ItemCode+"|"+row.ItemID); err != nil {
			return err
		}
		var clash string
		err = tx.QueryRow(ctx, `select id::text from trade_agreement
			where tenant_id = $1 and status = 'ACTIVE' and relation = $2 and account_code = $3
			and coalesce(account_id::text, '') = $4 and item_code = $5 and coalesce(item_id::text, '') = $6
			and currency = $7 and unit = $8 and valid_from = $9 and consignment = $10
			and from_qty < coalesce($11::numeric, 'Infinity'::numeric) and $12::numeric < coalesce(to_qty, 'Infinity'::numeric)
			limit 1`,
			p.TenantID, row.Relation, row.AccountCode, row.AccountID, row.ItemCode, row.ItemID, row.Currency, row.Unit,
			row.ValidFrom.Time, row.Consignment, row.ToQty, row.FromQty).Scan(&clash)
		switch {
		case err == nil:
			return httpx.NewProblem(http.StatusConflict, CodeTradeAgreementOverlap, fmt.Sprintf(
				"overlapping trade agreement row %s with equal specificity for %s/%s from %s", clash, levelName(row.AccountCode, row.AccountID),
				levelName(row.ItemCode, row.ItemID), row.ValidFrom))
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		if _, err := tx.Exec(ctx, `insert into trade_agreement (tenant_id, id, relation, account_code, account_id, item_code, item_id,
			currency, unit, from_qty, to_qty, price, price_unit, valid_from, valid_to, consignment, status)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
			p.TenantID, row.ID, row.Relation, row.AccountCode, nullable(row.AccountID), row.ItemCode, nullable(row.ItemID),
			row.Currency, row.Unit, row.FromQty, row.ToQty, row.Price, row.PriceUnit, row.ValidFrom.Time, optDay(row.ValidTo),
			row.Consignment, row.Status); err != nil {
			return err
		}
		out, err = loadTradeAgreement(ctx, tx, row.ID)
		return err
	})
	return out, err
}

func levelName(code, id string) string {
	if id == "" {
		return code
	}
	return code + " " + id
}

// GetTradeAgreement returns one trade agreement row.
func (s *Postgres) GetTradeAgreement(ctx context.Context, id string) (domain.TradeAgreement, error) {
	var out domain.TradeAgreement
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = loadTradeAgreement(ctx, tx, id)
		return err
	})
	return out, err
}

// ListTradeAgreements returns rows newest first, after the cursor id.
func (s *Postgres) ListTradeAgreements(ctx context.Context, f TradeAgreementFilter, after string, limit int) (Page[domain.TradeAgreement], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var items []domain.TradeAgreement
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+agreementColumns+` from trade_agreement
			where tenant_id = $1 and ($2 = '' or account_id::text = $2) and ($3 = '' or item_id::text = $3) and ($4 = '' or status = $4)
			and ($5::uuid is null or id < $5::uuid)
			order by id desc limit $6`, p.TenantID, f.AccountID, f.ItemID, f.Status, nullable(after), limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.TradeAgreement, error) { return scanAgreement(row) })
		return err
	})
	if err != nil {
		return Page[domain.TradeAgreement]{}, err
	}
	if items == nil {
		items = []domain.TradeAgreement{}
	}
	return pageOf(items, limit, func(a domain.TradeAgreement) string { return a.ID }), nil
}

// PriceCandidates loads the active, non-consignment rows that could price a
// line for the filter. domain.Resolve applies the quantity break and ranks them.
func (s *Postgres) PriceCandidates(ctx context.Context, f PriceFilter) ([]domain.TradeAgreement, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.TradeAgreement{}
	vendorGroups, productGroups := uuidList(f.VendorGroupIDs), uuidList(f.ProductGroupIDs)
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+agreementColumns+` from trade_agreement
			where tenant_id = $1 and relation = $2 and currency = $3 and unit = $4 and status = 'ACTIVE' and consignment = false
			and valid_from <= $5 and (valid_to is null or valid_to >= $5)
			and (account_code = 'ALL' or (account_code = 'VENDOR' and account_id = $6::uuid)
			     or (account_code = 'GROUP' and account_id = any($7::uuid[])))
			and (item_code = 'ALL' or (item_code = 'PRODUCT' and item_id = $8::uuid)
			     or (item_code = 'GROUP' and item_id = any($9::uuid[])))
			order by id`,
			p.TenantID, f.Relation, f.Currency, f.Unit, f.Date.Time, nullable(f.VendorID), vendorGroups, nullable(f.ProductID), productGroups)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAgreement(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// uuidList keeps the valid UUIDs of ids so the uuid[] cast never fails.
func uuidList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, id := range in {
		if ids.Valid(id) {
			out = append(out, id)
		}
	}
	return out
}

func loadTradeAgreement(ctx context.Context, tx pgx.Tx, id string) (domain.TradeAgreement, error) {
	p, _ := tenant.FromContext(ctx)
	row := tx.QueryRow(ctx, `select `+agreementColumns+` from trade_agreement where tenant_id = $1 and id = $2`, p.TenantID, id)
	a, err := scanAgreement(row)
	return a, notFound(err, CodeTradeAgreementNotFound, "trade agreement not found")
}

type scanner interface {
	Scan(dest ...any) error
}

func scanAgreement(row scanner) (domain.TradeAgreement, error) {
	var a domain.TradeAgreement
	var toQty *decimal.Decimal
	var validFrom time.Time
	var validTo *time.Time
	err := row.Scan(&a.ID, &a.Relation, &a.AccountCode, &a.AccountID, &a.ItemCode, &a.ItemID, &a.Currency, &a.Unit,
		&a.FromQty, &toQty, &a.Price, &a.PriceUnit, &validFrom, &validTo, &a.Consignment, &a.Status)
	if err != nil {
		return domain.TradeAgreement{}, err
	}
	a.ToQty = toQty
	a.ValidFrom = day(validFrom)
	if validTo != nil {
		d := day(*validTo)
		a.ValidTo = &d
	}
	return a, nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optDay(d *domain.Day) *time.Time {
	if d == nil {
		return nil
	}
	t := d.Time
	return &t
}
