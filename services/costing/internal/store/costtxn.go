package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
)

// Cost transaction problem codes (FR-20.4). CST-CTX-001: a source key already
// recorded with other content. CST-CTX-002: a currency other than the
// valuation ledger's (a ledger values in one currency, FR-20.15).
const (
	CodeCostTxnConflict = "CST-CTX-001"
	CodeCostTxnCurrency = "CST-CTX-002"
	// MaxCostTransactionBatch bounds one record call; producers page larger feeds.
	MaxCostTransactionBatch = 500
	costTxnColumns          = `id::text, source_key, legal_entity_id::text, coalesce(inventory_transaction_id::text, ''),
		coalesce(ledger_entry_id::text, ''), product_id::text, site_id::text, coalesce(warehouse_id::text, ''), valuation_type,
		coalesce(valuation_ledger_id::text, ''), kind, qty::text, amount_acct::text, currency, financial_date::text, created_at`
)

// CostTransaction is one value-affecting event of the value report (FR-20.4).
// Quantities and amounts are decimal strings; the amount is in the valuation
// ledger's currency. An empty ValuationLedgerID is the primary valuation: a row
// recorded with the primary ledger's id is stored, and read back, without it.
type CostTransaction struct {
	ID                     string    `json:"id"`
	SourceKey              string    `json:"sourceKey"`
	LegalEntityID          string    `json:"legalEntityId"`
	InventoryTransactionID string    `json:"inventoryTransactionId,omitempty"`
	LedgerEntryID          string    `json:"ledgerEntryId,omitempty"`
	ProductID              string    `json:"productId"`
	SiteID                 string    `json:"siteId"`
	WarehouseID            string    `json:"warehouseId,omitempty"`
	ValuationType          string    `json:"valuationType,omitempty"`
	ValuationLedgerID      string    `json:"valuationLedgerId,omitempty"`
	Kind                   string    `json:"kind"`
	Qty                    string    `json:"qty"`
	Amount                 string    `json:"amount"`
	Currency               string    `json:"currency"`
	FinancialDate          string    `json:"financialDate"`
	CreatedAt              time.Time `json:"createdAt"`
}

// sameContent compares everything a producer sends; decimals compare by value.
func (c CostTransaction) sameContent(o CostTransaction) bool {
	qa, errA := decimal.NewFromString(c.Qty)
	qb, errB := decimal.NewFromString(o.Qty)
	aa, errC := decimal.NewFromString(c.Amount)
	ab, errD := decimal.NewFromString(o.Amount)
	if errA != nil || errB != nil || errC != nil || errD != nil {
		return false
	}
	return c.LegalEntityID == o.LegalEntityID && c.InventoryTransactionID == o.InventoryTransactionID &&
		c.LedgerEntryID == o.LedgerEntryID && c.ProductID == o.ProductID && c.SiteID == o.SiteID &&
		c.WarehouseID == o.WarehouseID && c.ValuationType == o.ValuationType && c.ValuationLedgerID == o.ValuationLedgerID &&
		c.Kind == o.Kind && qa.Equal(qb) && aa.Equal(ab) && c.Currency == o.Currency && c.FinancialDate == o.FinancialDate
}

// BalanceQuery selects the cost transactions summed into the value report: one
// legal entity and valuation ledger ("" for the primary), financial date up to
// and including AsOf, optionally one site.
type BalanceQuery struct {
	LegalEntityID     string
	ValuationLedgerID string
	AsOf              string
	SiteID            string
}

// CostTxnFilter selects cost transactions for the drill-down list. Empty fields
// do not filter, except ValuationLedgerID, where "" is the primary valuation.
type CostTxnFilter struct {
	LegalEntityID     string
	ValuationLedgerID string
	ProductID         string
	SiteID            string
	Kind              string
	AsOf              string
}

// CostTxnPage is one page of cost transactions, newest financial date first.
type CostTxnPage struct {
	Items []CostTransaction
	Next  string
}

func scanCostTxn(row pgx.Row) (CostTransaction, error) {
	var c CostTransaction
	err := row.Scan(&c.ID, &c.SourceKey, &c.LegalEntityID, &c.InventoryTransactionID, &c.LedgerEntryID, &c.ProductID,
		&c.SiteID, &c.WarehouseID, &c.ValuationType, &c.ValuationLedgerID, &c.Kind, &c.Qty, &c.Amount, &c.Currency,
		&c.FinancialDate, &c.CreatedAt)
	return c, err
}

type ledgerRef struct {
	legalEntityID, currency string
	primary                 bool
}

// RecordCostTransactions appends the batch in one transaction and returns the
// number of rows inserted. A source key already recorded with the same content
// is a replay and is skipped, so a repeated feed never double-counts; with other
// content the whole batch is refused (CST-CTX-001). A named valuation ledger must
// belong to the row's legal entity (CST-VLG-005), and every row is in its
// ledger's currency (CST-CTX-002): the primary ledger's, or, before any primary
// ledger exists, the currency of the entity's first default rows.
func (s *Postgres) RecordCostTransactions(ctx context.Context, in []CostTransaction) (int, error) {
	p, _ := tenant.FromContext(ctx)
	rows := append([]CostTransaction(nil), in...)
	inserted := 0
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := resolveLedgers(ctx, tx, p.TenantID, rows); err != nil {
			return err
		}
		n := len(rows)
		cols := make([][]string, 14)
		for i := range cols {
			cols[i] = make([]string, n)
		}
		for i, c := range rows {
			for j, v := range []string{ids.New(), c.SourceKey, c.LegalEntityID, c.InventoryTransactionID, c.LedgerEntryID, c.ProductID,
				c.SiteID, c.WarehouseID, c.ValuationType, c.ValuationLedgerID, c.Kind, c.Qty, c.Amount, c.Currency} {
				cols[j][i] = v
			}
		}
		dates := make([]string, n)
		for i, c := range rows {
			dates[i] = c.FinancialDate
		}
		res, err := tx.Query(ctx, `insert into cost_transaction (tenant_id, id, source_key, legal_entity_id, inventory_transaction_id,
				ledger_entry_id, product_id, site_id, warehouse_id, valuation_type, valuation_ledger_id, kind, qty, amount_acct,
				currency, financial_date)
			select $1, i.id::uuid, i.sk, i.le::uuid, nullif(i.inv, '')::uuid, nullif(i.je, '')::uuid, i.pr::uuid, i.si::uuid,
				nullif(i.wh, '')::uuid, i.vt, nullif(i.vl, '')::uuid, i.kind, i.qty::numeric, i.amt::numeric, i.cur, i.fd::date
			from unnest($2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[], $9::text[], $10::text[],
				$11::text[], $12::text[], $13::text[], $14::text[], $15::text[], $16::text[])
				as i(id, sk, le, inv, je, pr, si, wh, vt, vl, kind, qty, amt, cur, fd)
			on conflict (tenant_id, source_key) do nothing
			returning source_key`,
			p.TenantID, cols[0], cols[1], cols[2], cols[3], cols[4], cols[5], cols[6], cols[7], cols[8], cols[9], cols[10],
			cols[11], cols[12], cols[13], dates)
		if err != nil {
			return err
		}
		fresh := map[string]bool{}
		for res.Next() {
			var key string
			if err := res.Scan(&key); err != nil {
				res.Close()
				return err
			}
			fresh[key] = true
		}
		res.Close()
		if err := res.Err(); err != nil {
			return err
		}
		inserted = len(fresh)
		return checkReplays(ctx, tx, p.TenantID, rows, fresh)
	})
	if err != nil {
		return 0, err
	}
	return inserted, nil
}

// resolveLedgers checks each row's ledger and currency and stores the primary
// ledger's rows without a ledger id. Default rows of an entity without a primary
// ledger take an advisory lock, so two first batches cannot pick two currencies.
func resolveLedgers(ctx context.Context, tx pgx.Tx, tenantID string, rows []CostTransaction) error {
	entities := map[string]bool{}
	for _, c := range rows {
		entities[c.LegalEntityID] = true
	}
	entityList := make([]string, 0, len(entities))
	for le := range entities {
		entityList = append(entityList, le)
	}
	sort.Strings(entityList)
	ledgers := map[string]ledgerRef{}
	primary := map[string]string{} // legal entity -> primary ledger currency
	lr, err := tx.Query(ctx, `select id::text, legal_entity_id::text, currency, is_primary from valuation_ledger
		where tenant_id = $1 and legal_entity_id = any($2::uuid[])`, tenantID, entityList)
	if err != nil {
		return err
	}
	for lr.Next() {
		var id string
		var l ledgerRef
		if err := lr.Scan(&id, &l.legalEntityID, &l.currency, &l.primary); err != nil {
			lr.Close()
			return err
		}
		ledgers[id] = l
		if l.primary {
			primary[l.legalEntityID] = l.currency
		}
	}
	lr.Close()
	if err := lr.Err(); err != nil {
		return err
	}
	unknown, fields := map[string]string{}, map[string]string{}
	for i := range rows {
		c := &rows[i]
		if c.ValuationLedgerID == "" {
			continue
		}
		l, ok := ledgers[c.ValuationLedgerID]
		if !ok || l.legalEntityID != c.LegalEntityID {
			unknown[fmt.Sprintf("items[%d].valuationLedgerId", i)] = "is not a valuation ledger of the legal entity"
			continue
		}
		if c.Currency != l.currency {
			fields[fmt.Sprintf("items[%d].currency", i)] = "must be the valuation ledger's currency " + l.currency
		}
		if l.primary {
			c.ValuationLedgerID = ""
		}
	}
	if len(unknown) > 0 {
		prob := httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerNotFound, "a cost transaction names a valuation ledger the legal entity does not have")
		prob.Errors = unknown
		return prob
	}
	// Default rows: the primary ledger's currency, else the entity's established one.
	want := map[string]string{}
	for _, le := range entityList {
		if cur, ok := primary[le]; ok {
			want[le] = cur
			continue
		}
		needed := false
		for _, c := range rows {
			if c.LegalEntityID == le && c.ValuationLedgerID == "" {
				needed = true
				break
			}
		}
		if !needed {
			continue
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "cost_transaction:"+tenantID+":"+le); err != nil {
			return err
		}
		var cur string
		err := tx.QueryRow(ctx, `select currency from cost_transaction
			where tenant_id = $1 and legal_entity_id = $2 and valuation_ledger_id is null limit 1`, tenantID, le).Scan(&cur)
		if err == nil {
			want[le] = cur
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	for i, c := range rows {
		if c.ValuationLedgerID != "" {
			continue
		}
		cur, ok := want[c.LegalEntityID]
		if !ok && c.Kind == domain.KindPPV {
			fields[fmt.Sprintf("items[%d].kind", i)] = "a PPV row needs a primary valuation ledger or earlier stock rows to fix the currency"
			continue
		}
		if !ok {
			want[c.LegalEntityID] = c.Currency // the batch's first default row sets it
			continue
		}
		if c.Currency != cur {
			fields[fmt.Sprintf("items[%d].currency", i)] = "must be the primary valuation's currency " + cur
		}
	}
	if len(fields) > 0 {
		prob := httpx.NewProblem(http.StatusUnprocessableEntity, CodeCostTxnCurrency, "a cost transaction is not in its valuation ledger's currency")
		prob.Errors = fields
		return prob
	}
	return nil
}

// checkReplays compares every row that was not inserted with the stored row of
// its source key; any difference refuses the batch (the transaction rolls back).
func checkReplays(ctx context.Context, tx pgx.Tx, tenantID string, rows []CostTransaction, fresh map[string]bool) error {
	var keys []string
	for _, c := range rows {
		if !fresh[c.SourceKey] {
			keys = append(keys, c.SourceKey)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	res, err := tx.Query(ctx, `select `+costTxnColumns+` from cost_transaction where tenant_id = $1 and source_key = any($2::text[])`, tenantID, keys)
	if err != nil {
		return err
	}
	stored := map[string]CostTransaction{}
	for res.Next() {
		c, err := scanCostTxn(res)
		if err != nil {
			res.Close()
			return err
		}
		stored[c.SourceKey] = c
	}
	res.Close()
	if err := res.Err(); err != nil {
		return err
	}
	fields := map[string]string{}
	firstInBatch := map[string]int{}
	for i, c := range rows {
		if j, seen := firstInBatch[c.SourceKey]; seen && fresh[c.SourceKey] {
			if !c.sameContent(rows[j]) {
				fields[fmt.Sprintf("items[%d].sourceKey", i)] = "repeats an earlier item with other content"
			}
			continue
		}
		firstInBatch[c.SourceKey] = i
		if fresh[c.SourceKey] {
			continue
		}
		if s, ok := stored[c.SourceKey]; !ok || !c.sameContent(s) {
			fields[fmt.Sprintf("items[%d].sourceKey", i)] = "was already recorded with other content"
		}
	}
	if len(fields) > 0 {
		prob := httpx.NewProblem(http.StatusUnprocessableEntity, CodeCostTxnConflict, "a source key was already recorded with other content")
		prob.Errors = fields
		return prob
	}
	return nil
}

// ValueBalances sums the inventory kinds (every kind but PPV) to product x site x
// warehouse x valuation type for one ledger, the finest grain of the value
// report. The database sums, so the report cost follows the number of balances,
// not of transactions. A balance with zero quantity and zero value is left out;
// a residual value on zero quantity stays, because it must be explained.
func (s *Postgres) ValueBalances(ctx context.Context, q BalanceQuery) ([]domain.Balance, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.Balance{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select product_id::text, site_id::text, coalesce(warehouse_id::text, ''), valuation_type, currency,
				sum(qty)::text,
				coalesce(sum(amount_acct) filter (where kind = 'PHYSICAL'), 0)::text,
				coalesce(sum(amount_acct) filter (where kind <> 'PHYSICAL'), 0)::text
			from cost_transaction
			where tenant_id = $1 and legal_entity_id = $2::uuid and valuation_ledger_id is not distinct from $3::uuid
				and financial_date <= $4::date and ($5 = '' or site_id::text = $5) and kind <> 'PPV'
			group by product_id, site_id, warehouse_id, valuation_type, currency
			having sum(qty) <> 0 or sum(amount_acct) <> 0
			order by product_id, site_id, warehouse_id nulls first, valuation_type`,
			p.TenantID, q.LegalEntityID, optUUID(q.ValuationLedgerID), q.AsOf, q.SiteID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b domain.Balance
			var qty, phys, fin string
			if err := rows.Scan(&b.ProductID, &b.SiteID, &b.WarehouseID, &b.ValuationType, &b.Currency, &qty, &phys, &fin); err != nil {
				return err
			}
			for _, f := range []struct {
				raw string
				dst *decimal.Decimal
			}{{qty, &b.Qty}, {phys, &b.PhysicalValue}, {fin, &b.FinancialValue}} {
				if *f.dst, err = decimal.NewFromString(f.raw); err != nil {
					return err
				}
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// ListCostTransactions lists cost transactions of one legal entity and ledger,
// newest financial date first. after is the Next of the previous page.
func (s *Postgres) ListCostTransactions(ctx context.Context, f CostTxnFilter, after string, limit int) (CostTxnPage, error) {
	p, _ := tenant.FromContext(ctx)
	afterDate, afterID, _ := strings.Cut(after, "|")
	page := CostTxnPage{Items: []CostTransaction{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+costTxnColumns+` from cost_transaction
			where tenant_id = $1 and legal_entity_id = $2::uuid and valuation_ledger_id is not distinct from $3::uuid
			  and ($4 = '' or product_id::text = $4) and ($5 = '' or site_id::text = $5) and ($6 = '' or kind = $6)
			  and ($7::date is null or financial_date <= $7::date)
			  and (not $8::boolean or (financial_date, id) < ($9::date, $10::uuid))
			order by financial_date desc, id desc
			limit $11`,
			p.TenantID, f.LegalEntityID, optUUID(f.ValuationLedgerID), f.ProductID, f.SiteID, f.Kind, nullDate(f.AsOf),
			after != "", nullDate(afterDate), nullUUID(afterID), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCostTxn(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, c)
		}
		return rows.Err()
	})
	if err != nil {
		return CostTxnPage{}, err
	}
	if len(page.Items) > limit {
		last := page.Items[limit-1]
		page.Items, page.Next = page.Items[:limit], last.FinancialDate+"|"+last.ID
	}
	return page, nil
}

// optUUID is a nullable uuid parameter: "" is SQL NULL.
func optUUID(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
