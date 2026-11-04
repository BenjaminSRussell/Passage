package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
)

// OrderWithdrawal is production taking an order out of schedule: a reset to
// Estimated or Created, a cancel or a merge into another order (its
// statuschanged event carries no dates).
type OrderWithdrawal struct {
	OrderID string
	// To is the status production moved the order to.
	To      string
	EventID string
	// EventTime is the CloudEvent time of the statuschanged event; zero when
	// unknown. An event older than the last one applied to the order (a newer
	// schedule event already imported it again) withdraws nothing.
	EventTime time.Time
}

// withdrawnReason is the reason of the orderimport.recorded event of a withdrawal.
const withdrawnReason = "withdraw"

// WithdrawProductionOrder withdraws the operations scheduling registered for an
// order that production no longer schedules (FR-23.10), in one transaction with
// its outbox event: the operations and their non-firmed jobs are deleted, an
// operation a firmed job holds stays (listed on the import row), and the
// order's import row becomes withdrawn. The bool is false, and nothing is
// written, when the order was never received, is already withdrawn, or the
// event is older than the last production event applied to the order. A later
// order.scheduled.v1 of the order imports it again.
func (s *Postgres) WithdrawProductionOrder(ctx context.Context, req OrderWithdrawal) (OrderImport, bool, error) {
	var out OrderImport
	changed := false
	err := s.loadTx(&ctx, func(tx pgx.Tx) error {
		if err := lockOrderImport(ctx, tx, req.OrderID); err != nil {
			return err
		}
		prior, err := scanImport(tx.QueryRow(ctx, `select `+importCols+` from scheduling_order_import where order_id = $1::uuid for update`, req.OrderID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if prior.Status == ImportWithdrawn || olderThan(req.EventTime, prior.EventAt) {
			out = prior
			out.Stale = prior.Status != ImportWithdrawn
			return nil
		}
		rows, err := tx.Query(ctx, `select id::text, oper_number from scheduling_operation where order_id = $1::uuid order by oper_number for update`, req.OrderID)
		if err != nil {
			return err
		}
		var held []importedRow
		for rows.Next() {
			var r importedRow
			if err := rows.Scan(&r.id, &r.number); err != nil {
				rows.Close()
				return err
			}
			held = append(held, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		firmed := map[string]bool{}
		frows, err := tx.Query(ctx, `select distinct job_id::text from scheduled_job where order_id = $1::uuid and firmed`, req.OrderID)
		if err != nil {
			return err
		}
		for frows.Next() {
			var id string
			if err := frows.Scan(&id); err != nil {
				frows.Close()
				return err
			}
			firmed[id] = true
		}
		frows.Close()
		if err := frows.Err(); err != nil {
			return err
		}

		data := schedevents.OrderImportRecordedData{OrderID: prior.OrderID, OrderNumber: prior.OrderNumber, OrderVersion: prior.OrderVersion, Reason: withdrawnReason,
			Status: ImportWithdrawn, WithdrawnTo: req.To, Operations: []schedevents.OrderImportOperation{}, Skipped: []schedevents.OrderImportSkipped{},
			RemovedOperNumbers: []int{}, KeptFirmed: []int{}}
		skipped := []domain.SkippedOperation{}
		removed, kept := 0, 0
		for _, op := range held {
			if firmed[op.id] {
				kept++
				data.KeptFirmed = append(data.KeptFirmed, op.number)
				skipped = append(skipped, domain.SkippedOperation{OperNumber: op.number, Code: domain.SkipFirmed,
					Detail: "production withdrew the order but a firmed job holds this operation; it was kept"})
				continue
			}
			if _, err := dropJobs(ctx, tx, op.id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `delete from scheduling_operation where id = $1::uuid`, op.id); err != nil {
				return err
			}
			removed++
			data.RemovedOperNumbers = append(data.RemovedOperNumbers, op.number)
		}
		slices.Sort(data.RemovedOperNumbers)
		skippedJSON, err := json.Marshal(skipped)
		if err != nil {
			return err
		}
		out, err = scanImport(tx.QueryRow(ctx, `update scheduling_order_import set status = $2, created_count = 0, updated_count = 0, unchanged_count = 0,
				removed_count = $3, kept_firmed_count = $4, skipped = $5, replan_required = false, event_id = $6, source = null,
				withdrawn_at = now(), withdrawn_to = $7, received_at = now(), order_event_at = coalesce($8, order_event_at)
			where order_id = $1::uuid returning `+importCols,
			req.OrderID, ImportWithdrawn, removed, kept, skippedJSON, req.EventID, req.To, eventTime(req.EventTime)))
		if err != nil {
			return err
		}
		changed = true
		return enqueue(ctx, tx, "orderimport", req.OrderID, schedevents.OrderImportRecorded, data)
	})
	if err != nil {
		return OrderImport{}, false, err
	}
	return out, changed, nil
}
