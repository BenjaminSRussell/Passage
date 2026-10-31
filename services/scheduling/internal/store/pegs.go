package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
)

// Error codes of order pegs (README lists them all).
const (
	CodePegOrderUnknown = "SCHED-0120"
	CodePegCycle        = "SCHED-0121"
	CodePegNotFound     = "SCHED-0122"
)

// Peg records that the dependent order consumes the parent order's output
// (FR-23.8): a move with propagate pushes the dependent behind the parent.
type Peg struct {
	ID               string    `json:"id"`
	ParentOrderID    string    `json:"parentOrderId"`
	DependentOrderID string    `json:"dependentOrderId"`
	CreatedAt        time.Time `json:"createdAt"`
}

// PegStore is the order pegging part of the Store.
type PegStore interface {
	CreatePeg(ctx context.Context, in Peg, idem Idem) (Peg, bool, error)
	ListPegs(ctx context.Context, orderID string) ([]Peg, error)
	DeletePeg(ctx context.Context, id string) error
}

const pegCols = "id::text, parent_order_id::text, dependent_order_id::text, created_at"

func scanPeg(row pgx.Row) (Peg, error) {
	var p Peg
	err := row.Scan(&p.ID, &p.ParentOrderID, &p.DependentOrderID, &p.CreatedAt)
	return p, err
}

// CreatePeg records a peg. Both orders must have operations in scheduling
// (SCHED-0120), and a peg that would make an order depend on itself through
// other pegs is refused (SCHED-0121). Pegging a pair that is already pegged
// returns the existing peg with created false. Pegs of a tenant are written
// one at a time, so two requests cannot close a loop between them.
func (s *Postgres) CreatePeg(ctx context.Context, in Peg, idem Idem) (Peg, bool, error) {
	var out Peg
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayRow(ctx, tx, "scheduling_order_peg", idem)
		if err != nil {
			return err
		}
		if !found {
			if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('scheduling_order_peg:' || $1, 0))`, tenantOf(ctx)); err != nil {
				return err
			}
			err := tx.QueryRow(ctx, `select id::text from scheduling_order_peg where parent_order_id = $1::uuid and dependent_order_id = $2::uuid`,
				in.ParentOrderID, in.DependentOrderID).Scan(&id)
			switch {
			case err == nil:
				found = true
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}
		}
		if !found {
			var n int
			if err := tx.QueryRow(ctx, `select count(distinct order_id) from scheduling_operation where order_id = any($1::uuid[])`,
				[]string{in.ParentOrderID, in.DependentOrderID}).Scan(&n); err != nil {
				return err
			}
			if n != 2 {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodePegOrderUnknown, "both orders need operations in scheduling before they can be pegged")
			}
			pegs, err := loadPegs(ctx, tx)
			if err != nil {
				return err
			}
			if domain.WouldCycle(pegs, in.ParentOrderID, in.DependentOrderID) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodePegCycle, "the peg would make the parent order depend on its own dependent")
			}
			id = ids.New()
			if _, err := tx.Exec(ctx, `insert into scheduling_order_peg (tenant_id, id, parent_order_id, dependent_order_id, idempotency_key, idempotency_fingerprint)
				values ($1, $2, $3, $4, $5, $6)`, tenantOf(ctx), id, in.ParentOrderID, in.DependentOrderID, nullable(idem.Key), nullable(idem.Fingerprint)); err != nil {
				return err
			}
			created = true
		}
		out, err = scanPeg(tx.QueryRow(ctx, `select `+pegCols+` from scheduling_order_peg where id = $1`, id))
		return err
	})
	if err != nil {
		return Peg{}, false, mapWriteErr(err, CodeIdempotencyReuse, "a concurrent request used the same Idempotency-Key; retry")
	}
	return out, created, nil
}

// ListPegs returns the pegs, or those where orderID is the parent or the
// dependent, by parent then dependent.
func (s *Postgres) ListPegs(ctx context.Context, orderID string) ([]Peg, error) {
	out := []Peg{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+pegCols+` from scheduling_order_peg
			where ($1 = '' or parent_order_id::text = $1 or dependent_order_id::text = $1)
			order by parent_order_id, dependent_order_id limit 1000`, orderID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPeg(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// DeletePeg removes a peg; later moves no longer push its dependent.
func (s *Postgres) DeletePeg(ctx context.Context, id string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from scheduling_order_peg where id = $1::uuid`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NewProblem(http.StatusNotFound, CodePegNotFound, "order peg not found")
		}
		return nil
	})
}

// loadPegs returns every peg of the tenant.
func loadPegs(ctx context.Context, tx pgx.Tx) ([]domain.Peg, error) {
	rows, err := tx.Query(ctx, `select parent_order_id::text, dependent_order_id::text from scheduling_order_peg`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Peg
	for rows.Next() {
		var p domain.Peg
		if err := rows.Scan(&p.ParentOrderID, &p.DependentOrderID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// pegsAmong keeps the pegs whose two orders are both in orders.
func pegsAmong(pegs []domain.Peg, orders []string) []domain.Peg {
	in := uniqueStrings(orders)
	var out []domain.Peg
	for _, p := range pegs {
		if in[p.ParentOrderID] && in[p.DependentOrderID] {
			out = append(out, p)
		}
	}
	return out
}

// pegsForRun loads the pegs whose dependent order is part of a run and, for
// each parent order outside it, its last scheduled end and whether it holds
// an unplaced operation (FR-23.8): a run places a dependent no earlier than
// its parents end, and not at all behind an unplaced parent.
func pegsForRun(ctx context.Context, tx pgx.Tx, orderIDs []string) ([]domain.Peg, map[string]time.Time, map[string]bool, error) {
	rows, err := tx.Query(ctx, `select parent_order_id::text, dependent_order_id::text from scheduling_order_peg
		where dependent_order_id = any($1::uuid[]) order by parent_order_id, dependent_order_id`, orderIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	var pegs []domain.Peg
	var outside []string
	inRun := map[string]bool{}
	for _, id := range orderIDs {
		inRun[id] = true
	}
	for rows.Next() {
		var p domain.Peg
		if err := rows.Scan(&p.ParentOrderID, &p.DependentOrderID); err != nil {
			return nil, nil, nil, err
		}
		pegs = append(pegs, p)
		if !inRun[p.ParentOrderID] {
			outside = append(outside, p.ParentOrderID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	ends, unplaced := map[string]time.Time{}, map[string]bool{}
	if len(outside) == 0 {
		return pegs, ends, unplaced, nil
	}
	er, err := tx.Query(ctx, `select o.order_id::text, bool_or(o.status = 'unplaced'), max(j.end_at)
		from scheduling_operation o
		left join scheduled_job j on j.tenant_id = o.tenant_id and j.job_id = o.id
		where o.order_id = any($1::uuid[]) group by o.order_id`, outside)
	if err != nil {
		return nil, nil, nil, err
	}
	defer er.Close()
	for er.Next() {
		var id string
		var blocked bool
		var end *time.Time
		if err := er.Scan(&id, &blocked, &end); err != nil {
			return nil, nil, nil, err
		}
		if blocked {
			unplaced[id] = true
		}
		if end != nil {
			ends[id] = *end
		}
	}
	return pegs, ends, unplaced, er.Err()
}
