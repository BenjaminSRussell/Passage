package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

const agreementReasonMaxSize = 500

// AgreementRevision is a partial change of an agreement (PATCH). A nil field
// is kept; the empty string clears ValidFrom or ValidTo.
type AgreementRevision struct {
	Commitment   *decimal.Decimal
	UnitPrice    *decimal.Decimal
	CumTolerance *decimal.Decimal
	ValidFrom    *string
	ValidTo      *string
	Reason       string
}

// AgreementVersion is one entry of an agreement's history: a revision, the
// close, or quantity returned by a cancelled order.
type AgreementVersion struct {
	Version   int                  `json:"version"`
	Kind      string               `json:"kind"`
	Actor     string               `json:"actor"`
	Reason    string               `json:"reason"`
	Diff      []domain.FieldChange `json:"diff"`
	ChangedAt time.Time            `json:"changedAt"`
}

func agreementClosed(a Agreement) error {
	return problem(http.StatusUnprocessableEntity, CodeAgreementClosed, "agreement "+a.AgreementNumber+" is closed and takes no more releases or changes")
}

func insertAgreementVersion(ctx context.Context, tx pgx.Tx, tenantID string, a Agreement, version int, kind, actor, reason string, diff []domain.FieldChange, now time.Time) error {
	if diff == nil {
		diff = []domain.FieldChange{}
	}
	raw, err := json.Marshal(diff)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into sales_agreement_version (tenant_id, legal_entity_id, agreement_id, version, kind, actor, reason, diff, changed_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)`, tenantID, a.LegalEntityID, a.ID, version, kind, actor, reason, string(raw), now)
	return err
}

func lifecycleData(a Agreement, status string, commitment, released decimal.Decimal, price decimal.Decimal, diff []domain.FieldChange, version int, actor string) salesevents.AgreementLifecycleData {
	return salesevents.AgreementLifecycleData{AgreementID: a.ID, AgreementNumber: a.AgreementNumber, LegalEntityID: a.LegalEntityID,
		CustomerID: a.CustomerID, ProductID: a.ProductID, Commitment: commitment.String(), Released: released.String(),
		Remaining: commitment.Sub(released).String(), UnitPrice: price.String(), Status: status, Diff: salesevents.DiffOf(diff),
		Version: version, Actor: actor}
}

// ReviseAgreement changes the commitment, price, validity or cumulative
// tolerance of an ACTIVE or EXHAUSTED agreement at its ETag, under the row
// lock, so it cannot race a release. The commitment cannot fall below the
// released quantity (422 SLS-0053); a closed agreement is 422 SLS-0052. The new
// price applies to later releases only. A request that changes nothing returns
// the agreement without a new version. The revision, its diff and
// agreement.changed.v1 commit together.
func (s *Postgres) ReviseAgreement(ctx context.Context, id string, version int, in AgreementRevision, actor string, now time.Time) (Agreement, error) {
	p, _ := tenant.FromContext(ctx)
	if len(in.Reason) > agreementReasonMaxSize {
		return Agreement{}, problem(http.StatusBadRequest, "COM-0001", "reason is too long")
	}
	var out Agreement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		a, err := loadAgreement(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if a.Version != version {
			return versionConflict()
		}
		next, diff, err := domain.ReviseAgreement(domain.AgreementState{Commitment: a.Commitment, Released: a.Released, UnitPrice: a.UnitPrice,
			CumTolerance: a.CumTolerance, ValidFrom: a.ValidFrom, ValidTo: a.ValidTo, Status: a.Status},
			domain.AgreementRevision{Commitment: in.Commitment, UnitPrice: in.UnitPrice, CumTolerance: in.CumTolerance, ValidFrom: in.ValidFrom, ValidTo: in.ValidTo})
		switch err {
		case nil:
		case domain.ErrAgreementClosed:
			return agreementClosed(a)
		case domain.ErrCommitmentBelowReleased:
			return problem(http.StatusUnprocessableEntity, CodeCommitmentBelow,
				fmt.Sprintf("commitment cannot fall below the %s %s already released", a.Released, a.Unit))
		default:
			return problem(http.StatusBadRequest, "COM-0001", err.Error())
		}
		if len(diff) == 0 {
			out = a
			return nil
		}
		v := a.Version + 1
		if _, err := tx.Exec(ctx, `update sales_agreement set commitment_qty = $3::numeric, unit_price = $4::numeric, cum_tolerance = $5::numeric,
				valid_from = $6::date, valid_to = $7::date, status = $8, version = $9, updated_at = $10 where tenant_id = $1 and id = $2`,
			p.TenantID, id, next.Commitment.String(), next.UnitPrice.String(), next.CumTolerance.String(), next.ValidFrom, next.ValidTo, next.Status, v, now); err != nil {
			return err
		}
		if err := insertAgreementVersion(ctx, tx, p.TenantID, a, v, "REVISED", actor, in.Reason, diff, now); err != nil {
			return err
		}
		data := lifecycleData(a, next.Status, next.Commitment, a.Released, next.UnitPrice, diff, v, actor)
		data.Reason = in.Reason
		if err := enqueue(ctx, tx, "agreement", id, salesevents.AgreementChanged, p.TenantID, data); err != nil {
			return err
		}
		out, err = loadAgreement(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// CloseAgreement ends an agreement early at its ETag. A closed agreement takes
// no more releases, schedule releases or changes; quantity already released
// stays released and its orders are untouched. The reason is required.
func (s *Postgres) CloseAgreement(ctx context.Context, id string, version int, reason, actor string, now time.Time) (Agreement, error) {
	p, _ := tenant.FromContext(ctx)
	var out Agreement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		a, err := loadAgreement(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if a.Version != version {
			return versionConflict()
		}
		if a.Status == domain.AgreementClosed {
			return agreementClosed(a)
		}
		v := a.Version + 1
		if _, err := tx.Exec(ctx, `update sales_agreement set status = 'CLOSED', close_reason = $3, closed_by = $4, closed_at = $5, version = $6, updated_at = $5
			where tenant_id = $1 and id = $2`, p.TenantID, id, reason, actor, now, v); err != nil {
			return err
		}
		diff := domain.DiffHeader("status", a.Status, domain.AgreementClosed)
		if err := insertAgreementVersion(ctx, tx, p.TenantID, a, v, "CLOSED", actor, reason, diff, now); err != nil {
			return err
		}
		data := lifecycleData(a, domain.AgreementClosed, a.Commitment, a.Released, a.UnitPrice, diff, v, actor)
		data.Reason = reason
		if err := enqueue(ctx, tx, "agreement", id, salesevents.AgreementClosed, p.TenantID, data); err != nil {
			return err
		}
		out, err = loadAgreement(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// ListAgreementVersions returns an agreement's history, newest first. An
// agreement that does not exist or is outside the caller's scope is 404.
func (s *Postgres) ListAgreementVersions(ctx context.Context, id string) ([]AgreementVersion, error) {
	p, _ := tenant.FromContext(ctx)
	out := []AgreementVersion{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadAgreement(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select version, kind, actor, reason, diff::text, changed_at from sales_agreement_version
			where tenant_id = $1 and agreement_id = $2 order by version desc limit $3`, p.TenantID, id, maxVersionsListed)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v AgreementVersion
			var raw string
			if err := rows.Scan(&v.Version, &v.Kind, &v.Actor, &v.Reason, &raw, &v.ChangedAt); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(raw), &v.Diff); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

// returnAgreementTx gives the quantity of every not yet returned release that
// created order o back to its agreement, when the order is cancelled (BR-27.9).
// Each agreement is locked in id order (after the order's own lock; a release
// locks the agreement first but creates a new order nobody else holds, so the
// two cannot deadlock). The agreement is ACTIVE again unless it was closed, the
// return is recorded once per release, and agreement.returned.v1 is queued.
// A limit gives back at most that quantity (the remainder of a partly shipped
// order); nil gives back the whole release.
func returnAgreementTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, actor string, now time.Time, limit *decimal.Decimal) error {
	type pending struct{ releaseID, agreementID, qty string }
	var todo []pending
	rows, err := tx.Query(ctx, `select r.id::text, r.agreement_id::text, r.quantity::text from agreement_release r
		where r.tenant_id = $1 and r.sales_order_id = $2
		  and not exists (select 1 from agreement_return x where x.tenant_id = r.tenant_id and x.release_id = r.id)
		order by r.agreement_id, r.id`, tenantID, o.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var t pending
		if err := rows.Scan(&t.releaseID, &t.agreementID, &t.qty); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range todo {
		a, err := loadAgreement(ctx, tx, tenantID, t.agreementID, true)
		if err != nil {
			return err
		}
		qty := decimal.RequireFromString(t.qty)
		if limit != nil {
			if limit.LessThan(qty) {
				qty = *limit
			}
			left := limit.Sub(qty)
			limit = &left
		}
		if !qty.IsPositive() {
			continue
		}
		t.qty = qty.String()
		next, status, err := domain.ReturnToAgreement(a.Commitment, a.Released, qty, a.Status)
		if err != nil {
			return problem(http.StatusInternalServerError, "COM-0500", err.Error())
		}
		v := a.Version + 1
		if _, err := tx.Exec(ctx, `update sales_agreement set released_qty = $3::numeric, status = $4, version = $5, updated_at = $6
			where tenant_id = $1 and id = $2`, tenantID, a.ID, next.String(), status, v, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into agreement_return (tenant_id, id, legal_entity_id, agreement_id, release_id, sales_order_id, quantity, actor, returned_at)
			values ($1, $2, $3, $4, $5, $6, $7::numeric, $8, $9)`, tenantID, ids.New(), a.LegalEntityID, a.ID, t.releaseID, o.ID, t.qty, actor, now); err != nil {
			return err
		}
		diff := []domain.FieldChange{{Field: "releasedQty", Old: a.Released.String(), New: next.String()}}
		if status != a.Status {
			diff = append(diff, domain.FieldChange{Field: "status", Old: a.Status, New: status})
		}
		reason := "order " + o.OrderNumber + " cancelled"
		if limit != nil {
			reason = "remainder of order " + o.OrderNumber + " cancelled"
		}
		if err := insertAgreementVersion(ctx, tx, tenantID, a, v, "RETURNED", actor, reason, diff, now); err != nil {
			return err
		}
		data := lifecycleData(a, status, a.Commitment, next, a.UnitPrice, diff, v, actor)
		data.Reason, data.SalesOrderID, data.ReturnedQty = reason, o.ID, t.qty
		if err := enqueue(ctx, tx, "agreement", a.ID, salesevents.AgreementReturned, tenantID, data); err != nil {
			return err
		}
	}
	return nil
}
