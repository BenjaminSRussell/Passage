package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

type IssueClaimInput struct {
	IdempotencyKey string
	LegalEntityID  string
	ProductID      string
	Warehouse      string
	Batch          string
	Kind           string
	Qty            decimal.Decimal
}

// ClaimIssue records that a service is about to move qty of a product out of
// a warehouse (a pick, a shipment or a production consumption) and refuses
// the claim when it would reach stock a hold still keeps. The on-hand rows of
// the product are locked first, so the held quantity read is the one a
// concurrent hold placement or an earlier claim has committed; the claims of
// the same kind already recorded stand in for the issues that inventory does
// not see as journals. Restricted (BLOCKED, QUARANTINE) rows are not free
// stock. The rule holds over the whole warehouse and, for a claim that names
// a batch, within that batch too. A repeated key is a replay.
func (s *Postgres) ClaimIssue(ctx context.Context, in IssueClaimInput) (replayed bool, err error) {
	tp, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "issue-claim", tp.TenantID, in.IdempotencyKey); err != nil {
			return err
		}
		prior, found, err := loadIssueClaim(ctx, tx, tp.TenantID, in.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if prior.LegalEntityID != in.LegalEntityID || prior.ProductID != in.ProductID || prior.Warehouse != in.Warehouse ||
				prior.Batch != in.Batch || prior.Kind != in.Kind || !prior.Qty.Equal(in.Qty) {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			replayed = true
			return nil
		}
		rows, err := lockProductRows(ctx, tx, tp.TenantID, in.LegalEntityID, in.ProductID)
		if err != nil {
			return err
		}
		// The warehouse is always checked: a claim that names a batch with no
		// hold of its own must still not reach held stock of another batch
		// (more than its batch has, or what batchless claims already took).
		// A batch claim is checked within its batch as well.
		scopes := []string{""}
		if in.Batch != "" {
			scopes = append(scopes, in.Batch)
		}
		for _, batch := range scopes {
			var physical, held decimal.Decimal
			for _, r := range rows {
				if r.dims[domain.DimWarehouse] != in.Warehouse || (batch != "" && r.dims[domain.DimBatch] != batch) ||
					domain.IsRestricted(r.dims[domain.DimStatus]) {
					continue
				}
				physical, held = physical.Add(r.oh.PhysicalInvent()), held.Add(r.oh.HeldPhysical)
			}
			var claimed string
			if err := tx.QueryRow(ctx, `select coalesce(sum(qty), 0)::text from inventory_issue_claim
				where tenant_id = $1 and legal_entity_id = $2 and product_id = $3 and warehouse = $4 and kind = $5
				  and ($6 = '' or batch = '' or batch = $6)`,
				tp.TenantID, in.LegalEntityID, in.ProductID, in.Warehouse, in.Kind, batch).Scan(&claimed); err != nil {
				return err
			}
			if err := domain.CheckIssueClaim(in.Qty, physical, held, decimal.RequireFromString(claimed)); err != nil {
				return domainProblem(err)
			}
		}
		_, err = tx.Exec(ctx, `insert into inventory_issue_claim (tenant_id, legal_entity_id, idempotency_key, product_id, warehouse, batch, kind, qty)
			values ($1, $2, $3, $4, $5, $6, $7, $8)`,
			tp.TenantID, in.LegalEntityID, in.IdempotencyKey, in.ProductID, in.Warehouse, in.Batch, in.Kind, in.Qty.String())
		return scopeProblem(err)
	})
	return replayed, err
}

// ReleaseIssueClaim withdraws a claim whose movement did not commit. An
// unknown key is already released.
func (s *Postgres) ReleaseIssueClaim(ctx context.Context, key string) error {
	tp, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "issue-claim", tp.TenantID, key); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `delete from inventory_issue_claim where tenant_id = $1 and idempotency_key = $2`, tp.TenantID, key)
		return err
	})
}

func loadIssueClaim(ctx context.Context, tx pgx.Tx, tenantID, key string) (IssueClaimInput, bool, error) {
	in := IssueClaimInput{IdempotencyKey: key}
	var qty string
	err := tx.QueryRow(ctx, `select legal_entity_id::text, product_id::text, warehouse, batch, kind, qty::text
		from inventory_issue_claim where tenant_id = $1 and idempotency_key = $2`, tenantID, key).
		Scan(&in.LegalEntityID, &in.ProductID, &in.Warehouse, &in.Batch, &in.Kind, &qty)
	if errors.Is(err, pgx.ErrNoRows) {
		return in, false, nil
	}
	if err != nil {
		return in, false, fmt.Errorf("load issue claim: %w", err)
	}
	in.Qty = decimal.RequireFromString(qty)
	return in, true, nil
}
