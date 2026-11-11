package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Budget call kinds and statuses (FR-38.5).
const (
	BudgetCallConvert = "convert"
	BudgetCallRelease = "release"

	BudgetCallPending = "pending"
	BudgetCallDone    = "done"
)

// BudgetCall is a call to budget control that is owed: converting a requisition line's pre-encumbrance into the encumbrance
// of the PO line made from it, or releasing the pre-encumbrances of a cancelled approved requisition. It stays pending
// until budgeting accepted it, so a call that failed can be repeated.
type BudgetCall struct {
	ID                  string     `json:"id"`
	LegalEntityID       string     `json:"legalEntityId"`
	Kind                string     `json:"kind"`
	RequisitionID       string     `json:"requisitionId,omitempty"`
	RequisitionLineID   string     `json:"requisitionLineId,omitempty"`
	PurchaseOrderID     string     `json:"purchaseOrderId,omitempty"`
	PurchaseOrderLineID string     `json:"purchaseOrderLineId,omitempty"`
	Status              string     `json:"status"`
	Attempts            int        `json:"attempts"`
	LastError           string     `json:"lastError,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
	AttemptedAt         *time.Time `json:"attemptedAt,omitempty"`
	DoneAt              *time.Time `json:"doneAt,omitempty"`
}

// WithBudgetCalls turns the budget call queue on: purchase orders made from requisition lines and cancelled approved
// requisitions then write a pending call in their own transaction. Off, nothing is queued (no budgeting service).
func (s *Postgres) WithBudgetCalls(on bool) *Postgres {
	s.queueBudget = on
	return s
}

// enqueueBudgetConvert records that the PO's requisition-linked lines owe a conversion. It is part of the order's own
// transaction, so an order that exists always has its calls queued; a repeat for the same PO line changes nothing.
func enqueueBudgetConvert(ctx context.Context, tx pgx.Tx, o PurchaseOrder, queue bool) error {
	if !queue {
		return nil
	}
	p, _ := tenant.FromContext(ctx)
	for _, l := range o.Lines {
		if l.RequisitionLineID == "" || l.ID == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `insert into budget_call (tenant_id, id, legal_entity_id, kind, requisition_line_id, purchase_order_id, purchase_order_line_id)
			values ($1, $2, $3, 'convert', $4, $5, $6) on conflict do nothing`, p.TenantID, ids.New(), o.LegalEntityID, l.RequisitionLineID, o.ID, l.ID); err != nil {
			return err
		}
	}
	return nil
}

func enqueueBudgetRelease(ctx context.Context, tx pgx.Tx, queue bool, legalEntityID, requisitionID string, lineIDs []string) error {
	if !queue {
		return nil
	}
	p, _ := tenant.FromContext(ctx)
	for _, lineID := range lineIDs {
		if _, err := tx.Exec(ctx, `insert into budget_call (tenant_id, id, legal_entity_id, kind, requisition_id, requisition_line_id) values ($1, $2, $3, 'release', $4, $5)
			on conflict do nothing`, p.TenantID, ids.New(), legalEntityID, requisitionID, lineID); err != nil {
			return err
		}
	}
	return nil
}

const budgetCallColumns = `id, legal_entity_id, kind, coalesce(requisition_id::text, ''), coalesce(requisition_line_id::text, ''), coalesce(purchase_order_id::text, ''),
	coalesce(purchase_order_line_id::text, ''), status, attempts, coalesce(last_error, ''), created_at, attempted_at, done_at`

func scanBudgetCall(row pgx.Row) (BudgetCall, error) {
	var c BudgetCall
	err := row.Scan(&c.ID, &c.LegalEntityID, &c.Kind, &c.RequisitionID, &c.RequisitionLineID, &c.PurchaseOrderID, &c.PurchaseOrderLineID, &c.Status,
		&c.Attempts, &c.LastError, &c.CreatedAt, &c.AttemptedAt, &c.DoneAt)
	return c, err
}

// ListBudgetCalls lists the calls with the status ("" for every status), oldest first.
func (s *Postgres) ListBudgetCalls(ctx context.Context, status string, limit int) ([]BudgetCall, error) {
	p, _ := tenant.FromContext(ctx)
	out := []BudgetCall{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+budgetCallColumns+` from budget_call where tenant_id = $1 and ($2 = '' or status = $2) order by created_at, id limit $3`,
			p.TenantID, status, clampLimit(limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanBudgetCall(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// BudgetCallsFor lists the calls of one purchase order or one requisition, any status.
func (s *Postgres) BudgetCallsFor(ctx context.Context, purchaseOrderID, requisitionID string) ([]BudgetCall, error) {
	p, _ := tenant.FromContext(ctx)
	out := []BudgetCall{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+budgetCallColumns+` from budget_call where tenant_id = $1
			and (($2 <> '' and purchase_order_id::text = $2) or ($3 <> '' and requisition_id::text = $3)) order by created_at, id`, p.TenantID, purchaseOrderID, requisitionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanBudgetCall(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// RecordBudgetCall notes an attempt: done when callErr is nil, otherwise one more failed attempt with its message.
func (s *Postgres) RecordBudgetCall(ctx context.Context, id string, callErr error) error {
	p, _ := tenant.FromContext(ctx)
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if callErr == nil {
			_, err := tx.Exec(ctx, `update budget_call set status = 'done', attempts = attempts + 1, attempted_at = now(), done_at = now(), last_error = null
				where tenant_id = $1 and id = $2 and status = 'pending'`, p.TenantID, id)
			return err
		}
		msg := callErr.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		_, err := tx.Exec(ctx, `update budget_call set attempts = attempts + 1, attempted_at = now(), last_error = $3 where tenant_id = $1 and id = $2 and status = 'pending'`, p.TenantID, id, msg)
		return err
	})
}
