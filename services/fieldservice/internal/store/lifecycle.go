package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	svcevents "github.com/BenjaminSRussell/passage/services/fieldservice/internal/events"
)

// saveClocks writes the SLA state and status of a case and bumps its version.
func saveClocks(ctx context.Context, tx pgx.Tx, tenantID, id string, c domain.CaseClocks, now time.Time) error {
	_, err := tx.Exec(ctx, `update service_case set status = $3, sla_state = $4, responded_at = $5, resolved_at = $6,
		sla_paused_at = $7, sla_paused_minutes = $8, sla_resolution_due = $9, sla_resolution_warn_at = $10, sla_alerts = $11,
		version = version + 1, updated_at = $12 where tenant_id = $1 and id = $2`,
		tenantID, id, string(c.Status), c.State, c.RespondedAt, c.ResolvedAt, c.PausedAt, c.PausedMinutes,
		c.ResolutionDue, c.ResolutionWarnAt, c.Alerts, now)
	return err
}

// emitAlerts writes one sla.warning or sla.breached event per alert to the
// outbox, in the transaction that recorded the alert on the case.
func emitAlerts(ctx context.Context, tx pgx.Tx, tenantID string, c Case, alerts []domain.SLAAlert, now time.Time) error {
	for _, a := range alerts {
		due, at := a.DueAt.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339)
		var err error
		if a.Kind == domain.AlertBreach {
			err = enqueue(ctx, tx, "case", c.ID, svcevents.SLABreached, tenantID, svcevents.SLABreachedData{
				CaseID: c.ID, Number: c.Number, SLAType: a.Clock, Priority: c.Priority, DueAt: due, BreachedAt: at})
		} else {
			err = enqueue(ctx, tx, "case", c.ID, svcevents.SLAWarning, tenantID, svcevents.SLAWarningData{
				CaseID: c.ID, Number: c.Number, SLAType: a.Clock, Priority: c.Priority, DueAt: due, WarnedAt: at,
				ThresholdPercent: domain.DefaultWarningPercent})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// TransitionCase moves a case along its status machine at now (FR-45.5) and
// applies BR-45.2: pauses open and close, due instants move by the paused
// business minutes, leaving NEW records the response, and a clock found past
// due is breached once. The version must match (412 otherwise). A request
// carrying an Idempotency-Key is stored with its answer, so a retry gets the
// same answer instead of a 412 against the version it already moved.
func (s *Postgres) TransitionCase(ctx context.Context, id string, version int, to domain.CaseStatus, now time.Time, key, requestHash string) (Case, bool, error) {
	tid := tenantOf(ctx)
	var out Case
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockCase(ctx, tx, tid, id)
		if err != nil {
			return err
		}
		if found, err := keyedAnswer(ctx, tx, tid, key, requestHash, &out); err != nil || found {
			replayed = found
			return err
		}
		if cur.Version != version {
			return versionConflict(cur.Version)
		}
		out = cur
		if domain.CaseStatus(cur.Status) != to {
			profile, err := getProfile(ctx, tx, tid, cur.SLAProfileID)
			if err != nil {
				return err
			}
			cal, err := profile.Calendar()
			if err != nil {
				return unprocessable(CodeInvalidCalendar, "the SLA profile calendar is not valid")
			}
			target, ok := profile.Target(cur.Priority)
			if !ok {
				return unprocessable(CodeNoTarget, "the SLA profile has no target for this priority")
			}
			next, alerts, ok := cur.clocks().Transition(to, now, cal, target, profile.PauseOnParts)
			if !ok {
				return unprocessable(CodeCaseTransition, fmt.Sprintf("a case cannot move from %s to %s", cur.Status, to))
			}
			if err := saveClocks(ctx, tx, tid, id, next, now); err != nil {
				return err
			}
			if err := emitAlerts(ctx, tx, tid, cur, alerts, now); err != nil {
				return err
			}
			if out, err = getCase(ctx, tx, tid, id); err != nil {
				return err
			}
		}
		return storeAnswer(ctx, tx, tid, key, requestHash, out)
	})
	return out, replayed, err
}

// keyedAnswer loads the stored answer of an earlier request with the same
// Idempotency-Key into out. A different request under the key is refused.
func keyedAnswer(ctx context.Context, tx pgx.Tx, tenantID, key, requestHash string, out any) (bool, error) {
	if key == "" {
		return false, nil
	}
	var hash string
	var body []byte
	err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where tenant_id = $1 and key = $2`, tenantID, key).Scan(&hash, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash != requestHash {
		return false, unprocessable(CodeIdempotencyReuse, "Idempotency-Key was already used with a different request")
	}
	return true, json.Unmarshal(body, out)
}

func storeAnswer(ctx context.Context, tx pgx.Tx, tenantID, key, requestHash string, answer any) error {
	if key == "" {
		return nil
	}
	body, err := json.Marshal(answer)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body) values ($1, $2, $3, $4, $5)`,
		tenantID, key, requestHash, http.StatusOK, body)
	return err
}

// recordResponse stops the response clock of a case when one of its work
// orders is DISPATCHED (BR-45.2), unless an agent already responded.
func recordResponse(ctx context.Context, tx pgx.Tx, tenantID, caseID string, now time.Time) error {
	c, err := lockCase(ctx, tx, tenantID, caseID)
	if err != nil {
		return err
	}
	if c.RespondedAt != nil || !domain.CaseStatus(c.Status).Open() {
		return nil
	}
	next, alerts := c.clocks().Respond(now)
	if err := saveClocks(ctx, tx, tenantID, caseID, next, now); err != nil {
		return err
	}
	return emitAlerts(ctx, tx, tenantID, c, alerts, now)
}

// ScanResult counts what one SLA monitor pass did.
type ScanResult struct {
	Scanned  int `json:"scanned"`
	Warnings int `json:"warnings"`
	Breaches int `json:"breaches"`
}

// ScanSLA is one pass of the SLA monitor (BR-45.2, AC-45.3, job
// service-sla-monitor) over the caller's tenant. It selects only RUNNING
// clocks of open cases that have an alert due and not yet emitted, oldest
// resolution due first, at most limit of them; every selected case gets its
// alert recorded or its clock BREACHED, so it is never selected for the same
// alert again and a backlog drains batch by batch. SKIP LOCKED lets passes
// overlap without emitting twice.
func (s *Postgres) ScanSLA(ctx context.Context, now time.Time, limit int) (ScanResult, error) {
	tid := tenantOf(ctx)
	var res ScanResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+caseColumns+` from service_case
			where tenant_id = $1 and sla_state = 'RUNNING' and status in ('NEW', 'IN_PROGRESS', 'WAITING_CUSTOMER', 'WAITING_PARTS') and (
				(status = 'NEW' and responded_at is null and (
					(sla_response_due < $2 and not ('RESPONSE_BREACH' = any(sla_alerts)))
					or (coalesce(sla_response_warn_at, sla_response_due) <= $2 and not ('RESPONSE_WARNING' = any(sla_alerts)))))
				or sla_resolution_due < $2
				or (coalesce(sla_resolution_warn_at, sla_resolution_due) <= $2 and not ('RESOLUTION_WARNING' = any(sla_alerts))))
			order by sla_resolution_due limit $3 for update skip locked`, tid, now, limit)
		if err != nil {
			return err
		}
		var cases []Case
		for rows.Next() {
			c, err := scanCase(rows)
			if err != nil {
				rows.Close()
				return err
			}
			cases = append(cases, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range cases {
			res.Scanned++
			next, alerts := c.clocks().DueAlerts(now)
			// A clock past due whose breach was already emitted still moves to
			// BREACHED here, so the "resolution due < now" arm stops selecting it.
			if len(alerts) == 0 && next.State == c.SLAState {
				continue
			}
			if _, err := tx.Exec(ctx, `update service_case set sla_state = $3, sla_alerts = $4, version = version + 1, updated_at = $5
				where tenant_id = $1 and id = $2`, tid, c.ID, next.State, next.Alerts, now); err != nil {
				return err
			}
			if err := emitAlerts(ctx, tx, tid, c, alerts, now); err != nil {
				return err
			}
			for _, a := range alerts {
				if a.Kind == domain.AlertBreach {
					res.Breaches++
				} else {
					res.Warnings++
				}
			}
		}
		return nil
	})
	return res, err
}

// CreateFollowUp opens the follow-up of a finished work order (BR-45.5). The new
// order is UNSCHEDULED, links the parent by follow_up_of and keeps its case,
// agreement, customer, legal entity, territory, incident type and estimate; it
// starts without lines. The parent's first_time_fix becomes false. A replay
// with the same Idempotency-Key returns the order created earlier.
func (s *Postgres) CreateFollowUp(ctx context.Context, parentID string, version int, key, requestHash string) (WorkOrder, bool, error) {
	tid := tenantOf(ctx)
	var out WorkOrder
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		parent, err := lockWorkOrder(ctx, tx, tid, parentID)
		if err != nil {
			return err
		}
		id, found, err := replayOf(ctx, tx, "service_work_order", tid, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = getWorkOrder(ctx, tx, tid, id)
			return err
		}
		if parent.Version != version {
			return versionConflict(parent.Version)
		}
		if !domain.CanFollowUp(domain.WOStatus(parent.Status)) {
			return unprocessable(CodeWOFollowUp, fmt.Sprintf("a %s work order cannot have a follow-up; only COMPLETED or POSTED ones can", parent.Status))
		}
		number, err := nextNumber(ctx, tx, tid, "workorder", "SWO")
		if err != nil {
			return err
		}
		childID := ids.New()
		if _, err := tx.Exec(ctx, `insert into service_work_order (tenant_id, id, number, case_id, agreement_id, customer_party_id, legal_entity_id,
			territory_id, incident_type_id, est_duration_minutes, currency, status, lines, version, follow_up_of, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, '[]', 1, $13, $14, $15)`,
			tid, childID, number, parent.CaseID, parent.AgreementID, parent.CustomerPartyID, parent.LegalEntityID, parent.TerritoryID,
			parent.IncidentTypeID, parent.EstDurationMinutes, parent.Currency, string(domain.WOUnscheduled),
			parentID, nullable(key), requestHash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update service_work_order set first_time_fix = false, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, tid, parentID); err != nil {
			return err
		}
		out, err = getWorkOrder(ctx, tx, tid, childID)
		return err
	})
	return out, replayed, err
}
