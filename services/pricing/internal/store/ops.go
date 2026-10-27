package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// ExpiryReason is the reason conditionrecord.withdrawn carries when the nightly expiry job,
// not a user, took a record out of the ACTIVE set (BR-26.9).
const ExpiryReason = "EXPIRED"

// ExpireRecords moves the ACTIVE records of one legal entity whose validity ended before asOf
// to EXPIRED (BR-26.9, the 00:10 record expiry job of §5.26.16). Each record changes by
// (id, version): the update bumps the version and is guarded by status, so a second run
// finds nothing and changes nothing. Every expired record writes conditionrecord.withdrawn
// with reason EXPIRED to the outbox in the same transaction, so the sales cache drops it.
// It returns the records it expired.
func (s *Postgres) ExpireRecords(ctx context.Context, legalEntityID string, asOf time.Time) ([]Entry, error) {
	var expired []Entry
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		expired = nil
		rows, err := tx.Query(ctx, `update condition_record set status = 'EXPIRED', version = version + 1
			where legal_entity_id = $1::uuid and status = 'ACTIVE' and valid_to < $2::date
			returning `+recordColumns, legalEntityID, dayStr(asOf))
		if err != nil {
			return err
		}
		for rows.Next() {
			e, err := scanEntry(rows)
			if err != nil {
				rows.Close()
				return fmt.Errorf("scan expired record: %w", err)
			}
			expired = append(expired, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, e := range expired {
			if err := enqueue(ctx, tx, e, pevents.ConditionRecordWithdrawn, pevents.ConditionRecordWithdrawnData{
				ID: e.ID, Version: e.Version, Reason: ExpiryReason}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return expired, nil
}

// SettlementTotals sums the commission transactions of one legal entity per rep and currency
// for invoices dated from..to inclusive (FR-26.18 settlement export). Credit notes are
// included with their negative amounts, so a rep's total is what is owed for the period.
// Rows are ordered by rep, then currency, so the export is deterministic.
func (s *Postgres) SettlementTotals(ctx context.Context, legalEntityID string, from, to time.Time) ([]domain.SettlementRow, error) {
	out := []domain.SettlementRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out = out[:0]
		rows, err := tx.Query(ctx, `select rep_id, currency::text, sum(amount)::text, count(*)::int
			from commission_transaction
			where legal_entity_id = $1::uuid and invoice_date between $2::date and $3::date
			group by rep_id, currency order by rep_id, currency`, legalEntityID, dayStr(from), dayStr(to))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r domain.SettlementRow
			var amount string
			if err := rows.Scan(&r.RepID, &r.Currency, &amount, &r.Transactions); err != nil {
				return fmt.Errorf("scan settlement row: %w", err)
			}
			if r.Amount, err = decimal.NewFromString(amount); err != nil {
				return fmt.Errorf("settlement amount %q: %w", amount, err)
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// AccrualTotal is the rebate accrued in one currency for one party role (customer rebates
// are an expense, vendor rebates a receivable, so they are never added together).
type AccrualTotal struct {
	PartyRole string
	Currency  string
	Amount    decimal.Decimal
}

// WorkspaceSummary holds the Pricing workspace tiles (§5.26.6) on one day.
type WorkspaceSummary struct {
	// Month is the calendar month of the day, YYYY-MM.
	Month string
	// ActiveRecords and DraftRecords count condition records by status.
	ActiveRecords int
	DraftRecords  int
	// ExpiringIn30Days counts ACTIVE records whose validity ends within 30 days of the day.
	ExpiringIn30Days int
	// JournalsToPost counts price journals still OPEN or VALIDATED.
	JournalsToPost int
	// LiveCampaigns counts campaigns valid on the day and not killed.
	LiveCampaigns int
	// ClaimsAwaitingApproval counts OPEN vendor rebate claims.
	ClaimsAwaitingApproval int
	// AccrualsThisMonth is the rebate accrued on invoices dated in Month, per party role and currency.
	AccrualsThisMonth []AccrualTotal
}

// WorkspaceSummary reads the workspace tiles in one tenant transaction. An empty
// legalEntityID counts every legal entity the caller's scope can read (row-level security
// applies the scope).
func (s *Postgres) WorkspaceSummary(ctx context.Context, legalEntityID string, asOf time.Time) (WorkspaceSummary, error) {
	month := time.Date(asOf.Year(), asOf.Month(), 1, 0, 0, 0, 0, time.UTC)
	out := WorkspaceSummary{Month: month.Format("2006-01")}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out.AccrualsThisMonth = []AccrualTotal{}
		day := dayStr(asOf)
		// $1 is the legal entity ('' for every entity in scope), $2 the day.
		const scope = `($1 = '' or legal_entity_id = nullif($1, '')::uuid)`
		if err := tx.QueryRow(ctx, `select count(*) filter (where status = 'ACTIVE')::int, count(*) filter (where status = 'DRAFT')::int,
				count(*) filter (where status = 'ACTIVE' and valid_to between $2::date and $2::date + 30)::int
				from condition_record where `+scope, legalEntityID, day).
			Scan(&out.ActiveRecords, &out.DraftRecords, &out.ExpiringIn30Days); err != nil {
			return fmt.Errorf("count condition records: %w", err)
		}
		tiles := []struct {
			sql  string
			args []any
			into *int
		}{
			{`select count(*)::int from price_journal where status in ('OPEN', 'VALIDATED') and ` + scope, []any{legalEntityID}, &out.JournalsToPost},
			{`select count(*)::int from campaign where not killed and $2::date between valid_from and valid_to and ` + scope,
				[]any{legalEntityID, day}, &out.LiveCampaigns},
			{`select count(*)::int from vendor_rebate_claim where status = 'OPEN' and ` + scope, []any{legalEntityID}, &out.ClaimsAwaitingApproval},
		}
		for _, t := range tiles {
			if err := tx.QueryRow(ctx, t.sql, t.args...).Scan(t.into); err != nil {
				return fmt.Errorf("count workspace tile: %w", err)
			}
		}
		rows, err := tx.Query(ctx, `select ra.party_role, acc.currency::text, sum(acc.accrued_amount)::text
			from rebate_accrual acc join rebate_agreement ra on ra.tenant_id = acc.tenant_id and ra.id = acc.agreement_id
			where ($1 = '' or acc.legal_entity_id = nullif($1, '')::uuid) and acc.invoice_date between $2::date and $3::date
			group by ra.party_role, acc.currency order by ra.party_role, acc.currency`,
			legalEntityID, dayStr(month), dayStr(month.AddDate(0, 1, -1)))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t AccrualTotal
			var amount string
			if err := rows.Scan(&t.PartyRole, &t.Currency, &amount); err != nil {
				return fmt.Errorf("scan accrual total: %w", err)
			}
			if t.Amount, err = decimal.NewFromString(amount); err != nil {
				return fmt.Errorf("accrual total %q: %w", amount, err)
			}
			out.AccrualsThisMonth = append(out.AccrualsThisMonth, t)
		}
		return rows.Err()
	})
	return out, err
}
