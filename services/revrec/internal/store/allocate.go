package store

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
)

const (
	// CodeContractCancelled is returned when a modification names a cancelled contract.
	CodeContractCancelled = "REV-0104"
	// CodeObligationExists is returned when a modification adds a product the contract already has an obligation for.
	CodeObligationExists = "REV-0105"
)

// AddObligations is the contract modification POST /v1/revrec/contracts/{id}:allocate (#29): it adds performance obligations to a
// contract, for products first invoiced after the contract was opened. The added goods or services are accounted for as a separate
// contract (ASC 606-10-25-12): their price, increase, is allocated over the new obligations only, the transaction price grows by it,
// and the obligations and schedules the contract already had are left as they are. A completed contract is reopened when a new
// obligation has recognition left to do. expectedVersion, when not zero, must be the contract version the caller reviewed (REV-0103).
// The new lines are numbered after the contract's last line. contract.allocated is published again with every obligation.
func (s *Postgres) AddObligations(ctx context.Context, contractID string, increase decimal.Decimal, add []Obligation, expectedVersion int) (Contract, error) {
	p, err := principal(ctx)
	if err != nil {
		return Contract{}, err
	}
	var out Contract
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		var version, lastLine int
		err := tx.QueryRow(ctx, `select c.status, c.version, coalesce((select max(line_no) from performance_obligation o where o.tenant_id = c.tenant_id and o.contract_id = c.id), 0)
			from revenue_contract c where c.id = $1 for update`, contractID).Scan(&status, &version, &lastLine)
		if err == pgx.ErrNoRows {
			return notFound("contract")
		}
		if err != nil {
			return err
		}
		switch {
		case status == "CANCELLED":
			return httpx.NewProblem(http.StatusConflict, CodeContractCancelled, "the contract is cancelled and cannot take new obligations")
		case expectedVersion != 0 && expectedVersion != version:
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersion, "the contract changed since it was read; reload and review it")
		}
		for _, o := range add {
			var n int
			if err := tx.QueryRow(ctx, `select count(*) from performance_obligation where contract_id = $1 and product_id = $2::uuid`, contractID, o.ProductID).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return httpx.NewProblem(http.StatusConflict, CodeObligationExists, "the contract already has an obligation for product "+o.ProductID)
			}
		}
		for i, o := range add {
			o.LineNo = lastLine + i + 1
			if err := insertObligation(ctx, tx, p.TenantID, contractID, o); err != nil {
				return fmt.Errorf("add obligation: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `update revenue_contract c set version = c.version + 1, transaction_price = c.transaction_price + $2::numeric,
		  status = case when exists (
		      select 1 from performance_obligation o
		      left join revenue_schedule s on s.tenant_id = o.tenant_id and s.obligation_id = o.id
		      where o.tenant_id = c.tenant_id and o.contract_id = c.id and (o.method = 'USAGE' or s.status = 'SCHEDULED'))
		    then 'OPEN' else 'COMPLETE' end
		  where c.id = $1`, contractID, increase.String()); err != nil {
			return err
		}
		out, err = loadTree(ctx, tx, "id = $1", contractID)
		if err != nil {
			return err
		}
		return enqueueAllocated(ctx, tx, p.TenantID, out)
	})
	if err != nil {
		return Contract{}, err
	}
	return out, nil
}
