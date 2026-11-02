package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

// SnapshotResult reports one aging snapshot run for a legal entity.
type SnapshotResult struct {
	LegalEntityID string `json:"legalEntityId"`
	AsOf          string `json:"asOf"`
	// Rows is the number of customer and currency balances written for the date.
	Rows int `json:"rows"`
	// Changed is the number of customerbalance.changed.v1 events enqueued.
	Changed int `json:"changed"`
}

// BalanceSnapshot is one stored customer balance for a key date.
type BalanceSnapshot struct {
	LegalEntityID  string          `json:"legalEntityId"`
	CustomerID     string          `json:"customerId"`
	Currency       string          `json:"currency"`
	AsOf           string          `json:"asOf"`
	TotalOpen      decimal.Decimal `json:"totalOpen"`
	Overdue        decimal.Decimal `json:"overdue"`
	OldestItemDays int             `json:"oldestItemDays"`
	DunningLevel   int             `json:"dunningLevel"`
	RiskScore      int             `json:"riskScore"`
	TakenAt        time.Time       `json:"takenAt"`
}

// SnapshotFilter narrows a snapshot list. An empty AsOf reads the latest date
// that has snapshots.
type SnapshotFilter struct {
	LegalEntityID string
	AsOf          string
	CustomerID    string
}

// sameBalance reports whether two snapshots carry the same balance, overdue
// amount, dunning level and risk score. Oldest-item days grow every day an item
// stays open, so they alone never count as a change.
func sameBalance(a, b BalanceSnapshot) bool {
	return a.TotalOpen.Equal(b.TotalOpen) && a.Overdue.Equal(b.Overdue) && a.DunningLevel == b.DunningLevel && a.RiskScore == b.RiskScore
}

// RunAgingSnapshot stores the legal entity's net open balance per customer and
// currency (§5.30.16 aging snapshot and risk score, key = (customer, date)), credits
// netted against debits as in domain.CustomerBalances, so a customer can be net in
// credit (a negative balance), and enqueues customerbalance.changed.v1 for every
// balance that differs from the latest snapshot on or before asOf. A customer whose
// last snapshot shows a non-zero balance but who has nothing open any more gets a
// zero row and one event, so credit never keeps a stale balance. Re-running for the same date updates the
// rows and emits nothing for unchanged balances. The balances are those at the
// time of the run, aged at asOf; the API refuses a future asOf.
func (s *Postgres) RunAgingSnapshot(ctx context.Context, legalEntityID string, asOf time.Time) (SnapshotResult, error) {
	p, _ := tenant.FromContext(ctx)
	res := SnapshotResult{LegalEntityID: legalEntityID, AsOf: asOf.Format(dateLayout)}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		items, err := openWorkItems(ctx, tx, legalEntityID)
		if err != nil {
			return err
		}
		disputes, err := activeDisputeCounts(ctx, tx, legalEntityID)
		if err != nil {
			return err
		}
		broken, err := brokenPromiseCounts(ctx, tx, legalEntityID, asOf)
		if err != nil {
			return err
		}
		seen := map[[2]string]bool{}
		for _, r := range domain.CustomerBalances(asOf, items, disputes, broken) {
			seen[[2]string{r.CustomerID, r.Currency}] = true
			changed, err := recordBalance(ctx, tx, p.TenantID, BalanceSnapshot{LegalEntityID: legalEntityID, CustomerID: r.CustomerID,
				Currency: r.Currency, AsOf: res.AsOf, TotalOpen: r.TotalOpen, Overdue: r.Overdue, OldestItemDays: r.OldestDays,
				DunningLevel: r.DunningLevel, RiskScore: r.RiskScore})
			if err != nil {
				return err
			}
			res.Rows++
			if changed {
				res.Changed++
			}
		}
		stale, err := openSnapshotKeys(ctx, tx, legalEntityID, res.AsOf)
		if err != nil {
			return err
		}
		for _, k := range stale {
			if seen[k] {
				continue
			}
			changed, err := recordBalance(ctx, tx, p.TenantID, BalanceSnapshot{LegalEntityID: legalEntityID, CustomerID: k[0],
				Currency: k[1], AsOf: res.AsOf, TotalOpen: decimal.Zero, Overdue: decimal.Zero})
			if err != nil {
				return err
			}
			res.Rows++
			if changed {
				res.Changed++
			}
		}
		return nil
	})
	return res, err
}

// recordBalance upserts one snapshot row and enqueues the change event when the
// balance moved since the latest snapshot on or before its date (the same date's
// row on a re-run). It reports whether an event was enqueued.
func recordBalance(ctx context.Context, tx pgx.Tx, tenantID string, b BalanceSnapshot) (bool, error) {
	prior, err := scanSnapshot(tx.QueryRow(ctx, `select `+snapshotColumns+` from customer_balance_snapshot
		where legal_entity_id = $1 and customer_id = $2 and currency = $3 and as_of <= $4::date
		order by as_of desc limit 1`, b.LegalEntityID, b.CustomerID, b.Currency, b.AsOf))
	known := true
	if errors.Is(err, pgx.ErrNoRows) {
		known = false
	} else if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `insert into customer_balance_snapshot
		(tenant_id, legal_entity_id, customer_id, currency, as_of, total_open, overdue, oldest_item_days, dunning_level, risk_score)
		values ($1, $2, $3, $4, $5::date, $6::numeric, $7::numeric, $8, $9, $10)
		on conflict (tenant_id, legal_entity_id, customer_id, currency, as_of) do update set
		total_open = excluded.total_open, overdue = excluded.overdue, oldest_item_days = excluded.oldest_item_days,
		dunning_level = excluded.dunning_level, risk_score = excluded.risk_score, taken_at = now()`,
		tenantID, b.LegalEntityID, b.CustomerID, b.Currency, b.AsOf, b.TotalOpen.String(), b.Overdue.String(),
		b.OldestItemDays, b.DunningLevel, b.RiskScore); err != nil {
		return false, err
	}
	// A first snapshot of a zero balance is not news: nothing was ever reported open.
	if known && sameBalance(prior, b) || !known && b.TotalOpen.IsZero() {
		return false, nil
	}
	return true, enqueue(ctx, tx, "customer", b.CustomerID, rcvevents.CustomerBalanceChanged, tenantID, rcvevents.CustomerBalanceChangedData{
		LegalEntityID: b.LegalEntityID, CustomerID: b.CustomerID, Currency: b.Currency, AsOf: b.AsOf,
		Balance: b.TotalOpen, Overdue: b.Overdue, OldestItemDays: b.OldestItemDays,
		DunningLevel: b.DunningLevel, RiskScore: b.RiskScore,
	})
}

// openSnapshotKeys returns the customer and currency of every latest snapshot on or
// before asOf whose balance is not zero (owed, or net in credit).
func openSnapshotKeys(ctx context.Context, tx pgx.Tx, legalEntityID, asOf string) ([][2]string, error) {
	rows, err := tx.Query(ctx, `select customer_id::text, currency from (
		select distinct on (customer_id, currency) customer_id, currency, total_open
		from customer_balance_snapshot where legal_entity_id = $1 and as_of <= $2::date
		order by customer_id, currency, as_of desc) latest
		where total_open <> 0`, legalEntityID, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var k [2]string
		if err := rows.Scan(&k[0], &k[1]); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

const snapshotColumns = `legal_entity_id, customer_id, currency, as_of::text, total_open::text, overdue::text,
	oldest_item_days, dunning_level, risk_score, taken_at`

func scanSnapshot(row rowScanner) (BalanceSnapshot, error) {
	var b BalanceSnapshot
	var total, overdue string
	if err := row.Scan(&b.LegalEntityID, &b.CustomerID, &b.Currency, &b.AsOf, &total, &overdue,
		&b.OldestItemDays, &b.DunningLevel, &b.RiskScore, &b.TakenAt); err != nil {
		return BalanceSnapshot{}, err
	}
	var err error
	if b.TotalOpen, err = decimal.NewFromString(total); err != nil {
		return BalanceSnapshot{}, err
	}
	if b.Overdue, err = decimal.NewFromString(overdue); err != nil {
		return BalanceSnapshot{}, err
	}
	return b, nil
}

// ListBalanceSnapshots reads the stored balances of one legal entity for a key
// date (the latest date with snapshots when none is given), riskiest first.
func (s *Postgres) ListBalanceSnapshots(ctx context.Context, f SnapshotFilter, limit int) ([]BalanceSnapshot, error) {
	out := []BalanceSnapshot{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+snapshotColumns+` from customer_balance_snapshot
			where legal_entity_id = $1
			and as_of = coalesce(nullif($2, '')::date, (select max(as_of) from customer_balance_snapshot where legal_entity_id = $1))
			and ($3 = '' or customer_id::text = $3)
			order by risk_score desc, total_open desc, customer_id, currency
			limit $4`, f.LegalEntityID, f.AsOf, f.CustomerID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanSnapshot(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}
