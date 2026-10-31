package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// Problem codes of scheduling-agreement releases.
const (
	CodeInvalidRelease   = "SLS-0026"
	CodeCumMismatch      = "SLS-0014"
	CodeReleaseDuplicate = "SLS-0038"
	CodeReleaseNotHeld   = "SLS-0039"
	CodeShippedRefused   = "SLS-0040"
	CodeReleaseNotFound  = "SLS-0110"

	cumMismatchReason = "CUM_QTY_MISMATCH"
)

// ReleaseBucketRecord is one requirement date of a stored release with the
// cumulative quantity required up to and including it.
type ReleaseBucketRecord struct {
	Date        string          `json:"date"`
	CumRequired decimal.Decimal `json:"cumRequired"`
}

// ScheduleReleaseLine is an open quantity due on a date from a stored release.
type ScheduleReleaseLine struct {
	ID       string          `json:"id"`
	DueDate  string          `json:"dueDate"`
	Quantity decimal.Decimal `json:"quantity"`
	Status   string          `json:"status"`
}

// ScheduleRelease is a stored scheduling-agreement release (FR-27.20). A HELD
// release changed nothing and waits for a reviewer; an ACCEPTED one owns the
// agreement's open schedule lines until a newer release replaces them.
type ScheduleRelease struct {
	ID              string          `json:"id"`
	AgreementID     string          `json:"agreementId"`
	AgreementNumber string          `json:"agreementNumber"`
	ReleaseNo       string          `json:"releaseNo"`
	ReleaseType     string          `json:"releaseType"`
	Status          string          `json:"status"`
	CumReceived     decimal.Decimal `json:"cumReceived"`
	CumShipped      decimal.Decimal `json:"cumShipped"`
	// ShippedOffset is shipped quantity from earlier cumulative periods that
	// still counts against this release's figures (added when the agreement's
	// reset date moves forward); CumShipped includes it.
	ShippedOffset decimal.Decimal       `json:"shippedOffset"`
	Tolerance     decimal.Decimal       `json:"tolerance"`
	Difference    decimal.Decimal       `json:"difference"`
	OpenQty       decimal.Decimal       `json:"openQuantity"`
	Code          string                `json:"code,omitempty"`
	ServiceCode   string                `json:"serviceCode,omitempty"`
	Buckets       []ReleaseBucketRecord `json:"buckets"`
	Lines         []ScheduleReleaseLine `json:"lines,omitempty"`
	ReceivedBy    string                `json:"receivedBy"`
	ReceivedAt    time.Time             `json:"receivedAt"`
	ResolvedBy    *string               `json:"resolvedBy,omitempty"`
	ResolvedAt    *time.Time            `json:"resolvedAt,omitempty"`
	Reason        *string               `json:"resolutionReason,omitempty"`
	Version       int                   `json:"version"`
}

// NewScheduleRelease is a validated release received against an agreement.
type NewScheduleRelease struct {
	AgreementID    string
	ReleaseNo      string
	ReleaseType    string
	CumReceived    decimal.Decimal
	Buckets        []domain.ReleaseBucket
	IdempotencyKey string
	RequestHash    string
	Actor          string
}

func releaseNotFound() error {
	return problem(http.StatusNotFound, CodeReleaseNotFound, "schedule release not found")
}

const releaseColumns = `r.id::text, r.agreement_id::text, a.agreement_number, r.release_no, r.release_type, r.status, r.cum_received::text,
	r.cum_shipped::text, r.tolerance::text, r.difference::text, r.open_qty::text, r.buckets, r.received_by, r.received_at,
	r.resolved_by, r.resolved_at, r.resolution_reason, r.version, r.shipped_offset::text`

func scanRelease(row pgx.Row) (ScheduleRelease, error) {
	var r ScheduleRelease
	var recv, shipped, tol, diff, open, offset string
	var buckets []byte
	if err := row.Scan(&r.ID, &r.AgreementID, &r.AgreementNumber, &r.ReleaseNo, &r.ReleaseType, &r.Status, &recv, &shipped, &tol, &diff,
		&open, &buckets, &r.ReceivedBy, &r.ReceivedAt, &r.ResolvedBy, &r.ResolvedAt, &r.Reason, &r.Version, &offset); err != nil {
		return ScheduleRelease{}, err
	}
	r.ShippedOffset = decimal.RequireFromString(offset)
	r.CumReceived = decimal.RequireFromString(recv)
	r.CumShipped = decimal.RequireFromString(shipped)
	r.Tolerance = decimal.RequireFromString(tol)
	r.Difference = decimal.RequireFromString(diff)
	r.OpenQty = decimal.RequireFromString(open)
	if err := json.Unmarshal(buckets, &r.Buckets); err != nil {
		return ScheduleRelease{}, err
	}
	if r.Status == domain.ReleaseHeld {
		r.Code, r.ServiceCode = cumMismatchReason, CodeCumMismatch
	}
	return r, nil
}

const releaseFrom = ` from schedule_release r join sales_agreement a on a.tenant_id = r.tenant_id and a.id = r.agreement_id `

func loadRelease(ctx context.Context, tx pgx.Tx, tenantID, agreementID, id string, lock bool) (ScheduleRelease, error) {
	q := `select ` + releaseColumns + releaseFrom + `where r.tenant_id = $1 and r.agreement_id = $2 and r.id = $3`
	if lock {
		q += " for update of r"
	}
	r, err := scanRelease(tx.QueryRow(ctx, q, tenantID, agreementID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ScheduleRelease{}, releaseNotFound()
	}
	return r, err
}

func loadReleaseLines(ctx context.Context, tx pgx.Tx, tenantID, releaseID string) ([]ScheduleReleaseLine, error) {
	rows, err := tx.Query(ctx, `select id::text, due_date::text, quantity::text, status from schedule_release_line
		where tenant_id = $1 and release_id = $2 order by due_date, id`, tenantID, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScheduleReleaseLine{}
	for rows.Next() {
		var l ScheduleReleaseLine
		var q string
		if err := rows.Scan(&l.ID, &l.DueDate, &q, &l.Status); err != nil {
			return nil, err
		}
		l.Quantity = decimal.RequireFromString(q)
		out = append(out, l)
	}
	return out, rows.Err()
}

func bucketRecords(in []domain.ReleaseBucket) []ReleaseBucketRecord {
	out := make([]ReleaseBucketRecord, len(in))
	for i, b := range in {
		out[i] = ReleaseBucketRecord{Date: b.Date, CumRequired: b.CumRequired}
	}
	return out
}

func domainBuckets(in []ReleaseBucketRecord) []domain.ReleaseBucket {
	out := make([]domain.ReleaseBucket, len(in))
	for i, b := range in {
		out[i] = domain.ReleaseBucket{Date: b.Date, CumRequired: b.CumRequired}
	}
	return out
}

func eventLines(lines []domain.ReleaseLine) []salesevents.ScheduleReleaseLine {
	out := make([]salesevents.ScheduleReleaseLine, len(lines))
	for i, l := range lines {
		out[i] = salesevents.ScheduleReleaseLine{Date: l.Date, Quantity: l.Quantity.String()}
	}
	return out
}

func releaseEvent(a Agreement, r ScheduleRelease, lines []domain.ReleaseLine, actor string) salesevents.ScheduleReleaseData {
	return salesevents.ScheduleReleaseData{ReleaseID: r.ID, ReleaseNo: r.ReleaseNo, ReleaseType: r.ReleaseType, LegalEntityID: a.LegalEntityID, AgreementID: a.ID,
		AgreementNumber: a.AgreementNumber, CustomerID: a.CustomerID, ProductID: a.ProductID, Status: r.Status,
		CumReceived: r.CumReceived.String(), CumShipped: r.CumShipped.String(), Difference: r.Difference.String(),
		OpenQty: r.OpenQty.String(), Lines: eventLines(lines), Actor: actor}
}

// supersedeEffective retires the agreement's effective release and its open
// lines, returning the retired release id ("" when none was effective).
func supersedeEffective(ctx context.Context, tx pgx.Tx, tenantID, agreementID string) (string, error) {
	var prior string
	err := tx.QueryRow(ctx, `update schedule_release set status = 'SUPERSEDED', version = version + 1
		where tenant_id = $1 and agreement_id = $2 and status = 'ACCEPTED' returning id::text`, tenantID, agreementID).Scan(&prior)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `update schedule_release_line set status = 'SUPERSEDED' where tenant_id = $1 and agreement_id = $2 and status = 'OPEN'`,
		tenantID, agreementID)
	return prior, err
}

func insertReleaseLines(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, agreementID, releaseID string, lines []domain.ReleaseLine) error {
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `insert into schedule_release_line (tenant_id, id, legal_entity_id, agreement_id, release_id, due_date, quantity, status)
			values ($1, $2, $3, $4, $5, $6::date, $7::numeric, 'OPEN')`,
			tenantID, ids.New(), legalEntityID, agreementID, releaseID, l.Date, l.Quantity.String()); err != nil {
			return err
		}
	}
	return nil
}

// CreateScheduleRelease stores a release against the agreement under the
// agreement's row lock and reconciles its cumulative received quantity with
// the quantity shipped since the reset (BR-27.12). Within tolerance the
// release is ACCEPTED and replaces the previous release's open schedule lines
// in the same transaction; beyond it the release is stored HELD with
// CUM_QTY_MISMATCH and no schedule line changes (AC-27.13). A repeat of the
// same Idempotency-Key and body returns the stored release with created=false.
func (s *Postgres) CreateScheduleRelease(ctx context.Context, in NewScheduleRelease, now time.Time) (ScheduleRelease, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out ScheduleRelease
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The agreement row lock comes first so that a concurrent repeat of the
		// same key waits for the first request and then replays it.
		a, err := loadAgreement(ctx, tx, p.TenantID, in.AgreementID, true)
		if err != nil {
			return err
		}
		var priorAgreement, priorID, hash string
		err = tx.QueryRow(ctx, `select agreement_id::text, id::text, request_hash from schedule_release where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, in.IdempotencyKey).Scan(&priorAgreement, &priorID, &hash)
		if err == nil {
			if priorAgreement != in.AgreementID || hash != in.RequestHash {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request")
			}
			if out, err = loadRelease(ctx, tx, p.TenantID, priorAgreement, priorID, false); err != nil {
				return err
			}
			out.Lines, err = loadReleaseLines(ctx, tx, p.TenantID, out.ID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		from, err := parseDay(a.ValidFrom)
		if err != nil {
			return err
		}
		to, err := parseDay(a.ValidTo)
		if err != nil {
			return err
		}
		if !domain.AgreementValidOn(from, to, now.UTC().Truncate(24*time.Hour)) {
			return problem(http.StatusUnprocessableEntity, CodeAgreementNotValid, "agreement "+a.AgreementNumber+" is not valid on "+now.UTC().Format(dayLayout))
		}
		var dup bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from schedule_release where tenant_id = $1 and agreement_id = $2 and release_no = $3)`,
			p.TenantID, a.ID, in.ReleaseNo).Scan(&dup); err != nil {
			return err
		}
		if dup {
			return problem(http.StatusConflict, CodeReleaseDuplicate, "release "+in.ReleaseNo+" was already received for agreement "+a.AgreementNumber)
		}
		res, err := domain.CheckRelease(in.CumReceived, a.CumShipped, a.CumTolerance, in.Buckets)
		if err != nil {
			return problem(http.StatusUnprocessableEntity, CodeInvalidRelease, err.Error())
		}
		status := domain.ReleaseAccepted
		if res.Mismatch {
			status = domain.ReleaseHeld
		}
		id := ids.New()
		raw, err := json.Marshal(bucketRecords(in.Buckets))
		if err != nil {
			return err
		}
		superseded := ""
		if status == domain.ReleaseAccepted {
			if superseded, err = supersedeEffective(ctx, tx, p.TenantID, a.ID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `insert into schedule_release (tenant_id, id, legal_entity_id, agreement_id, release_no, release_type, status, cum_received,
				cum_shipped, tolerance, difference, open_qty, buckets, idempotency_key, request_hash, received_by, received_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9::numeric, $10::numeric, $11::numeric, $12::numeric, $13::jsonb, $14, $15, $16, $17)`,
			p.TenantID, id, a.LegalEntityID, a.ID, in.ReleaseNo, in.ReleaseType, status, in.CumReceived.String(), a.CumShipped.String(),
			a.CumTolerance.String(), res.Difference.String(), res.OpenQuantity.String(), string(raw), in.IdempotencyKey, in.RequestHash, in.Actor, now); err != nil {
			return err
		}
		if err := insertReleaseLines(ctx, tx, p.TenantID, a.LegalEntityID, a.ID, id, res.Lines); err != nil {
			return err
		}
		if out, err = loadRelease(ctx, tx, p.TenantID, a.ID, id, false); err != nil {
			return err
		}
		ev := releaseEvent(a, out, res.Lines, in.Actor)
		ev.SupersededReleaseID = superseded
		if err := enqueue(ctx, tx, "schedulerelease", id, salesevents.ScheduleReleaseReceived, p.TenantID, ev); err != nil {
			return err
		}
		created = true
		out.Lines, err = loadReleaseLines(ctx, tx, p.TenantID, id)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return ScheduleRelease{}, false, problem(http.StatusConflict, CodeDuplicateIntake, "a concurrent request stored this release first; retry to receive it")
	}
	if err != nil {
		return ScheduleRelease{}, false, err
	}
	return out, created, nil
}

// ListScheduleReleases returns the agreement's releases, newest first, without
// lines.
func (s *Postgres) ListScheduleReleases(ctx context.Context, agreementID string, limit int) ([]ScheduleRelease, error) {
	p, _ := tenant.FromContext(ctx)
	out := []ScheduleRelease{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadAgreement(ctx, tx, p.TenantID, agreementID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+releaseColumns+releaseFrom+`where r.tenant_id = $1 and r.agreement_id = $2
			order by r.received_at desc, r.id desc limit $3`, p.TenantID, agreementID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRelease(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetScheduleRelease returns one release with its schedule lines (open and
// superseded).
func (s *Postgres) GetScheduleRelease(ctx context.Context, agreementID, id string) (ScheduleRelease, error) {
	p, _ := tenant.FromContext(ctx)
	var out ScheduleRelease
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if out, err = loadRelease(ctx, tx, p.TenantID, agreementID, id, false); err != nil {
			return err
		}
		out.Lines, err = loadReleaseLines(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// ResolveScheduleRelease decides a HELD release at its version. ACCEPT is the
// reviewer's override of the cumulative mismatch: the release replaces the
// previous release's open lines, with open quantity computed against the
// agreement's current shipped quantity; it is refused when a newer release was
// accepted meanwhile. REJECT closes it with the reason and changes nothing.
func (s *Postgres) ResolveScheduleRelease(ctx context.Context, agreementID, id string, version int, decision, reason, actor string, now time.Time) (ScheduleRelease, error) {
	p, _ := tenant.FromContext(ctx)
	var out ScheduleRelease
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		a, err := loadAgreement(ctx, tx, p.TenantID, agreementID, true)
		if err != nil {
			return err
		}
		r, err := loadRelease(ctx, tx, p.TenantID, agreementID, id, true)
		if err != nil {
			return err
		}
		if r.Version != version {
			return versionConflict()
		}
		if r.Status != domain.ReleaseHeld {
			return problem(http.StatusUnprocessableEntity, CodeReleaseNotHeld, "only a HELD release can be resolved; release "+r.ReleaseNo+" is "+r.Status)
		}
		var lines []domain.ReleaseLine
		superseded := ""
		next := domain.ReleaseRejected
		if decision == domain.DecisionAccept {
			var newer bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from schedule_release where tenant_id = $1 and agreement_id = $2
				and received_at > $3 and status in ('ACCEPTED', 'SUPERSEDED'))`, p.TenantID, agreementID, r.ReceivedAt).Scan(&newer); err != nil {
				return err
			}
			if newer {
				return problem(http.StatusUnprocessableEntity, CodeReleaseNotHeld, "a newer release of agreement "+a.AgreementNumber+" is in effect; reject this one")
			}
			// A release held across a reset keeps its figures on its own basis:
			// the shipped quantity of the closed period is in its offset.
			shipped := r.ShippedOffset.Add(a.CumShipped)
			res, err := domain.OpenReleaseLines(shipped, domainBuckets(r.Buckets))
			if err != nil {
				return problem(http.StatusUnprocessableEntity, CodeInvalidRelease, err.Error())
			}
			lines = res.Lines
			next = domain.ReleaseAccepted
			if superseded, err = supersedeEffective(ctx, tx, p.TenantID, agreementID); err != nil {
				return err
			}
			if err := insertReleaseLines(ctx, tx, p.TenantID, a.LegalEntityID, agreementID, id, lines); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update schedule_release set cum_shipped = $4::numeric, open_qty = $5::numeric
				where tenant_id = $1 and agreement_id = $2 and id = $3`, p.TenantID, agreementID, id, shipped.String(), res.OpenQuantity.String()); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update schedule_release set status = $4, resolved_by = $5, resolved_at = $6, resolution_reason = $7, version = version + 1
			where tenant_id = $1 and agreement_id = $2 and id = $3`, p.TenantID, agreementID, id, next, actor, now, reason); err != nil {
			return err
		}
		if out, err = loadRelease(ctx, tx, p.TenantID, agreementID, id, false); err != nil {
			return err
		}
		ev := releaseEvent(a, out, lines, actor)
		ev.SupersededReleaseID, ev.Decision, ev.Reason = superseded, decision, reason
		if err := enqueue(ctx, tx, "schedulerelease", id, salesevents.ScheduleReleaseResolved, p.TenantID, ev); err != nil {
			return err
		}
		out.Lines, err = loadReleaseLines(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// RecordShipped sets the agreement's cumulative shipped quantity at its
// version (wave 28 shipments will drive this). The quantity may fall only when
// the reset date moves forward; such a reset carries the closed period's
// shipped quantity into the offset of the releases received in it. When a
// release is in effect its open schedule lines are recalculated (cumulative
// required less offset and shipped) in the same transaction and
// schedulerelease.recalculated is written.
func (s *Postgres) RecordShipped(ctx context.Context, agreementID string, version int, cumShipped decimal.Decimal, resetDate *string, actor string, now time.Time) (Agreement, error) {
	p, _ := tenant.FromContext(ctx)
	var out Agreement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		a, err := loadAgreement(ctx, tx, p.TenantID, agreementID, true)
		if err != nil {
			return err
		}
		if a.Version != version {
			return versionConflict()
		}
		if err := domain.CheckShippedUpdate(a.CumShipped, cumShipped, a.CumResetDate, resetDate); err != nil {
			return problem(http.StatusUnprocessableEntity, CodeShippedRefused, err.Error())
		}
		reset := a.CumResetDate
		if resetDate != nil {
			reset = resetDate
		}
		// A later reset date closes the cumulative period. Releases received in
		// it (the one in effect and any still HELD) keep their cumulative
		// figures, so the period's shipped quantity moves into their offset and
		// what was already shipped against them is not reopened.
		if resetDate != nil && (a.CumResetDate == nil || *resetDate > *a.CumResetDate) && a.CumShipped.IsPositive() {
			if _, err := tx.Exec(ctx, `update schedule_release set shipped_offset = shipped_offset + $3::numeric
				where tenant_id = $1 and agreement_id = $2 and status in ('ACCEPTED', 'HELD')`, p.TenantID, agreementID, a.CumShipped.String()); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update sales_agreement set cum_shipped = $3::numeric, cum_reset_date = $4::date, version = version + 1, updated_at = $5
			where tenant_id = $1 and id = $2`, p.TenantID, agreementID, cumShipped.String(), reset, now); err != nil {
			return err
		}
		var effective string
		err = tx.QueryRow(ctx, `select id::text from schedule_release where tenant_id = $1 and agreement_id = $2 and status = 'ACCEPTED' for update`,
			p.TenantID, agreementID).Scan(&effective)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			r, err := loadRelease(ctx, tx, p.TenantID, agreementID, effective, false)
			if err != nil {
				return err
			}
			shipped := r.ShippedOffset.Add(cumShipped)
			res, err := domain.OpenReleaseLines(shipped, domainBuckets(r.Buckets))
			if err != nil {
				return problem(http.StatusUnprocessableEntity, CodeInvalidRelease, err.Error())
			}
			if _, err := tx.Exec(ctx, `update schedule_release_line set status = 'SUPERSEDED' where tenant_id = $1 and release_id = $2 and status = 'OPEN'`,
				p.TenantID, effective); err != nil {
				return err
			}
			if err := insertReleaseLines(ctx, tx, p.TenantID, a.LegalEntityID, agreementID, effective, res.Lines); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update schedule_release set cum_shipped = $3::numeric, open_qty = $4::numeric, version = version + 1
				where tenant_id = $1 and id = $2`, p.TenantID, effective, shipped.String(), res.OpenQuantity.String()); err != nil {
				return err
			}
			if r, err = loadRelease(ctx, tx, p.TenantID, agreementID, effective, false); err != nil {
				return err
			}
			if err := enqueue(ctx, tx, "schedulerelease", effective, salesevents.ScheduleReleaseRecalculated, p.TenantID, releaseEvent(a, r, res.Lines, actor)); err != nil {
				return err
			}
		}
		out, err = loadAgreement(ctx, tx, p.TenantID, agreementID, false)
		return err
	})
	return out, err
}
