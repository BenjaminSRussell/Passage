package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

// Invoicing plan problem codes raised by the store (404s).
const (
	CodeInvoicingPlanNotFound = "PRC-IP-NOT-FOUND"
	CodePOLineNotFound        = "PRC-PO-LINE-NOT-FOUND"
)

// InvoicingPlanInput is a requested plan. Kind selects Periodic or Milestones.
type InvoicingPlanInput struct {
	Kind                 string
	Periodic             domain.PeriodicSpec
	Milestones           []domain.Milestone
	SettleWithoutReceipt bool
}

// PlanResult is a plan with the PO row version after the call: plan writes bump
// the PO's row version, so the next PO or plan write must carry the new ETag.
type PlanResult struct {
	domain.InvoicingPlan
	PORowVersion int `json:"poRowVersion"`
}

// PutInvoicingPlan replaces the invoicing plan of one PO line (FR-17.21),
// generating its rows from the line's current amount so they sum to it
// (BR-17.17). ifMatch is the PO row version the caller read (BR-17.8); the
// write bumps it. A cancelled or finalized PO or a cancelled line cannot take a
// plan (PRC-IP-003), and a plan with invoiced rows cannot be replaced (PRC-IP-002).
func (s *Postgres) PutInvoicingPlan(ctx context.Context, poID string, lineNo, ifMatch int, in InvoicingPlanInput) (PlanResult, error) {
	var out PlanResult
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		po, line, err := lockPlanLine(ctx, tx, poID, lineNo, ifMatch)
		if err != nil {
			return err
		}
		if err := domain.CheckPlanTarget(po.ApprovalStatus, line.Status); err != nil {
			return err
		}
		if err := refuseInvoiced(ctx, tx, poID, lineNo, "replaced"); err != nil {
			return err
		}
		minor := money.MinorUnits(po.Currency)
		plan := domain.InvoicingPlan{PurchaseOrderID: poID, LineNo: lineNo, Kind: in.Kind,
			SettleWithoutReceipt: in.SettleWithoutReceipt, LineAmount: line.LineAmount, Currency: po.Currency}
		switch in.Kind {
		case domain.PlanPeriodic:
			plan.Rows, err = domain.GeneratePeriodic(in.Periodic, line.LineAmount, minor)
			plan.Frequency, plan.BillingDay = in.Periodic.Frequency, in.Periodic.BillingDay
			if plan.BillingDay == 0 {
				plan.BillingDay = in.Periodic.Start.Day()
			}
		case domain.PlanPartial:
			plan.Rows, err = domain.GeneratePartial(in.Milestones, line.LineAmount, minor)
		default:
			err = &domain.Error{Code: domain.CodeValidation, Detail: "kind must be PERIODIC or PARTIAL"}
		}
		if err != nil {
			return err
		}
		if err := writePlan(ctx, tx, plan); err != nil {
			return err
		}
		if _, err := bumpPORowVersion(ctx, tx, poID); err != nil {
			return err
		}
		return planResult(ctx, tx, poID, lineNo, &out)
	})
	return out, err
}

// DeleteInvoicingPlan removes the plan of one PO line unless a row is
// invoiced (PRC-IP-002); ifMatch is the PO row version, which the delete bumps.
func (s *Postgres) DeleteInvoicingPlan(ctx context.Context, poID string, lineNo, ifMatch int) (int, error) {
	var rowVersion int
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if _, _, err := lockPlanLine(ctx, tx, poID, lineNo, ifMatch); err != nil {
			return err
		}
		if _, err := loadPlan(ctx, tx, poID, lineNo); err != nil {
			return err
		}
		if err := refuseInvoiced(ctx, tx, poID, lineNo, "removed"); err != nil {
			return err
		}
		if err := deletePlan(ctx, tx, poID, lineNo); err != nil {
			return err
		}
		var err error
		rowVersion, err = bumpPORowVersion(ctx, tx, poID)
		return err
	})
	return rowVersion, err
}

// GetInvoicingPlan returns the plan of one PO line; 404 when the PO is not
// visible or the line has no plan.
func (s *Postgres) GetInvoicingPlan(ctx context.Context, poID string, lineNo int) (PlanResult, error) {
	var out PlanResult
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := checkVisible(ctx, tx, poID); err != nil {
			return err
		}
		return planResult(ctx, tx, poID, lineNo, &out)
	})
	return out, err
}

// ListInvoicingPlans returns every plan of a PO in line order; 404 when the PO
// is not visible.
func (s *Postgres) ListInvoicingPlans(ctx context.Context, poID string) ([]domain.InvoicingPlan, error) {
	out := []domain.InvoicingPlan{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := checkVisible(ctx, tx, poID); err != nil {
			return err
		}
		var err error
		out, err = loadPlans(ctx, tx, poID)
		return err
	})
	return out, err
}

// PlanRowSettlement is the record of a plan row settled by receiving (#289).
type PlanRowSettlement struct {
	PurchaseOrderID string          `json:"purchaseOrderId"`
	LineNo          int             `json:"lineNo"`
	Seq             int             `json:"seq"`
	Status          string          `json:"status"`
	SettlementRef   string          `json:"settlementRef"`
	Amount          decimal.Decimal `json:"amount"`
	Replayed        bool            `json:"replayed"`
}

// MarkPlanRowInvoiced flips one OPEN plan row to INVOICED for the settlement
// receiving posted (#289) and keeps who settled it. It is idempotent per row and
// settlement reference: the same reference and amount again answers Replayed,
// another reference or amount for a settled row, or an amount that differs from
// the row's (a reprice re-split it after receiving read it), is PRC-IP-004 (409).
// It takes the PO lock, so a concurrent reprice sees either the OPEN row or the
// INVOICED one, and bumps the PO row version because the plan changed.
func (s *Postgres) MarkPlanRowInvoiced(ctx context.Context, poID string, lineNo, seq int, ref string, amount decimal.Decimal) (PlanRowSettlement, error) {
	p, _ := tenant.FromContext(ctx)
	out := PlanRowSettlement{PurchaseOrderID: poID, LineNo: lineNo, Seq: seq, Status: domain.PlanRowInvoiced, SettlementRef: ref, Amount: amount}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		out.Replayed = false
		if err := lockPurchaseOrder(ctx, tx, poID); err != nil {
			return err
		}
		var priorRef string
		var priorAmount decimal.Decimal
		err := tx.QueryRow(ctx, `select settlement_ref, amount from purchase_invoicing_plan_row_settlement
			where tenant_id = $1 and purchase_order_id = $2 and line_no = $3 and seq = $4`, p.TenantID, poID, lineNo, seq).Scan(&priorRef, &priorAmount)
		if err == nil {
			if priorRef != ref || !priorAmount.Equal(amount) {
				return planRowConflict(lineNo, seq, "is already settled by %s for %s", priorRef, priorAmount.String())
			}
			out.Replayed = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var status string
		var rowAmount decimal.Decimal
		err = tx.QueryRow(ctx, `select status, amount from purchase_invoicing_plan_row
			where tenant_id = $1 and purchase_order_id = $2 and line_no = $3 and seq = $4 for update`, p.TenantID, poID, lineNo, seq).Scan(&status, &rowAmount)
		if err != nil {
			return notFound(err, CodeInvoicingPlanNotFound, fmt.Sprintf("line %d has no plan row %d", lineNo, seq))
		}
		if status != domain.PlanRowOpen {
			return planRowConflict(lineNo, seq, "is %s without a settlement record", status)
		}
		if !rowAmount.Equal(amount) {
			return planRowConflict(lineNo, seq, "is now %s; the settlement was for %s", rowAmount.String(), amount.String())
		}
		if _, err := tx.Exec(ctx, `update purchase_invoicing_plan_row set status = $5
			where tenant_id = $1 and purchase_order_id = $2 and line_no = $3 and seq = $4`, p.TenantID, poID, lineNo, seq, domain.PlanRowInvoiced); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into purchase_invoicing_plan_row_settlement (tenant_id, purchase_order_id, line_no, seq, settlement_ref, amount, settled_by, settled_via)
			values ($1, $2, $3, $4, $5, $6, $7, $8)`, p.TenantID, poID, lineNo, seq, ref, amount, p.UserID, p.Service); err != nil {
			return err
		}
		_, err = bumpPORowVersion(ctx, tx, poID)
		return err
	})
	return out, err
}

func planRowConflict(lineNo, seq int, format string, args ...any) error {
	return httpx.NewProblem(http.StatusConflict, domain.CodePlanRowSettlement,
		fmt.Sprintf("plan row %d of line %d ", seq, lineNo)+fmt.Sprintf(format, args...))
}

// syncInvoicingPlans keeps every plan of a PO equal to its line after the
// lines changed (BR-17.17): a changed line amount is reproportioned over the
// open rows, and the plan of a cancelled line is removed. A plan with invoiced
// rows refuses both a line amount below what it invoiced and cancelling the
// line (PRC-IP-001, PRC-IP-002). Runs inside the edit or cancel transaction.
func syncInvoicingPlans(ctx context.Context, tx pgx.Tx, poID string) error {
	plans, err := loadPlans(ctx, tx, poID)
	if err != nil || len(plans) == 0 {
		return err
	}
	po, err := loadPurchaseOrder(ctx, tx, poID)
	if err != nil {
		return err
	}
	lines := make(map[int]PurchaseOrderLine, len(po.Lines))
	for _, l := range po.Lines {
		lines[l.LineNo] = l
	}
	minor := money.MinorUnits(po.Currency)
	for _, plan := range plans {
		line := lines[plan.LineNo]
		if line.Status == domain.POLineCancelled {
			if err := refuseInvoiced(ctx, tx, poID, plan.LineNo, "removed with its cancelled line"); err != nil {
				return err
			}
			if err := deletePlan(ctx, tx, poID, plan.LineNo); err != nil {
				return err
			}
			continue
		}
		if line.LineAmount.Equal(plan.LineAmount) {
			continue
		}
		rows, err := domain.Reproportion(plan.Rows, line.LineAmount, minor)
		if err != nil {
			var de *domain.Error
			if errors.As(err, &de) {
				de.Detail = fmt.Sprintf("line %d: %s", plan.LineNo, de.Detail)
			}
			return err
		}
		plan.Rows, plan.LineAmount = rows, line.LineAmount
		if err := writePlan(ctx, tx, plan); err != nil {
			return err
		}
	}
	return nil
}

// lockPlanLine locks the PO, checks If-Match and returns the PO and the line.
func lockPlanLine(ctx context.Context, tx pgx.Tx, poID string, lineNo, ifMatch int) (PurchaseOrder, PurchaseOrderLine, error) {
	if err := lockPurchaseOrder(ctx, tx, poID); err != nil {
		return PurchaseOrder{}, PurchaseOrderLine{}, err
	}
	po, err := loadPurchaseOrder(ctx, tx, poID)
	if err != nil {
		return PurchaseOrder{}, PurchaseOrderLine{}, err
	}
	if err := checkRowVersion(po, ifMatch); err != nil {
		return PurchaseOrder{}, PurchaseOrderLine{}, err
	}
	for _, l := range po.Lines {
		if l.LineNo == lineNo {
			return po, l, nil
		}
	}
	return PurchaseOrder{}, PurchaseOrderLine{}, httpx.NewProblem(http.StatusNotFound, CodePOLineNotFound,
		fmt.Sprintf("purchase order %s has no line %d", po.Number, lineNo))
}

// refuseInvoiced is PRC-IP-002 when the plan of the line has an invoiced row.
func refuseInvoiced(ctx context.Context, tx pgx.Tx, poID string, lineNo int, what string) error {
	p, _ := tenant.FromContext(ctx)
	var n int
	if err := tx.QueryRow(ctx, `select count(*) from purchase_invoicing_plan_row
		where tenant_id = $1 and purchase_order_id = $2 and line_no = $3 and status = 'INVOICED'`,
		p.TenantID, poID, lineNo).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return &domain.Error{Code: domain.CodePlanInvoiced,
			Detail: fmt.Sprintf("the invoicing plan of line %d has %d invoiced rows and cannot be %s", lineNo, n, what)}
	}
	return nil
}

// bumpPORowVersion increments the PO row version (its ETag) and returns it.
func bumpPORowVersion(ctx context.Context, tx pgx.Tx, poID string) (int, error) {
	p, _ := tenant.FromContext(ctx)
	var v int
	err := tx.QueryRow(ctx, `update purchase_order set row_version = row_version + 1, updated_at = now()
		where tenant_id = $1 and id = $2 returning row_version`, p.TenantID, poID).Scan(&v)
	return v, err
}

// planResult reads the plan and the PO's current row version into out.
func planResult(ctx context.Context, tx pgx.Tx, poID string, lineNo int, out *PlanResult) error {
	plan, err := loadPlan(ctx, tx, poID, lineNo)
	if err != nil {
		return err
	}
	p, _ := tenant.FromContext(ctx)
	var v int
	if err := tx.QueryRow(ctx, `select row_version from purchase_order where tenant_id = $1 and id = $2`, p.TenantID, poID).Scan(&v); err != nil {
		return err
	}
	*out = PlanResult{InvoicingPlan: plan, PORowVersion: v}
	return nil
}

// writePlan replaces the header and rows of one plan.
func writePlan(ctx context.Context, tx pgx.Tx, plan domain.InvoicingPlan) error {
	p, _ := tenant.FromContext(ctx)
	if err := deletePlan(ctx, tx, plan.PurchaseOrderID, plan.LineNo); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into purchase_invoicing_plan (tenant_id, purchase_order_id, line_no, kind, frequency,
		billing_day, settle_without_receipt, line_amount, currency)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		p.TenantID, plan.PurchaseOrderID, plan.LineNo, plan.Kind, nullable(plan.Frequency), nullableInt(plan.BillingDay),
		plan.SettleWithoutReceipt, plan.LineAmount, plan.Currency); err != nil {
		return err
	}
	for _, r := range plan.Rows {
		if _, err := tx.Exec(ctx, `insert into purchase_invoicing_plan_row (tenant_id, purchase_order_id, line_no, seq, due_date,
			amount, percent, status) values ($1, $2, $3, $4, $5, $6, $7, $8)`,
			p.TenantID, plan.PurchaseOrderID, plan.LineNo, r.Seq, r.DueDate.Time, r.Amount, r.Percent, r.Status); err != nil {
			return err
		}
	}
	return nil
}

func deletePlan(ctx context.Context, tx pgx.Tx, poID string, lineNo int) error {
	p, _ := tenant.FromContext(ctx)
	_, err := tx.Exec(ctx, `delete from purchase_invoicing_plan where tenant_id = $1 and purchase_order_id = $2 and line_no = $3`,
		p.TenantID, poID, lineNo)
	return err
}

// loadPlan reads one plan; 404 PRC-IP-NOT-FOUND when the line has none.
func loadPlan(ctx context.Context, tx pgx.Tx, poID string, lineNo int) (domain.InvoicingPlan, error) {
	plans, err := queryPlans(ctx, tx, poID, &lineNo)
	if err != nil {
		return domain.InvoicingPlan{}, err
	}
	if len(plans) == 0 {
		return domain.InvoicingPlan{}, httpx.NewProblem(http.StatusNotFound, CodeInvoicingPlanNotFound,
			fmt.Sprintf("line %d has no invoicing plan", lineNo))
	}
	return plans[0], nil
}

func loadPlans(ctx context.Context, tx pgx.Tx, poID string) ([]domain.InvoicingPlan, error) {
	return queryPlans(ctx, tx, poID, nil)
}

// queryPlans reads the plans of a PO (one line when lineNo is set) with their
// rows in sequence order.
func queryPlans(ctx context.Context, tx pgx.Tx, poID string, lineNo *int) ([]domain.InvoicingPlan, error) {
	p, _ := tenant.FromContext(ctx)
	rows, err := tx.Query(ctx, `select line_no, kind, coalesce(frequency, ''), coalesce(billing_day, 0), settle_without_receipt,
		line_amount, currency, updated_at from purchase_invoicing_plan
		where tenant_id = $1 and purchase_order_id = $2 and ($3::int is null or line_no = $3) order by line_no`, p.TenantID, poID, lineNo)
	if err != nil {
		return nil, err
	}
	plans, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.InvoicingPlan, error) {
		pl := domain.InvoicingPlan{PurchaseOrderID: poID, Rows: []domain.PlanRow{}}
		err := row.Scan(&pl.LineNo, &pl.Kind, &pl.Frequency, &pl.BillingDay, &pl.SettleWithoutReceipt, &pl.LineAmount,
			&pl.Currency, &pl.UpdatedAt)
		return pl, err
	})
	if err != nil || len(plans) == 0 {
		return []domain.InvoicingPlan{}, err
	}
	index := make(map[int]int, len(plans))
	for i, pl := range plans {
		index[pl.LineNo] = i
	}
	rs, err := tx.Query(ctx, `select line_no, seq, due_date, amount, percent, status from purchase_invoicing_plan_row
		where tenant_id = $1 and purchase_order_id = $2 and ($3::int is null or line_no = $3) order by line_no, seq`, p.TenantID, poID, lineNo)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	for rs.Next() {
		var no int
		var r domain.PlanRow
		var due time.Time
		var pct *decimal.Decimal
		if err := rs.Scan(&no, &r.Seq, &due, &r.Amount, &pct, &r.Status); err != nil {
			return nil, err
		}
		r.DueDate, r.Percent = day(due), pct
		if i, ok := index[no]; ok {
			plans[i].Rows = append(plans[i].Rows, r)
		}
	}
	return plans, rs.Err()
}

func nullableInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}
