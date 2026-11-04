package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	svcevents "github.com/BenjaminSRussell/passage/services/fieldservice/internal/events"
)

// Problem codes of the agreement lifecycle and visits (FR-45.3).
const (
	CodeAgreementTransition = "SVC-AGR-005"
	CodeAgreementEnded      = "SVC-AGR-006"
	CodeAgreementNotActive  = "SVC-AGR-007"
	CodeVisitOutsideTerm    = "SVC-AGR-008"
	CodeVisitLineUnknown    = "SVC-AGR-009"
	CodeVisitsExhausted     = "SVC-AGR-010"
	CodeVisitWorkOrder      = "SVC-AGR-011"
	CodeVisitWorkOrderUsed  = "SVC-AGR-012"
)

// reasonExpired is the status reason the expiry pass records.
const reasonExpired = "validTo passed"

// AgreementChange is one row of an agreement's status history.
type AgreementChange struct {
	From   string    `json:"from"`
	To     string    `json:"to"`
	Reason string    `json:"reason"`
	Actor  string    `json:"actor"`
	At     time.Time `json:"at"`
}

// AgreementVisit is one included visit consumed from an agreement line.
type AgreementVisit struct {
	ID          string    `json:"id"`
	AgreementID string    `json:"agreementId"`
	LineIndex   int       `json:"lineIndex"`
	Kind        string    `json:"kind"`
	VisitDate   string    `json:"visitDate"`
	WorkOrderID *string   `json:"workOrderId"`
	Note        string    `json:"note"`
	RecordedBy  string    `json:"recordedBy"`
	CreatedAt   time.Time `json:"createdAt"`
	// VisitsLeft is what remained of the line's includedVisits once this visit
	// was recorded.
	VisitsLeft int `json:"visitsLeft"`
}

// VisitInput is a visit to record against an agreement line.
type VisitInput struct {
	AgreementID string
	LineIndex   int
	Kind        string
	VisitDate   string
	WorkOrderID *string
	Note        string
}

// ExpireResult is the outcome of one expiry pass.
type ExpireResult struct {
	Scanned int `json:"scanned"`
	Expired int `json:"expired"`
}

// agreementNotFound maps the "unknown reference" 422 of getAgreement to the 404
// of a request that addresses the agreement by its path.
func agreementNotFound(err error) error {
	var p *httpx.Problem
	if errors.As(err, &p) && p.Code == CodeAgreementUnknown {
		return httpx.NewProblem(http.StatusNotFound, CodeAgreementNotFound, "service agreement not found")
	}
	return err
}

// changeAgreementStatus writes one status change: the row, its history, and the
// agreement.changed event, all in the caller's transaction.
func changeAgreementStatus(ctx context.Context, tx pgx.Tx, tid string, cur Agreement, to, reason string, now time.Time) error {
	if _, err := tx.Exec(ctx, `update service_agreement set status = $3, status_reason = $4, version = version + 1, updated_at = $5
		where tenant_id = $1 and id = $2`, tid, cur.ID, to, nullable(reason), now); err != nil {
		return err
	}
	actor := caller(ctx).UserID
	if _, err := tx.Exec(ctx, `insert into service_agreement_status_log (tenant_id, id, agreement_id, legal_entity_id, from_status, to_status, reason, actor, at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, tid, ids.New(), cur.ID, cur.LegalEntityID, cur.Status, to, nullable(reason), actor, now); err != nil {
		return err
	}
	return enqueue(ctx, tx, "agreement", cur.ID, svcevents.AgreementChanged, tid, svcevents.AgreementChangedData{
		AgreementID: cur.ID, Number: cur.Number, CustomerPartyID: cur.CustomerPartyID, LegalEntityID: cur.LegalEntityID,
		BillingModel: cur.BillingModel, From: cur.Status, To: to, Reason: reason, ValidTo: cur.ValidTo, At: now.UTC().Format(time.RFC3339), Actor: actor})
}

// TransitionAgreement moves an agreement along DRAFT, ACTIVE, SUSPENDED and
// CANCELED (FR-45.3). EXPIRED is reached only by ExpireAgreements. The version
// must match (412 otherwise). Activating or resuming an agreement whose validTo
// is already behind the day is refused (SVC-AGR-006). A retry carrying the same
// Idempotency-Key gets the first answer instead of a 412.
func (s *Postgres) TransitionAgreement(ctx context.Context, id string, version int, to, reason string, now time.Time, key, requestHash string) (Agreement, bool, error) {
	tid := tenantOf(ctx)
	var out Agreement
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := getAgreement(ctx, tx, tid, id, true)
		if err != nil {
			return agreementNotFound(err)
		}
		if found, err := keyedAnswer(ctx, tx, tid, key, requestHash, &out); err != nil || found {
			replayed = found
			return err
		}
		if cur.Version != version {
			return versionConflict(cur.Version)
		}
		if to == domain.AgreementExpired || !domain.AgreementCanMove(cur.Status, to) {
			return unprocessable(CodeAgreementTransition, fmt.Sprintf("an agreement cannot move from %s to %s", cur.Status, to))
		}
		if to == domain.AgreementActive && domain.AgreementEnded(cur.ValidTo, now.UTC().Format(dateLayout)) {
			return unprocessable(CodeAgreementEnded, "the agreement's validTo is already behind today")
		}
		if err := changeAgreementStatus(ctx, tx, tid, cur, to, reason, now); err != nil {
			return err
		}
		if out, err = getAgreement(ctx, tx, tid, id, false); err != nil {
			return err
		}
		return storeAnswer(ctx, tx, tid, key, requestHash, out)
	})
	return out, replayed, err
}

// ExpireAgreements expires up to limit ACTIVE or SUSPENDED agreements whose
// validTo is behind the day of now. It is safe to repeat and to overlap: an
// expired agreement is no longer selected and SKIP LOCKED passes the rest on.
func (s *Postgres) ExpireAgreements(ctx context.Context, now time.Time, limit int) (ExpireResult, error) {
	tid := tenantOf(ctx)
	var res ExpireResult
	day := now.UTC().Format(dateLayout)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select a.id::text from service_agreement a
			where a.tenant_id = $1 and a.status in ('ACTIVE', 'SUSPENDED') and a.valid_to < $2::date
			order by a.valid_to, a.id limit $3 for update of a skip locked`, tid, day, limit)
		if err != nil {
			return err
		}
		var pending []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			pending = append(pending, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range pending {
			res.Scanned++
			cur, err := getAgreement(ctx, tx, tid, id, false)
			if err != nil {
				return err
			}
			if err := changeAgreementStatus(ctx, tx, tid, cur, domain.AgreementExpired, reasonExpired, now); err != nil {
				return err
			}
			res.Expired++
		}
		return nil
	})
	return res, err
}

// AgreementHistory lists the status changes of an agreement, oldest first.
func (s *Postgres) AgreementHistory(ctx context.Context, id string) ([]AgreementChange, error) {
	tid := tenantOf(ctx)
	out := []AgreementChange{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := getAgreement(ctx, tx, tid, id, false); err != nil {
			return agreementNotFound(err)
		}
		rows, err := tx.Query(ctx, `select from_status, to_status, coalesce(reason, ''), actor, at from service_agreement_status_log
			where tenant_id = $1 and agreement_id = $2 order by at, id`, tid, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c AgreementChange
			if err := rows.Scan(&c.From, &c.To, &c.Reason, &c.Actor, &c.At); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// RecordVisit consumes one included visit of an agreement line (FR-45.3). The
// agreement must be ACTIVE and cover the visit date; the line must still have a
// visit left (SVC-AGR-010). A visit tied to a work order requires that order to
// belong to the agreement and consumes at most one visit. The agreement row is
// locked, so concurrent visits cannot both take the last one.
func (s *Postgres) RecordVisit(ctx context.Context, in VisitInput, key, requestHash string) (AgreementVisit, bool, error) {
	tid := tenantOf(ctx)
	var out AgreementVisit
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		agr, err := getAgreement(ctx, tx, tid, in.AgreementID, true)
		if err != nil {
			return agreementNotFound(err)
		}
		id, found, err := replayOf(ctx, tx, "service_agreement_visit", tid, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = getVisit(ctx, tx, tid, id)
			return err
		}
		if agr.Status != domain.AgreementActive {
			return unprocessable(CodeAgreementNotActive, "only an ACTIVE agreement takes visits")
		}
		if !domain.AgreementCovers(agr.ValidFrom, agr.ValidTo, in.VisitDate) {
			return unprocessable(CodeVisitOutsideTerm, "the visit date is outside the agreement's validity")
		}
		if in.LineIndex < 0 || in.LineIndex >= len(agr.Lines) {
			return unprocessable(CodeVisitLineUnknown, "the agreement has no such line")
		}
		var used int
		if err := tx.QueryRow(ctx, `select count(*)::int from service_agreement_visit where tenant_id = $1 and agreement_id = $2 and line_index = $3`,
			tid, in.AgreementID, in.LineIndex).Scan(&used); err != nil {
			return err
		}
		line := agr.Lines[in.LineIndex]
		if !domain.VisitIncluded(line, used) {
			return unprocessable(CodeVisitsExhausted, fmt.Sprintf("the line includes %d visits and all are used", line.IncludedVisits))
		}
		if in.WorkOrderID != nil {
			var woAgreement *string
			err := tx.QueryRow(ctx, `select agreement_id::text from service_work_order where tenant_id = $1 and id = $2`, tid, *in.WorkOrderID).Scan(&woAgreement)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && (woAgreement == nil || *woAgreement != in.AgreementID) {
				return unprocessable(CodeVisitWorkOrder, "the work order does not belong to this agreement")
			}
			if err != nil {
				return err
			}
			var taken bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from service_agreement_visit where tenant_id = $1 and work_order_id = $2)`, tid, *in.WorkOrderID).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return unprocessable(CodeVisitWorkOrderUsed, "the work order already consumed a visit")
			}
		}
		out = AgreementVisit{ID: ids.New(), AgreementID: in.AgreementID, LineIndex: in.LineIndex, Kind: in.Kind, VisitDate: in.VisitDate,
			WorkOrderID: in.WorkOrderID, Note: in.Note, RecordedBy: caller(ctx).UserID, VisitsLeft: line.IncludedVisits - used - 1}
		if err := tx.QueryRow(ctx, `insert into service_agreement_visit (tenant_id, id, agreement_id, legal_entity_id, line_index, kind, visit_date,
			work_order_id, note, visits_left, recorded_by, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7::date, $8, $9, $10, $11, $12, $13) returning created_at`,
			tid, out.ID, in.AgreementID, agr.LegalEntityID, in.LineIndex, in.Kind, in.VisitDate, in.WorkOrderID, nullable(in.Note), out.VisitsLeft,
			out.RecordedBy, nullable(key), requestHash).Scan(&out.CreatedAt); err != nil {
			return err
		}
		return enqueue(ctx, tx, "agreement", in.AgreementID, svcevents.AgreementVisitRecorded, tid, svcevents.AgreementVisitRecordedData{
			VisitID: out.ID, AgreementID: in.AgreementID, Number: agr.Number, CustomerPartyID: agr.CustomerPartyID, LegalEntityID: agr.LegalEntityID,
			BillingModel: agr.BillingModel, LineIndex: in.LineIndex, Kind: in.Kind, VisitDate: in.VisitDate, WorkOrderID: in.WorkOrderID,
			VisitsLeft: out.VisitsLeft})
	})
	if err != nil && isUnique(err) {
		return out, false, unprocessable(CodeVisitWorkOrderUsed, "the work order already consumed a visit")
	}
	return out, replayed, err
}

const visitColumns = `v.id::text, v.agreement_id::text, v.line_index, v.kind, v.visit_date::text, v.work_order_id::text, coalesce(v.note, ''),
	v.visits_left, v.recorded_by, v.created_at`

func scanVisit(row pgx.Row) (AgreementVisit, error) {
	var v AgreementVisit
	err := row.Scan(&v.ID, &v.AgreementID, &v.LineIndex, &v.Kind, &v.VisitDate, &v.WorkOrderID, &v.Note, &v.VisitsLeft, &v.RecordedBy, &v.CreatedAt)
	return v, err
}

func getVisit(ctx context.Context, tx pgx.Tx, tid, id string) (AgreementVisit, error) {
	return scanVisit(tx.QueryRow(ctx, `select `+visitColumns+` from service_agreement_visit v where v.tenant_id = $1 and v.id = $2`, tid, id))
}

// ListVisits returns the visits of an agreement, oldest first.
func (s *Postgres) ListVisits(ctx context.Context, agreementID string) ([]AgreementVisit, error) {
	tid := tenantOf(ctx)
	out := []AgreementVisit{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := getAgreement(ctx, tx, tid, agreementID, false); err != nil {
			return agreementNotFound(err)
		}
		rows, err := tx.Query(ctx, `select `+visitColumns+` from service_agreement_visit v where v.tenant_id = $1 and v.agreement_id = $2
			order by v.visit_date, v.created_at, v.id`, tid, agreementID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanVisit(rows)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}
