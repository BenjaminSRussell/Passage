package store

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// WorkspaceSummary holds the counts behind the inventory management workspace
// tiles (journals awaiting approval, negative on-hand, open count sheets,
// reconciliation differences, transfer orders by status). Every count is read
// under the caller's row-level security, so it covers the legal entities the
// caller may see.
type WorkspaceSummary struct {
	JournalsDraft             int
	JournalsAwaitingApproval  int
	NegativeOnHandRows        int
	OpenCountSheets           int
	CountSheetsCounted        int
	ReconciliationDifferences int
	// LastReconciliationAt is when the latest reconciliation run in scope was
	// computed (the "last run" of the tile); nil when none has run.
	LastReconciliationAt   *time.Time
	ActiveReservations     int
	TransferOrdersByStatus map[string]int
}

// WorkspaceSummary counts the open work of the legal entity (or of every
// visible legal entity when legalEntityID is empty).
func (s *Postgres) WorkspaceSummary(ctx context.Context, legalEntityID string) (WorkspaceSummary, error) {
	tp, _ := tenant.FromContext(ctx)
	// The column is compared as text, so match the canonical lower-case form.
	legalEntityID = strings.ToLower(legalEntityID)
	out := WorkspaceSummary{TransferOrdersByStatus: map[string]int{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `select
			count(*) filter (where status = 'DRAFT'), count(*) filter (where status = 'SUBMITTED')
			from inventory_journal where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2)`,
			tp.TenantID, legalEntityID).Scan(&out.JournalsDraft, &out.JournalsAwaitingApproval); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select count(*) from on_hand
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2)
			  and posted_qty + received + registered - deducted - picked < 0`,
			tp.TenantID, legalEntityID).Scan(&out.NegativeOnHandRows); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select
			count(*) filter (where status in ('OPEN', 'COUNTED', 'APPROVED')), count(*) filter (where status = 'COUNTED')
			from count_sheet where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2)`,
			tp.TenantID, legalEntityID).Scan(&out.OpenCountSheets, &out.CountSheetsCounted); err != nil {
			return err
		}
		// Only the latest run of each legal entity, account and currency counts:
		// an earlier difference that a later run matched is resolved.
		if err := tx.QueryRow(ctx, `select count(*) filter (where l.status = 'DIFFERENCE'), max(l.created_at) from (
			select distinct on (legal_entity_id, account, currency) status, created_at
			from reconciliation_run where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2)
			order by legal_entity_id, account, currency, run_date desc, created_at desc) l`,
			tp.TenantID, legalEntityID).Scan(&out.ReconciliationDifferences, &out.LastReconciliationAt); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select count(*) from inventory_reservation
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and status = 'ACTIVE'`,
			tp.TenantID, legalEntityID).Scan(&out.ActiveReservations); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select status, count(*) from transfer_order
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) group by status`, tp.TenantID, legalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var status string
			var n int
			if err := rows.Scan(&status, &n); err != nil {
				return err
			}
			out.TransferOrdersByStatus[status] = n
		}
		return rows.Err()
	})
	if err != nil {
		return WorkspaceSummary{}, err
	}
	return out, nil
}
