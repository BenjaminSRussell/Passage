package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

// JournalFilter narrows the production journal inquiry across orders (PRD
// 4.21.14). OrderNumber matches the order number exactly (it is unique per
// legal entity, so it can select orders of several entities); Posted is "",
// "true" or "false"; From and To bound the posting date; empty fields do not
// filter. Cursor is the id of the last journal of the previous page.
type JournalFilter struct {
	LegalEntityID string
	OrderID       string
	OrderNumber   string
	Type          domain.JournalType
	Posted        string
	From          string
	To            string
	Cursor        string
	Limit         int
}

// JournalRow is one journal of the inquiry with its line count and values:
// Consumed is the issue and hours lines (material, labor and overhead into WIP)
// and Received the receipt and co-product lines, both negative on a reversal.
type JournalRow struct {
	ID             string
	OrderID        string
	OrderNumber    string
	LegalEntityID  string
	Type           domain.JournalType
	JournalNumber  string
	PostingDate    string
	Posted         bool
	PostedAt       *time.Time
	QtyGood        decimal.Decimal
	QtyError       decimal.Decimal
	ReversalOf     *string
	JournalEntryID *string
	Currency       string
	Lines          int
	Consumed       decimal.Decimal
	Received       decimal.Decimal
}

// JournalInquiry is a page of the journal inquiry and the number of journals the filter selects.
type JournalInquiry struct {
	Items      []JournalRow
	Total      int
	NextCursor string
}

const journalsSelected = `with j as (
	select j.id, j.order_id, o.order_number, j.legal_entity_id, j.journal_type, j.journal_number, j.posting_date, j.posted, j.posted_at,
		j.qty_good, j.qty_error, j.reversal_of, j.journal_entry_id, o.currency::text as currency,
		(select count(*) from production_journal_line l where l.tenant_id = j.tenant_id and l.journal_id = j.id) as lines,
		coalesce((select sum(l.amount + l.overhead_amount) from production_journal_line l
		          where l.tenant_id = j.tenant_id and l.journal_id = j.id and l.kind in ('issue', 'hours')), 0) as consumed,
		coalesce((select sum(l.amount + l.overhead_amount) from production_journal_line l
		          where l.tenant_id = j.tenant_id and l.journal_id = j.id and l.kind in ('receipt', 'coproduct')), 0) as received
	from production_journal j
	join production_order o on o.tenant_id = j.tenant_id and o.id = j.order_id
	where j.tenant_id = $1
	  and ($2 = '' or j.legal_entity_id = nullif($2, '')::uuid)
	  and ($3 = '' or j.order_id = nullif($3, '')::uuid)
	  and ($4 = '' or j.journal_type = $4)
	  and ($5 = '' or j.posted = ($5 = 'true'))
	  and ($6 = '' or j.posting_date >= nullif($6, '')::date)
	  and ($7 = '' or j.posting_date <= nullif($7, '')::date)
	  and ($8 = '' or o.order_number = $8)
)
`

// JournalInquiry lists production journals across orders (PRD 4.21.14), one page
// ordered by id, with the count over the whole filter. RLS limits it to the
// caller's legal entities.
func (s *Postgres) JournalInquiry(ctx context.Context, f JournalFilter) (JournalInquiry, error) {
	p, _ := tenant.FromContext(ctx)
	if f.Limit <= 0 {
		f.Limit = 50
	}
	out := JournalInquiry{Items: []JournalRow{}}
	args := []any{p.TenantID, f.LegalEntityID, f.OrderID, string(f.Type), f.Posted, f.From, f.To, f.OrderNumber}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, journalsSelected+`select id::text, order_id::text, order_number, legal_entity_id::text, journal_type, journal_number,
				posting_date::text, posted, posted_at, qty_good::text, qty_error::text, reversal_of::text, journal_entry_id::text, currency,
				lines, consumed::text, received::text
			from j where ($9 = '' or id > nullif($9, '')::uuid) order by id limit $10`, append(args, f.Cursor, f.Limit+1)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r JournalRow
			var typ, good, bad, consumed, received string
			if err := rows.Scan(&r.ID, &r.OrderID, &r.OrderNumber, &r.LegalEntityID, &typ, &r.JournalNumber, &r.PostingDate, &r.Posted, &r.PostedAt,
				&good, &bad, &r.ReversalOf, &r.JournalEntryID, &r.Currency, &r.Lines, &consumed, &received); err != nil {
				rows.Close()
				return err
			}
			r.Type = domain.JournalType(typ)
			r.QtyGood, r.QtyError, r.Consumed, r.Received = dec(good), dec(bad), dec(consumed), dec(received)
			out.Items = append(out.Items, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		return tx.QueryRow(ctx, journalsSelected+`select count(*) from j`, args...).Scan(&out.Total)
	})
	if err != nil {
		return JournalInquiry{}, err
	}
	if len(out.Items) > f.Limit {
		out.Items = out.Items[:f.Limit]
		out.NextCursor = out.Items[f.Limit-1].ID
	}
	return out, nil
}
