package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
)

type Subscription struct {
	domain.Subscription
	ID               string
	LegalEntityID    string
	CustomerID       string
	InvoiceAccountID string
	RevenueAccount   string
	TaxCode          string
	PaymentTermDays  int
	Status           string
	Version          int
	IdempotencyKey   string
	RequestHash      string
	BilledThrough    time.Time
}

const subscriptionColumns = `id::text, legal_entity_id::text, customer_id::text, invoice_account_id::text, currency::text, period_months,
  start_date, cancel_effective, status, revenue_account, tax_code, payment_term_days, version, idempotency_key, request_hash,
  (select max(p.period_to) from subscription_period p where p.tenant_id = subscription.tenant_id and p.subscription_id = subscription.id)`

func loadSubscription(ctx context.Context, tx pgx.Tx, where string, args ...any) (Subscription, error) {
	var sub Subscription
	var billedThrough *time.Time
	err := tx.QueryRow(ctx, `select `+subscriptionColumns+` from subscription `+where, args...).Scan(
		&sub.ID, &sub.LegalEntityID, &sub.CustomerID, &sub.InvoiceAccountID, &sub.Currency, &sub.PeriodMonths, &sub.Start,
		&sub.CancelEffective, &sub.Status, &sub.RevenueAccount, &sub.TaxCode, &sub.PaymentTermDays, &sub.Version,
		&sub.IdempotencyKey, &sub.RequestHash, &billedThrough)
	if err != nil {
		return Subscription{}, err
	}
	if billedThrough != nil {
		sub.BilledThrough = *billedThrough
	}
	rows, err := tx.Query(ctx, `select plan_code, price::text, effective_from from subscription_term where subscription_id = $1::uuid order by seq`, sub.ID)
	if err != nil {
		return Subscription{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var term domain.PlanTerm
		var price string
		if err := rows.Scan(&term.PlanCode, &price, &term.From); err != nil {
			return Subscription{}, err
		}
		if term.Price, err = decimal.NewFromString(price); err != nil {
			return Subscription{}, err
		}
		sub.Terms = append(sub.Terms, term)
	}
	return sub, rows.Err()
}

func subscriptionNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NewProblem(http.StatusNotFound, CodeNotFound, "subscription not found")
	}
	return err
}

func (s *Postgres) FindSubscriptionByKey(ctx context.Context, key string) (Subscription, bool, error) {
	var sub Subscription
	found := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := loadSubscription(ctx, tx, `where idempotency_key = $1`, key)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		sub, found = row, err == nil
		return err
	})
	return sub, found, err
}

func (s *Postgres) GetSubscription(ctx context.Context, id string) (Subscription, error) {
	var sub Subscription
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		sub, err = loadSubscription(ctx, tx, `where id = $1`, id)
		return subscriptionNotFound(err)
	})
	return sub, err
}

func (s *Postgres) CreateSubscription(ctx context.Context, sub Subscription) (Subscription, error) {
	p, _ := tenant.FromContext(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into subscription (tenant_id, id, legal_entity_id, customer_id, invoice_account_id, currency, period_months,
			start_date, status, revenue_account, tax_code, payment_term_days, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7, $8::date, 'ACTIVE', $9, $10, $11, $12, $13)`,
			p.TenantID, sub.ID, sub.LegalEntityID, sub.CustomerID, sub.InvoiceAccountID, sub.Currency, sub.PeriodMonths,
			sub.Start.Format(dateLayout), sub.RevenueAccount, sub.TaxCode, sub.PaymentTermDays, sub.IdempotencyKey, sub.RequestHash); err != nil {
			return err
		}
		return insertTerm(ctx, tx, p.TenantID, sub.ID, 1, sub.Terms[0])
	})
	if err != nil {
		return Subscription{}, writeProblem(err)
	}
	sub.Status, sub.Version = domain.SubscriptionActive, 1
	return sub, nil
}

func insertTerm(ctx context.Context, tx pgx.Tx, tenantID, subscriptionID string, seq int, term domain.PlanTerm) error {
	_, err := tx.Exec(ctx, `insert into subscription_term (tenant_id, subscription_id, seq, plan_code, price, effective_from)
		values ($1, $2, $3, $4, $5, $6::date)`, tenantID, subscriptionID, seq, term.PlanCode, term.Price.String(), term.From.Format(dateLayout))
	return err
}

func lockSubscription(ctx context.Context, tx pgx.Tx, id string, expectedVersion int) (Subscription, error) {
	sub, err := loadSubscription(ctx, tx, `where id = $1 for update`, id)
	if err != nil {
		return Subscription{}, subscriptionNotFound(err)
	}
	if expectedVersion != 0 && expectedVersion != sub.Version {
		return Subscription{}, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersion, "the subscription changed since it was read; reload and review it")
	}
	return sub, nil
}

func invoicedProblem() error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeEffectiveInvoiced,
		"the date falls in a billing period that is already invoiced; correct it with a credit note")
}

func (s *Postgres) ChangeSubscription(ctx context.Context, id string, term domain.PlanTerm, expectedVersion int) (Subscription, error) {
	p, _ := tenant.FromContext(ctx)
	var out Subscription
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sub, err := lockSubscription(ctx, tx, id, expectedVersion)
		if err != nil {
			return err
		}
		if err := sub.CheckChange(term.From); err != nil {
			return ruleProblem(err)
		}
		if !sub.BilledThrough.IsZero() && !term.From.After(sub.BilledThrough) {
			return invoicedProblem()
		}
		if err := insertTerm(ctx, tx, p.TenantID, id, len(sub.Terms)+1, term); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update subscription set version = version + 1 where id = $1::uuid`, id); err != nil {
			return err
		}
		sub.Terms = append(sub.Terms, term)
		sub.Version++
		out = sub
		return nil
	})
	return out, err
}

func (s *Postgres) CancelSubscription(ctx context.Context, id string, lastServiceDay time.Time, expectedVersion int) (Subscription, error) {
	var out Subscription
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sub, err := lockSubscription(ctx, tx, id, expectedVersion)
		if err != nil {
			return err
		}
		if err := sub.CheckCancel(lastServiceDay); err != nil {
			return ruleProblem(err)
		}
		if lastServiceDay.Before(sub.BilledThrough) {
			return invoicedProblem()
		}
		if _, err := tx.Exec(ctx, `update subscription set cancel_effective = $2::date, status = 'CANCELLED', version = version + 1 where id = $1::uuid`,
			id, lastServiceDay.Format(dateLayout)); err != nil {
			return err
		}
		sub.CancelEffective, sub.Status = &lastServiceDay, domain.SubscriptionCancelled
		sub.Version++
		out = sub
		return nil
	})
	return out, err
}

func loadPeriodInvoice(ctx context.Context, tx pgx.Tx, subscriptionID string, periodFrom time.Time) (Invoice, bool, error) {
	var invoiceID string
	err := tx.QueryRow(ctx, `select invoice_id::text from subscription_period where subscription_id = $1::uuid and period_from = $2::date`,
		subscriptionID, periodFrom.Format(dateLayout)).Scan(&invoiceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invoice{}, false, nil
	}
	if err != nil {
		return Invoice{}, false, err
	}
	inv, err := loadInvoice(ctx, tx, `where id = $1::uuid`, invoiceID)
	return inv, err == nil, err
}

func (s *Postgres) FindPeriodInvoice(ctx context.Context, subscriptionID string, periodFrom time.Time) (Invoice, bool, error) {
	var inv Invoice
	var found bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		inv, found, err = loadPeriodInvoice(ctx, tx, subscriptionID, periodFrom)
		return err
	})
	return inv, found, err
}

func (s *Postgres) BillSubscription(ctx context.Context, id string, expectedVersion int, periodFrom, periodTo time.Time, inv Invoice) (Invoice, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var replay Invoice
	var replayed bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockSubscription(ctx, tx, id, expectedVersion); err != nil {
			return err
		}
		var err error
		if replay, replayed, err = loadPeriodInvoice(ctx, tx, id, periodFrom); err != nil || replayed {
			return err
		}
		var overlaps bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from subscription_period where subscription_id = $1::uuid and period_from <= $3::date and period_to >= $2::date)`,
			id, periodFrom.Format(dateLayout), periodTo.Format(dateLayout)).Scan(&overlaps); err != nil {
			return err
		}
		if overlaps {
			return httpx.NewProblem(http.StatusConflict, domain.CodePeriodInvoiced, "a billing period overlapping this one is already invoiced")
		}
		var gap bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from subscription_period where subscription_id = $1::uuid)
			and not exists (select 1 from subscription_period where subscription_id = $1::uuid and (period_to = $2::date or period_from = $3::date))`,
			id, periodFrom.AddDate(0, 0, -1).Format(dateLayout), periodTo.AddDate(0, 0, 1).Format(dateLayout)).Scan(&gap); err != nil {
			return err
		}
		if gap {
			return httpx.NewProblem(http.StatusBadRequest, "COM-0001", "a billing period must start the day after the latest invoiced period ends or end the day before the earliest one starts; leave no gap")
		}
		if err := writeProposal(ctx, tx, inv); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into subscription_period (tenant_id, subscription_id, period_from, period_to, invoice_id)
			values ($1, $2, $3::date, $4::date, $5)`, p.TenantID, id, periodFrom.Format(dateLayout), periodTo.Format(dateLayout), inv.ID)
		return err
	})
	if err != nil {
		return Invoice{}, false, writeProblem(err)
	}
	if replayed {
		return replay, true, nil
	}
	inv.Version, inv.Status = 1, domain.StatusProposal
	return inv, false, nil
}
