package store

import (
	"context"
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

func (s *Postgres) ClaimPostings(ctx context.Context, legalEntityID, ledgerID string, through time.Time) ([]Posting, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	var out []Posting
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		groups, order, err := claimLines(ctx, tx, legalEntityID, through)
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
			where legal_entity_id = $1::uuid and entry_id is null and rejected_code is null order by posting_date, id`, legalEntityID)
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
			out = append(out, posting)
		}
		return rows.Err()
	})
	return out, err
}

func claimLines(ctx context.Context, tx pgx.Tx, legalEntityID string, through time.Time) (map[postingKey]*postingGroup, []postingKey, error) {
	rows, err := tx.Query(ctx, `select s.id::text, c.currency::text, date_trunc('month', s.recognized_on)::date, s.amount::text
		from revenue_schedule s
		join performance_obligation o on o.tenant_id = s.tenant_id and o.id = s.obligation_id
		join revenue_contract c on c.tenant_id = o.tenant_id and c.id = o.contract_id
		where c.legal_entity_id = $1::uuid and o.method = any($2::text[]) and s.status = $3 and s.journal_posting_id is null
		  and s.amount > 0 and s.recognized_on <= $4::date
		order by s.recognized_on, s.id for update of s`,
		legalEntityID, domain.DeferredMethods(), domain.ScheduleRecognized, through.Format(dateLayout))
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

func (s *Postgres) CompletePosting(ctx context.Context, postingID, entryID string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update journal_posting set entry_id = $2 where id = $1::uuid and entry_id is null`, postingID, entryID)
		return err
	})
}

func (s *Postgres) RejectPosting(ctx context.Context, postingID, code string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update journal_posting set rejected_code = $2 where id = $1::uuid and entry_id is null and rejected_code is null`, postingID, code)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = tx.Exec(ctx, `update revenue_schedule set journal_posting_id = null where journal_posting_id = $1::uuid`, postingID)
		return err
	})
}
