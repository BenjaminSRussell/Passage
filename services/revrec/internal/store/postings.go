package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/domain"
)

const (
	DeferredAccount = deferredAccount
	RevenueAccount  = revenueAccount
)

type Posting struct {
	ID            string
	LegalEntityID string
	LedgerID      string
	Currency      string
	PostingDate   time.Time
	Amount        decimal.Decimal
	LineCount     int
}

type postingKey struct {
	currency string
	month    time.Time
}

type postingGroup struct {
	lineIDs []string
	amount  decimal.Decimal
}

func (s *Postgres) ClaimPostings(ctx context.Context, legalEntityID, ledgerID string, through time.Time, retryRejected bool) ([]Posting, []Posting, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, nil, err
	}
	var out, otherLedger []Posting
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		groups, order, err := claimLines(ctx, tx, legalEntityID, through, retryRejected)
		if err != nil {
			return err
		}
		for _, key := range order {
			g, id := groups[key], ids.New()
			if _, err := tx.Exec(ctx, `insert into journal_posting (tenant_id, id, legal_entity_id, ledger_id, currency, posting_date, amount, line_count)
				values ($1, $2, $3, $4, $5, $6::date, $7, $8)`, p.TenantID, id, legalEntityID, ledgerID, key.currency,
				key.month.AddDate(0, 1, -1).Format(dateLayout), g.amount.String(), len(g.lineIDs)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update revenue_schedule set journal_posting_id = $1::uuid where id = any($2::uuid[])`, id, g.lineIDs); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `select id::text, ledger_id::text, currency::text, posting_date, amount::text, line_count from journal_posting
			where legal_entity_id = $1::uuid and entry_id is null and rejected_code is null order by posting_date, id for update skip locked`, legalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			posting, amount := Posting{LegalEntityID: legalEntityID}, ""
			if err := rows.Scan(&posting.ID, &posting.LedgerID, &posting.Currency, &posting.PostingDate, &amount, &posting.LineCount); err != nil {
				return err
			}
			if posting.Amount, err = decimal.NewFromString(amount); err != nil {
				return err
			}
			if posting.LedgerID == ledgerID {
				out = append(out, posting)
			} else {
				otherLedger = append(otherLedger, posting)
			}
		}
		return rows.Err()
	})
	return out, otherLedger, err
}

func claimLines(ctx context.Context, tx pgx.Tx, legalEntityID string, through time.Time, retryRejected bool) (map[postingKey]*postingGroup, []postingKey, error) {
	rows, err := tx.Query(ctx, `select s.id::text, c.currency::text, date_trunc('month', s.recognized_on)::date, s.amount::text
		from revenue_schedule s
		join performance_obligation o on o.tenant_id = s.tenant_id and o.id = s.obligation_id
		join revenue_contract c on c.tenant_id = o.tenant_id and c.id = o.contract_id
		where c.legal_entity_id = $1::uuid and o.method = any($2::text[]) and s.status = $3 and s.journal_posting_id is null
		  and (s.rejected_posting_id is null or $5::boolean) and s.amount > 0 and s.recognized_on <= $4::date
		order by s.recognized_on, s.id for update of s`,
		legalEntityID, domain.DeferredMethods(), domain.ScheduleRecognized, through.Format(dateLayout), retryRejected)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	groups := map[postingKey]*postingGroup{}
	var order []postingKey
	for rows.Next() {
		var id, amountText string
		var key postingKey
		if err := rows.Scan(&id, &key.currency, &key.month, &amountText); err != nil {
			return nil, nil, err
		}
		amount, err := decimal.NewFromString(amountText)
		if err != nil {
			return nil, nil, err
		}
		g, ok := groups[key]
		if !ok {
			g = &postingGroup{amount: decimal.Zero}
			groups[key] = g
			order = append(order, key)
		}
		g.lineIDs = append(g.lineIDs, id)
		g.amount = g.amount.Add(amount)
	}
	return groups, order, rows.Err()
}

// ContractAsset is the revenue contracts recognized through end beyond what billing booked for them: the part the 2400 balance
// cannot cover because the allocation put more on the deferred obligations than the invoice billed.
func (s *Postgres) ContractAsset(ctx context.Context, legalEntityID, currency string, end time.Time) (decimal.Decimal, error) {
	var raw string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select coalesce(sum(greatest(r.recognized - `+billedDeferredOf+`, 0)), 0)::text
			from revenue_contract c
			cross join lateral (select coalesce(sum(s.amount), 0) as recognized from revenue_schedule s
				join performance_obligation o on o.tenant_id = s.tenant_id and o.id = s.obligation_id
				where o.tenant_id = c.tenant_id and o.contract_id = c.id and o.method = any($5::text[]) and s.status = 'RECOGNIZED'
				  and s.recognized_on <= $3::date) r
			where c.legal_entity_id = $1::uuid and c.currency = $2 and c.contract_date <= $3::date and $4::text = ''`,
			legalEntityID, currency, end.Format(dateLayout), "", domain.DeferredMethods()).Scan(&raw)
	})
	if err != nil {
		return decimal.Zero, err
	}
	return decimal.NewFromString(raw)
}

type Completion struct {
	Reattached   bool
	Superseded   []string
	DoublePosted []string
}

const SupersededCode = "SUPERSEDED"

func (s *Postgres) CompletePosting(ctx context.Context, postingID, entryID string) (Completion, error) {
	var done Completion
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update journal_posting set entry_id = $2 where id = $1::uuid and entry_id is null and rejected_code is null`, postingID, entryID)
		if err != nil || tag.RowsAffected() > 0 {
			return err
		}
		var rejectedCode string
		err = tx.QueryRow(ctx, `select rejected_code from journal_posting where id = $1::uuid and entry_id is null and rejected_code is not null for update`, postingID).Scan(&rejectedCode)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		done.Reattached = true
		if rejectedCode == SupersededCode {
			done.DoublePosted = append(done.DoublePosted, postingID)
		}
		rows, err := tx.Query(ctx, `select j.id::text, j.entry_id is not null from journal_posting j
			where j.id in (select s.journal_posting_id from revenue_schedule s where s.rejected_posting_id = $1::uuid and s.journal_posting_id <> $1::uuid)
			for update of j`, postingID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var posted bool
			if err := rows.Scan(&id, &posted); err != nil {
				rows.Close()
				return err
			}
			if posted {
				done.DoublePosted = append(done.DoublePosted, id)
			} else {
				done.Superseded = append(done.Superseded, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(done.Superseded) > 0 {
			if _, err := tx.Exec(ctx, `update revenue_schedule set journal_posting_id = null, rejected_posting_id = journal_posting_id
				where journal_posting_id = any($1::uuid[]) and rejected_posting_id is distinct from $2::uuid`, done.Superseded, postingID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update journal_posting set rejected_code = $2 where id = any($1::uuid[])`, done.Superseded, SupersededCode); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update journal_posting set entry_id = $2, rejected_code = null where id = $1::uuid`, postingID, entryID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update revenue_schedule set journal_posting_id = $1::uuid, rejected_posting_id = null
			where rejected_posting_id = $1::uuid and (journal_posting_id is null or journal_posting_id = any($2::uuid[]))`, postingID, done.Superseded)
		return err
	})
	return done, err
}

func (s *Postgres) RejectPosting(ctx context.Context, postingID, code string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update journal_posting set rejected_code = $2 where id = $1::uuid and entry_id is null and rejected_code is null`, postingID, code)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = tx.Exec(ctx, `update revenue_schedule set journal_posting_id = null, rejected_posting_id = $1::uuid where journal_posting_id = $1::uuid`, postingID)
		return err
	})
}
