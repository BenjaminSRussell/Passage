package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

// statementQuery reads a customer's documents still open at the end of the
// statement date ($3) in one legal entity, on the same as-of basis as the balance
// confirmation (asOfBalances), so the closing balance is the balance that would be
// confirmed:
//   - a debit item is its amount less the settlements, down-payment clearings and
//     root deductions (which moved the amount off the item) booked by then;
//   - a deduction still being worked on that day (created by then, not resolved by
//     then) is its own line: the customer still owes it, but it is aged apart from
//     invoices (FR-30.17), so it has no due date;
//   - a payment's unapplied credit is its amount less the settlements it made by then.
const statementQuery = `with debits as (
	select o.source_type, o.document_number, o.document_date, o.due_date, o.currency, o.amount,
		o.amount
		- coalesce((select sum(st.settled_amount + st.cash_discount_taken + st.write_off_amount) from settlement st
			where st.tenant_id = o.tenant_id and st.open_item_id = o.id and st.settled_at::date <= $3::date), 0)
		- coalesce((select sum(cs.amount) from credit_settlement cs
			where cs.tenant_id = o.tenant_id and cs.debit_item_id = o.id and cs.settled_at::date <= $3::date), 0)
		+ coalesce((select sum(cs.amount) from credit_settlement cs
			where cs.tenant_id = o.tenant_id and cs.credit_item_id = o.id and cs.settled_at::date <= $3::date), 0)
		- coalesce((select sum(c.amount) from down_payment_clearing c
			where c.tenant_id = o.tenant_id and c.open_item_id = o.id and c.cleared_at::date <= $3::date), 0)
		- coalesce((select sum(d.amount) from deduction d
			where d.tenant_id = o.tenant_id and d.open_item_id = o.id and d.parent_id is null and d.created_at::date <= $3::date), 0)
		as open_as_of
	from open_item o
	where o.legal_entity_id = $1 and o.customer_id = $2 and o.document_date <= $3::date and o.status <> 'REVERSED'
),
deductions as (
	select 'DEDUCTION' as source_type, o.document_number, d.created_at::date as document_date, null::date as due_date,
		d.currency, d.amount, d.amount as open_as_of
	from deduction d join open_item o on o.tenant_id = d.tenant_id and o.id = d.open_item_id
	where d.legal_entity_id = $1 and d.customer_id = $2 and d.created_at::date <= $3::date
		and (d.resolved_at is null or d.resolved_at::date > $3::date)
),
credits as (
	select 'PAYMENT' as source_type, coalesce(p.source_ref, 'PAY-' || left(p.id::text, 8)) as document_number,
		p.received_date as document_date, null::date as due_date, p.currency, -p.amount as amount,
		-(p.amount - coalesce((select sum(st.settled_amount) from settlement st
			where st.tenant_id = p.tenant_id and st.payment_id = p.id and st.settled_at::date <= $3::date), 0)) as open_as_of
	from payment p
	where p.legal_entity_id = $1 and p.customer_id = $2 and p.received_date <= $3::date and p.status <> 'REVERSED'
)
select source_type, document_number, document_date::text, due_date::text, currency, amount::text, open_as_of::text
from (select * from debits union all select * from deductions union all select * from credits) docs
where open_as_of <> 0
order by currency, document_date, document_number`

// Statement returns a customer's account statement in one legal entity as of a
// date (FR-30.13, BR-30.10): the documents open at the end of that day by
// currency, with running and closing balances and an aging footer. It only reads.
func (s *Postgres) Statement(ctx context.Context, legalEntityID, customerID string, asOf time.Time) ([]domain.StatementSection, error) {
	out := []domain.StatementSection{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, statementQuery, legalEntityID, customerID, asOf.Format(dateLayout))
		if err != nil {
			return err
		}
		defer rows.Close()
		var docs []domain.StatementCandidate
		for rows.Next() {
			var d domain.StatementCandidate
			var docDate, amount, open string
			var dueDate *string
			if err := rows.Scan(&d.SourceType, &d.DocumentNumber, &docDate, &dueDate, &d.Currency, &amount, &open); err != nil {
				return err
			}
			if d.DocumentDate, err = ParseDay(docDate); err != nil {
				return err
			}
			if dueDate != nil {
				due, err := ParseDay(*dueDate)
				if err != nil {
					return err
				}
				d.DueDate = &due
			}
			if d.Amount, err = decimal.NewFromString(amount); err != nil {
				return err
			}
			if d.OpenAsOf, err = decimal.NewFromString(open); err != nil {
				return err
			}
			docs = append(docs, d)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out = domain.BuildStatement(asOf, docs)
		return nil
	})
	return out, err
}
