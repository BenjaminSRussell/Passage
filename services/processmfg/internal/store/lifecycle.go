package store

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	pmfevents "github.com/BenjaminSRussell/passage/services/processmfg/internal/events"
)

// Share is one recipient of an ended order's batch cost (BR-24.3).
type Share struct {
	Role       string          `json:"role"`
	LineNumber int             `json:"lineNumber"`
	ItemID     string          `json:"itemId"`
	RafQty     decimal.Decimal `json:"rafQty"`
	Amount     decimal.Decimal `json:"amount"`
	Percent    decimal.Decimal `json:"percent"`
}

// Allocation is the cost split an order ended with. The shares add up to
// BatchCost exactly.
type Allocation struct {
	Method    string          `json:"method"`
	Currency  string          `json:"currency"`
	BatchCost decimal.Decimal `json:"batchCost"`
	Shares    []Share         `json:"shares"`
}

// ReportInput is the report-as-finished command: the quantity of the order's
// own item, and the quantity of each co- or by-product line by line number.
// A line left out is reported at its planned quantity.
type ReportInput struct {
	MainQty decimal.Decimal
	Outputs map[int]decimal.Decimal
}

// EndInput is the end command: the cost the order ends with and how to split it.
// CoBasis and ByRates are keyed by line number.
type EndInput struct {
	Method    domain.AllocationMethod
	Currency  string
	BatchCost decimal.Decimal
	MainBasis domain.CostBasis
	CoBasis   map[int]domain.CostBasis
	ByRates   map[int]domain.ByRate
}

func loadAllocation(ctx context.Context, tx pgx.Tx, orderID string) (*Allocation, error) {
	a := Allocation{Shares: []Share{}}
	if err := tx.QueryRow(ctx, `select allocation_method, currency, batch_cost from batch_order where id = $1`, orderID).
		Scan(&a.Method, &a.Currency, &a.BatchCost); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `select role, line_number, item_id, raf_qty, amount, percent from batch_order_allocation
		where order_id = $1 order by case role when 'main' then 0 when 'co-product' then 1 else 2 end, line_number`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sh Share
		if err := rows.Scan(&sh.Role, &sh.LineNumber, &sh.ItemID, &sh.RafQty, &sh.Amount, &sh.Percent); err != nil {
			return nil, err
		}
		a.Shares = append(a.Shares, sh)
	}
	return &a, rows.Err()
}

// hooks are the parts of a lifecycle command that differ; the rest (locking,
// replay, version and status checks, the status update, the state event and
// the stored answer) is shared.
type hooks struct {
	// apply runs after the checks and before the status update. It may fill
	// state-change event fields and write the command's own rows and events.
	apply func(tx pgx.Tx, o *BatchOrder, evt *pmfevents.BatchOrderStateChangedData) error
}

var statusColumn = map[string]string{
	domain.StatusReleased: "released_at",
	domain.StatusStarted:  "started_at",
	domain.StatusReported: "reported_at",
	domain.StatusEnded:    "ended_at",
}

// run executes one lifecycle command on an order in one transaction.
func (s *Postgres) run(ctx context.Context, id, cmd string, version int, idem Idem, h hooks) (BatchOrder, bool, error) {
	var out BatchOrder
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Lock first, then look for a replay, as RescaleBatchOrder does.
		o, err := loadOrder(ctx, tx, id, " for update")
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if o.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
				fmt.Sprintf("batch order %s is at version %d, not %d; reload and try again", o.OrderNumber, o.Version, version))
		}
		to, requires, ok := domain.NextStatus(cmd, o.Status)
		if !ok {
			return httpx.NewProblem(http.StatusConflict, CodeOrderState,
				fmt.Sprintf("batch order %s is %s; %s needs a %s order", o.OrderNumber, o.Status, cmd, requires))
		}
		evt := pmfevents.BatchOrderStateChangedData{OrderID: o.ID, OrderNumber: o.OrderNumber, LegalEntityID: o.LegalEntityID,
			ItemID: o.ItemID, From: o.Status, To: to, Version: o.Version + 1}
		if h.apply != nil {
			if err := h.apply(tx, &o, &evt); err != nil {
				return err
			}
		}
		// statusColumn holds constants only.
		if _, err := tx.Exec(ctx, `update batch_order set status = $2, version = version + 1, updated_at = now(), `+statusColumn[to]+` = now() where id = $1`, id, to); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "batchorder", o.ID, pmfevents.BatchOrderStateChanged, evt); err != nil {
			return err
		}
		out, err = loadOrder(ctx, tx, id, "")
		if err != nil {
			return err
		}
		changed = true
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, changed, err
}

// AdvanceBatchOrder releases or starts an order (PRD 4.24.5 :release, :start).
func (s *Postgres) AdvanceBatchOrder(ctx context.Context, id, command string, version int, idem Idem) (BatchOrder, bool, error) {
	if command != domain.CmdRelease && command != domain.CmdStart {
		return BatchOrder{}, false, fmt.Errorf("unsupported command %q", command)
	}
	return s.run(ctx, id, command, version, idem, hooks{})
}

func reportProblem(format string, args ...any) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeReportInvalid, fmt.Sprintf(format, args...))
}

// ReportBatchOrder records the quantities reported as finished (RAF) and moves
// a started order to reported.
func (s *Postgres) ReportBatchOrder(ctx context.Context, id string, in ReportInput, version int, idem Idem) (BatchOrder, bool, error) {
	return s.run(ctx, id, domain.CmdReport, version, idem, hooks{apply: func(tx pgx.Tx, o *BatchOrder, evt *pmfevents.BatchOrderStateChangedData) error {
		if !in.MainQty.IsPositive() {
			return reportProblem("the reported quantity of the order's own item must be positive")
		}
		isOutput := map[int]bool{}
		for _, l := range o.Lines {
			if domain.IsOutput(l.Type) {
				isOutput[l.LineNumber] = true
			}
		}
		for n, q := range in.Outputs {
			if !isOutput[n] {
				return reportProblem("line %d is not a co-product or by-product line of the order", n)
			}
			if q.IsNegative() {
				return reportProblem("the reported quantity of line %d must not be negative", n)
			}
		}
		if _, err := tx.Exec(ctx, `update batch_order set actual_qty = $2 where id = $1`, o.ID, in.MainQty); err != nil {
			return err
		}
		evt.ActualQty = in.MainQty.String()
		for _, l := range o.Lines {
			if !isOutput[l.LineNumber] {
				continue
			}
			q, given := in.Outputs[l.LineNumber]
			if !given {
				q = l.Qty
			}
			if _, err := tx.Exec(ctx, `update batch_order_line set raf_qty = $3 where order_id = $1 and line_number = $2`, o.ID, l.LineNumber, q); err != nil {
				return err
			}
			evt.Outputs = append(evt.Outputs, pmfevents.ReportedLine{LineNumber: l.LineNumber, ItemID: l.ItemID, Type: string(l.Type), RafQty: q.String()})
		}
		return nil
	}})
}

// EndBatchOrder ends a reported order: it splits the batch cost over the main
// item, the co-products and the by-product burdens (BR-24.3), stores the split
// and writes the allocated event in the same transaction as the state change.
func (s *Postgres) EndBatchOrder(ctx context.Context, id string, in EndInput, version int, idem Idem) (BatchOrder, bool, error) {
	return s.run(ctx, id, domain.CmdEnd, version, idem, hooks{apply: func(tx pgx.Tx, o *BatchOrder, evt *pmfevents.BatchOrderStateChangedData) error {
		if o.ActualQty == nil {
			return reportProblem("batch order %s has no reported quantity", o.OrderNumber)
		}
		input := domain.OrderAllocationInput{Method: in.Method, BatchCost: in.BatchCost, MainItemID: o.ItemID, MainRafQty: *o.ActualQty,
			MainBasis: in.MainBasis, CoBasis: in.CoBasis, ByRates: in.ByRates}
		for _, l := range o.Lines {
			raf := l.Qty
			if l.RafQty != nil {
				raf = *l.RafQty
			}
			ref := domain.OutputRef{LineNumber: l.LineNumber, ItemID: l.ItemID, RafQty: raf}
			switch l.Type {
			case domain.LineCoProduct:
				input.CoProducts = append(input.CoProducts, ref)
			case domain.LineByProduct:
				input.ByProducts = append(input.ByProducts, ref)
			}
		}
		shares, err := domain.AllocateOrder(input)
		if err != nil {
			return err
		}
		method := string(in.Method)
		if _, err := tx.Exec(ctx, `update batch_order set batch_cost = $2, currency = $3, allocation_method = $4 where id = $1`,
			o.ID, in.BatchCost, in.Currency, method); err != nil {
			return err
		}
		data := pmfevents.CoProductAllocatedData{OrderID: o.ID, OrderNumber: o.OrderNumber, LegalEntityID: o.LegalEntityID,
			Method: method, Currency: in.Currency, BatchCost: in.BatchCost.String()}
		p := principal(ctx)
		for _, sh := range shares {
			if _, err := tx.Exec(ctx, `insert into batch_order_allocation (tenant_id, order_id, legal_entity_id, role, line_number, item_id, raf_qty, amount, percent)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
				p.TenantID, o.ID, o.LegalEntityID, sh.Role, sh.LineNumber, sh.ItemID, sh.RafQty, sh.Amount, sh.Percent); err != nil {
				return err
			}
			data.Shares = append(data.Shares, pmfevents.AllocationShare{Role: sh.Role, LineNumber: sh.LineNumber, ItemID: sh.ItemID,
				RafQty: sh.RafQty.String(), Amount: sh.Amount.String(), Percent: sh.Percent.String()})
		}
		evt.BatchCost, evt.Currency, evt.Method = in.BatchCost.String(), in.Currency, method
		return enqueue(ctx, tx, "batchorder", o.ID, pmfevents.CoProductAllocated, data)
	}})
}
