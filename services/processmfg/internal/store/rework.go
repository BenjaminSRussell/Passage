package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	pmfevents "github.com/BenjaminSRussell/passage/services/processmfg/internal/events"
)

// Problem codes of the rework batch order.
const (
	CodeReworkNotFound    = "PMF-0131"
	CodeReworkOpen        = "PMF-0132"
	CodeReworkReceived    = "PMF-0133"
	CodeReworkNotReported = "PMF-0134"
)

// ReworkOrder is a rework batch order (FR-24.18): the batch order that does the
// work, the stored batch it reworks and the plan's parameters. OutputBatch* are
// set once the output is received.
type ReworkOrder struct {
	ID                   string           `json:"id"`
	LegalEntityID        string           `json:"legalEntityId"`
	BatchOrderID         string           `json:"batchOrderId"`
	OrderNumber          string           `json:"orderNumber"`
	OrderStatus          string           `json:"orderStatus"`
	SourceBatchID        string           `json:"sourceBatchId"`
	SourceBatchNumber    string           `json:"sourceBatchNumber"`
	ItemID               string           `json:"itemId"`
	InputQty             decimal.Decimal  `json:"inputQty"`
	OutputQty            decimal.Decimal  `json:"outputQty"`
	KeepBatchNumber      bool             `json:"keepBatchNumber"`
	QualityOrderRequired bool             `json:"qualityOrderRequired"`
	OutputDisposition    string           `json:"outputDisposition"`
	VarianceType         string           `json:"varianceType"`
	Status               string           `json:"status"`
	OutputBatchID        *string          `json:"outputBatchId,omitempty"`
	OutputBatchNumber    *string          `json:"outputBatchNumber,omitempty"`
	ReceivedQty          *decimal.Decimal `json:"receivedQty,omitempty"`
	ReceivedBy           *string          `json:"receivedBy,omitempty"`
	ReceivedAt           *time.Time       `json:"receivedAt,omitempty"`
	Version              int              `json:"version"`
	CreatedBy            string           `json:"createdBy"`
	CreatedAt            time.Time        `json:"createdAt"`
	UpdatedAt            time.Time        `json:"updatedAt"`
}

// NewRework is the create command. The register keeps no quantity, so the
// caller gives the quantity of the source batch (SourceQty) and the version of
// the batch it read.
type NewRework struct {
	LegalEntityID string
	FormulaID     string
	BatchSize     decimal.Decimal
	SourceBatchID string
	SourceVersion int
	SourceQty     decimal.Decimal
	InputQty      decimal.Decimal
	OutputQty     decimal.Decimal
	Params        domain.ReworkParams
}

// ReworkFilter narrows the rework list.
type ReworkFilter struct {
	Status        string
	SourceBatchID string
	Limit         int
}

const reworkCols = `r.id, r.legal_entity_id, r.batch_order_id, o.order_number, o.status, r.source_batch_id, sb.batch_number, r.item_id,
	r.input_qty, r.output_qty, r.keep_batch_number, r.quality_order_required, r.output_disposition, r.variance_type, r.status,
	r.output_batch_id, ob.batch_number, r.received_qty, r.received_by, r.received_at, r.version, r.created_by, r.created_at, r.updated_at`

const reworkFrom = ` from rework_order r
	join batch_order o on o.tenant_id = r.tenant_id and o.id = r.batch_order_id
	join batch_record sb on sb.tenant_id = r.tenant_id and sb.id = r.source_batch_id
	left join batch_record ob on ob.tenant_id = r.tenant_id and ob.id = r.output_batch_id`

func scanRework(row pgx.Row) (ReworkOrder, error) {
	var r ReworkOrder
	err := row.Scan(&r.ID, &r.LegalEntityID, &r.BatchOrderID, &r.OrderNumber, &r.OrderStatus, &r.SourceBatchID, &r.SourceBatchNumber, &r.ItemID,
		&r.InputQty, &r.OutputQty, &r.KeepBatchNumber, &r.QualityOrderRequired, &r.OutputDisposition, &r.VarianceType, &r.Status,
		&r.OutputBatchID, &r.OutputBatchNumber, &r.ReceivedQty, &r.ReceivedBy, &r.ReceivedAt, &r.Version, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func loadRework(ctx context.Context, tx pgx.Tx, id string) (ReworkOrder, error) {
	r, err := scanRework(tx.QueryRow(ctx, `select `+reworkCols+reworkFrom+` where r.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, notFound(CodeReworkNotFound, "rework order", id)
	}
	return r, err
}

// CreateReworkOrder plans the rework (domain.PlanRework: quantity limits, the
// output disposition against the quality-order rule), creates the batch order
// from the rework formula, which must make the item of the source batch, and
// records the link, in one transaction with the created events. The source
// batch is locked and its version checked, so a hold, disposition change or
// merge made since the caller read it is refused as stale (PMF-0104), and its
// version moves by one, so a merge, derivation or disposition change based on
// the version read before the rework is refused in turn (the rework takes stock
// out of the batch; the register holds no quantity to check it against). A
// batch has at most one open rework (PMF-0132).
func (s *Postgres) CreateReworkOrder(ctx context.Context, in NewRework, idem Idem) (ReworkOrder, bool, error) {
	var out ReworkOrder
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		src, err := loadBatch(ctx, tx, in.SourceBatchID, " for update")
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if src.Version != in.SourceVersion {
			return staleBatch(src, in.SourceVersion)
		}
		if src.LegalEntityID != in.LegalEntityID {
			return httpx.NewProblem(http.StatusBadRequest, domain.CodeInvalid,
				fmt.Sprintf("batch %s belongs to legal entity %s, not %s", src.BatchNumber, src.LegalEntityID, in.LegalEntityID))
		}
		plan, err := domain.PlanRework(domain.Batch{ID: src.ID, ItemID: src.ItemID, Disposition: src.Disposition, Qty: in.SourceQty},
			in.InputQty, in.OutputQty, in.Params)
		if err != nil {
			return err
		}
		f, err := getFormula(ctx, tx, in.FormulaID)
		if err != nil {
			return err
		}
		if err := domain.CheckReworkFormula(src.ItemID, f.ItemID); err != nil {
			return err
		}
		var open bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from rework_order where source_batch_id = $1 and status = 'planned')`, src.ID).Scan(&open); err != nil {
			return err
		}
		if open {
			return httpx.NewProblem(http.StatusConflict, CodeReworkOpen, fmt.Sprintf("batch %s already has an open rework order", src.BatchNumber))
		}
		order, err := insertBatchOrder(ctx, tx, NewBatchOrder{LegalEntityID: in.LegalEntityID, FormulaID: in.FormulaID, BatchSize: in.BatchSize}, Idem{})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update batch_record set version = version + 1, updated_at = now() where id = $1`, src.ID); err != nil {
			return err
		}
		p := principal(ctx)
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into rework_order (tenant_id, id, legal_entity_id, batch_order_id, source_batch_id, item_id, input_qty, output_qty,
				keep_batch_number, quality_order_required, output_disposition, variance_type, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			p.TenantID, id, in.LegalEntityID, order.ID, src.ID, src.ItemID, plan.InputQty, plan.OutputQty,
			in.Params.KeepBatchNumber, plan.QualityOrderRequired, plan.OutputDisposition, plan.VarianceType, p.UserID); err != nil {
			return mapDuplicate(err, CodeReworkOpen, fmt.Sprintf("batch %s already has an open rework order", src.BatchNumber))
		}
		if err := enqueue(ctx, tx, "reworkorder", id, pmfevents.ReworkOrderCreated, pmfevents.ReworkOrderCreatedData{
			ReworkID: id, BatchOrderID: order.ID, LegalEntityID: in.LegalEntityID, ItemID: src.ItemID, SourceBatchID: src.ID,
			InputQty: plan.InputQty.String(), OutputQty: plan.OutputQty.String(), KeepBatchNumber: in.Params.KeepBatchNumber,
			QualityOrderRequired: plan.QualityOrderRequired, OutputDisposition: plan.OutputDisposition, VarianceType: plan.VarianceType}); err != nil {
			return err
		}
		if out, err = loadRework(ctx, tx, id); err != nil {
			return err
		}
		created = true
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, created, err
}

// batchReworks returns the rework orders a batch was reworked by or made by,
// oldest first: the genealogy of a rework that keeps the batch number (AC-24.18),
// which has no parent and child to link.
func batchReworks(ctx context.Context, tx pgx.Tx, batchID string) ([]ReworkOrder, error) {
	rows, err := tx.Query(ctx, `select `+reworkCols+reworkFrom+`
		where r.source_batch_id = $1 or r.output_batch_id = $1
		order by r.created_at, r.id limit `+fmt.Sprint(maxHistory), batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReworkOrder{}
	for rows.Next() {
		r, err := scanRework(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListReworkOrders returns rework orders newest first.
func (s *Postgres) ListReworkOrders(ctx context.Context, f ReworkFilter) ([]ReworkOrder, error) {
	out := []ReworkOrder{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+reworkCols+reworkFrom+`
			where ($1 = '' or r.status = $1) and ($2 = '' or r.source_batch_id::text = $2)
			order by r.created_at desc, r.id desc limit $3`, f.Status, f.SourceBatchID, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRework(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetReworkOrder returns one rework order; one outside the caller's scope is PMF-0131.
func (s *Postgres) GetReworkOrder(ctx context.Context, id string) (ReworkOrder, error) {
	var out ReworkOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadRework(ctx, tx, id)
		return err
	})
	return out, err
}

// ReceiveReworkOrder takes the output of a rework back into the batch register
// once its batch order is reported as finished (PMF-0134 before). A rework that
// keeps the batch number moves the source batch to the planned output
// disposition (history reason "Rework <order>", the release gate when
// available); one that does not registers a new batch under newBatchNumber with
// that disposition and a genealogy link to the source (relation rework). The
// reported quantity of the order is the received quantity. A second receive is
// PMF-0133. version is the rework version the caller read. An output planned
// as available is a release of the reworked batch, so it also needs the
// disposition duty (canRelease, PRD 4.24.17 Maintain batch disposition);
// without it the receive is refused with AUTH-0003 and nothing is written.
func (s *Postgres) ReceiveReworkOrder(ctx context.Context, id, newBatchNumber string, version int, canRelease bool, idem Idem) (ReworkOrder, bool, error) {
	var out ReworkOrder
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		var ver int
		err := tx.QueryRow(ctx, `select status, version from rework_order where id = $1 for update`, id).Scan(&status, &ver)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeReworkNotFound, "rework order", id)
		}
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if ver != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
				fmt.Sprintf("rework order %s is at version %d, not %d; reload and try again", id, ver, version))
		}
		if status != "planned" {
			return httpx.NewProblem(http.StatusConflict, CodeReworkReceived, "the output of this rework order was already received")
		}
		r, err := loadRework(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.OutputDisposition == domain.DispositionAvailable && !canRelease {
			return httpx.NewProblem(http.StatusForbidden, "AUTH-0003",
				"the output of this rework is planned as available, which releases the batch: missing duty production.batch-disposition.write")
		}
		if err := domain.CheckReworkOutputNumber(r.KeepBatchNumber, newBatchNumber); err != nil {
			return err
		}
		var actual *decimal.Decimal
		var orderStatus string
		if err := tx.QueryRow(ctx, `select status, actual_qty from batch_order where id = $1 for share`, r.BatchOrderID).Scan(&orderStatus, &actual); err != nil {
			return err
		}
		if (orderStatus != domain.StatusReported && orderStatus != domain.StatusEnded) || actual == nil || !actual.IsPositive() {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeReworkNotReported,
				fmt.Sprintf("batch order %s must be reported as finished with a quantity before its output is received", r.OrderNumber))
		}
		p := principal(ctx)
		outputID := r.SourceBatchID
		number := r.SourceBatchNumber
		if r.KeepBatchNumber {
			src, err := loadBatch(ctx, tx, r.SourceBatchID, " for update")
			if err != nil {
				return err
			}
			if src.Disposition != r.OutputDisposition {
				if err := moveDisposition(ctx, tx, src, r.OutputDisposition, "Rework "+r.OrderNumber); err != nil {
					return err
				}
			}
		} else {
			if r.OutputDisposition == domain.DispositionAvailable {
				if err := releaseGate(ctx, tx, r.LegalEntityID, r.ItemID, nil, newBatchNumber); err != nil {
					return err
				}
			}
			outputID, number = ids.New(), newBatchNumber
			if _, err := tx.Exec(ctx, `insert into batch_record (tenant_id, id, legal_entity_id, item_id, batch_number, batch_order_id, disposition, created_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8)`,
				p.TenantID, outputID, r.LegalEntityID, r.ItemID, number, r.BatchOrderID, r.OutputDisposition, p.UserID); err != nil {
				return mapDuplicate(err, CodeBatchDuplicate, fmt.Sprintf("batch %s of item %s already exists", number, r.ItemID))
			}
			if _, err := tx.Exec(ctx, `insert into batch_genealogy (tenant_id, legal_entity_id, child_batch_id, parent_batch_id, relation, qty, unit_cost, created_by)
				values ($1, $2, $3, $4, 'rework', $5, 0, $6)`, p.TenantID, r.LegalEntityID, outputID, r.SourceBatchID, r.InputQty, p.UserID); err != nil {
				return err
			}
			if err := enqueue(ctx, tx, "batch", outputID, pmfevents.BatchRegistered, pmfevents.BatchRegisteredData{
				BatchID: outputID, LegalEntityID: r.LegalEntityID, ItemID: r.ItemID, BatchNumber: number,
				Disposition: r.OutputDisposition, BatchOrderID: r.BatchOrderID}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update rework_order set status = 'received', output_batch_id = $2, received_qty = $3, received_by = $4,
			received_at = now(), version = version + 1, updated_at = now() where id = $1`, id, outputID, *actual, p.UserID); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "reworkorder", id, pmfevents.ReworkOrderReceived, pmfevents.ReworkOrderReceivedData{
			ReworkID: id, BatchOrderID: r.BatchOrderID, LegalEntityID: r.LegalEntityID, ItemID: r.ItemID, SourceBatchID: r.SourceBatchID,
			OutputBatchID: outputID, BatchNumber: number, Qty: actual.String(), Disposition: r.OutputDisposition,
			KeepBatchNumber: r.KeepBatchNumber, QualityOrderRequired: r.QualityOrderRequired, VarianceType: r.VarianceType}); err != nil {
			return err
		}
		if out, err = loadRework(ctx, tx, id); err != nil {
			return err
		}
		changed = true
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, changed, err
}
