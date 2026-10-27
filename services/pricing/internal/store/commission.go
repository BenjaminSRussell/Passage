package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// SalesGroup is a sales group with its reps and their shares in entered order (FR-26.18;
// AX CommissionSalesGroup, SAP VBPA VE/PE).
type SalesGroup struct {
	ID            string
	LegalEntityID string
	Name          string
	Reps          []domain.Share
}

// CommissionRuleRow is a stored commission rule with its id and owning legal entity.
type CommissionRuleRow struct {
	ID            string
	LegalEntityID string
	Rule          domain.CommissionRule
}

// CommissionTxn is one rep's commission transaction on one invoice line (FR-26.18).
// Credit notes carry negative amounts and name the line they reverse.
type CommissionTxn struct {
	ID                    string
	LegalEntityID         string
	InvoiceLineID         string
	ReversesInvoiceLineID string
	InvoiceDate           time.Time
	PeriodKey             string
	CustomerID            string
	CustomerGroup         string
	ItemGroup             string
	SalesGroupID          string
	RepID                 string
	SharePct              decimal.Decimal
	Basis                 string
	LineNet               decimal.Decimal
	CostSnapshot          *decimal.Decimal
	BasisAmount           decimal.Decimal
	RatePct               decimal.Decimal
	Amount                decimal.Decimal
	Currency              string
	DebitAccount          string
	CreditAccount         string
	Status                string
}

// CommissionPosting is one invoice line presented for commission in one sales group.
type CommissionPosting struct {
	LegalEntityID string
	SalesGroupID  string
	Line          domain.CommissionLine
}

// CommissionFilter selects commission transactions for the inquiry (§5.26.14).
type CommissionFilter struct {
	LegalEntityID string
	SalesGroupID  string
	RepID         string
	CustomerID    string
	InvoiceLineID string
	// PeriodKey is an invoice month such as 2027-03.
	PeriodKey string
	Limit     int
}

const commissionColumns = `select id::text, legal_entity_id::text, invoice_line_id, coalesce(reverses_invoice_line_id, ''), invoice_date,
	period_key, customer_id, customer_group, item_group, sales_group_id::text, rep_id, share_pct::text, basis, line_net::text,
	cost_snapshot::text, basis_amount::text, rate_pct::text, amount::text, currency::text, debit_account, credit_account, status
	from commission_transaction`

func scanTxn(row pgx.Row) (CommissionTxn, error) {
	var t CommissionTxn
	var share, net, ba, rp, amt string
	var cost *string
	if err := row.Scan(&t.ID, &t.LegalEntityID, &t.InvoiceLineID, &t.ReversesInvoiceLineID, &t.InvoiceDate, &t.PeriodKey, &t.CustomerID,
		&t.CustomerGroup, &t.ItemGroup, &t.SalesGroupID, &t.RepID, &share, &t.Basis, &net, &cost, &ba, &rp, &amt, &t.Currency,
		&t.DebitAccount, &t.CreditAccount, &t.Status); err != nil {
		return CommissionTxn{}, err
	}
	t.SharePct, t.LineNet = decimal.RequireFromString(share), decimal.RequireFromString(net)
	t.BasisAmount, t.RatePct, t.Amount = decimal.RequireFromString(ba), decimal.RequireFromString(rp), decimal.RequireFromString(amt)
	if cost != nil {
		c := decimal.RequireFromString(*cost)
		t.CostSnapshot = &c
	}
	return t, nil
}

func salesGroupNotFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeSalesGroupNotFound, "no sales group "+id)
}

// querySalesGroups reads sales groups matched by a trailing WHERE clause, with their reps in seq order.
func querySalesGroups(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]SalesGroup, error) {
	rows, err := tx.Query(ctx, `select id::text, legal_entity_id::text, name from sales_group `+where, args...)
	if err != nil {
		return nil, err
	}
	out := []SalesGroup{}
	for rows.Next() {
		var g SalesGroup
		if err := rows.Scan(&g.ID, &g.LegalEntityID, &g.Name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan sales group: %w", err)
		}
		out = append(out, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	groupIDs := make([]string, len(out))
	index := map[string]int{}
	for i, g := range out {
		groupIDs[i] = g.ID
		index[g.ID] = i
	}
	reps, err := tx.Query(ctx, `select sales_group_id::text, rep_id, share_pct::text from sales_group_rep
		where sales_group_id = any($1::uuid[]) order by sales_group_id, seq`, groupIDs)
	if err != nil {
		return nil, err
	}
	defer reps.Close()
	for reps.Next() {
		var gid, rep, share string
		if err := reps.Scan(&gid, &rep, &share); err != nil {
			return nil, fmt.Errorf("scan sales group rep: %w", err)
		}
		i := index[gid]
		out[i].Reps = append(out[i].Reps, domain.Share{RepID: rep, SharePct: decimal.RequireFromString(share)})
	}
	return out, reps.Err()
}

// sameSalesGroup reports whether a replayed create carries the same content, reps in order.
func sameSalesGroup(stored, in SalesGroup) bool {
	if stored.LegalEntityID != in.LegalEntityID || stored.Name != in.Name || len(stored.Reps) != len(in.Reps) {
		return false
	}
	for i := range in.Reps {
		if stored.Reps[i].RepID != in.Reps[i].RepID || !stored.Reps[i].SharePct.Equal(in.Reps[i].SharePct) {
			return false
		}
	}
	return true
}

// CreateSalesGroup stores a sales group and its rep shares. A repeated Idempotency-Key
// returns the original group when the body matches and is rejected otherwise; the insert
// uses ON CONFLICT so two concurrent requests with one key cannot both create a group.
func (s *Postgres) CreateSalesGroup(ctx context.Context, g SalesGroup, idempotencyKey string) (SalesGroup, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out SalesGroup
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		g.ID = ids.New()
		var inserted string
		err := tx.QueryRow(ctx, `insert into sales_group (tenant_id, id, legal_entity_id, name, idempotency_key) values ($1, $2, $3, $4, $5)
			on conflict (tenant_id, idempotency_key) do nothing returning id::text`, p.TenantID, g.ID, g.LegalEntityID, g.Name, key).Scan(&inserted)
		if errors.Is(err, pgx.ErrNoRows) && key != nil {
			replay, err := querySalesGroups(ctx, tx, `where idempotency_key = $1`, idempotencyKey)
			if err != nil {
				return err
			}
			if len(replay) == 0 || !sameSalesGroup(replay[0], g) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request body")
			}
			out = replay[0]
			return nil
		}
		if err != nil {
			return err
		}
		for i, r := range g.Reps {
			if _, err := tx.Exec(ctx, `insert into sales_group_rep (tenant_id, sales_group_id, seq, rep_id, share_pct) values ($1, $2, $3, $4, $5::numeric)`,
				p.TenantID, g.ID, i+1, r.RepID, r.SharePct.String()); err != nil {
				return err
			}
		}
		out = g
		return nil
	})
	return out, err
}

// GetSalesGroup reads one sales group with its reps.
func (s *Postgres) GetSalesGroup(ctx context.Context, id string) (SalesGroup, error) {
	var out SalesGroup
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		groups, err := querySalesGroups(ctx, tx, `where id = $1`, id)
		if err != nil {
			return err
		}
		if len(groups) == 0 {
			return salesGroupNotFound(id)
		}
		out = groups[0]
		return nil
	})
	return out, err
}

// ListSalesGroups returns sales groups with their reps by name, optionally for one legal entity.
func (s *Postgres) ListSalesGroups(ctx context.Context, legalEntityID string, limit int) ([]SalesGroup, error) {
	if limit <= 0 || limit > MaxListLimit {
		limit = MaxListLimit
	}
	var out []SalesGroup
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = querySalesGroups(ctx, tx, `where ($1::text = '' or legal_entity_id::text = $1) order by name, id limit $2`, legalEntityID, limit)
		return err
	})
	return out, err
}

const ruleColumns = `select id::text, legal_entity_id::text, sales_group_id::text, customer_group, item_group, basis, rate_pct::text from commission_rule`

func scanRule(row pgx.Row) (CommissionRuleRow, error) {
	var r CommissionRuleRow
	var rate string
	if err := row.Scan(&r.ID, &r.LegalEntityID, &r.Rule.SalesGroupID, &r.Rule.CustomerGroup, &r.Rule.ItemGroup, &r.Rule.Basis, &rate); err != nil {
		return CommissionRuleRow{}, err
	}
	r.Rule.RatePct = decimal.RequireFromString(rate)
	return r, nil
}

func queryRules(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]CommissionRuleRow, error) {
	rows, err := tx.Query(ctx, ruleColumns+` `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CommissionRuleRow{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan commission rule: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// sameRule compares the content of two commission rules.
func sameRule(a, b domain.CommissionRule) bool {
	return a.SalesGroupID == b.SalesGroupID && a.CustomerGroup == b.CustomerGroup && a.ItemGroup == b.ItemGroup &&
		a.Basis == b.Basis && a.RatePct.Equal(b.RatePct)
}

// CreateCommissionRule stores a rule under a sales group (BR-26.12). The rule takes the
// group's legal entity. The group must exist (PRC-0036) and may hold one rule per customer
// and item commission group pair (PRC-0038). A repeated Idempotency-Key returns the original.
func (s *Postgres) CreateCommissionRule(ctx context.Context, r domain.CommissionRule, idempotencyKey string) (CommissionRuleRow, error) {
	p, _ := tenant.FromContext(ctx)
	var out CommissionRuleRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if idempotencyKey != "" {
			replay, err := scanRule(tx.QueryRow(ctx, ruleColumns+` where idempotency_key = $1`, idempotencyKey))
			if err == nil {
				if !sameRule(replay.Rule, r) {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request body")
				}
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var le string
		if err := tx.QueryRow(ctx, `select legal_entity_id::text from sales_group where id = $1`, r.SalesGroupID).Scan(&le); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return salesGroupNotFound(r.SalesGroupID)
			}
			return err
		}
		var key *string
		if idempotencyKey != "" {
			key = &idempotencyKey
		}
		out = CommissionRuleRow{ID: ids.New(), LegalEntityID: le, Rule: r}
		_, err := tx.Exec(ctx, `insert into commission_rule (tenant_id, id, legal_entity_id, sales_group_id, customer_group, item_group, basis, rate_pct, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9)`,
			p.TenantID, out.ID, le, r.SalesGroupID, r.CustomerGroup, r.ItemGroup, r.Basis, r.RatePct.String(), key)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if pgErr.ConstraintName == "commission_rule_tenant_id_idempotency_key_key" {
				// A concurrent request with the same key won the insert; a retry replays it.
				return httpx.NewProblem(http.StatusConflict, CodeIdempotencyUse, "a request with this Idempotency-Key is in flight; retry it")
			}
			return httpx.NewProblem(http.StatusConflict, domain.CodeCommissionRuleExists,
				"sales group "+r.SalesGroupID+" already has a rule for customer group '"+r.CustomerGroup+"' and item group '"+r.ItemGroup+"'")
		}
		return err
	})
	return out, err
}

// ListCommissionRules returns rules, optionally of one sales group, from most specific to most general.
func (s *Postgres) ListCommissionRules(ctx context.Context, salesGroupID string, limit int) ([]CommissionRuleRow, error) {
	if limit <= 0 || limit > MaxListLimit {
		limit = MaxListLimit
	}
	var out []CommissionRuleRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = queryRules(ctx, tx, `where ($1::text = '' or sales_group_id::text = $1)
			order by sales_group_id, customer_group = '', item_group = '', customer_group, item_group, id limit $2`, salesGroupID, limit)
		return err
	})
	return out, err
}

// commissionLockKey names the advisory lock of one invoice line's commission.
func commissionLockKey(tenantID, invoiceLineID string) string {
	return "pricing.commission|" + tenantID + "|" + invoiceLineID
}

// sameCommissionLine reports whether a redelivered line matches the one already posted.
func sameCommissionLine(stored CommissionTxn, c CommissionPosting) bool {
	l := c.Line
	costSame := (stored.CostSnapshot == nil) == !l.HasCost && (stored.CostSnapshot == nil || stored.CostSnapshot.Equal(l.CostSnapshot))
	return stored.LegalEntityID == c.LegalEntityID && stored.SalesGroupID == c.SalesGroupID && stored.ReversesInvoiceLineID == l.ReversesInvoiceLineID &&
		dayStr(stored.InvoiceDate) == dayStr(l.InvoiceDate) && stored.Currency == l.Currency && stored.LineNet.Equal(l.LineNet) && costSame
}

// PostCommission computes and stores the commission of one invoice line (FR-26.18, BR-26.12):
// it resolves the sales group's rule specific to general, splits the amount across the reps
// and writes one transaction per rep (Dr 6200 / Cr 2170) with its commission.calculated event
// in the same transaction. A credit note line reverses the line it names with that line's
// basis, rate and shares, and credits may not exceed the invoiced net (PRC-0037). The line is
// locked, so a redelivery returns the stored transactions with created=false; a redelivery
// with different content is refused.
func (s *Postgres) PostCommission(ctx context.Context, c CommissionPosting) ([]CommissionTxn, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out []CommissionTxn
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		l := c.Line
		// A credit serializes with the invoice line it reverses (so two credits cannot exceed it).
		lockLine := l.InvoiceLineID
		if l.IsCredit() {
			lockLine = l.ReversesInvoiceLineID
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, commissionLockKey(p.TenantID, lockLine)); err != nil {
			return err
		}
		existing, err := commissionTxns(ctx, tx, `where invoice_line_id = $1 order by rep_seq`, l.InvoiceLineID)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			if !sameCommissionLine(existing[0], c) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse,
					"invoice line "+l.InvoiceLineID+" already posted commission with a different sales group, date, net or cost")
			}
			out = existing
			return nil
		}
		groups, err := querySalesGroups(ctx, tx, `where id = $1`, c.SalesGroupID)
		if err != nil {
			return err
		}
		if len(groups) == 0 {
			return salesGroupNotFound(c.SalesGroupID)
		}
		group := groups[0]
		if group.LegalEntityID != c.LegalEntityID {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "sales group "+group.ID+" belongs to another legal entity")
		}
		var basisKind string
		var rate, basis decimal.Decimal
		var splits []domain.Split
		var shares []domain.Share
		if l.IsCredit() {
			orig, err := commissionTxns(ctx, tx, `where invoice_line_id = $1 order by rep_seq`, l.ReversesInvoiceLineID)
			if err != nil {
				return err
			}
			if len(orig) == 0 || orig[0].ReversesInvoiceLineID != "" {
				return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCommissionCredit,
					"invoice line "+l.ReversesInvoiceLineID+" has no posted commission to credit")
			}
			if orig[0].SalesGroupID != c.SalesGroupID {
				return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCommissionCredit,
					"invoice line "+l.ReversesInvoiceLineID+" posted commission in sales group "+orig[0].SalesGroupID)
			}
			var credited string
			if err := tx.QueryRow(ctx, `select coalesce(sum(line_net), 0)::text from (select distinct invoice_line_id, line_net
				from commission_transaction where reverses_invoice_line_id = $1) c`, l.ReversesInvoiceLineID).Scan(&credited); err != nil {
				return err
			}
			original := domain.CommissionOriginal{Basis: orig[0].Basis, RatePct: orig[0].RatePct, LineNet: orig[0].LineNet,
				Currency: orig[0].Currency, Credited: decimal.RequireFromString(credited)}
			for _, t := range orig {
				original.Shares = append(original.Shares, domain.Share{RepID: t.RepID, SharePct: t.SharePct})
			}
			var e *domain.Error
			basis, splits, e = domain.CreditCommission(original, l)
			if e != nil {
				return problemOf(e)
			}
			basisKind, rate, shares = original.Basis, original.RatePct, original.Shares
		} else {
			rows, err := queryRules(ctx, tx, `where sales_group_id = $1`, c.SalesGroupID)
			if err != nil {
				return err
			}
			rules := make([]domain.CommissionRule, 0, len(rows))
			for _, r := range rows {
				rules = append(rules, r.Rule)
			}
			rule, ok := domain.ResolveCommissionRule(rules, c.SalesGroupID, l.CustomerGroup, l.ItemGroup)
			if !ok {
				return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCommissionRuleMissing, "Commission rule not found for sales group "+group.Name)
			}
			var e *domain.Error
			basis, splits, e = domain.CalculateCommission(rule, group.Reps, l)
			if e != nil {
				return problemOf(e)
			}
			basisKind, rate, shares = rule.Basis, rule.RatePct, group.Reps
		}
		var cost *string
		var costDec *decimal.Decimal
		if l.HasCost {
			v := l.CostSnapshot.String()
			cost, costDec = &v, &l.CostSnapshot
		}
		for i, sp := range splits {
			t := CommissionTxn{
				ID: ids.New(), LegalEntityID: c.LegalEntityID, InvoiceLineID: l.InvoiceLineID, ReversesInvoiceLineID: l.ReversesInvoiceLineID,
				InvoiceDate: l.InvoiceDate, PeriodKey: l.PeriodKey(), CustomerID: l.CustomerID, CustomerGroup: l.CustomerGroup, ItemGroup: l.ItemGroup,
				SalesGroupID: c.SalesGroupID, RepID: sp.RepID, SharePct: shares[i].SharePct, Basis: basisKind, LineNet: l.LineNet,
				CostSnapshot: costDec, BasisAmount: basis, RatePct: rate, Amount: sp.Amount, Currency: l.Currency,
				DebitAccount: domain.CommissionDebitAccount, CreditAccount: domain.CommissionCreditAccount, Status: "POSTED",
			}
			if _, err := tx.Exec(ctx, `insert into commission_transaction (tenant_id, id, legal_entity_id, invoice_line_id, reverses_invoice_line_id,
				invoice_date, period_key, customer_id, customer_group, item_group, sales_group_id, rep_id, rep_seq, share_pct, basis, line_net,
				cost_snapshot, basis_amount, rate_pct, amount, currency, debit_account, credit_account)
				values ($1, $2, $3, $4, nullif($5, ''), $6, $7, $8, $9, $10, $11, $12, $13, $14::numeric, $15, $16::numeric,
				$17::numeric, $18::numeric, $19::numeric, $20::numeric, $21, $22, $23)`,
				p.TenantID, t.ID, t.LegalEntityID, t.InvoiceLineID, t.ReversesInvoiceLineID, t.InvoiceDate, t.PeriodKey, t.CustomerID,
				t.CustomerGroup, t.ItemGroup, t.SalesGroupID, t.RepID, i+1, t.SharePct.String(), t.Basis, t.LineNet.String(), cost,
				t.BasisAmount.String(), t.RatePct.String(), t.Amount.String(), t.Currency, t.DebitAccount, t.CreditAccount); err != nil {
				return fmt.Errorf("insert commission transaction: %w", err)
			}
			evt, err := events.New(pevents.CommissionCalculated, eventSource, t.ID, p.TenantID, pevents.CommissionCalculatedData{
				TransactionID: t.ID, LegalEntityID: t.LegalEntityID, InvoiceLineID: t.InvoiceLineID, ReversesInvoiceLineID: t.ReversesInvoiceLineID,
				SalesGroupID: t.SalesGroupID, SalesRepID: t.RepID, Basis: t.Basis, BasisAmount: t.BasisAmount.String(), Rate: t.RatePct.String(),
				Amount: t.Amount.String(), Currency: t.Currency, PeriodID: t.PeriodKey, DebitAccount: t.DebitAccount, CreditAccount: t.CreditAccount})
			if err != nil {
				return err
			}
			if err := outbox.Enqueue(ctx, tx, "commission", t.ID, evt); err != nil {
				return err
			}
			out = append(out, t)
		}
		created = true
		return nil
	})
	return out, created, err
}

// problemOf turns a domain error into a 422 problem with its field errors.
func problemOf(e *domain.Error) error {
	p := httpx.NewProblem(http.StatusUnprocessableEntity, e.Code, e.Detail)
	if len(e.Fields) > 0 {
		p.Errors = e.Fields
	}
	return p
}

// commissionTxns reads transactions matched by a trailing WHERE (and ORDER BY) clause.
func commissionTxns(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]CommissionTxn, error) {
	rows, err := tx.Query(ctx, commissionColumns+` `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CommissionTxn{}
	for rows.Next() {
		t, err := scanTxn(rows)
		if err != nil {
			return nil, fmt.Errorf("scan commission transaction: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListCommissionTransactions is the commission inquiry (§5.26.14): newest invoice first.
func (s *Postgres) ListCommissionTransactions(ctx context.Context, f CommissionFilter) ([]CommissionTxn, error) {
	if f.Limit <= 0 || f.Limit > MaxListLimit {
		f.Limit = MaxListLimit
	}
	var out []CommissionTxn
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = commissionTxns(ctx, tx, `where ($1::text = '' or legal_entity_id::text = $1)
			and ($2::text = '' or sales_group_id::text = $2) and ($3::text = '' or rep_id = $3)
			and ($4::text = '' or customer_id = $4) and ($5::text = '' or invoice_line_id = $5 or reverses_invoice_line_id = $5)
			and ($6::text = '' or period_key = $6)
			order by invoice_date desc, created_at desc, invoice_line_id, rep_seq, id limit $7`,
			f.LegalEntityID, f.SalesGroupID, f.RepID, f.CustomerID, f.InvoiceLineID, f.PeriodKey, f.Limit)
		return err
	})
	return out, err
}
