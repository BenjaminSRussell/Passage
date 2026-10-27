package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	tmsevents "github.com/BenjaminSRussell/passage/services/transport/internal/events"
)

const gateVisitColumns = `id, site_id, direction, load_id, carrier_scac, trailer_no, seal_no, driver_name, driver_license_id,
	appointment_id, scheduled_at, arrival_status, arrival_delta_min, checked_in_at, checked_out_at, free_time_min, status,
	dwell_min, detention_min, detention_hours, detention_charge::text, version, request_hash, checkout_idempotency_key, checkout_request_hash`

const gateEventColumns = `id, visit_id, kind, occurred_at, recorded_by, detail`

// GateVisitFilter narrows the gate log list (site, date, carrier, §6.34.14).
// Date is a UTC check-in day as YYYY-MM-DD. Empty fields match everything.
type GateVisitFilter struct {
	SiteID      string
	Status      string
	CarrierSCAC string
	Date        string
}

// gateVisitRow is a scanned visit with its idempotency fingerprints.
type gateVisitRow struct {
	visit       domain.GateVisit
	hash        string
	checkoutKey string
	checkoutFP  string
}

// CheckIn opens a visit, appends the CHECK_IN row to the gate log and writes
// gate.checkedin to the outbox, in one transaction. Check-ins of one trailer at
// one site are serialised, so a trailer is in the yard once (TMS-0118). A
// repeat with the same Idempotency-Key and body returns the first visit.
func (s *Postgres) CheckIn(ctx context.Context, in domain.GateVisit, idem Idem) (domain.GateVisit, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.GateVisit
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay := func() (bool, error) {
			if idem.Key == "" {
				return false, nil
			}
			row, err := scanGateVisit(tx.QueryRow(ctx, `select `+gateVisitColumns+` from gate_visit where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idem.Key))
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if row.hash != idem.Fingerprint {
				return false, reuse()
			}
			out = row.visit
			return true, nil
		}
		if done, err := replay(); done || err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('transport.gate:' || $1 || ':' || $2 || ':' || $3 || ':' || $4))`,
			p.TenantID, in.SiteID, in.CarrierSCAC, in.TrailerNo); err != nil {
			return err
		}
		// A concurrent request with the same key may have committed while this one waited.
		if done, err := replay(); done || err != nil {
			return err
		}
		var inYard string
		err := tx.QueryRow(ctx, `select id from gate_visit where tenant_id = $1 and site_id = $2 and carrier_scac = $3 and trailer_no = $4
			and status = 'IN_YARD'`, p.TenantID, in.SiteID, in.CarrierSCAC, in.TrailerNo).Scan(&inYard)
		if err == nil {
			return httpx.NewProblem(http.StatusConflict, domain.CodeVisitState,
				"trailer "+in.TrailerNo+" of "+in.CarrierSCAC+" is already in the yard on visit "+inYard+"; check it out first")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var status *string
		var delta *int
		if in.Arrival != nil {
			status, delta = &in.Arrival.Status, &in.Arrival.DeltaMin
		}
		row, err := scanGateVisit(tx.QueryRow(ctx, `insert into gate_visit (tenant_id, id, site_id, direction, load_id, carrier_scac, trailer_no,
			seal_no, driver_name, driver_license_id, appointment_id, scheduled_at, arrival_status, arrival_delta_min, checked_in_at,
			free_time_min, status, idempotency_key, request_hash, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
			returning `+gateVisitColumns,
			p.TenantID, ids.New(), in.SiteID, in.Direction, nullable(in.LoadID), in.CarrierSCAC, in.TrailerNo, nullable(in.SealNo),
			in.DriverName, in.DriverLicenseID, nullable(in.AppointmentID), in.ScheduledAt, status, delta, in.CheckedInAt,
			in.FreeTimeMin, domain.VisitInYard, nullable(idem.Key), nullable(idem.Fingerprint), p.UserID))
		if isUnique(err) {
			return httpx.NewProblem(http.StatusConflict, domain.CodeVisitState, "trailer "+in.TrailerNo+" of "+in.CarrierSCAC+" is already in the yard")
		}
		if err != nil {
			return err
		}
		out = row.visit
		detail := map[string]string{"direction": out.Direction, "trailerNo": out.TrailerNo, "carrierScac": out.CarrierSCAC}
		if out.Arrival != nil {
			detail["arrival"] = out.Arrival.Status
			detail["deltaMin"] = strconv.Itoa(out.Arrival.DeltaMin)
		} else {
			detail["arrival"] = "WALK_IN"
		}
		if out.SealNo != "" {
			detail["sealNo"] = out.SealNo
		}
		if err := appendGateEvent(ctx, tx, p, out, domain.GateLogCheckIn, out.CheckedInAt, detail); err != nil {
			return err
		}
		data := tmsevents.GateCheckedInPayload{VisitID: out.ID, SiteID: out.SiteID, Direction: out.Direction, LoadID: out.LoadID,
			CarrierSCAC: out.CarrierSCAC, TrailerNo: out.TrailerNo, AppointmentID: out.AppointmentID,
			CheckedInAt: out.CheckedInAt.Format(time.RFC3339Nano)}
		if out.Arrival != nil {
			data.ArrivalStatus, data.ArrivalDelta = out.Arrival.Status, out.Arrival.DeltaMin
		}
		evt, err := events.New(tmsevents.GateCheckedIn, eventSource, out.ID, p.TenantID, data)
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "gate", out.ID, evt)
	})
	return out, err
}

// RecordSeal records the seal applied at load confirmation on a visit in the
// yard and appends a SEALED row to the gate log.
func (s *Postgres) RecordSeal(ctx context.Context, id, seal string, now time.Time) (domain.GateVisit, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.GateVisit
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := scanGateVisit(tx.QueryRow(ctx, `select `+gateVisitColumns+` from gate_visit where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return NotFound("gate visit")
		}
		if err != nil {
			return err
		}
		sealed, err := row.visit.RecordSeal(seal)
		if err != nil {
			return ruleProblem(err)
		}
		previous := row.visit.SealNo
		row, err = scanGateVisit(tx.QueryRow(ctx, `update gate_visit set seal_no = $3, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+gateVisitColumns, p.TenantID, id, sealed.SealNo))
		if err != nil {
			return err
		}
		out = row.visit
		detail := map[string]string{"sealNo": out.SealNo}
		if previous != "" {
			detail["replacedSealNo"] = previous
		}
		return appendGateEvent(ctx, tx, p, out, domain.GateLogSealed, now, detail)
	})
	return out, err
}

// CheckOut closes a visit under a row lock (BR-34.13, BR-34.14). A seal that
// does not match is logged as CHECK_OUT_BLOCKED and that row commits; the
// TMS-0129 refusal is returned after the commit, so the gate log keeps the
// attempt (AC-34.12). A repeat with the check-out's Idempotency-Key and body
// returns the checked-out visit.
func (s *Postgres) CheckOut(ctx context.Context, id, declaredSeal string, at time.Time, hourlyRate decimal.Decimal, idem Idem) (domain.GateVisit, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.GateVisit
	var refusal error
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := scanGateVisit(tx.QueryRow(ctx, `select `+gateVisitColumns+` from gate_visit where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return NotFound("gate visit")
		}
		if err != nil {
			return err
		}
		if idem.Key != "" && row.checkoutKey == idem.Key {
			if row.checkoutFP != idem.Fingerprint {
				return reuse()
			}
			out = row.visit
			return nil
		}
		done, rerr := row.visit.CheckOut(declaredSeal, at, hourlyRate)
		var re *domain.Error
		switch {
		case errors.Is(rerr, domain.ErrCheckOutBeforeCheckIn):
			bad := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
			bad.Errors = map[string]string{"checkOutAt": "must not be before the check-in at " + row.visit.CheckedInAt.Format(time.RFC3339)}
			return bad
		case errors.As(rerr, &re) && re.Code == domain.CodeSealMismatch:
			if err := appendGateEvent(ctx, tx, p, row.visit, domain.GateLogBlocked, at.UTC(),
				map[string]string{"code": re.Code, "declaredSealNo": declaredSeal, "recordedSealNo": row.visit.SealNo}); err != nil {
				return err
			}
			refusal = ruleProblem(rerr)
			return nil
		case rerr != nil:
			return ruleProblem(rerr)
		}
		d := done.Detention
		row, err = scanGateVisit(tx.QueryRow(ctx, `update gate_visit set status = $3, checked_out_at = $4, dwell_min = $5, detention_min = $6,
			detention_hours = $7, detention_charge = $8::numeric, checkout_idempotency_key = $9, checkout_request_hash = $10,
			version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+gateVisitColumns,
			p.TenantID, id, done.Status, *done.CheckedOutAt, d.DwellMin, d.DetentionMin, d.Hours, d.Charge.String(),
			nullable(idem.Key), nullable(idem.Fingerprint)))
		if isUnique(err) {
			return reuse()
		}
		if err != nil {
			return err
		}
		out = row.visit
		detail := map[string]string{"dwellMin": strconv.Itoa(d.DwellMin), "detentionMin": strconv.Itoa(d.DetentionMin),
			"detentionCharge": d.Charge.StringFixed(2)}
		if out.SealNo != "" {
			detail["sealNo"] = out.SealNo
		}
		if d.Code != "" {
			detail["code"] = d.Code
		}
		return appendGateEvent(ctx, tx, p, out, domain.GateLogCheckOut, *out.CheckedOutAt, detail)
	})
	if err != nil {
		return domain.GateVisit{}, err
	}
	if refusal != nil {
		return domain.GateVisit{}, refusal
	}
	return out, nil
}

// GetGateVisit returns a visit with its gate log in the order it was written.
func (s *Postgres) GetGateVisit(ctx context.Context, id string) (domain.GateVisit, []domain.GateLogEntry, error) {
	p, _ := tenant.FromContext(ctx)
	var v domain.GateVisit
	log := []domain.GateLogEntry{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := scanGateVisit(tx.QueryRow(ctx, `select `+gateVisitColumns+` from gate_visit where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return NotFound("gate visit")
		}
		if err != nil {
			return err
		}
		v = row.visit
		rows, err := tx.Query(ctx, `select `+gateEventColumns+` from gate_event where tenant_id = $1 and visit_id = $2
			order by created_at, id`, p.TenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e domain.GateLogEntry
			var raw []byte
			if err := rows.Scan(&e.ID, &e.VisitID, &e.Kind, &e.OccurredAt, &e.RecordedBy, &raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &e.Detail); err != nil {
				return err
			}
			e.OccurredAt = e.OccurredAt.UTC()
			log = append(log, e)
		}
		return rows.Err()
	})
	return v, log, err
}

// ListGateVisits returns the newest check-ins first, at most ListLimit.
func (s *Postgres) ListGateVisits(ctx context.Context, f GateVisitFilter) ([]domain.GateVisit, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.GateVisit{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+gateVisitColumns+` from gate_visit where tenant_id = $1
			and ($2::uuid is null or site_id = $2) and ($3::text = '' or status = $3) and ($4::text = '' or carrier_scac = $4)
			and ($5::date is null or (checked_in_at >= ($5::date)::timestamp at time zone 'UTC'
			  and checked_in_at < ($5::date + 1)::timestamp at time zone 'UTC'))
			order by checked_in_at desc, id limit $6`, p.TenantID, nullable(f.SiteID), f.Status, f.CarrierSCAC, nullable(f.Date), ListLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			row, err := scanGateVisit(rows)
			if err != nil {
				return err
			}
			out = append(out, row.visit)
		}
		return rows.Err()
	})
	return out, err
}

func appendGateEvent(ctx context.Context, tx pgx.Tx, p tenant.Principal, v domain.GateVisit, kind string, at time.Time, detail map[string]string) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into gate_event (tenant_id, id, visit_id, site_id, kind, occurred_at, recorded_by, detail)
		values ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)`, p.TenantID, ids.New(), v.ID, v.SiteID, kind, at, p.UserID, string(raw))
	return err
}

// ruleProblem turns a domain rule rejection into a problem: state conflicts are
// 409, every other rule 422.
func ruleProblem(err error) error {
	var re *domain.Error
	if !errors.As(err, &re) {
		return err
	}
	status := http.StatusUnprocessableEntity
	if re.Code == domain.CodeVisitState || re.Code == domain.CodeGITState {
		status = http.StatusConflict
	}
	return httpx.NewProblem(status, re.Code, re.Detail)
}

func scanGateVisit(row pgx.Row) (gateVisitRow, error) {
	var r gateVisitRow
	v := &r.visit
	var loadID, seal, appointment, arrival, charge, hash, checkoutKey, checkoutFP *string
	var delta, dwell, detMin, detHours *int
	if err := row.Scan(&v.ID, &v.SiteID, &v.Direction, &loadID, &v.CarrierSCAC, &v.TrailerNo, &seal, &v.DriverName, &v.DriverLicenseID,
		&appointment, &v.ScheduledAt, &arrival, &delta, &v.CheckedInAt, &v.CheckedOutAt, &v.FreeTimeMin, &v.Status,
		&dwell, &detMin, &detHours, &charge, &v.Version, &hash, &checkoutKey, &checkoutFP); err != nil {
		return gateVisitRow{}, err
	}
	v.LoadID, v.SealNo, v.AppointmentID = deref(loadID), deref(seal), deref(appointment)
	v.WalkIn = v.AppointmentID == ""
	v.CheckedInAt = v.CheckedInAt.UTC()
	if v.ScheduledAt != nil {
		t := v.ScheduledAt.UTC()
		v.ScheduledAt = &t
	}
	if v.CheckedOutAt != nil {
		t := v.CheckedOutAt.UTC()
		v.CheckedOutAt = &t
	}
	if arrival != nil && delta != nil {
		a := domain.Arrival{Status: *arrival, DeltaMin: *delta}
		if a.Status != domain.ArrivalOnTime {
			a.Code = domain.CodeArrivalOutsideWindow
		}
		v.Arrival = &a
	}
	if dwell != nil && detMin != nil && detHours != nil && charge != nil {
		d := domain.Detention{DwellMin: *dwell, DetentionMin: *detMin, Hours: *detHours, Charge: decimal.RequireFromString(*charge).Round(2)}
		if d.DetentionMin > 0 {
			d.Code = domain.CodeDetention
		}
		v.Detention = &d
	}
	r.hash, r.checkoutKey, r.checkoutFP = deref(hash), deref(checkoutKey), deref(checkoutFP)
	return r, nil
}
