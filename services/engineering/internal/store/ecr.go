package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	engevents "github.com/BenjaminSRussell/passage/services/engineering/internal/events"
)

const (
	CodeEcrNotFound = "ENG-ECR-NOTFOUND"
	aggregateEcr    = "ecr"
	ecrColumns      = `e.id::text, e.number, e.title, e.reason, e.priority, e.state, e.requested_by, e.decided_by, e.decision_reason,
		coalesce((select o.id::text from eco o where o.tenant_id = e.tenant_id and o.ecr_id = e.id and o.state not in ('rejected', 'cancelled') limit 1), ''),
		coalesce((select array_agg(p.product_id order by p.product_id) from ecr_product p where p.tenant_id = e.tenant_id and p.ecr_id = e.id), '{}')`
)

// EcrFilter narrows ListEcrs; empty fields do not filter.
type EcrFilter struct{ State, ProductID string }

type rowScanner interface{ Scan(dest ...any) error }

func scanEcr(row rowScanner) (domain.Ecr, error) {
	var e domain.Ecr
	err := row.Scan(&e.ID, &e.Number, &e.Title, &e.Reason, &e.Priority, &e.State, &e.RequestedBy, &e.DecidedBy, &e.DecisionReason, &e.EcoID, &e.Products)
	return e, err
}

func notFoundEcr() error {
	return httpx.NewProblem(http.StatusNotFound, CodeEcrNotFound, "ECR not found")
}

func getEcr(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (domain.Ecr, error) {
	query := `select ` + ecrColumns + ` from ecr e where e.tenant_id = $1 and e.id = $2::uuid`
	if lock {
		query += ` for update`
	}
	e, err := scanEcr(tx.QueryRow(ctx, query, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return e, notFoundEcr()
	}
	return e, err
}

// checkEcrSource runs inside CreateEco's transaction. The row lock serialises two ECOs naming the same
// ECR; the unique index on live ECOs is the backstop.
func checkEcrSource(ctx context.Context, tx pgx.Tx, tenantID, ecrID string, replacedProducts []string) error {
	e, err := getEcr(ctx, tx, tenantID, ecrID, true)
	if err != nil {
		return err
	}
	return e.CheckEcoSource(replacedProducts)
}

// CreateEcr stores a draft request with its affected products. A draft raises no event.
func (s *Postgres) CreateEcr(ctx context.Context, e domain.Ecr, idem Idem) (domain.Ecr, error) {
	p, _ := tenant.FromContext(ctx)
	created := e
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replay(ctx, tx, p.TenantID, idem)
		if err != nil {
			return err
		}
		if found {
			created, err = getEcr(ctx, tx, p.TenantID, id, false)
			return err
		}
		if _, err := tx.Exec(ctx, `insert into ecr (tenant_id, id, number, title, reason, priority, state, requested_by) values ($1, $2, $3, $4, $5, $6, $7, $8)`,
			p.TenantID, e.ID, e.Number, e.Title, e.Reason, e.Priority, string(e.State), e.RequestedBy); err != nil {
			return err
		}
		for _, product := range e.Products {
			if _, err := tx.Exec(ctx, `insert into ecr_product (tenant_id, ecr_id, product_id) values ($1, $2, $3)`, p.TenantID, e.ID, product); err != nil {
				return err
			}
		}
		return remember(ctx, tx, p.TenantID, idem, e.ID)
	})
	return created, conflictOnDuplicate(err)
}

func (s *Postgres) GetEcr(ctx context.Context, id string) (domain.Ecr, error) {
	p, _ := tenant.FromContext(ctx)
	var e domain.Ecr
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		e, err = getEcr(ctx, tx, p.TenantID, id, false)
		return err
	})
	return e, err
}

// ListEcrs pages through change requests by id, optionally for one state or one affected product.
func (s *Postgres) ListEcrs(ctx context.Context, f EcrFilter, afterID string, limit int) (Page[domain.Ecr], error) {
	p, _ := tenant.FromContext(ctx)
	if afterID == "" {
		afterID = ids.Nil
	}
	var items []domain.Ecr
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+ecrColumns+` from ecr e where e.tenant_id = $1 and e.id > $2::uuid and ($3 = '' or e.state = $3)
			and ($4 = '' or exists (select 1 from ecr_product x where x.tenant_id = e.tenant_id and x.ecr_id = e.id and x.product_id = $4))
			order by e.id limit $5`, p.TenantID, afterID, f.State, f.ProductID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanEcr(rows)
			if err != nil {
				return err
			}
			items = append(items, e)
		}
		return rows.Err()
	})
	return trimPage(items, limit, func(e domain.Ecr) string { return e.ID }), err
}

// SaveEcr writes a state change made by the domain, guarded by the state the caller loaded, bumps the
// version counter, and raises the matching event in the same transaction: submitted, approved and
// rejected have one, and so does withdrawing a request that was submitted (consumers saw it open); a
// withdrawn draft and the move to under_review have none.
func (s *Postgres) SaveEcr(ctx context.Context, e domain.Ecr, prev domain.EcrState) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update ecr set state = $3, decided_by = $4, decision_reason = $5, version = version + 1 where tenant_id = $1 and id = $2::uuid and state = $6`,
			p.TenantID, e.ID, string(e.State), e.DecidedBy, e.DecisionReason, string(prev))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return httpx.NewProblem(http.StatusConflict, domain.CodeEcrState, "the ECR changed concurrently, reload and retry")
		}
		typ := map[domain.EcrState]string{domain.EcrSubmitted: engevents.EcrSubmitted, domain.EcrApproved: engevents.EcrApproved, domain.EcrRejected: engevents.EcrRejected, domain.EcrCancelled: engevents.EcrCancelled}[e.State]
		if typ == "" || e.State == domain.EcrCancelled && prev == domain.EcrDraft {
			return nil
		}
		return emit(ctx, tx, p, e.ID, typ, aggregateEcr, e.ID, e.EventData())
	})
}
