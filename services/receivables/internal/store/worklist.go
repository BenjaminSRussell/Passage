package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

// Worklist ranks the legal entity's customers by risk as of asOf (FR-30.6, FR-30.10):
// one row per customer and currency, riskiest first. Open disputes and broken
// promises (promise date plus grace has passed while the item is still open) feed the score.
func (s *Postgres) Worklist(ctx context.Context, legalEntityID string, asOf time.Time) ([]domain.CustomerRisk, error) {
	var rows []domain.CustomerRisk
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
		rows = domain.Worklist(asOf, items, disputes, broken)
		return nil
	})
	if rows == nil {
		rows = []domain.CustomerRisk{}
	}
	return rows, err
}

// openWorkItems sums open amounts per customer, currency, due date and dunning
// level in SQL. The roll-up gives the same totals, overdue and weighted days as
// the item rows would, and keeps the read complete however many items are open.
func openWorkItems(ctx context.Context, tx pgx.Tx, legalEntityID string) ([]domain.WorkItem, error) {
	rows, err := tx.Query(ctx, `select customer_id::text, currency, due_date::text, sum(open_amount)::text, dunning_level
		from open_item where legal_entity_id = $1 and status in `+openStatuses+` and open_amount > 0
		group by customer_id, currency, due_date, dunning_level
		order by customer_id, currency, due_date`, legalEntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.WorkItem{}
	for rows.Next() {
		var it domain.WorkItem
		var due, open string
		if err := rows.Scan(&it.CustomerID, &it.Currency, &due, &open, &it.DunningLevel); err != nil {
			return nil, err
		}
		if it.DueDate, err = ParseDay(due); err != nil {
			return nil, err
		}
		if it.OpenAmount, err = decimal.NewFromString(open); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func activeDisputeCounts(ctx context.Context, tx pgx.Tx, legalEntityID string) (map[string]int, error) {
	rows, err := tx.Query(ctx, `select customer_id::text, count(*) from dispute
		where legal_entity_id = $1 and status in ('OPEN', 'UNDER_REVIEW') group by customer_id`, legalEntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var customer string
		var n int
		if err := rows.Scan(&customer, &n); err != nil {
			return nil, err
		}
		counts[customer] = n
	}
	return counts, rows.Err()
}

func brokenPromiseCounts(ctx context.Context, tx pgx.Tx, legalEntityID string, asOf time.Time) (map[string]int, error) {
	rows, err := tx.Query(ctx, `select p.customer_id::text, p.promise_date::text, o.open_amount::text
		from promise_to_pay p join open_item o on o.tenant_id = p.tenant_id and o.id = p.open_item_id
		where p.legal_entity_id = $1 and o.status in `+openStatuses, legalEntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var customer, promised, open string
		if err := rows.Scan(&customer, &promised, &open); err != nil {
			return nil, err
		}
		promiseDate, err := ParseDay(promised)
		if err != nil {
			return nil, err
		}
		openAmount, err := decimal.NewFromString(open)
		if err != nil {
			return nil, err
		}
		if domain.BrokenPromise(asOf, promiseDate, openAmount) {
			counts[customer]++
		}
	}
	return counts, rows.Err()
}
