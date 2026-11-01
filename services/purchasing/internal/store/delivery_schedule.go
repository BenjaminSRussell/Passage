package store

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// CodeDeliveryScheduleNotFound is the 404 for a line without a delivery schedule or a missing row.
const CodeDeliveryScheduleNotFound = "PRC-DS-NOT-FOUND"

// ScheduleResult is a delivery schedule with the PO row version after the call: schedule writes
// bump the PO's row version, so the next PO or schedule write must carry the new ETag.
type ScheduleResult struct {
	domain.DeliverySchedule
	PORowVersion int `json:"poRowVersion"`
}

// PutDeliverySchedule replaces the delivery schedule of one PO line (FR-17.10). The rows must add
// up to the line quantity (PRC-DS-001) and the PO must be DRAFT or REJECTED (PRC-DS-002).
// ifMatch is the PO row version the caller read (BR-17.8); the write bumps it.
func (s *Postgres) PutDeliverySchedule(ctx context.Context, poID string, lineNo, ifMatch int, in []domain.ScheduleInput) (ScheduleResult, error) {
	var out ScheduleResult
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		po, line, err := lockPlanLine(ctx, tx, poID, lineNo, ifMatch)
		if err != nil {
			return err
		}
		if err := domain.CheckScheduleTarget(po.ApprovalStatus, line.Status); err != nil {
			return err
		}
		rows, err := domain.BuildSchedule(in, line.Qty)
		if err != nil {
			return err
		}
		p, _ := tenant.FromContext(ctx)
		var otherRows int
		if err := tx.QueryRow(ctx, `select count(*) from purchase_delivery_schedule
			where tenant_id = $1 and purchase_order_id = $2 and line_no <> $3`, p.TenantID, poID, lineNo).Scan(&otherRows); err != nil {
			return err
		}
		if err := domain.CheckScheduleBudget(otherRows, len(rows)); err != nil {
			return err
		}
		// A REJECTED change of a confirmed PO keeps the vendor's confirmations on the rows it repeats.
		old, err := querySchedules(ctx, tx, poID, &lineNo)
		if err != nil {
			return err
		}
		if len(old) == 1 {
			rows = domain.CarryConfirmations(old[0].Rows, rows)
		}
		if err := writeSchedule(ctx, tx, poID, lineNo, rows); err != nil {
			return err
		}
		if _, err := bumpPORowVersion(ctx, tx, poID); err != nil {
			return err
		}
		return scheduleResult(ctx, tx, poID, lineNo, &out)
	})
	return out, err
}

// DeleteDeliverySchedule removes the schedule of one PO line, leaving the line's own delivery
// date; same preconditions as PutDeliverySchedule. It returns the new PO row version.
func (s *Postgres) DeleteDeliverySchedule(ctx context.Context, poID string, lineNo, ifMatch int) (int, error) {
	var rowVersion int
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		po, line, err := lockPlanLine(ctx, tx, poID, lineNo, ifMatch)
		if err != nil {
			return err
		}
		if err := domain.CheckScheduleTarget(po.ApprovalStatus, line.Status); err != nil {
			return err
		}
		if _, err := loadSchedule(ctx, tx, poID, lineNo); err != nil {
			return err
		}
		if err := deleteSchedule(ctx, tx, poID, lineNo); err != nil {
			return err
		}
		rowVersion, err = bumpPORowVersion(ctx, tx, poID)
		return err
	})
	return rowVersion, err
}

// GetDeliverySchedule returns the schedule of one PO line; 404 when the PO is not visible or the
// line has none.
func (s *Postgres) GetDeliverySchedule(ctx context.Context, poID string, lineNo int) (ScheduleResult, error) {
	var out ScheduleResult
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := checkVisible(ctx, tx, poID); err != nil {
			return err
		}
		return scheduleResult(ctx, tx, poID, lineNo, &out)
	})
	return out, err
}

// ListDeliverySchedules returns every schedule of a PO in line order; 404 when the PO is not
// visible.
func (s *Postgres) ListDeliverySchedules(ctx context.Context, poID string) ([]domain.DeliverySchedule, error) {
	out := []domain.DeliverySchedule{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := checkVisible(ctx, tx, poID); err != nil {
			return err
		}
		var err error
		out, err = querySchedules(ctx, tx, poID, nil)
		return err
	})
	return out, err
}

// ConfirmDeliveryDate records the date the vendor confirmed for one schedule row of a CONFIRMED
// PO (FR-17.10) and writes deliveryschedule.confirmed.v1. Setting the date the row already has is
// a replay: nothing changes, no event is written and a stale If-Match is not refused, so a retry
// after a lost response succeeds. Otherwise ifMatch must be the PO row version, which a change
// bumps.
func (s *Postgres) ConfirmDeliveryDate(ctx context.Context, poID string, lineNo, seq, ifMatch int, date domain.Day) (ScheduleResult, error) {
	var out ScheduleResult
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		// The version is checked below, after the replay test.
		if err := lockPurchaseOrder(ctx, tx, poID); err != nil {
			return err
		}
		po, err := loadPurchaseOrder(ctx, tx, poID)
		if err != nil {
			return err
		}
		var line PurchaseOrderLine
		for _, l := range po.Lines {
			if l.LineNo == lineNo {
				line = l
			}
		}
		if line.LineNo == 0 {
			return httpx.NewProblem(http.StatusNotFound, CodePOLineNotFound, fmt.Sprintf("purchase order %s has no line %d", po.Number, lineNo))
		}
		if err := domain.CheckConfirmTarget(po.ApprovalStatus, line.Status); err != nil {
			return err
		}
		sched, err := loadSchedule(ctx, tx, poID, lineNo)
		if err != nil {
			return err
		}
		var row *domain.ScheduleRow
		for i := range sched.Rows {
			if sched.Rows[i].Seq == seq {
				row = &sched.Rows[i]
			}
		}
		if row == nil {
			return httpx.NewProblem(http.StatusNotFound, CodeDeliveryScheduleNotFound,
				fmt.Sprintf("line %d has no delivery schedule row %d", lineNo, seq))
		}
		if row.ConfirmedDate != nil && row.ConfirmedDate.Equal(date.Time) {
			return scheduleResult(ctx, tx, poID, lineNo, &out)
		}
		if err := checkRowVersion(po, ifMatch); err != nil {
			return err
		}
		p, _ := tenant.FromContext(ctx)
		if _, err := tx.Exec(ctx, `update purchase_delivery_schedule set confirmed_date = $5, confirmed_by = $6, confirmed_at = now()
			where tenant_id = $1 and purchase_order_id = $2 and line_no = $3 and seq = $4`,
			p.TenantID, poID, lineNo, seq, date.Time, userOrNil(p.UserID)); err != nil {
			return err
		}
		data := purchasingevents.DeliveryScheduleConfirmedData{POID: poID, Number: po.Number, LineNo: lineNo, Seq: seq,
			ProductID: line.ProductID, Qty: row.Qty.String(), Unit: line.Unit, DeliveryDate: row.DeliveryDate.String(),
			ConfirmedDate: date.String()}
		if u := userOrNil(p.UserID); u != nil {
			data.ConfirmedBy = *u
		}
		if row.ConfirmedDate != nil {
			data.PreviousConfirmedDate = row.ConfirmedDate.String()
		}
		if err := enqueue(ctx, tx, "purchase_order", poID, purchasingevents.DeliveryScheduleConfirmed, data); err != nil {
			return err
		}
		if _, err := bumpPORowVersion(ctx, tx, poID); err != nil {
			return err
		}
		return scheduleResult(ctx, tx, poID, lineNo, &out)
	})
	return out, err
}

// syncDeliverySchedules keeps every schedule of a PO equal to its line after the lines changed
// (FR-17.10): a changed line quantity is absorbed by the last rows (domain.AdjustToQty) and the
// schedule of a cancelled line is removed. Runs inside the edit or cancel transaction.
func syncDeliverySchedules(ctx context.Context, tx pgx.Tx, poID string) error {
	schedules, err := querySchedules(ctx, tx, poID, nil)
	if err != nil || len(schedules) == 0 {
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
	for _, sc := range schedules {
		line := lines[sc.LineNo]
		if line.Status == domain.POLineCancelled {
			if err := deleteSchedule(ctx, tx, poID, sc.LineNo); err != nil {
				return err
			}
			continue
		}
		if domain.CheckScheduleBalanced(sc.Rows, line.Qty) == nil {
			continue
		}
		rows, err := domain.AdjustToQty(sc.Rows, line.Qty)
		if err != nil {
			return err
		}
		for i := range rows {
			rows[i].Seq = i + 1
		}
		if err := writeSchedule(ctx, tx, poID, sc.LineNo, rows); err != nil {
			return err
		}
	}
	return nil
}

// checkSchedulesBalanced is PRC-DS-001 when a live line's delivery schedule does not add up to its
// quantity; the confirm gate of PO confirmation (PRD ConfirmPO).
func checkSchedulesBalanced(ctx context.Context, tx pgx.Tx, po PurchaseOrder) error {
	schedules, err := querySchedules(ctx, tx, po.ID, nil)
	if err != nil {
		return err
	}
	lines := make(map[int]PurchaseOrderLine, len(po.Lines))
	for _, l := range po.Lines {
		lines[l.LineNo] = l
	}
	for _, sc := range schedules {
		line := lines[sc.LineNo]
		if line.Status == domain.POLineCancelled {
			continue
		}
		if err := domain.CheckScheduleBalanced(sc.Rows, line.Qty); err != nil {
			if de, ok := err.(*domain.Error); ok {
				de.Detail = fmt.Sprintf("line %d: %s", sc.LineNo, de.Detail)
			}
			return err
		}
	}
	return nil
}

// scheduleEventRows returns the schedule rows by line number for po.confirmed.v1.
func scheduleEventRows(ctx context.Context, tx pgx.Tx, poID string) (map[int][]purchasingevents.POScheduleRow, error) {
	schedules, err := querySchedules(ctx, tx, poID, nil)
	if err != nil {
		return nil, err
	}
	out := make(map[int][]purchasingevents.POScheduleRow, len(schedules))
	for _, sc := range schedules {
		for _, r := range sc.Rows {
			e := purchasingevents.POScheduleRow{Seq: r.Seq, Qty: r.Qty.String(), DeliveryDate: r.DeliveryDate.String()}
			if r.ConfirmedDate != nil {
				e.ConfirmedDate = r.ConfirmedDate.String()
			}
			out[sc.LineNo] = append(out[sc.LineNo], e)
		}
	}
	return out, nil
}

func scheduleResult(ctx context.Context, tx pgx.Tx, poID string, lineNo int, out *ScheduleResult) error {
	sc, err := loadSchedule(ctx, tx, poID, lineNo)
	if err != nil {
		return err
	}
	p, _ := tenant.FromContext(ctx)
	var v int
	if err := tx.QueryRow(ctx, `select row_version from purchase_order where tenant_id = $1 and id = $2`, p.TenantID, poID).Scan(&v); err != nil {
		return err
	}
	*out = ScheduleResult{DeliverySchedule: sc, PORowVersion: v}
	return nil
}

// writeSchedule replaces the rows of one line's schedule, keeping any confirmation on the rows given.
func writeSchedule(ctx context.Context, tx pgx.Tx, poID string, lineNo int, rows []domain.ScheduleRow) error {
	p, _ := tenant.FromContext(ctx)
	if err := deleteSchedule(ctx, tx, poID, lineNo); err != nil {
		return err
	}
	for _, r := range rows {
		var confirmed *time.Time
		if r.ConfirmedDate != nil {
			confirmed = &r.ConfirmedDate.Time
		}
		if _, err := tx.Exec(ctx, `insert into purchase_delivery_schedule (tenant_id, purchase_order_id, line_no, seq, qty, delivery_date,
			confirmed_date, confirmed_by, confirmed_at) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			p.TenantID, poID, lineNo, r.Seq, r.Qty, r.DeliveryDate.Time, confirmed, nullable(r.ConfirmedBy), r.ConfirmedAt); err != nil {
			return err
		}
	}
	return nil
}

func deleteSchedule(ctx context.Context, tx pgx.Tx, poID string, lineNo int) error {
	p, _ := tenant.FromContext(ctx)
	_, err := tx.Exec(ctx, `delete from purchase_delivery_schedule where tenant_id = $1 and purchase_order_id = $2 and line_no = $3`,
		p.TenantID, poID, lineNo)
	return err
}

// loadSchedule reads one line's schedule; 404 PRC-DS-NOT-FOUND when the line has none.
func loadSchedule(ctx context.Context, tx pgx.Tx, poID string, lineNo int) (domain.DeliverySchedule, error) {
	all, err := querySchedules(ctx, tx, poID, &lineNo)
	if err != nil {
		return domain.DeliverySchedule{}, err
	}
	if len(all) == 0 {
		return domain.DeliverySchedule{}, httpx.NewProblem(http.StatusNotFound, CodeDeliveryScheduleNotFound,
			fmt.Sprintf("line %d has no delivery schedule", lineNo))
	}
	return all[0], nil
}

// querySchedules reads the schedules of a PO (one line when lineNo is set) with their rows in
// sequence order; the line quantity and unit come from the line.
func querySchedules(ctx context.Context, tx pgx.Tx, poID string, lineNo *int) ([]domain.DeliverySchedule, error) {
	p, _ := tenant.FromContext(ctx)
	rs, err := tx.Query(ctx, `select s.line_no, l.unit, l.qty, s.seq, s.qty, s.delivery_date, s.confirmed_date,
		coalesce(s.confirmed_by::text, ''), s.confirmed_at
		from purchase_delivery_schedule s
		join purchase_order_line l on l.tenant_id = s.tenant_id and l.purchase_order_id = s.purchase_order_id and l.line_no = s.line_no
		where s.tenant_id = $1 and s.purchase_order_id = $2 and ($3::int is null or s.line_no = $3)
		order by s.line_no, s.seq`, p.TenantID, poID, lineNo)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []domain.DeliverySchedule{}
	for rs.Next() {
		var no int
		var unit string
		var line domain.DeliverySchedule
		var r domain.ScheduleRow
		var due time.Time
		var confirmed *time.Time
		if err := rs.Scan(&no, &unit, &line.LineQty, &r.Seq, &r.Qty, &due, &confirmed, &r.ConfirmedBy, &r.ConfirmedAt); err != nil {
			return nil, err
		}
		r.DeliveryDate = day(due)
		if confirmed != nil {
			d := day(*confirmed)
			r.ConfirmedDate = &d
		}
		if n := len(out); n == 0 || out[n-1].LineNo != no {
			out = append(out, domain.DeliverySchedule{PurchaseOrderID: poID, LineNo: no, Unit: unit, LineQty: line.LineQty, Rows: []domain.ScheduleRow{}})
		}
		out[len(out)-1].Rows = append(out[len(out)-1].Rows, r)
	}
	return out, rs.Err()
}
