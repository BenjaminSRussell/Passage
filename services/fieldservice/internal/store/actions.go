package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	svcevents "github.com/BenjaminSRussell/passage/services/fieldservice/internal/events"
)

// ActionInput is one action as the device recorded it. The id is client-generated and
// makes the action idempotent; DeviceTime is the device's clock (BR-45.6).
type ActionInput struct {
	ID         string
	Kind       domain.ActionKind
	DeviceTime time.Time
	// PART_USED: a PART line of the work order and the quantity used.
	LineID string
	Qty    decimal.Decimal
	// SIGNATURE: who signed and where the PNG sits in the object store.
	SignedBy     string
	SignatureRef string
	// NOTE: free text, last writer wins by device time.
	Text string
}

func (in ActionInput) payload() []byte {
	m := map[string]any{}
	switch in.Kind {
	case domain.ActPartUsed:
		m["lineId"], m["qty"] = in.LineID, in.Qty.String()
	case domain.ActSignature:
		m["signedBy"], m["signatureRef"] = in.SignedBy, in.SignatureRef
	case domain.ActNote:
		m["text"] = in.Text
	}
	b, _ := json.Marshal(m)
	return b
}

// Action is a stored entry of the action log.
type Action struct {
	ID           string          `json:"id"`
	WorkOrderID  string          `json:"workOrderId"`
	TechnicianID string          `json:"technicianId"`
	Kind         string          `json:"kind"`
	DeviceTime   time.Time       `json:"deviceTime"`
	ReceivedAt   time.Time       `json:"receivedAt"`
	Payload      json.RawMessage `json:"payload"`
	Outcome      string          `json:"outcome"`
	ReasonCode   string          `json:"reasonCode,omitempty"`
	Reason       string          `json:"reason,omitempty"`
}

// ActionResult is what the server decided for one submitted action. Duplicate is set
// when the id was recorded before: the stored outcome is returned and nothing re-applies.
type ActionResult struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Outcome   string `json:"outcome"`
	Code      string `json:"code,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Duplicate bool   `json:"duplicate"`
}

// SyncResult is the outcome of one batch with the work order after the merge.
type SyncResult struct {
	Results    []ActionResult `json:"results"`
	Applied    int            `json:"applied"`
	Duplicates int            `json:"duplicates"`
	Superseded int            `json:"superseded"`
	Rejected   int            `json:"rejected"`
	WorkOrder  WorkOrder      `json:"workOrder"`
}

// consumedQty sums the applied PART_USED quantities per work-order line.
func consumedQty(ctx context.Context, tx pgx.Tx, tenantID, workOrderID string) (map[string]decimal.Decimal, error) {
	rows, err := tx.Query(ctx, `select payload->>'lineId', sum((payload->>'qty')::numeric)::text from service_wo_action
		where tenant_id = $1 and work_order_id = $2 and kind = 'PART_USED' and outcome = 'APPLIED' group by 1`, tenantID, workOrderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]decimal.Decimal{}
	for rows.Next() {
		var line, qty string
		if err := rows.Scan(&line, &qty); err != nil {
			return nil, err
		}
		if out[line], err = decimal.NewFromString(qty); err != nil {
			return nil, err
		}
	}
	return out, rows.Err()
}

// maxClockSkew is how far a device clock may sit from the server's: a device time more
// than this before the work order existed is refused (a backdated or reset clock would
// otherwise inflate the working minutes).
const maxClockSkew = 5 * time.Minute

// actingFor checks that the caller may record actions for technician t: the caller is the
// technician's worker (the principal's user id is the worker id), or office staff holding
// fieldservice.work-order.maintain keying in a device's actions; they may already move the order by hand.
func actingFor(ctx context.Context, t Technician) error {
	p := caller(ctx)
	if t.WorkerID != nil && p.UserID != "" && strings.EqualFold(*t.WorkerID, p.UserID) {
		return nil
	}
	if p.HasDuty("fieldservice.work-order.maintain") {
		return nil
	}
	return httpx.NewProblem(http.StatusForbidden, domain.CodeActionTechnician, "actions can be recorded only by the technician's own worker or by office staff with fieldservice.work-order.maintain")
}

// mergeState is what decide needs beyond the work order: the consumed quantity per PART
// line, the device time of the latest applied status move, and the floor for device times.
type mergeState struct {
	consumed     map[string]decimal.Decimal
	lastMove     *time.Time
	floor        time.Time
	now          time.Time
	completedNow bool
}

// RecordActions merges a batch of technician actions into a work order (BR-45.6). The
// actions are ordered by (device time, position in the batch); each is applied, superseded
// by a further state, or rejected, and every one is stored so the timeline is complete.
// An action id seen before returns its stored outcome when it is the same action, and is
// refused (SVC-SYNC-005) when the id carries something else. The work order is locked, so
// two devices syncing together merge in turn and only one batch can complete it. A COMPLETE
// that finds the work order started with a signature (or an exemption) completes it and
// writes the completed event.
func (s *Postgres) RecordActions(ctx context.Context, workOrderID, technicianID string, in []ActionInput, now time.Time) (SyncResult, error) {
	tid := tenantOf(ctx)
	var res SyncResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		wo, err := lockWorkOrder(ctx, tx, tid, workOrderID)
		if err != nil {
			return err
		}
		tech, err := getTechnician(ctx, tx, tid, technicianID, false)
		if err != nil {
			return err
		}
		if err := actingFor(ctx, tech); err != nil {
			return err
		}
		st := mergeState{now: now}
		if st.consumed, err = consumedQty(ctx, tx, tid, workOrderID); err != nil {
			return err
		}
		var created time.Time
		if err := tx.QueryRow(ctx, `select w.created_at, (select max(device_time) from service_wo_action a where a.tenant_id = w.tenant_id and a.work_order_id = w.id
			and a.outcome = 'APPLIED' and a.kind in ('TRAVEL_START', 'WORK_START', 'HOLD', 'RESUME', 'COMPLETE'))
			from service_work_order w where w.tenant_id = $1 and w.id = $2`, tid, workOrderID).Scan(&created, &st.lastMove); err != nil {
			return err
		}
		st.floor = created.Add(-maxClockSkew)
		order := make([]int, len(in))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return in[order[a]].DeviceTime.Before(in[order[b]].DeviceTime) })

		before := wo
		res.Results = make([]ActionResult, 0, len(in))
		for _, i := range order {
			a := in[i]
			// Postgres keeps microseconds; compare and store the same instant.
			a.DeviceTime = a.DeviceTime.UTC().Truncate(time.Microsecond)
			var kind, storedWO, storedTech, outcome, code, reason string
			var sameAction bool
			err := tx.QueryRow(ctx, `select kind, work_order_id::text, technician_id::text, device_time = $3 and payload = $4::jsonb, outcome, reason_code, reason
				from service_wo_action where tenant_id = $1 and id = $2`,
				tid, a.ID, a.DeviceTime, a.payload()).Scan(&kind, &storedWO, &storedTech, &sameAction, &outcome, &code, &reason)
			if err == nil {
				if storedWO != workOrderID || kind != string(a.Kind) || storedTech != technicianID || !sameAction {
					return unprocessable(domain.CodeActionReuse, fmt.Sprintf("the action id %s was already used for another action", a.ID))
				}
				res.Results = append(res.Results, ActionResult{ID: a.ID, Kind: kind, Outcome: outcome, Code: code, Reason: reason, Duplicate: true})
				res.Duplicates++
				continue
			}
			if err != pgx.ErrNoRows {
				return err
			}
			outcome, code, reason = decide(&wo, technicianID, a, &st)
			if _, err := tx.Exec(ctx, `insert into service_wo_action (tenant_id, id, work_order_id, technician_id, legal_entity_id, kind, device_time, payload,
				outcome, reason_code, reason, received_at) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
				tid, a.ID, workOrderID, technicianID, wo.LegalEntityID, string(a.Kind), a.DeviceTime, a.payload(), outcome, code, reason, now); err != nil {
				if isUnique(err) {
					// The same id was committed meanwhile by a batch for another work order.
					return unprocessable(domain.CodeActionReuse, fmt.Sprintf("the action id %s was already used for another action", a.ID))
				}
				return err
			}
			res.Results = append(res.Results, ActionResult{ID: a.ID, Kind: string(a.Kind), Outcome: outcome, Code: code, Reason: reason})
			switch outcome {
			case domain.OutcomeApplied:
				res.Applied++
			case domain.OutcomeSuperseded:
				res.Superseded++
			default:
				res.Rejected++
			}
		}
		if wo.Status != before.Status || wo.SignedAt != before.SignedAt || wo.TechnicianNote != before.TechnicianNote {
			if _, err := tx.Exec(ctx, `update service_work_order set status = $3, completed_at = $4, first_time_fix = case when $4::timestamptz is not null then coalesce(first_time_fix, true) else first_time_fix end,
				signed_by = $5, signed_at = $6, signature_ref = $7, technician_note = $8, note_at = $9, version = version + 1, updated_at = $10
				where tenant_id = $1 and id = $2`,
				tid, workOrderID, wo.Status, wo.CompletedAt, wo.SignedBy, wo.SignedAt, wo.SignatureRef, wo.TechnicianNote, wo.noteAt, now); err != nil {
				return err
			}
		}
		res.WorkOrder, err = getWorkOrder(ctx, tx, tid, workOrderID)
		if err != nil {
			return err
		}
		if st.completedNow {
			return enqueueCompleted(ctx, tx, tid, res.WorkOrder, now)
		}
		return nil
	})
	return res, err
}

// decide applies one new action to the in-memory work order and returns its outcome.
func decide(wo *WorkOrder, technicianID string, a ActionInput, st *mergeState) (outcome, code, reason string) {
	if wo.TechnicianID == nil || *wo.TechnicianID != technicianID {
		return domain.OutcomeRejected, domain.CodeActionTechnician, "the work order is not assigned to this technician"
	}
	if a.DeviceTime.Before(st.floor) {
		return domain.OutcomeRejected, domain.CodeActionClock, "the device time is before the work order was created; check the device clock"
	}
	consumed := st.consumed
	status := domain.WOStatus(wo.Status)
	switch {
	case a.Kind.IsStatusAction():
		outcome, code, reason = domain.Merge(status, a.Kind)
		if outcome != domain.OutcomeApplied {
			return outcome, code, reason
		}
		if a.Kind == domain.ActComplete {
			if !wo.SignatureExempt && wo.SignedAt == nil {
				return domain.OutcomeRejected, domain.CodeSignatureRequired, "the customer's signature is required to complete this work order"
			}
			// The completion time is the device's, but never ahead of the server nor
			// before a status move already applied (a skewed second device).
			at := a.DeviceTime.UTC()
			if at.After(st.now) {
				at = st.now
			}
			if st.lastMove != nil && at.Before(*st.lastMove) {
				at = *st.lastMove
			}
			wo.CompletedAt, st.completedNow = &at, true
		}
		if st.lastMove == nil || a.DeviceTime.After(*st.lastMove) {
			moved := a.DeviceTime
			st.lastMove = &moved
		}
		switch a.Kind {
		case domain.ActTravelStart:
			wo.Status = string(domain.WOTraveling)
		case domain.ActWorkStart, domain.ActResume:
			wo.Status = string(domain.WOInProgress)
		case domain.ActHold:
			wo.Status = string(domain.WOOnHold)
		case domain.ActComplete:
			wo.Status = string(domain.WOCompleted)
		}
		return domain.OutcomeApplied, "", ""
	case a.Kind == domain.ActPartUsed:
		switch status {
		case domain.WODispatched, domain.WOTraveling, domain.WOInProgress, domain.WOOnHold:
		case domain.WOCompleted:
			// The completed event has already told inventory what was consumed.
			return domain.OutcomeRejected, domain.CodeActionStatus, "the work order was completed before this part action arrived, and its completion already reported the consumption"
		default:
			return domain.OutcomeRejected, domain.CodeActionStatus, fmt.Sprintf("parts are not recorded on a %s work order", status)
		}
		var line *domain.WorkLine
		for i := range wo.Lines {
			if wo.Lines[i].ID == a.LineID {
				line = &wo.Lines[i]
			}
		}
		switch {
		case line == nil || line.Kind != domain.KindPart:
			return domain.OutcomeRejected, domain.CodeActionPart, "the line is not a PART line of this work order"
		case !a.Qty.IsPositive():
			return domain.OutcomeRejected, domain.CodeActionPart, "the quantity must be positive"
		case consumed[line.ID].Add(a.Qty).GreaterThan(line.Qty):
			return domain.OutcomeRejected, domain.CodeActionPart, fmt.Sprintf("%s used would exceed the line quantity %s; add a line first", consumed[line.ID].Add(a.Qty), line.Qty)
		}
		consumed[line.ID] = consumed[line.ID].Add(a.Qty)
		return domain.OutcomeApplied, "", ""
	}
	// SIGNATURE and NOTE: allowed from dispatch until posting.
	switch status {
	case domain.WODispatched, domain.WOTraveling, domain.WOInProgress, domain.WOOnHold, domain.WOCompleted:
	default:
		return domain.OutcomeRejected, domain.CodeActionStatus, fmt.Sprintf("a %s work order takes no %s", status, a.Kind)
	}
	at := a.DeviceTime.UTC()
	if a.Kind == domain.ActSignature {
		if wo.SignedAt != nil && at.Before(*wo.SignedAt) {
			return domain.OutcomeSuperseded, "", "a later signature is already stored"
		}
		wo.SignedBy, wo.SignatureRef, wo.SignedAt = &a.SignedBy, &a.SignatureRef, &at
		return domain.OutcomeApplied, "", ""
	}
	if wo.noteAt != nil && at.Before(*wo.noteAt) {
		return domain.OutcomeSuperseded, "", "a later note is already stored"
	}
	wo.TechnicianNote, wo.noteAt = a.Text, &at
	return domain.OutcomeApplied, "", ""
}

// enqueueCompleted writes dev.passage.service.workorder.completed.v1 in the caller's
// transaction. The duration comes from the applied work marks of the action log.
func enqueueCompleted(ctx context.Context, tx pgx.Tx, tenantID string, wo WorkOrder, now time.Time) error {
	consumed, err := consumedQty(ctx, tx, tenantID, wo.ID)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `select kind, device_time from service_wo_action where tenant_id = $1 and work_order_id = $2 and outcome = 'APPLIED'
		and kind in ('WORK_START', 'RESUME', 'HOLD', 'COMPLETE')`, tenantID, wo.ID)
	if err != nil {
		return err
	}
	var marks []domain.Mark
	for rows.Next() {
		var m domain.Mark
		var kind string
		if err := rows.Scan(&kind, &m.At); err != nil {
			rows.Close()
			return err
		}
		m.Kind = domain.ActionKind(kind)
		if m.Kind == domain.ActComplete && wo.CompletedAt != nil {
			m.At = *wo.CompletedAt
		}
		marks = append(marks, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	lines := make([]svcevents.CompletedLine, 0, len(wo.Lines))
	for _, l := range wo.Lines {
		lines = append(lines, svcevents.CompletedLine{LineID: l.ID, Kind: string(l.Kind), Qty: l.Qty.String(), ConsumedQty: consumed[l.ID].String()})
	}
	at := now
	if wo.CompletedAt != nil {
		at = *wo.CompletedAt
	}
	return enqueue(ctx, tx, "workorder", wo.ID, svcevents.WorkOrderCompleted, tenantID, svcevents.WorkOrderCompletedData{
		WorkOrderID: wo.ID, Number: wo.Number, CustomerPartyID: wo.CustomerPartyID, LegalEntityID: wo.LegalEntityID, CaseID: wo.CaseID,
		TechnicianID: wo.TechnicianID, FirstTimeFix: wo.FirstTimeFix == nil || *wo.FirstTimeFix, DurationMinutes: domain.WorkMinutes(marks),
		CompletedAt: at.UTC().Format(time.RFC3339), Lines: lines,
	})
}

// ListActions returns the action log of a work order in merge order (device time, then
// receipt order): the timeline the dispatcher reviews (SVC-SYNC-001).
func (s *Postgres) ListActions(ctx context.Context, workOrderID string) ([]Action, error) {
	tid := tenantOf(ctx)
	out := []Action{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := getWorkOrder(ctx, tx, tid, workOrderID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select id::text, work_order_id::text, technician_id::text, kind, device_time, received_at, payload, outcome, reason_code, reason
			from service_wo_action where tenant_id = $1 and work_order_id = $2 order by device_time, seq limit 1000`, tid, workOrderID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a Action
			var payload []byte
			if err := rows.Scan(&a.ID, &a.WorkOrderID, &a.TechnicianID, &a.Kind, &a.DeviceTime, &a.ReceivedAt, &payload, &a.Outcome, &a.ReasonCode, &a.Reason); err != nil {
				return err
			}
			a.Payload = payload
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}
