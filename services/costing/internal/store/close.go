package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
)

const (
	CodePeriodLocked   = "CST-CLS-001"
	CodeCloseOrder     = "CST-CLS-002"
	CodeCloseEmpty     = "CST-CLS-003"
	CodeTieOutPeriod   = "CST-GL-008"
	closeProductsBatch = 200
	closeStatusRunning = "RUNNING"
	closeStatusDone    = "COMPLETED"
	defaultCloseMethod = domain.FIFO
)

// Close is one inventory close of a legal entity's primary valuation: it locks the cost transactions dated up to
// PeriodEnd, settles the issues against the receipts and books the settlement adjustments.
type Close struct {
	ID              string     `json:"id"`
	LegalEntityID   string     `json:"legalEntityId"`
	PeriodEnd       string     `json:"periodEnd"`
	Method          string     `json:"method"`
	Currency        string     `json:"currency"`
	Status          string     `json:"status"`
	Groups          int        `json:"groups"`
	AdjustedGroups  int        `json:"adjustedGroups"`
	AdjustmentTotal string     `json:"adjustmentTotal"`
	JournalEntryID  string     `json:"journalEntryId,omitempty"`
	CreatedBy       string     `json:"createdBy,omitempty"`
	StartedAt       time.Time  `json:"startedAt"`
	CompletedAt     *time.Time `json:"completedAt,omitempty"`
	Replayed        bool       `json:"-"`
}

type CloseRequest struct {
	LegalEntityID string
	PeriodEnd     string
}

const closeColumns = `id::text, legal_entity_id::text, period_end::text, method, currency, status, groups, adjusted_groups, adjustment_total::text,
	coalesce(journal_entry_id, ''), created_by, started_at, completed_at`

func scanClose(row pgx.Row) (Close, error) {
	var c Close
	err := row.Scan(&c.ID, &c.LegalEntityID, &c.PeriodEnd, &c.Method, &c.Currency, &c.Status, &c.Groups, &c.AdjustedGroups, &c.AdjustmentTotal,
		&c.JournalEntryID, &c.CreatedBy, &c.StartedAt, &c.CompletedAt)
	return c, err
}

func entityLockKey(tenantID, legalEntityID string) string {
	return "inventory_close:" + tenantID + ":" + legalEntityID
}

// checkPeriodsOpen refuses primary-valuation rows dated on or before a close of their legal entity. It takes the entity's close lock
// shared, so a close that starts afterwards waits for these writes and one that started before is seen.
func checkPeriodsOpen(ctx context.Context, tx pgx.Tx, tenantID string, rows []CostTransaction) error {
	set := map[string]bool{}
	for _, c := range rows {
		if c.ValuationLedgerID == "" {
			set[c.LegalEntityID] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	entities := make([]string, 0, len(set))
	for le := range set {
		entities = append(entities, le)
	}
	sort.Strings(entities)
	for _, le := range entities {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock_shared(hashtextextended($1, 0))`, entityLockKey(tenantID, le)); err != nil {
			return err
		}
	}
	res, err := tx.Query(ctx, `select legal_entity_id::text, max(period_end)::text from inventory_close
		where tenant_id = $1 and legal_entity_id = any($2::uuid[]) group by legal_entity_id`, tenantID, entities)
	if err != nil {
		return err
	}
	closedTo := map[string]string{}
	for res.Next() {
		var le, end string
		if err := res.Scan(&le, &end); err != nil {
			res.Close()
			return err
		}
		closedTo[le] = end
	}
	res.Close()
	if err := res.Err(); err != nil {
		return err
	}
	fields := map[string]string{}
	for i, c := range rows {
		if end, ok := closedTo[c.LegalEntityID]; ok && c.ValuationLedgerID == "" && c.FinancialDate <= end {
			fields[fmt.Sprintf("items[%d].financialDate", i)] = "is in an inventory period closed through " + end
		}
	}
	if len(fields) > 0 {
		prob := httpx.NewProblem(http.StatusUnprocessableEntity, CodePeriodLocked, "a cost transaction is dated in a closed inventory period")
		prob.Errors = fields
		return prob
	}
	return nil
}

func (s *Postgres) startClose(ctx context.Context, in CloseRequest, actor string) (Close, error) {
	t, _ := tenant.FromContext(ctx)
	var out Close
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, entityLockKey(t.TenantID, in.LegalEntityID)); err != nil {
			return err
		}
		existing, err := scanClose(tx.QueryRow(ctx, `select `+closeColumns+` from inventory_close
			where tenant_id = $1 and legal_entity_id = $2 and valuation_ledger_id is null and period_end = $3::date`, t.TenantID, in.LegalEntityID, in.PeriodEnd))
		if err == nil {
			out = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var later *string
		if err := tx.QueryRow(ctx, `select max(period_end)::text from inventory_close where tenant_id = $1 and legal_entity_id = $2 and period_end > $3::date`,
			t.TenantID, in.LegalEntityID, in.PeriodEnd).Scan(&later); err != nil {
			return err
		}
		if later != nil {
			return httpx.NewProblem(http.StatusConflict, CodeCloseOrder, "the legal entity is already closed through "+*later)
		}
		method, currency := string(defaultCloseMethod), ""
		err = tx.QueryRow(ctx, `select method, currency from valuation_ledger where tenant_id = $1 and legal_entity_id = $2 and is_primary`, t.TenantID, in.LegalEntityID).Scan(&method, &currency)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `select currency from cost_transaction where tenant_id = $1 and legal_entity_id = $2 and valuation_ledger_id is null limit 1`,
				t.TenantID, in.LegalEntityID).Scan(&currency)
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewProblem(http.StatusConflict, CodeCloseEmpty, "the legal entity has no cost transactions to close")
			}
		}
		if err != nil {
			return err
		}
		out, err = scanClose(tx.QueryRow(ctx, `insert into inventory_close (tenant_id, id, legal_entity_id, period_end, method, currency, created_by)
			values ($1, $2, $3, $4::date, $5, $6, $7) returning `+closeColumns, t.TenantID, ids.New(), in.LegalEntityID, in.PeriodEnd, method, currency, actor))
		return err
	})
	return out, err
}

type settleGroup struct {
	product, site, valuationType string
	txns                         []domain.Txn
	booked                       decimal.Decimal
	issues                       int
}

func (g settleGroup) adjustment(opt domain.Options) (decimal.Decimal, error) {
	if g.issues == 0 {
		return decimal.Zero, nil
	}
	res, err := domain.Settle(g.txns, opt)
	if err != nil {
		return decimal.Zero, err
	}
	settled := decimal.Zero
	for _, cost := range res.IssueCosts {
		settled = settled.Add(cost)
	}
	return settled.Neg().Sub(g.booked), nil
}

// settleBatch settles the groups of the products against the receipts up to the period end and appends one SETTLEMENT_ADJ row per group
// whose booked issue value (the issues plus earlier adjustments) differs from the settled value. The rows are idempotent per close and
// group, so a resumed close adds nothing twice. It returns the number of groups with stock movements.
func (s *Postgres) settleBatch(ctx context.Context, c Close, products []string) (int, error) {
	t, _ := tenant.FromContext(ctx)
	scale := money.MinorUnits(c.Currency)
	opt := domain.Options{Method: domain.Method(c.Method), Scale: scale}
	groups := 0
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select product_id::text, site_id::text, valuation_type, id::text, kind, qty::text, amount_acct::text, financial_date
			from cost_transaction
			where tenant_id = $1 and legal_entity_id = $2 and valuation_ledger_id is null and product_id = any($3::uuid[])
				and financial_date <= $4::date and kind in ('FINANCIAL', 'SETTLEMENT_ADJ')
			order by product_id, site_id, valuation_type`, t.TenantID, c.LegalEntityID, products, c.PeriodEnd)
		if err != nil {
			return err
		}
		var adjustments []CostTransaction
		var current *settleGroup
		flush := func() error {
			if current == nil {
				return nil
			}
			groups++
			adj, err := current.adjustment(opt)
			if err != nil {
				return err
			}
			if adj.Round(scale).IsZero() {
				return nil
			}
			adjustments = append(adjustments, CostTransaction{SourceKey: fmt.Sprintf("close:%s:%s:%s:%s", c.ID, current.product, current.site, current.valuationType),
				LegalEntityID: c.LegalEntityID, ProductID: current.product, SiteID: current.site, ValuationType: current.valuationType,
				Kind: domain.KindSettlementAdj, Qty: "0", Amount: adj.StringFixed(scale), Currency: c.Currency, FinancialDate: c.PeriodEnd})
			return nil
		}
		for rows.Next() {
			var product, site, vt, id, kind, qtyRaw, amountRaw string
			var date time.Time
			if err := rows.Scan(&product, &site, &vt, &id, &kind, &qtyRaw, &amountRaw, &date); err != nil {
				rows.Close()
				return err
			}
			qty, errQ := decimal.NewFromString(qtyRaw)
			amount, errA := decimal.NewFromString(amountRaw)
			if err := errors.Join(errQ, errA); err != nil {
				rows.Close()
				return err
			}
			if current == nil || current.product != product || current.site != site || current.valuationType != vt {
				if err := flush(); err != nil {
					rows.Close()
					return err
				}
				current = &settleGroup{product: product, site: site, valuationType: vt}
			}
			switch {
			case kind == domain.KindSettlementAdj:
				current.booked = current.booked.Add(amount)
			case qty.IsPositive():
				current.txns = append(current.txns, domain.Txn{ID: id, Kind: domain.Receipt, Qty: qty, Value: amount, FinancialDate: date, PhysicalDate: date})
			case qty.IsNegative():
				current.txns = append(current.txns, domain.Txn{ID: id, Kind: domain.Issue, Qty: qty.Neg(), FinancialDate: date, PhysicalDate: date})
				current.booked = current.booked.Add(amount)
				current.issues++
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if err := flush(); err != nil {
			return err
		}
		if len(adjustments) == 0 {
			return nil
		}
		_, err = insertCostTransactions(ctx, tx, t.TenantID, adjustments)
		return err
	})
	return groups, err
}

func (s *Postgres) closeProducts(ctx context.Context, c Close) ([]string, error) {
	t, _ := tenant.FromContext(ctx)
	var out []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select distinct product_id::text from cost_transaction
			where tenant_id = $1 and legal_entity_id = $2 and valuation_ledger_id is null and financial_date <= $3::date
				and kind in ('FINANCIAL', 'SETTLEMENT_ADJ') order by 1`, t.TenantID, c.LegalEntityID, c.PeriodEnd)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) closeAdjustments(ctx context.Context, c Close) (count int, total decimal.Decimal, err error) {
	t, _ := tenant.FromContext(ctx)
	var raw string
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*), coalesce(sum(amount_acct), 0)::text from cost_transaction
			where tenant_id = $1 and legal_entity_id = $2 and valuation_ledger_id is null and kind = 'SETTLEMENT_ADJ' and financial_date = $3::date
				and left(source_key, $5) = $4`, t.TenantID, c.LegalEntityID, c.PeriodEnd, "close:"+c.ID+":", len("close:"+c.ID+":")).Scan(&count, &raw)
	})
	if err != nil {
		return 0, decimal.Zero, err
	}
	total, err = decimal.NewFromString(raw)
	return count, total, err
}

func (s *Postgres) completeClose(ctx context.Context, c Close, groups, adjusted int, total decimal.Decimal, entryID string) (Close, error) {
	t, _ := tenant.FromContext(ctx)
	var out Close
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = scanClose(tx.QueryRow(ctx, `update inventory_close set status = 'COMPLETED', groups = $3, adjusted_groups = $4, adjustment_total = $5::numeric,
			journal_entry_id = nullif($6, ''), completed_at = now() where tenant_id = $1 and id = $2 returning `+closeColumns,
			t.TenantID, c.ID, groups, adjusted, total.String(), entryID))
		return err
	})
	return out, err
}

// RunClose closes the legal entity's primary valuation through the period end: it locks the period (later cost transactions dated on or before it
// are refused), settles every product's issues against its receipts under the valuation ledger's method (FIFO without one), appends the settlement
// adjustments and books their net on the profile's inventory and adjustment accounts. A close that stopped part way is resumed by running it again;
// a completed close is returned as it is. A STANDARD valuation books issues at the standard already, so it settles nothing.
func (s *Postgres) RunClose(ctx context.Context, ledger ledgerclient.Ledger, in CloseRequest, actor string) (Close, error) {
	profile, err := s.GetPostingProfile(ctx, in.LegalEntityID)
	if err != nil {
		return Close{}, err
	}
	if err := CheckLedgerOwner(ctx, ledger, in.LegalEntityID, profile.GLLedgerID); err != nil {
		return Close{}, err
	}
	c, err := s.startClose(ctx, in, actor)
	if err != nil {
		return c, err
	}
	if c.Status == closeStatusDone {
		c.Replayed = true
		return c, nil
	}
	groups := 0
	if domain.Method(c.Method) != domain.Standard {
		products, err := s.closeProducts(ctx, c)
		if err != nil {
			return Close{}, err
		}
		for len(products) > 0 {
			n := min(len(products), closeProductsBatch)
			settled, err := s.settleBatch(ctx, c, products[:n])
			if err != nil {
				return Close{}, err
			}
			groups += settled
			products = products[n:]
		}
	}
	adjusted, total, err := s.closeAdjustments(ctx, c)
	if err != nil {
		return Close{}, err
	}
	entryID := ""
	if !total.IsZero() {
		entryID, err = s.postVoucher(ctx, ledger, voucher{legalEntityID: c.LegalEntityID, sourceKey: "close:" + c.ID, glLedgerID: profile.GLLedgerID,
			currency: c.Currency, date: c.PeriodEnd, key: "costing-close:" + c.ID, lines: domain.CloseVoucher(profile.accounts(), total)})
		if err != nil {
			slog.ErrorContext(ctx, "costing close voucher failed", "close", c.ID, "code", glCode(err), "err", err, "correlationId", middleware.GetReqID(ctx))
			return Close{}, err
		}
	}
	return s.completeClose(ctx, c, groups, adjusted, total, entryID)
}

func (s *Postgres) ListCloses(ctx context.Context, legalEntityID string) ([]Close, error) {
	t, _ := tenant.FromContext(ctx)
	out := []Close{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+closeColumns+` from inventory_close where tenant_id = $1 and legal_entity_id = $2 order by period_end desc limit 100`, t.TenantID, legalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanClose(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// InventoryReconciliation ties the inventory value report of a legal entity to the general ledger. Ledger is the net balance of the profile's
// inventory account through the calendar month ending on AsOf, which must be a month end; BookedByCosting is the net the confirmed vouchers of this service booked on it (shown, not
// compared with the account, which other sources also move). Unposted counts PPV cost transactions with no confirmed or parked voucher and Parked the
// vouchers parked after failing, with their reason codes in ParkedReasons; the inventory ties only when the difference is zero and both are zero.
type InventoryReconciliation struct {
	LegalEntityID   string
	AsOf            string
	Currency        string
	Account         string
	Inventory       decimal.Decimal
	Ledger          decimal.Decimal
	Difference      decimal.Decimal
	BookedByCosting decimal.Decimal
	Unposted        int
	Parked          int
	ParkedReasons   map[string]int
	Ties            bool
}

func monthEnd(date string) bool {
	d, err := time.Parse(time.DateOnly, date)
	return err == nil && d.AddDate(0, 0, 1).Day() == 1
}

func (s *Postgres) ReconcileInventory(ctx context.Context, ledger ledgerclient.Ledger, legalEntityID, asOf string) (InventoryReconciliation, error) {
	t, _ := tenant.FromContext(ctx)
	if !monthEnd(asOf) {
		return InventoryReconciliation{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeTieOutPeriod, "asOf must be the last day of a month: the ledger balance is read by calendar month")
	}
	profile, err := s.GetPostingProfile(ctx, legalEntityID)
	if err != nil {
		return InventoryReconciliation{}, err
	}
	if err := CheckLedgerOwner(ctx, ledger, legalEntityID, profile.GLLedgerID); err != nil {
		return InventoryReconciliation{}, err
	}
	balances, err := s.ValueBalances(ctx, BalanceQuery{LegalEntityID: legalEntityID, AsOf: asOf})
	if err != nil {
		return InventoryReconciliation{}, err
	}
	out := InventoryReconciliation{LegalEntityID: legalEntityID, AsOf: asOf, Account: profile.InventoryAccount, ParkedReasons: map[string]int{}}
	for _, b := range balances {
		if out.Currency != "" && out.Currency != b.Currency {
			return InventoryReconciliation{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeReportCurrency, "the inventory is held in more than one currency")
		}
		out.Currency = b.Currency
		out.Inventory = out.Inventory.Add(b.PhysicalValue).Add(b.FinancialValue)
	}
	bal, err := ledger.Balance(ctx, ledgerclient.BalanceQuery{LegalEntityID: legalEntityID, LedgerID: profile.GLLedgerID, Account: profile.InventoryAccount, PeriodTo: asOf[:7]})
	if err != nil {
		return InventoryReconciliation{}, fmt.Errorf("general ledger balance of %s: %w", profile.InventoryAccount, err)
	}
	if out.Currency == "" {
		out.Currency = bal.Currency
	}
	if bal.Currency != out.Currency && !bal.Net().IsZero() {
		return InventoryReconciliation{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeReportCurrency, "the ledger account is in "+bal.Currency+", the inventory in "+out.Currency)
	}
	out.Ledger = bal.Net()
	out.Difference = out.Ledger.Sub(out.Inventory)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var booked string
		if err := tx.QueryRow(ctx, `select coalesce(sum(case when (l->>'credit')::boolean then -(l->>'amount')::numeric else (l->>'amount')::numeric end), 0)::text
			from costing_gl_voucher v cross join lateral jsonb_array_elements(v.lines) l
			where v.tenant_id = $1 and v.legal_entity_id = $2 and v.status = 'POSTED' and l->>'account' = $3 and v.posting_date <= $4::date`,
			t.TenantID, legalEntityID, profile.InventoryAccount, asOf).Scan(&booked); err != nil {
			return err
		}
		var err error
		if out.BookedByCosting, err = decimal.NewFromString(booked); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select count(*) from cost_transaction ct where ct.tenant_id = $1 and ct.legal_entity_id = $2 and ct.valuation_ledger_id is null
				and ct.kind = 'PPV' and ct.gr_ir_amount is not null and ct.gr_ir_amount <> 0 and ct.financial_date <= $3::date
				and not exists (select 1 from costing_gl_voucher v where v.tenant_id = ct.tenant_id and v.source_key = 'ctxn:' || ct.id::text and v.status in ('POSTED', 'PARKED'))`,
			t.TenantID, legalEntityID, asOf).Scan(&out.Unposted); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select coalesce(parked_reason, ''), count(*) from costing_gl_voucher where tenant_id = $1 and legal_entity_id = $2 and status = 'PARKED' and posting_date <= $3::date group by 1`, t.TenantID, legalEntityID, asOf)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var reason string
			var n int
			if err := rows.Scan(&reason, &n); err != nil {
				return err
			}
			out.ParkedReasons[reason] = n
			out.Parked += n
		}
		return rows.Err()
	})
	if err != nil {
		return InventoryReconciliation{}, err
	}
	out.Ties = out.Difference.IsZero() && out.Unposted == 0 && out.Parked == 0
	return out, nil
}
