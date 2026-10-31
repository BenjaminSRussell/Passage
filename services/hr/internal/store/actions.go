package store

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

const (
	CodeActionNotFound    = "HCM-ACT-NOT-FOUND"
	CodeEmploymentMissing = "HCM-EMP-NOT-FOUND"
	CodeActionTooFar      = "HCM-ACT-001"
	CodeActionEmployment  = "HCM-ACT-003"
	CodeActionInactive    = "HCM-ACT-004"
	CodeActionDateOrder   = "HCM-ACT-006"
	CodeActionCompLater   = "HCM-ACT-007"
	CodeActionState       = "HCM-ACT-STATE"
	CodeTerminationDate   = "HCM-EMP-002"
	CodeCompCurrency      = "HCM-CMP-004"
	CodeCompOverlap       = "HCM-CMP-005"

	MaxFutureDays = 365

	ActionStatusPending   = "PENDING"
	ActionStatusApplied   = "APPLIED"
	ActionStatusWithdrawn = "WITHDRAWN"
	ActionStatusFailed    = "FAILED"

	sourceOffer     = "OFFER"
	sourceCompCycle = "COMP_CYCLE"
	defaultFTEShare = "1.00"
	sweepBatch      = 200
)

type ActionPayload struct {
	LegalEntityID     string `json:"legalEntityId,omitempty"`
	EmploymentType    string `json:"employmentType,omitempty"`
	AssignmentKind    string `json:"assignmentKind,omitempty"`
	PositionID        string `json:"positionId,omitempty"`
	FTEShare          string `json:"fteShare,omitempty"`
	CompPlanID        string `json:"compPlanId,omitempty"`
	Amount            string `json:"amount,omitempty"`
	Currency          string `json:"currency,omitempty"`
	TerminationReason string `json:"terminationReason,omitempty"`
}

type PersonnelAction struct {
	ID            string        `json:"id"`
	ActionType    string        `json:"actionType"`
	WorkerID      string        `json:"workerId"`
	EmploymentID  *string       `json:"employmentId,omitempty"`
	EffectiveDate string        `json:"effectiveDate"`
	Status        string        `json:"status"`
	Payload       ActionPayload `json:"payload"`
	Reason        *string       `json:"reason,omitempty"`
	SourceKind    *string       `json:"sourceKind,omitempty"`
	SourceID      *string       `json:"sourceId,omitempty"`
	FailureCode   *string       `json:"failureCode,omitempty"`
	FailureDetail *string       `json:"failureDetail,omitempty"`
	SubmittedBy   string        `json:"submittedBy"`
	SubmittedAt   time.Time     `json:"submittedAt"`
	AppliedBy     *string       `json:"appliedBy,omitempty"`
	AppliedAt     *time.Time    `json:"appliedAt,omitempty"`
	WithdrawnBy   *string       `json:"withdrawnBy,omitempty"`
	WithdrawnAt   *time.Time    `json:"withdrawnAt,omitempty"`
}

func (a PersonnelAction) WithoutPay() PersonnelAction {
	a.Payload.Amount, a.Payload.CompPlanID, a.Payload.Currency = "", "", ""
	return a
}

type ActionInput struct {
	ActionType    string
	WorkerID      string
	EmploymentID  *string
	EffectiveDate time.Time
	Payload       ActionPayload
	Reason        string
	SourceKind    *string
	SourceID      *string
}

const actionColumns = `id::text, action_type, worker_id::text, employment_id::text, to_char(effective_date, 'YYYY-MM-DD'), status, payload, reason,
	source_kind, source_id::text, failure_code, failure_detail, submitted_by, submitted_at, applied_by, applied_at, withdrawn_by, withdrawn_at`

func scanAction(row pgx.Row) (PersonnelAction, error) {
	var a PersonnelAction
	var payload []byte
	err := row.Scan(&a.ID, &a.ActionType, &a.WorkerID, &a.EmploymentID, &a.EffectiveDate, &a.Status, &payload, &a.Reason,
		&a.SourceKind, &a.SourceID, &a.FailureCode, &a.FailureDetail, &a.SubmittedBy, &a.SubmittedAt, &a.AppliedBy, &a.AppliedAt, &a.WithdrawnBy, &a.WithdrawnAt)
	if err != nil {
		return a, err
	}
	a.SubmittedAt, a.AppliedAt, a.WithdrawnAt = utc(a.SubmittedAt), utcPtr(a.AppliedAt), utcPtr(a.WithdrawnAt)
	return a, json.Unmarshal(payload, &a.Payload)
}

func sameAction(a PersonnelAction, in ActionInput) bool {
	return a.ActionType == in.ActionType && a.WorkerID == in.WorkerID && (in.ActionType == hrevents.ActionHire || sameStringPtr(a.EmploymentID, in.EmploymentID)) &&
		a.EffectiveDate == in.EffectiveDate.Format("2006-01-02") && reflect.DeepEqual(a.Payload, in.Payload)
}

func (s *Postgres) SubmitAction(ctx context.Context, in ActionInput, idempotencyKey string) (PersonnelAction, bool, error) {
	p := principal(ctx)
	key := idemKey(idempotencyKey)
	var out PersonnelAction
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanAction(tx.QueryRow(ctx, `select `+actionColumns+` from hr_personnel_action where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var err error
		out, err = s.submitActionTx(ctx, tx, p, in, key)
		inserted = err == nil
		return err
	})
	if idempotencyRace(err) {
		return PersonnelAction{}, false, idempotencyInFlight()
	}
	if err == nil && key != nil && !inserted && !sameAction(out, in) {
		return PersonnelAction{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

func (s *Postgres) submitActionTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, in ActionInput, key *string) (PersonnelAction, error) {
	today := s.today()
	if in.EffectiveDate.After(today.AddDate(0, 0, MaxFutureDays)) {
		return PersonnelAction{}, problem(http.StatusUnprocessableEntity, CodeActionTooFar, "the effective date is more than 365 days in the future")
	}
	var exists bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from worker where tenant_id = $1 and id = $2)`, p.TenantID, in.WorkerID).Scan(&exists); err != nil {
		return PersonnelAction{}, err
	}
	if !exists {
		return PersonnelAction{}, problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
	}
	legalEntity := in.Payload.LegalEntityID
	if in.ActionType != hrevents.ActionHire {
		if in.EmploymentID == nil {
			return PersonnelAction{}, problem(http.StatusUnprocessableEntity, CodeActionEmployment, "the action needs the worker's employment")
		}
		var employmentWorker string
		err := tx.QueryRow(ctx, `select worker_id::text, legal_entity_id::text from employment where tenant_id = $1 and id = $2`, p.TenantID, *in.EmploymentID).Scan(&employmentWorker, &legalEntity)
		if errors.Is(err, pgx.ErrNoRows) {
			return PersonnelAction{}, problem(http.StatusNotFound, CodeEmploymentMissing, "employment not found")
		}
		if err != nil {
			return PersonnelAction{}, err
		}
		if employmentWorker != in.WorkerID {
			return PersonnelAction{}, problem(http.StatusUnprocessableEntity, CodeActionEmployment, "the employment does not belong to this worker")
		}
	}
	payload, err := json.Marshal(in.Payload)
	if err != nil {
		return PersonnelAction{}, err
	}
	var reason *string
	if in.Reason != "" {
		reason = &in.Reason
	}
	act, err := scanAction(tx.QueryRow(ctx, `insert into hr_personnel_action (tenant_id, id, action_type, worker_id, legal_entity_id, employment_id, effective_date, status, payload, reason, source_kind, source_id, submitted_by, idempotency_key)
		values ($1, $2, $3, $4, $13, $5, $6::date, 'PENDING', $7, $8, $9, $10, $11, $12) returning `+actionColumns,
		p.TenantID, ids.New(), in.ActionType, in.WorkerID, in.EmploymentID, in.EffectiveDate, payload, reason, in.SourceKind, in.SourceID, p.UserID, key, legalEntity))
	if err != nil {
		return PersonnelAction{}, err
	}
	if in.EffectiveDate.After(today) {
		return act, nil
	}
	return s.applyActionTx(ctx, tx, p, act)
}

func (s *Postgres) applyActionTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, a PersonnelAction) (PersonnelAction, error) {
	at, err := time.Parse("2006-01-02", a.EffectiveDate)
	if err != nil {
		return PersonnelAction{}, err
	}
	var employmentID string
	if a.EmploymentID != nil {
		employmentID = *a.EmploymentID
	}
	switch a.ActionType {
	case hrevents.ActionHire:
		employmentID, err = s.applyHire(ctx, tx, p, a, at)
	case hrevents.ActionTransfer:
		err = s.applyTransfer(ctx, tx, p, a, employmentID, at)
	case hrevents.ActionTerminate:
		err = s.applyTerminate(ctx, tx, p, a, employmentID, at)
	case hrevents.ActionCompChange:
		err = s.applyCompChange(ctx, tx, p, a, employmentID, at)
	default:
		err = problem(http.StatusUnprocessableEntity, CodeActionState, "unknown action type")
	}
	if err != nil {
		return PersonnelAction{}, err
	}
	return scanAction(tx.QueryRow(ctx, `update hr_personnel_action set status = 'APPLIED', applied_by = $3, applied_at = now(), employment_id = $4
		where tenant_id = $1 and id = $2 and status = 'PENDING' returning `+actionColumns, p.TenantID, a.ID, p.UserID, employmentID))
}

func (s *Postgres) applyHire(ctx context.Context, tx pgx.Tx, p tenant.Principal, a PersonnelAction, at time.Time) (string, error) {
	pl := a.Payload
	kind := pl.AssignmentKind
	if kind == "" {
		kind = "PRIMARY"
	}
	employmentID := ids.New()
	if _, err := tx.Exec(ctx, `insert into employment (tenant_id, id, worker_id, legal_entity_id, employment_type, assignment_kind, hire_date, valid_from)
		values ($1, $2, $3, $4, $5, $6, $7::date, $8)`, p.TenantID, employmentID, a.WorkerID, pl.LegalEntityID, pl.EmploymentType, kind, at, at); err != nil {
		if pgConstraint(err, "employment_primary_no_overlap") {
			return "", problem(http.StatusConflict, CodeEmploymentOverlap, "the worker already has an active primary employment in this legal entity on that date")
		}
		return "", err
	}
	if err := employmentEvent(ctx, tx, p.TenantID, employmentID, hrevents.ActionHire); err != nil {
		return "", err
	}
	if pl.PositionID != "" {
		if err := s.assignPrimary(ctx, tx, p.TenantID, pl.PositionID, a.WorkerID, employmentID, pl.FTEShare, kind == "PRIMARY", at); err != nil {
			return "", err
		}
	}
	if pl.CompPlanID != "" {
		if err := changeCompTx(ctx, tx, p.TenantID, a.WorkerID, employmentID, pl.CompPlanID, pl.Amount, pl.Currency, at); err != nil {
			return "", err
		}
	}
	return employmentID, nil
}

func (s *Postgres) assignPrimary(ctx context.Context, tx pgx.Tx, tenantID, positionID, workerID, employmentID, share string, primary bool, at time.Time) error {
	if share == "" {
		share = defaultFTEShare
	}
	var end *time.Time
	err := tx.QueryRow(ctx, `select least(p.valid_to, e.valid_to) from hr_position p, employment e
		where p.tenant_id = $1 and p.id = $2 and e.tenant_id = $1 and e.id = $3`, tenantID, positionID, employmentID).Scan(&end)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	_, err = assignTx(ctx, tx, tenantID, positionID, PositionAssignment{
		WorkerID: workerID, EmploymentID: employmentID, FTEShare: share, IsPrimary: primary, ValidFrom: at, ValidTo: utcPtr(end),
	}, nil)
	return err
}

func employmentEvent(ctx context.Context, tx pgx.Tx, tenantID, employmentID, action string) error {
	e, err := scanEmployment(tx.QueryRow(ctx, `select `+employmentColumns+` from employment where tenant_id = $1 and id = $2`, tenantID, employmentID))
	if err != nil {
		return err
	}
	return enqueue(ctx, tx, "employment", e.ID, hrevents.EmploymentChanged, tenantID, hrevents.EmploymentChangedData{
		ID: e.ID, WorkerID: e.WorkerID, LegalEntityID: e.LegalEntityID, EmploymentType: e.EmploymentType, AssignmentKind: e.AssignmentKind,
		Action: action, ValidFrom: e.ValidFrom.Format(time.RFC3339), ValidTo: rfc3339Ptr(e.ValidTo),
	})
}

func lockActiveEmployment(ctx context.Context, tx pgx.Tx, tenantID, employmentID string, at time.Time) error {
	var covers bool
	err := tx.QueryRow(ctx, `select validity @> $3::timestamptz from employment where tenant_id = $1 and id = $2 for update`, tenantID, employmentID, at).Scan(&covers)
	if err != nil {
		return err
	}
	if !covers {
		return problem(http.StatusUnprocessableEntity, CodeActionInactive, "the employment is not active on the effective date")
	}
	return nil
}

func (s *Postgres) applyTransfer(ctx context.Context, tx pgx.Tx, p tenant.Principal, a PersonnelAction, employmentID string, at time.Time) error {
	if err := lockActiveEmployment(ctx, tx, p.TenantID, employmentID, at); err != nil {
		return err
	}
	pl := a.Payload
	var oldID, oldPosition, oldShare string
	var oldFrom time.Time
	err := tx.QueryRow(ctx, `select id::text, position_id::text, fte_share::text, valid_from from position_assignment
		where tenant_id = $1 and employment_id = $2 and is_primary and validity @> $3::timestamptz for update`, p.TenantID, employmentID, at).Scan(&oldID, &oldPosition, &oldShare, &oldFrom)
	have := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	share := pl.FTEShare
	if have {
		if oldPosition == pl.PositionID {
			return problem(http.StatusUnprocessableEntity, CodeActionDateOrder, "the worker already holds that position on the effective date")
		}
		if !utc(oldFrom).Before(at) {
			return problem(http.StatusUnprocessableEntity, CodeActionDateOrder, "the current assignment starts on or after the effective date")
		}
		if share == "" {
			share = oldShare
		}
		if _, err := tx.Exec(ctx, `update position_assignment set valid_to = $3 where tenant_id = $1 and id = $2`, p.TenantID, oldID, at); err != nil {
			return err
		}
		end := at.Format(time.RFC3339)
		if err := enqueue(ctx, tx, "positionassignment", oldID, hrevents.PositionAssignmentChanged, p.TenantID, hrevents.PositionAssignmentChangedData{
			ID: oldID, WorkerID: a.WorkerID, PositionID: oldPosition, FTEShare: oldShare, IsPrimary: true, ValidFrom: utc(oldFrom).Format(time.RFC3339), ValidTo: &end,
		}); err != nil {
			return err
		}
	}
	if err := s.assignPrimary(ctx, tx, p.TenantID, pl.PositionID, a.WorkerID, employmentID, share, true, at); err != nil {
		return err
	}
	return employmentEvent(ctx, tx, p.TenantID, employmentID, hrevents.ActionTransfer)
}

func (s *Postgres) applyCompChange(ctx context.Context, tx pgx.Tx, p tenant.Principal, a PersonnelAction, employmentID string, at time.Time) error {
	if err := lockActiveEmployment(ctx, tx, p.TenantID, employmentID, at); err != nil {
		return err
	}
	pl := a.Payload
	return changeCompTx(ctx, tx, p.TenantID, a.WorkerID, employmentID, pl.CompPlanID, pl.Amount, pl.Currency, at)
}

func changeCompTx(ctx context.Context, tx pgx.Tx, tenantID, workerID, employmentID, planID, amount, currency string, at time.Time) error {
	var planCurrency string
	err := tx.QueryRow(ctx, `select currency from hr_compensation_plan where tenant_id = $1 and id = $2`, tenantID, planID).Scan(&planCurrency)
	if errors.Is(err, pgx.ErrNoRows) {
		return problem(http.StatusNotFound, CodeCompPlanNotFound, "compensation plan not found")
	}
	if err != nil {
		return err
	}
	if planCurrency != currency {
		return problem(http.StatusUnprocessableEntity, CodeCompCurrency, "the currency differs from the compensation plan's currency "+planCurrency)
	}
	var later bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from hr_fixed_compensation where tenant_id = $1 and employment_id = $2 and valid_from >= $3)`,
		tenantID, employmentID, at).Scan(&later); err != nil {
		return err
	}
	if later {
		return problem(http.StatusUnprocessableEntity, CodeActionCompLater, "compensation already starts on or after the effective date")
	}
	if err := closeCompensation(ctx, tx, tenantID, workerID, employmentID, at); err != nil {
		return err
	}
	var legalEntity string
	var end *time.Time
	if err := tx.QueryRow(ctx, `select legal_entity_id::text, valid_to from employment where tenant_id = $1 and id = $2`, tenantID, employmentID).Scan(&legalEntity, &end); err != nil {
		return err
	}
	id := ids.New()
	if _, err := tx.Exec(ctx, `insert into hr_fixed_compensation (tenant_id, id, employment_id, worker_id, legal_entity_id, plan_id, amount, currency, valid_from, valid_to)
		values ($1, $2, $3, $4, $5, $6, $7::numeric, $8, $9, $10)`, tenantID, id, employmentID, workerID, legalEntity, planID, amount, currency, at, end); err != nil {
		if pgConstraint(err, "hr_fixed_compensation_no_overlap") {
			return problem(http.StatusConflict, CodeCompOverlap, "the employment already has compensation on that date")
		}
		return err
	}
	return enqueue(ctx, tx, "compensation", id, hrevents.CompensationChanged, tenantID, hrevents.CompensationChangedData{
		ID: id, WorkerID: workerID, EmploymentID: employmentID, PlanID: planID, Amount: amount, Currency: currency,
		ValidFrom: at.Format(time.RFC3339), ValidTo: rfc3339Ptr(utcPtr(end)),
	})
}

func (s *Postgres) applyTerminate(ctx context.Context, tx pgx.Tx, p tenant.Principal, a PersonnelAction, employmentID string, lastDay time.Time) error {
	var hire, validFrom time.Time
	var validTo *time.Time
	err := tx.QueryRow(ctx, `select hire_date, valid_from, valid_to from employment where tenant_id = $1 and id = $2 for update`, p.TenantID, employmentID).Scan(&hire, &validFrom, &validTo)
	if err != nil {
		return err
	}
	end := lastDay.AddDate(0, 0, 1)
	if lastDay.Before(utc(hire)) || !end.After(utc(validFrom)) {
		return problem(http.StatusUnprocessableEntity, CodeTerminationDate, "the termination date is before the hire date")
	}
	if validTo != nil && !end.Before(utc(*validTo)) {
		return problem(http.StatusUnprocessableEntity, CodeActionInactive, "the employment already ends on or before the termination date")
	}
	var lastEmployment, future bool
	if err := tx.QueryRow(ctx, `select not hr_worker_has_other_employment($1::uuid, $2::uuid, $3::timestamptz)`,
		a.WorkerID, employmentID, end).Scan(&lastEmployment); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `select exists (select 1 from position_assignment where tenant_id = $1 and employment_id = $2 and valid_from >= $3)
		or exists (select 1 from hr_fixed_compensation where tenant_id = $1 and employment_id = $2 and valid_from >= $3)
		or ($4 and exists (select 1 from benefit_enrollment where tenant_id = $1 and worker_id = $5 and valid_to is null and valid_from > $6))`,
		p.TenantID, employmentID, end, lastEmployment, a.WorkerID, lastDay).Scan(&future); err != nil {
		return err
	}
	if future {
		return problem(http.StatusUnprocessableEntity, CodeActionDateOrder, "assignments, compensation or enrollments start after the termination date; withdraw or end them first")
	}
	if _, err := tx.Exec(ctx, `update employment set termination_date = $3::date, valid_to = $4, termination_reason = $5 where tenant_id = $1 and id = $2`,
		p.TenantID, employmentID, lastDay, end, a.Payload.TerminationReason); err != nil {
		return err
	}
	if err := s.closeAssignments(ctx, tx, p.TenantID, a.WorkerID, employmentID, end); err != nil {
		return err
	}
	if err := closeCompensation(ctx, tx, p.TenantID, a.WorkerID, employmentID, end); err != nil {
		return err
	}
	if lastEmployment {
		if err := s.endOpenEnrollments(ctx, tx, p, a.WorkerID, lastDay); err != nil {
			return err
		}
	}
	return employmentEvent(ctx, tx, p.TenantID, employmentID, hrevents.ActionTerminate)
}

func (s *Postgres) closeAssignments(ctx context.Context, tx pgx.Tx, tenantID, workerID, employmentID string, end time.Time) error {
	rows, err := tx.Query(ctx, `update position_assignment set valid_to = $3 where tenant_id = $1 and employment_id = $2 and valid_from < $3 and (valid_to is null or valid_to > $3)
		returning id::text, position_id::text, fte_share::text, is_primary, valid_from`, tenantID, employmentID, end)
	if err != nil {
		return err
	}
	var closed []PositionAssignment
	for rows.Next() {
		a := PositionAssignment{WorkerID: workerID, EmploymentID: employmentID}
		if err := rows.Scan(&a.ID, &a.PositionID, &a.FTEShare, &a.IsPrimary, &a.ValidFrom); err != nil {
			rows.Close()
			return err
		}
		closed = append(closed, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	to := end.Format(time.RFC3339)
	for _, a := range closed {
		if err := enqueue(ctx, tx, "positionassignment", a.ID, hrevents.PositionAssignmentChanged, tenantID, hrevents.PositionAssignmentChangedData{
			ID: a.ID, WorkerID: workerID, PositionID: a.PositionID, FTEShare: a.FTEShare, IsPrimary: a.IsPrimary, ValidFrom: utc(a.ValidFrom).Format(time.RFC3339), ValidTo: &to,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Postgres) endOpenEnrollments(ctx context.Context, tx pgx.Tx, p tenant.Principal, workerID string, lastDay time.Time) error {
	rows, err := tx.Query(ctx, `select id::text from benefit_enrollment where tenant_id = $1 and worker_id = $2 and valid_to is null order by id`, p.TenantID, workerID)
	if err != nil {
		return err
	}
	open, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range open {
		if _, _, err := s.endEnrollmentTx(ctx, tx, p, id, lastDay, domain.EndTermination); err != nil {
			return err
		}
	}
	return nil
}

func (s *Postgres) GetAction(ctx context.Context, id string) (PersonnelAction, error) {
	p := principal(ctx)
	var out PersonnelAction
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = scanAction(tx.QueryRow(ctx, `select `+actionColumns+` from hr_personnel_action where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PersonnelAction{}, problem(http.StatusNotFound, CodeActionNotFound, "personnel action not found")
	}
	return out, err
}

type ActionFilter struct {
	Status   string
	WorkerID string
}

type ActionPage struct {
	Items     []PersonnelAction
	NextAfter string
}

func (s *Postgres) ListActions(ctx context.Context, f ActionFilter, after string, limit int) (ActionPage, error) {
	p := principal(ctx)
	page := ActionPage{Items: []PersonnelAction{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+actionColumns+` from hr_personnel_action
			where tenant_id = $1 and id::text > $2 and ($3 = '' or status = $3) and ($4 = '' or worker_id::text = $4) order by id limit $5`,
			p.TenantID, after, f.Status, f.WorkerID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAction(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, a)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextAfter = page.Items[limit-1].ID
	}
	return page, err
}

func (s *Postgres) WithdrawAction(ctx context.Context, id string) (PersonnelAction, bool, error) {
	p := principal(ctx)
	var out PersonnelAction
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanAction(tx.QueryRow(ctx, `select `+actionColumns+` from hr_personnel_action where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeActionNotFound, "personnel action not found")
		}
		if err != nil {
			return err
		}
		switch cur.Status {
		case ActionStatusWithdrawn:
			out = cur
			return nil
		case ActionStatusPending:
		default:
			return problem(http.StatusConflict, CodeActionState, "only a pending action can be withdrawn; this one is "+cur.Status)
		}
		out, err = scanAction(tx.QueryRow(ctx, `update hr_personnel_action set status = 'WITHDRAWN', withdrawn_by = $3, withdrawn_at = now()
			where tenant_id = $1 and id = $2 returning `+actionColumns, p.TenantID, id, p.UserID))
		if err != nil {
			return err
		}
		changed = true
		return releaseOffer(ctx, tx, p.TenantID, id, s.now())
	})
	return out, changed, err
}

type ApplyResult struct {
	Applied int `json:"applied"`
	Failed  int `json:"failed"`
}

func (s *Postgres) ApplyDueActions(ctx context.Context, asOf time.Time) (ApplyResult, error) {
	p := principal(ctx)
	var res ApplyResult
	var due []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text from hr_personnel_action where tenant_id = $1 and status = 'PENDING' and effective_date <= $2::date
			order by effective_date, submitted_at, id limit $3`, p.TenantID, asOf, sweepBatch)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return res, err
	}
	for _, id := range due {
		applied, failed := s.applyOne(ctx, p, id)
		switch {
		case failed:
			res.Failed++
		case applied:
			res.Applied++
		}
	}
	return res, nil
}

func (s *Postgres) applyOne(ctx context.Context, p tenant.Principal, id string) (applied, failed bool) {
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		act, err := scanAction(tx.QueryRow(ctx, `select `+actionColumns+` from hr_personnel_action where tenant_id = $1 and id = $2 and status = 'PENDING' for update skip locked`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := s.applyActionTx(ctx, tx, p, act); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err == nil {
		return applied, false
	}
	code, detail := "HCM-ACT-ERROR", "the action could not be applied; see the service log"
	var pr *httpx.Problem
	if errors.As(err, &pr) && pr.Status < http.StatusInternalServerError {
		code, detail = pr.Code, pr.Detail
	} else {
		slog.Error("apply personnel action failed", "err", err, "actionId", id, "tenantId", p.TenantID)
	}
	failErr := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update hr_personnel_action set status = 'FAILED', failure_code = $3, failure_detail = $4
			where tenant_id = $1 and id = $2 and status = 'PENDING'`, p.TenantID, id, code, detail)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return releaseOffer(ctx, tx, p.TenantID, id, s.now())
	})
	if failErr != nil {
		slog.Error("mark personnel action failed", "err", failErr, "actionId", id, "tenantId", p.TenantID)
		return false, false
	}
	return false, true
}

func releaseOffer(ctx context.Context, tx pgx.Tx, tenantID, actionID string, at time.Time) error {
	var candidateID string
	err := tx.QueryRow(ctx, `update hr_offer set hire_action_id = null, updated_at = now() where tenant_id = $1 and hire_action_id = $2 returning candidate_id::text`, tenantID, actionID).Scan(&candidateID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `update hr_candidate set stage = 'OFFER', stage_changed_at = $3 where tenant_id = $1 and id = $2 and stage = 'HIRED'`, tenantID, candidateID, at)
	return err
}

func closeCompensation(ctx context.Context, tx pgx.Tx, tenantID, workerID, employmentID string, at time.Time) error {
	rows, err := tx.Query(ctx, `update hr_fixed_compensation set valid_to = $3 where tenant_id = $1 and employment_id = $2 and valid_from < $3 and (valid_to is null or valid_to > $3)
		returning id::text, plan_id::text, amount::text, currency, valid_from`, tenantID, employmentID, at)
	if err != nil {
		return err
	}
	var closed []hrevents.CompensationChangedData
	for rows.Next() {
		d := hrevents.CompensationChangedData{WorkerID: workerID, EmploymentID: employmentID}
		var from time.Time
		if err := rows.Scan(&d.ID, &d.PlanID, &d.Amount, &d.Currency, &from); err != nil {
			rows.Close()
			return err
		}
		to := at.Format(time.RFC3339)
		d.ValidFrom, d.ValidTo = utc(from).Format(time.RFC3339), &to
		closed = append(closed, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range closed {
		if err := enqueue(ctx, tx, "compensation", d.ID, hrevents.CompensationChanged, tenantID, d); err != nil {
			return err
		}
	}
	return nil
}

func scopeIsAll(ctx context.Context, tx pgx.Tx) (bool, error) {
	var scopes string
	if err := tx.QueryRow(ctx, `select coalesce(current_setting('passage.legal_entity_scopes', true), '')`).Scan(&scopes); err != nil {
		return false, err
	}
	return scopes == "*", nil
}
