package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// Pay setup problem codes (see README).
const (
	CodeProfileNotFound   = "SF-0140"
	CodeAgreementNotFound = "SF-0141"
	CodeSetupCodeTaken    = "SF-0142"
	CodeVersionMismatch   = "SF-0143"
	CodeSetupInactive     = "SF-0144"
)

const (
	profileSource   = "timeprofile"
	agreementSource = "payagreement"
)

// TimeProfile is a working pattern: day rules per day type (ProfileDay, FR-25.7).
type TimeProfile struct {
	ID        string                   `json:"id"`
	Code      string                   `json:"code"`
	Name      string                   `json:"name"`
	Active    bool                     `json:"active"`
	Version   int                      `json:"version"`
	Days      []domain.ProfileDayInput `json:"days"`
	CreatedBy string                   `json:"createdBy"`
	CreatedAt time.Time                `json:"createdAt"`
	UpdatedAt time.Time                `json:"updatedAt"`
}

// TimeProfileInput is what a create or update writes. An update replaces the days.
type TimeProfileInput struct {
	Code   string
	Name   string
	Active bool
	Days   []domain.ProfileDayInput
}

// PayAgreement is the overtime thresholds and multipliers of a group of workers (FR-25.7).
type PayAgreement struct {
	ID                 string                     `json:"id"`
	Code               string                     `json:"code"`
	Name               string                     `json:"name"`
	DailyThreshold     decimal.Decimal            `json:"dailyThreshold"`
	WeeklyThreshold    decimal.Decimal            `json:"weeklyThreshold"`
	OvertimeMultiplier decimal.Decimal            `json:"overtimeMultiplier"`
	SundayMultiplier   decimal.Decimal            `json:"sundayMultiplier"`
	WeekStart          int                        `json:"weekStart"`
	ShiftPremiums      []domain.ShiftPremiumInput `json:"shiftPremiums"`
	Active             bool                       `json:"active"`
	Version            int                        `json:"version"`
	CreatedBy          string                     `json:"createdBy"`
	CreatedAt          time.Time                  `json:"createdAt"`
	UpdatedAt          time.Time                  `json:"updatedAt"`
}

// PayAgreementInput is what a create or update writes.
type PayAgreementInput struct {
	Code   string
	Name   string
	Active bool
	Rules  domain.Rules
}

func profileNotFound(status int) error {
	return httpx.NewProblem(status, CodeProfileNotFound, "time profile not found")
}

func agreementNotFound(status int) error {
	return httpx.NewProblem(status, CodeAgreementNotFound, "pay agreement not found")
}

func versionMismatch(what string) error {
	return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionMismatch, what+" was changed by someone else; reload it and retry with its current version")
}

func codeTaken(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return httpx.NewProblem(http.StatusConflict, CodeSetupCodeTaken, "code is already used")
	}
	return err
}

const profileColumns = `id::text, code, name, active, version, created_by, created_at, updated_at`

func scanProfile(row pgx.Row) (TimeProfile, error) {
	var p TimeProfile
	err := row.Scan(&p.ID, &p.Code, &p.Name, &p.Active, &p.Version, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	p.Days = []domain.ProfileDayInput{}
	return p, err
}

func loadProfileDays(ctx context.Context, tx pgx.Tx, profileIDs ...string) (map[string][]domain.ProfileDayInput, error) {
	out := map[string][]domain.ProfileDayInput{}
	rows, err := tx.Query(ctx, `select profile_id::text, day_type, to_char(shift_start, 'HH24:MI'), to_char(shift_end, 'HH24:MI'), break_windows,
			rounding_minutes, tolerance_before_min, tolerance_after_min, flex_allowed
		from profile_day where profile_id = any($1::uuid[])
		order by profile_id, case day_type when 'weekday' then 1 when 'saturday' then 2 else 3 end`, profileIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid string
		var d domain.ProfileDayInput
		var raw []byte
		if err := rows.Scan(&pid, &d.DayType, &d.ShiftStart, &d.ShiftEnd, &raw, &d.RoundingMinutes, &d.ToleranceBeforeMin, &d.ToleranceAfterMin, &d.FlexAllowed); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &d.Breaks); err != nil {
			return nil, err
		}
		if d.Breaks == nil {
			d.Breaks = []domain.BreakInput{}
		}
		out[pid] = append(out[pid], d)
	}
	return out, rows.Err()
}

func writeProfileDays(ctx context.Context, tx pgx.Tx, tenantID, profileID string, days []domain.ProfileDayInput) error {
	if _, err := tx.Exec(ctx, `delete from profile_day where tenant_id = $1 and profile_id = $2`, tenantID, profileID); err != nil {
		return err
	}
	for _, d := range days {
		breaks := d.Breaks
		if breaks == nil {
			breaks = []domain.BreakInput{}
		}
		raw, err := json.Marshal(breaks)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into profile_day (tenant_id, profile_id, day_type, shift_start, shift_end, break_windows,
				rounding_minutes, tolerance_before_min, tolerance_after_min, flex_allowed)
			values ($1, $2, $3, $4::time, $5::time, $6, $7, $8, $9, $10)`,
			tenantID, profileID, d.DayType, d.ShiftStart, d.ShiftEnd, raw, d.RoundingMinutes, d.ToleranceBeforeMin, d.ToleranceAfterMin, d.FlexAllowed); err != nil {
			return err
		}
	}
	return nil
}

func profileEvent(ctx context.Context, tx pgx.Tx, tenantID string, p TimeProfile, by string) error {
	return enqueue(ctx, tx, profileSource, p.ID, sfevents.TimeProfileSaved, tenantID,
		sfevents.TimeProfileSavedData{ProfileID: p.ID, Code: p.Code, Name: p.Name, Active: p.Active, Version: p.Version, SavedBy: by})
}

// CreateTimeProfile stores a profile with its days under the Idempotency-Key and
// publishes dev.passage.shopfloor.timeprofile.saved.v1 in the same transaction.
// A code already used is SF-0142.
func (s *Postgres) CreateTimeProfile(ctx context.Context, in TimeProfileInput, createdBy string, idem Idem) (TimeProfile, error) {
	p, _ := tenant.FromContext(ctx)
	var out TimeProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanProfile(tx.QueryRow(ctx, `insert into time_profile (tenant_id, id, code, name, active, created_by) values ($1, $2, $3, $4, $5, $6) returning `+profileColumns,
			p.TenantID, ids.New(), in.Code, in.Name, in.Active, createdBy))
		if err != nil {
			return codeTaken(err)
		}
		if err := writeProfileDays(ctx, tx, p.TenantID, out.ID, in.Days); err != nil {
			return err
		}
		out.Days = nonNilDays(in.Days)
		if err := profileEvent(ctx, tx, p.TenantID, out, createdBy); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, err
}

func nonNilDays(d []domain.ProfileDayInput) []domain.ProfileDayInput {
	if d == nil {
		return []domain.ProfileDayInput{}
	}
	return d
}

// UpdateTimeProfile replaces a profile's name, status and days when version is the
// stored version (SF-0143 otherwise, 412) and bumps the version. The code does not change.
// Pay lines already calculated are not touched.
func (s *Postgres) UpdateTimeProfile(ctx context.Context, id string, version int, in TimeProfileInput, updatedBy string) (TimeProfile, error) {
	p, _ := tenant.FromContext(ctx)
	var out TimeProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanProfile(tx.QueryRow(ctx, `update time_profile set name = $3, active = $4, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and version = $5 returning `+profileColumns, p.TenantID, id, in.Name, in.Active, version))
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from time_profile where tenant_id = $1 and id = $2)`, p.TenantID, id).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return profileNotFound(http.StatusNotFound)
			}
			return versionMismatch("time profile")
		}
		if err != nil {
			return err
		}
		if err := writeProfileDays(ctx, tx, p.TenantID, id, in.Days); err != nil {
			return err
		}
		out.Days = nonNilDays(in.Days)
		return profileEvent(ctx, tx, p.TenantID, out, updatedBy)
	})
	return out, err
}

// GetTimeProfile returns a profile with its days, or SF-0140 (404).
func (s *Postgres) GetTimeProfile(ctx context.Context, id string) (TimeProfile, error) {
	var out TimeProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanProfile(tx.QueryRow(ctx, `select `+profileColumns+` from time_profile where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return profileNotFound(http.StatusNotFound)
		}
		if err != nil {
			return err
		}
		days, err := loadProfileDays(ctx, tx, id)
		if err != nil {
			return err
		}
		out.Days = nonNilDays(days[id])
		return nil
	})
	return out, err
}

// ListTimeProfiles returns profiles by code with their days, at most limit rows.
func (s *Postgres) ListTimeProfiles(ctx context.Context, activeOnly bool, limit int) ([]TimeProfile, error) {
	out := []TimeProfile{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+profileColumns+` from time_profile where ($1 = false or active) order by code, id limit $2`, activeOnly, limit)
		if err != nil {
			return err
		}
		var pids []string
		for rows.Next() {
			p, err := scanProfile(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out = append(out, p)
			pids = append(pids, p.ID)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		days, err := loadProfileDays(ctx, tx, pids...)
		if err != nil {
			return err
		}
		for i := range out {
			out[i].Days = nonNilDays(days[out[i].ID])
		}
		return nil
	})
	return out, err
}

const agreementColumns = `id::text, code, name, daily_threshold::text, weekly_threshold::text, overtime_multiplier::text, sunday_multiplier::text,
	week_start, shift_premiums, active, version, created_by, created_at, updated_at`

func scanAgreement(row pgx.Row) (PayAgreement, error) {
	var a PayAgreement
	var daily, weekly, ot, sun string
	var premiums []byte
	if err := row.Scan(&a.ID, &a.Code, &a.Name, &daily, &weekly, &ot, &sun, &a.WeekStart, &premiums, &a.Active, &a.Version, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return PayAgreement{}, err
	}
	a.ShiftPremiums = []domain.ShiftPremiumInput{}
	if err := json.Unmarshal(premiums, &a.ShiftPremiums); err != nil {
		return PayAgreement{}, err
	}
	var err error
	for _, f := range []struct {
		dst *decimal.Decimal
		src string
	}{{&a.DailyThreshold, daily}, {&a.WeeklyThreshold, weekly}, {&a.OvertimeMultiplier, ot}, {&a.SundayMultiplier, sun}} {
		if *f.dst, err = decimal.NewFromString(f.src); err != nil {
			return PayAgreement{}, err
		}
	}
	return a, nil
}

func agreementEvent(ctx context.Context, tx pgx.Tx, tenantID string, a PayAgreement, by string) error {
	return enqueue(ctx, tx, agreementSource, a.ID, sfevents.PayAgreementSaved, tenantID, sfevents.PayAgreementSavedData{
		AgreementID: a.ID, Code: a.Code, Name: a.Name, DailyThreshold: a.DailyThreshold.String(), WeeklyThreshold: a.WeeklyThreshold.String(),
		OvertimeMultiplier: a.OvertimeMultiplier.String(), SundayMultiplier: a.SundayMultiplier.String(), WeekStart: a.WeekStart,
		ShiftPremiums: a.ShiftPremiums, Active: a.Active, Version: a.Version, SavedBy: by})
}

// CreatePayAgreement stores an agreement under the Idempotency-Key and publishes
// dev.passage.shopfloor.payagreement.saved.v1 in the same transaction. A code already
// used is SF-0142.
func (s *Postgres) CreatePayAgreement(ctx context.Context, in PayAgreementInput, createdBy string, idem Idem) (PayAgreement, error) {
	p, _ := tenant.FromContext(ctx)
	var out PayAgreement
	premiums, err := json.Marshal(domain.ShiftPremiumInputs(in.Rules.ShiftPremiums))
	if err != nil {
		return out, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanAgreement(tx.QueryRow(ctx, `insert into pay_agreement (tenant_id, id, code, name, daily_threshold, weekly_threshold, overtime_multiplier, sunday_multiplier, week_start, shift_premiums, active, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) returning `+agreementColumns,
			p.TenantID, ids.New(), in.Code, in.Name, in.Rules.DailyThreshold.String(), in.Rules.WeeklyThreshold.String(), in.Rules.OvertimeMultiplier.String(),
			in.Rules.SundayMultiplier.String(), int(in.Rules.WeekStart), premiums, in.Active, createdBy))
		if err != nil {
			return codeTaken(err)
		}
		if err := agreementEvent(ctx, tx, p.TenantID, out, createdBy); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, err
}

// UpdatePayAgreement replaces an agreement's name, status and rules when version is the
// stored version (SF-0143, 412) and bumps the version. Pay lines already calculated are
// not touched; recalculation of a day uses the new rules.
func (s *Postgres) UpdatePayAgreement(ctx context.Context, id string, version int, in PayAgreementInput, updatedBy string) (PayAgreement, error) {
	p, _ := tenant.FromContext(ctx)
	var out PayAgreement
	premiums, err := json.Marshal(domain.ShiftPremiumInputs(in.Rules.ShiftPremiums))
	if err != nil {
		return out, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanAgreement(tx.QueryRow(ctx, `update pay_agreement set name = $3, daily_threshold = $4, weekly_threshold = $5, overtime_multiplier = $6,
				sunday_multiplier = $7, week_start = $8, shift_premiums = $11, active = $9, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and version = $10 returning `+agreementColumns,
			p.TenantID, id, in.Name, in.Rules.DailyThreshold.String(), in.Rules.WeeklyThreshold.String(), in.Rules.OvertimeMultiplier.String(),
			in.Rules.SundayMultiplier.String(), int(in.Rules.WeekStart), in.Active, version, premiums))
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from pay_agreement where tenant_id = $1 and id = $2)`, p.TenantID, id).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return agreementNotFound(http.StatusNotFound)
			}
			return versionMismatch("pay agreement")
		}
		if err != nil {
			return err
		}
		return agreementEvent(ctx, tx, p.TenantID, out, updatedBy)
	})
	return out, err
}

// GetPayAgreement returns an agreement, or SF-0141 (404).
func (s *Postgres) GetPayAgreement(ctx context.Context, id string) (PayAgreement, error) {
	var out PayAgreement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanAgreement(tx.QueryRow(ctx, `select `+agreementColumns+` from pay_agreement where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return agreementNotFound(http.StatusNotFound)
		}
		return err
	})
	return out, err
}

// ListPayAgreements returns agreements by code, at most limit rows.
func (s *Postgres) ListPayAgreements(ctx context.Context, activeOnly bool, limit int) ([]PayAgreement, error) {
	out := []PayAgreement{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+agreementColumns+` from pay_agreement where ($1 = false or active) order by code, id limit $2`, activeOnly, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAgreement(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// AssignWorkerSetup links a worker to a time profile and a pay agreement (nil clears the
// link, so the defaults apply) when version is the worker's stored version (SF-0143, 412).
// A newly chosen profile or agreement must exist (SF-0140, SF-0141, 422) and be active
// (SF-0144, 422); the one the worker is already linked to may stay linked after it was
// deactivated, so changing only the other link still works. It publishes
// dev.passage.shopfloor.worker.assigned.v1 in the same transaction. Days already
// calculated keep their lines until recalculated.
func (s *Postgres) AssignWorkerSetup(ctx context.Context, workerID string, version int, profileID, agreementID *string, assignedBy string, idem Idem) (Worker, error) {
	p, _ := tenant.FromContext(ctx)
	var out Worker
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var curProfile, curAgreement *string
		err := tx.QueryRow(ctx, `select profile_id::text, pay_agreement_id::text from worker where tenant_id = $1 and id = $2 for update`, p.TenantID, workerID).
			Scan(&curProfile, &curAgreement)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
		}
		if err != nil {
			return err
		}
		if err := checkSetupLink(ctx, tx, p.TenantID, "time_profile", "time profile", profileID, curProfile, profileNotFound); err != nil {
			return err
		}
		if err := checkSetupLink(ctx, tx, p.TenantID, "pay_agreement", "pay agreement", agreementID, curAgreement, agreementNotFound); err != nil {
			return err
		}
		out, err = scanWorker(tx.QueryRow(ctx, `update worker set profile_id = $3, pay_agreement_id = $4, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and version = $5 returning `+workerColumns, p.TenantID, workerID, profileID, agreementID, version))
		if errors.Is(err, pgx.ErrNoRows) {
			return versionMismatch("worker")
		}
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, workerSource, workerID, sfevents.WorkerAssigned, p.TenantID,
			sfevents.WorkerAssignedData{WorkerID: workerID, ProfileID: out.ProfileID, PayAgreementID: out.PayAgreementID, AssignedBy: assignedBy}); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, err
}

// checkSetupLink refuses a link to a profile or agreement that does not exist (422) or,
// unless it is the one the worker already has, that is inactive (SF-0144).
func checkSetupLink(ctx context.Context, tx pgx.Tx, tenantID, table, what string, id, current *string, notFound func(int) error) error {
	if id == nil {
		return nil
	}
	var active bool
	err := tx.QueryRow(ctx, `select active from `+table+` where tenant_id = $1 and id = $2`, tenantID, *id).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(http.StatusUnprocessableEntity)
	}
	if err != nil {
		return err
	}
	if !active && (current == nil || !strings.EqualFold(*current, *id)) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeSetupInactive, what+" is inactive")
	}
	return nil
}

// LoadPaySetup reads the day rules and pay agreement of a worker's links. A worker with
// no profile or agreement gets the defaults; a deactivated profile or agreement still
// applies to the workers linked to it, so deactivating never changes pay silently.
func (s *Postgres) LoadPaySetup(ctx context.Context, workerID string) (domain.PaySetup, error) {
	p, _ := tenant.FromContext(ctx)
	var setup domain.PaySetup
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var profileID, agreementID *string
		err := tx.QueryRow(ctx, `select profile_id::text, pay_agreement_id::text from worker where tenant_id = $1 and id = $2`, p.TenantID, workerID).Scan(&profileID, &agreementID)
		if errors.Is(err, pgx.ErrNoRows) {
			return workerNotFound()
		}
		if err != nil {
			return err
		}
		if profileID != nil {
			days, err := loadProfileDays(ctx, tx, *profileID)
			if err != nil {
				return err
			}
			set, bad := domain.ProfileFromDays(days[*profileID])
			if len(bad) > 0 {
				return errors.New("stored time profile is invalid")
			}
			setup.Profiles = set
		}
		if agreementID != nil {
			a, err := scanAgreement(tx.QueryRow(ctx, `select `+agreementColumns+` from pay_agreement where tenant_id = $1 and id = $2`, p.TenantID, *agreementID))
			if err != nil {
				return err
			}
			prem, bad := domain.ShiftPremiums(a.ShiftPremiums)
			if len(bad) > 0 {
				return errors.New("stored pay agreement has invalid shift premiums")
			}
			setup.Rules = &domain.Rules{DailyThreshold: a.DailyThreshold, WeeklyThreshold: a.WeeklyThreshold, OvertimeMultiplier: a.OvertimeMultiplier,
				SundayMultiplier: a.SundayMultiplier, WeekStart: time.Weekday(a.WeekStart), ShiftPremiums: prem}
		}
		return nil
	})
	return setup, err
}
