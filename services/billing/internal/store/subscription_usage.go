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
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	// CodeUsageConflict is BIL-0110: a usage record with this external id exists with other values.
	CodeUsageConflict = "BIL-0110"
	// CodeUsageInvalid is BIL-0111: the usage record cannot be taken by this subscription.
	CodeUsageInvalid = "BIL-0111"
	// CodeUsageBilled is BIL-0112: a usage record of the bill was invoiced meanwhile by another request.
	CodeUsageBilled = "BIL-0112"
)

// Usage is one metered quantity of a subscription: Quantity units of the meter at UnitPrice on UsageDate, invoiced once with
// the billing period it falls in or the first period billed after it.
type Usage struct {
	ID             string
	SubscriptionID string
	LegalEntityID  string
	ExternalID     string
	MeterCode      string
	UsageDate      time.Time
	Quantity       decimal.Decimal
	UnitPrice      decimal.Decimal
	RequestHash    string
	InvoiceID      string
}

const usageColumns = `id::text, subscription_id::text, legal_entity_id::text, external_id, meter_code, usage_date, quantity::text, unit_price::text,
	request_hash, coalesce(invoice_id::text, '')`

func scanUsage(row pgx.Row) (Usage, error) {
	var u Usage
	var qty, price string
	if err := row.Scan(&u.ID, &u.SubscriptionID, &u.LegalEntityID, &u.ExternalID, &u.MeterCode, &u.UsageDate, &qty, &price, &u.RequestHash, &u.InvoiceID); err != nil {
		return Usage{}, err
	}
	var err error
	if u.Quantity, err = decimal.NewFromString(qty); err != nil {
		return Usage{}, err
	}
	u.UnitPrice, err = decimal.NewFromString(price)
	return u, err
}

// RecordUsage stores a usage record of a subscription. A repeat of the same external id with the same values returns the stored
// record (replayed); other values under the same external id are refused (BIL-0110). A usage date before the subscription
// started, after its last service day, or inside a period already invoiced is refused (BIL-0111): that invoice is issued
// and cannot carry it.
func (s *Postgres) RecordUsage(ctx context.Context, subscriptionID string, in Usage) (Usage, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Usage
	var replayed bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sub, err := lockSubscription(ctx, tx, subscriptionID, 0)
		if err != nil {
			return err
		}
		existing, err := scanUsage(tx.QueryRow(ctx, `select `+usageColumns+` from subscription_usage where subscription_id = $1::uuid and external_id = $2`, subscriptionID, in.ExternalID))
		if err == nil {
			if existing.RequestHash != in.RequestHash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeUsageConflict, "a usage record with this externalId already exists with other values")
			}
			out, replayed = existing, true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		switch {
		case in.UsageDate.Before(dateOnly(sub.Start)):
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeUsageInvalid, "the usage date is before the subscription started")
		case sub.CancelEffective != nil && in.UsageDate.After(*sub.CancelEffective):
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeUsageInvalid, "the usage date is after the subscription's last service day")
		case !sub.BilledThrough.IsZero() && !in.UsageDate.After(sub.BilledThrough):
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeUsageInvalid,
				"the usage date falls in a billing period that is already invoiced; correct it with a credit or a manual invoice")
		}
		in.ID, in.SubscriptionID, in.LegalEntityID = ids.New(), subscriptionID, sub.LegalEntityID
		_, err = tx.Exec(ctx, `insert into subscription_usage (tenant_id, id, legal_entity_id, subscription_id, external_id, meter_code, usage_date,
			quantity, unit_price, request_hash) values ($1, $2, $3, $4, $5, $6, $7::date, $8::numeric, $9::numeric, $10)`,
			p.TenantID, in.ID, in.LegalEntityID, in.SubscriptionID, in.ExternalID, in.MeterCode, in.UsageDate.Format(dateLayout),
			in.Quantity.String(), in.UnitPrice.String(), in.RequestHash)
		out = in
		return err
	})
	if err != nil {
		return Usage{}, false, writeProblem(err)
	}
	return out, replayed, nil
}

// ListUnbilledUsage returns the usage of a subscription not yet invoiced and dated on or before upTo, oldest first.
func (s *Postgres) ListUnbilledUsage(ctx context.Context, subscriptionID string, upTo time.Time) ([]Usage, error) {
	var out []Usage
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+usageColumns+` from subscription_usage
			where subscription_id = $1::uuid and invoice_id is null and usage_date <= $2::date order by usage_date, external_id`, subscriptionID, upTo.Format(dateLayout))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			u, err := scanUsage(rows)
			if err != nil {
				return err
			}
			out = append(out, u)
		}
		return rows.Err()
	})
	return out, err
}

// DueSubscriptions lists the subscriptions of a legal entity with a billing period due on asOf: the period after the one
// billed last (the first of the start month when none was billed) opens on or before asOf, and the subscription was not
// cancelled before it. At most limit, oldest first; a subscription with several periods due is listed once.
func (s *Postgres) DueSubscriptions(ctx context.Context, legalEntityID string, asOf time.Time, limit int) ([]string, error) {
	var out []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select sub.id::text from (
				select s.id, s.start_date, s.cancel_effective,
					coalesce((select max(p.period_to) + 1 from subscription_period p where p.tenant_id = s.tenant_id and p.subscription_id = s.id),
						date_trunc('month', s.start_date)::date) as next_from
				from subscription s where s.legal_entity_id = $1::uuid) sub
			where sub.next_from <= $2::date and (sub.cancel_effective is null or sub.next_from <= sub.cancel_effective)
			order by sub.next_from, sub.id limit $3`, legalEntityID, asOf.Format(dateLayout), limit)
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

// SubscriptionLegalEntities lists the legal entities of the caller's tenant that hold subscriptions, so a worker can run the
// due-period job once per legal entity under a principal scoped to it.
func (s *Postgres) SubscriptionLegalEntities(ctx context.Context) ([]string, error) {
	p, _ := tenant.FromContext(ctx)
	var out []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select distinct legal_entity_id::text from subscription where tenant_id = $1`, p.TenantID)
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

func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
