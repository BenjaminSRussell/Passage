package store

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
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

// FundAttribution charges usage against a fund in a fiscal period (BR-26.15).
type FundAttribution struct {
	FundID    string
	Period    string
	Source    string
	Reference string
	Amount    decimal.Decimal
}

// FundPosition is a fund's budget state for one period after an attribution or read.
type FundPosition struct {
	Fund   domain.Fund
	Period string
	Used   decimal.Decimal
}

// FundRevision changes a fund's budget per period and overspend flag at an expected version.
type FundRevision struct {
	ID              string
	Version         int
	BudgetPerPeriod decimal.Decimal
	AllowOverspend  bool
}

// FundUsageLine is usage summed per period and source (§5.26.14 Fund usage).
type FundUsageLine struct {
	Period string
	Source string
	Amount decimal.Decimal
}

const fundColumns = `id::text, legal_entity_id::text, name, fund_type, currency::text, customer_scope, owner,
	budget_per_period::text, allow_overspend, closed, version`

func scanFund(row pgx.Row) (domain.Fund, error) {
	var f domain.Fund
	var budget string
	if err := row.Scan(&f.ID, &f.LegalEntityID, &f.Name, &f.Type, &f.Currency, &f.CustomerScope, &f.Owner,
		&budget, &f.AllowOverspend, &f.Closed, &f.Version); err != nil {
		return domain.Fund{}, err
	}
	f.BudgetPerPeriod = decimal.RequireFromString(budget)
	return f, nil
}

func fundNotFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeFundNotFound, "no trade fund "+id)
}

// RuleProblem maps a domain fund rule violation to its problem response; other errors pass through.
func RuleProblem(err error) error {
	var de *domain.RuleError
	if errors.As(err, &de) {
		return httpx.NewProblem(de.Status, de.Code, de.Message)
	}
	return err
}

// lockFund reads a fund FOR UPDATE: every attribution, revision, close and reopen of one
// fund is serialized on this row, so each budget check sees every committed usage row.
func lockFund(ctx context.Context, tx pgx.Tx, id string) (domain.Fund, error) {
	f, err := scanFund(tx.QueryRow(ctx, `select `+fundColumns+` from trade_fund where id = $1 for update`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Fund{}, fundNotFound(id)
	}
	return f, err
}

// sameFund reports whether a replayed create carries the same content.
func sameFund(a, b domain.Fund) bool {
	return a.LegalEntityID == b.LegalEntityID && a.Name == b.Name && a.Type == b.Type && a.Currency == b.Currency &&
		a.CustomerScope == b.CustomerScope && a.Owner == b.Owner && a.BudgetPerPeriod.Equal(b.BudgetPerPeriod) &&
		a.AllowOverspend == b.AllowOverspend
}

// CreateFund stores an open fund at version 1. A repeated Idempotency-Key returns the
// original fund, or idempotency_key_reuse when the body differs.
func (s *Postgres) CreateFund(ctx context.Context, f domain.Fund, idempotencyKey string) (domain.Fund, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out domain.Fund
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanFund(tx.QueryRow(ctx, `select `+fundColumns+` from trade_fund where idempotency_key = $1`, idempotencyKey))
			if err == nil {
				if !sameFund(replay, f) {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request body")
				}
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var err error
		out, err = scanFund(tx.QueryRow(ctx, `insert into trade_fund (tenant_id, id, legal_entity_id, name, fund_type, currency, customer_scope, owner,
			budget_per_period, allow_overspend, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10, $11)
			returning `+fundColumns,
			p.TenantID, ids.New(), f.LegalEntityID, f.Name, f.Type, f.Currency, f.CustomerScope, f.Owner,
			f.BudgetPerPeriod.String(), f.AllowOverspend, key))
		return err
	})
	return out, err
}

// GetFund reads one fund.
func (s *Postgres) GetFund(ctx context.Context, id string) (domain.Fund, error) {
	var f domain.Fund
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		f, err = scanFund(tx.QueryRow(ctx, `select `+fundColumns+` from trade_fund where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return fundNotFound(id)
		}
		return err
	})
	return f, err
}

// ListFunds returns funds ordered by id, optionally for one legal entity.
func (s *Postgres) ListFunds(ctx context.Context, legalEntityID string, limit int) ([]domain.Fund, error) {
	if limit <= 0 || limit > MaxListLimit {
		limit = MaxListLimit
	}
	out := []domain.Fund{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+fundColumns+` from trade_fund
			where ($1::text = '' or legal_entity_id::text = $1) order by id limit $2`, legalEntityID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanFund(rows)
			if err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
}

// usedInPeriod sums the usage already attributed to a fund in a period.
func usedInPeriod(ctx context.Context, tx pgx.Tx, fundID, period string) (decimal.Decimal, error) {
	var sum string
	if err := tx.QueryRow(ctx, `select coalesce(sum(amount), 0)::text from trade_fund_usage where fund_id = $1 and period = $2`,
		fundID, period).Scan(&sum); err != nil {
		return decimal.Zero, err
	}
	return decimal.RequireFromString(sum), nil
}

// enqueueEvent writes an event into the outbox inside tx (same transaction as the change).
func enqueueEvent(ctx context.Context, tx pgx.Tx, aggregate, id, typ string, data any) error {
	p, _ := tenant.FromContext(ctx)
	evt, err := events.New(typ, eventSource, id, p.TenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregate, id, evt)
}

// AttributeFund records usage after the budget rules pass (BR-26.15) and announces it in
// the same transaction. A repeated Idempotency-Key replays the first response without
// adding usage; the same key with a different attribution is idempotency_key_reuse.
func (s *Postgres) AttributeFund(ctx context.Context, a FundAttribution, idempotencyKey string) (FundPosition, error) {
	p, _ := tenant.FromContext(ctx)
	var out FundPosition
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		f, err := lockFund(ctx, tx, a.FundID)
		if err != nil {
			return err
		}
		hash := commandHash("fund-attribute", a.FundID, a.Period, a.Source, a.Reference, a.Amount.String())
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		used, err := usedInPeriod(ctx, tx, a.FundID, a.Period)
		if err != nil {
			return err
		}
		if err := domain.CheckAttribution(f, used, a.Amount); err != nil {
			return RuleProblem(err)
		}
		usageID := ids.New()
		if _, err := tx.Exec(ctx, `insert into trade_fund_usage (tenant_id, id, legal_entity_id, fund_id, period, source, reference, amount)
			values ($1, $2, $3, $4, $5, $6, $7, $8::numeric)`,
			p.TenantID, usageID, f.LegalEntityID, a.FundID, a.Period, a.Source, a.Reference, a.Amount.String()); err != nil {
			return err
		}
		out = FundPosition{Fund: f, Period: a.Period, Used: used.Add(a.Amount)}
		if err := enqueueEvent(ctx, tx, "fundusage", usageID, pevents.FundUsageRecorded, pevents.FundUsageRecordedData{
			UsageID: usageID, FundID: f.ID, LegalEntityID: f.LegalEntityID, Period: a.Period, Source: a.Source,
			Reference: a.Reference, Amount: a.Amount.String(), Currency: f.Currency, PeriodUsage: out.Used.String(),
			Budget: f.BudgetPerPeriod.String()}); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// ReviseFund changes the budget per period and the overspend flag at the expected version
// (412 conflict_version otherwise). Without overspend the new budget must cover the busiest
// period's usage (PRC-0012); a closed fund is frozen (PRC-0011).
func (s *Postgres) ReviseFund(ctx context.Context, rev FundRevision, idempotencyKey string) (domain.Fund, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Fund
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		f, err := lockFund(ctx, tx, rev.ID)
		if err != nil {
			return err
		}
		hash := commandHash("fund-revise", rev.ID, strconv.Itoa(rev.Version), rev.BudgetPerPeriod.String(), strconv.FormatBool(rev.AllowOverspend))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if f.Version != rev.Version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "fund version does not match")
		}
		var peak string
		if err := tx.QueryRow(ctx, `select coalesce(max(total), 0)::text from (select sum(amount) as total from trade_fund_usage
			where fund_id = $1 group by period) per_period`, rev.ID).Scan(&peak); err != nil {
			return err
		}
		if err := domain.CheckRevision(f, rev.BudgetPerPeriod, rev.AllowOverspend, decimal.RequireFromString(peak)); err != nil {
			return RuleProblem(err)
		}
		out, err = scanFund(tx.QueryRow(ctx, `update trade_fund set budget_per_period = $2::numeric, allow_overspend = $3, version = version + 1
			where id = $1 returning `+fundColumns, rev.ID, rev.BudgetPerPeriod.String(), rev.AllowOverspend))
		if err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// CloseFund freezes a fund: usage stays as recorded and new attribution fails with PRC-0011.
func (s *Postgres) CloseFund(ctx context.Context, id string, version int, idempotencyKey string) (domain.Fund, error) {
	return s.setFundClosed(ctx, id, version, true, idempotencyKey)
}

// ReopenFund reopens a closed fund (the PRC-0011 remedy, FundMaintain).
func (s *Postgres) ReopenFund(ctx context.Context, id string, version int, idempotencyKey string) (domain.Fund, error) {
	return s.setFundClosed(ctx, id, version, false, idempotencyKey)
}

// setFundClosed moves a fund between open and closed at the expected version and writes
// fund.closed or fund.reopened to the outbox in the same transaction.
func (s *Postgres) setFundClosed(ctx context.Context, id string, version int, closed bool, idempotencyKey string) (domain.Fund, error) {
	p, _ := tenant.FromContext(ctx)
	verb, typ := "fund-reopen", pevents.FundReopened
	if closed {
		verb, typ = "fund-close", pevents.FundClosed
	}
	var out domain.Fund
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		f, err := lockFund(ctx, tx, id)
		if err != nil {
			return err
		}
		hash := commandHash(verb, id, strconv.Itoa(version))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if f.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "fund version does not match")
		}
		if f.Closed == closed {
			state := "open"
			if closed {
				state = "closed"
			}
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "fund is already "+state)
		}
		out, err = scanFund(tx.QueryRow(ctx, `update trade_fund set closed = $2, version = version + 1 where id = $1 returning `+fundColumns, id, closed))
		if err != nil {
			return err
		}
		if err := enqueueEvent(ctx, tx, "fund", out.ID, typ, pevents.FundStatusData{FundID: out.ID, LegalEntityID: out.LegalEntityID,
			Closed: out.Closed, Version: out.Version}); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// FundUsage sums a fund's usage per period and source, oldest period first. It returns
// PRC-0039 when the fund does not exist (or is outside the caller's legal entities).
func (s *Postgres) FundUsage(ctx context.Context, id string) (domain.Fund, []FundUsageLine, error) {
	var f domain.Fund
	out := []FundUsageLine{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		f, err = scanFund(tx.QueryRow(ctx, `select `+fundColumns+` from trade_fund where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return fundNotFound(id)
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select period, source, sum(amount)::text from trade_fund_usage
			where fund_id = $1 group by period, source order by period, source`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l FundUsageLine
			var amount string
			if err := rows.Scan(&l.Period, &l.Source, &amount); err != nil {
				return err
			}
			l.Amount = decimal.RequireFromString(amount)
			out = append(out, l)
		}
		return rows.Err()
	})
	return f, out, err
}
