package store

import (
	"context"
	"encoding/json"
	"errors"
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
	Commitment *decimal.Decimal
	// CommitmentIsAmount is set when Commitment came from commitmentAmount: it is
	// refused on a QUANTITY agreement, and commitmentQty on an AMOUNT one.
	CommitmentIsAmount bool
	UnitPrice          *decimal.Decimal
	CumTolerance       *decimal.Decimal
	// OverdeliveryPct changes the tolerance above the commitment (BR-27.9).
	OverdeliveryPct *decimal.Decimal
	ValidFrom       *string
	ValidTo         *string
	Reason          string
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

func lifecycleData(a Agreement, status string, commitment, pct, released decimal.Decimal, price decimal.Decimal, diff []domain.FieldChange, version int, actor string) salesevents.AgreementLifecycleData {
	return salesevents.AgreementLifecycleData{AgreementID: a.ID, AgreementNumber: a.AgreementNumber, LegalEntityID: a.LegalEntityID,
		CustomerID: a.CustomerID, ProductID: a.ProductID, CommitmentType: a.CommitmentType, Commitment: commitment.String(), OverdeliveryPct: pct.String(), Released: released.String(),
		Remaining: domain.AgreementCeiling(commitment, pct).Sub(released).String(), UnitPrice: price.String(), Status: status, Diff: salesevents.DiffOf(diff),
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
		if in.Commitment != nil && in.CommitmentIsAmount != (a.CommitmentType == domain.CommitmentAmount) {
			field, want := "commitmentQty", "commitmentAmount"
			if in.CommitmentIsAmount {
				field, want = want, field
			}
			return problem(http.StatusBadRequest, "COM-0001", field+" does not apply to a "+a.CommitmentType+" agreement; use "+want)
		}
		if in.UnitPrice != nil && !in.UnitPrice.IsPositive() && a.CommitmentType == domain.CommitmentAmount {
			return problem(http.StatusBadRequest, "COM-0001", "unitPrice must be greater than 0 on an AMOUNT agreement")
		}
		next, diff, err := domain.ReviseAgreement(domain.AgreementState{Type: a.CommitmentType, Commitment: a.Commitment, Released: a.Released, UnitPrice: a.UnitPrice,
			CumTolerance: a.CumTolerance, OverdeliveryPct: a.OverdeliveryPct, ValidFrom: a.ValidFrom, ValidTo: a.ValidTo, Status: a.Status},
			domain.AgreementRevision{Commitment: in.Commitment, UnitPrice: in.UnitPrice, CumTolerance: in.CumTolerance, OverdeliveryPct: in.OverdeliveryPct,
				ValidFrom: in.ValidFrom, ValidTo: in.ValidTo})
		switch err {
		case nil:
		case domain.ErrAgreementClosed:
			return agreementClosed(a)
		case domain.ErrCommitmentBelowReleased:
			return problem(http.StatusUnprocessableEntity, CodeCommitmentBelow,
				fmt.Sprintf("commitment, with its over-delivery tolerance, cannot fall below the %s %s already released", a.Released, a.CommitmentUnit))
		default:
			return problem(http.StatusBadRequest, "COM-0001", err.Error())
		}
		if len(diff) == 0 {
			out = a
			return nil
		}
		v := a.Version + 1
		if _, err := tx.Exec(ctx, `update sales_agreement set commitment_qty = $3::numeric, unit_price = $4::numeric, cum_tolerance = $5::numeric,
				valid_from = $6::date, valid_to = $7::date, status = $8, version = $9, updated_at = $10, overdelivery_pct = $11::numeric
			where tenant_id = $1 and id = $2`,
			p.TenantID, id, next.Commitment.String(), next.UnitPrice.String(), next.CumTolerance.String(), next.ValidFrom, next.ValidTo, next.Status, v, now,
			next.OverdeliveryPct.String()); err != nil {
			return err
		}
		if err := insertAgreementVersion(ctx, tx, p.TenantID, a, v, "REVISED", actor, in.Reason, diff, now); err != nil {
			return err
		}
		data := lifecycleData(a, next.Status, next.Commitment, next.OverdeliveryPct, a.Released, next.UnitPrice, diff, v, actor)
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
		data := lifecycleData(a, domain.AgreementClosed, a.Commitment, a.OverdeliveryPct, a.Released, a.UnitPrice, diff, v, actor)
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

// agreementRelease is a release of an order, with what is still drawn on it.
type agreementRelease struct {
	id, agreementID string
	// kind is the agreement's commitment type and unitPrice the price the release
	// line was created at; outstanding is in the commitment unit.
	kind        string
	unitPrice   decimal.Decimal
	outstanding decimal.Decimal
}

// commitmentWorth is what qty of a release line is worth against its
// agreement: the quantity on a QUANTITY agreement, its value at the price the
// release was made at (no discount, rounded like a line net) on an AMOUNT one.
// Every draw and return of a release is valued this way, so a price or
// discount edit on the order line never moves the commitment and what a
// release gives back matches what it drew (BR-27.9: agreement lines re-price
// only when the agreement price changes).
func commitmentWorth(kind string, qty, releasePrice decimal.Decimal) (decimal.Decimal, error) {
	if kind != domain.CommitmentAmount || !qty.IsPositive() {
		return qty, nil
	}
	return domain.LineNet(qty, releasePrice, decimal.Zero)
}

// outstandingReleasesTx lists the releases that created order o and still hold
// quantity: the release quantity plus later draws less returns, above zero, in
// agreement and release order so the locks are taken in a fixed order.
func outstandingReleasesTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order) ([]agreementRelease, error) {
	rows, err := tx.Query(ctx, `select r.id::text, r.agreement_id::text, a.commitment_type, r.unit_price::text,
			(r.units
			 + coalesce((select sum(d.quantity) from agreement_draw d where d.tenant_id = r.tenant_id and d.release_id = r.id), 0)
			 - coalesce((select sum(x.quantity) from agreement_return x where x.tenant_id = r.tenant_id and x.release_id = r.id), 0))::text
		from agreement_release r join sales_agreement a on a.tenant_id = r.tenant_id and a.id = r.agreement_id
		where r.tenant_id = $1 and r.sales_order_id = $2 order by r.agreement_id, r.id`, tenantID, o.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []agreementRelease
	for rows.Next() {
		var t agreementRelease
		var qty, price string
		if err := rows.Scan(&t.id, &t.agreementID, &t.kind, &price, &qty); err != nil {
			return nil, err
		}
		t.unitPrice = decimal.RequireFromString(price)
		if t.outstanding = decimal.RequireFromString(qty); t.outstanding.IsPositive() {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// returnAgreementTx gives the outstanding quantity of every release that
// created order o back to its agreement, when the order is cancelled (BR-27.9).
// Each agreement is locked in id order (after the order's own lock; a release
// locks the agreement first but creates a new order nobody else holds, so the
// two cannot deadlock). The agreement is ACTIVE again unless it was closed,
// every return is recorded (a release can be returned in parts), and
// agreement.returned.v1 is queued. A limit gives back at most that quantity (the
// remainder of a partly shipped order); nil gives back everything outstanding.
func returnAgreementTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, actor string, now time.Time, limit *decimal.Decimal) error {
	reason := "order " + o.OrderNumber + " cancelled"
	if limit != nil {
		reason = "remainder of order " + o.OrderNumber + " cancelled"
	}
	return giveBackReleasesTx(ctx, tx, tenantID, o, limit, actor, reason, now)
}

// giveBackReleasesTx gives back what the releases of order o still hold, in
// lock order. A limit is a quantity of the release line: each release gives
// back at most its worth (commitmentWorth) and the limit shrinks by the
// quantity that covered; nil gives back everything outstanding.
func giveBackReleasesTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, limit *decimal.Decimal, actor, reason string, now time.Time) error {
	todo, err := outstandingReleasesTx(ctx, tx, tenantID, o)
	if err != nil {
		return err
	}
	for _, t := range todo {
		give := t.outstanding
		if limit != nil {
			if !limit.IsPositive() {
				return nil
			}
			worth, err := commitmentWorth(t.kind, *limit, t.unitPrice)
			if err != nil {
				return err
			}
			used := *limit
			if worth.GreaterThan(give) {
				// Only part of the limit is needed to clear this release.
				used = give
				if t.kind == domain.CommitmentAmount {
					used = limit.Mul(give).Div(worth)
				}
			} else {
				give = worth
			}
			left := limit.Sub(used)
			limit = &left
		}
		if err := giveBackTx(ctx, tx, tenantID, o, t, give, actor, reason, now); err != nil {
			return err
		}
	}
	return nil
}

// giveBackTx returns qty of release t to its agreement under the agreement's
// row lock: released quantity, a RETURNED version, the agreement_return row and
// agreement.returned.v1, together. A zero or negative qty does nothing.
func giveBackTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, t agreementRelease, qty decimal.Decimal, actor, reason string, now time.Time) error {
	if !qty.IsPositive() {
		return nil
	}
	a, err := loadAgreement(ctx, tx, tenantID, t.agreementID, true)
	if err != nil {
		return err
	}
	next, status, err := domain.ReturnToAgreement(a.Commitment, a.OverdeliveryPct, a.Released, qty, a.Status)
	if err != nil {
		return problem(http.StatusInternalServerError, "COM-0500", err.Error())
	}
	v := a.Version + 1
	if _, err := tx.Exec(ctx, `update sales_agreement set released_qty = $3::numeric, status = $4, version = $5, updated_at = $6
		where tenant_id = $1 and id = $2`, tenantID, a.ID, next.String(), status, v, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into agreement_return (tenant_id, id, legal_entity_id, agreement_id, release_id, sales_order_id, quantity, actor, returned_at)
		values ($1, $2, $3, $4, $5, $6, $7::numeric, $8, $9)`, tenantID, ids.New(), a.LegalEntityID, a.ID, t.id, o.ID, qty.String(), actor, now); err != nil {
		return err
	}
	diff := []domain.FieldChange{{Field: domain.ReleasedField(a.CommitmentType), Old: a.Released.String(), New: next.String()}}
	if status != a.Status {
		diff = append(diff, domain.FieldChange{Field: "status", Old: a.Status, New: status})
	}
	if err := insertAgreementVersion(ctx, tx, tenantID, a, v, "RETURNED", actor, reason, diff, now); err != nil {
		return err
	}
	data := lifecycleData(a, status, a.Commitment, a.OverdeliveryPct, next, a.UnitPrice, diff, v, actor)
	data.Reason, data.SalesOrderID, data.ReturnedQty = reason, o.ID, qty.String()
	return enqueue(ctx, tx, "agreement", a.ID, salesevents.AgreementReturned, tenantID, data)
}

// drawAgreementTx draws the worth of raise, a quantity of the release line
// (commitmentWorth), against the agreement a release of order o came from,
// when the quantity of the release line is raised by PATCH (BR-27.9:
// a released order may not take more than the commitment and its tolerance). It
// runs under the agreement's row lock; beyond the ceiling it is 422
// AGREEMENT_EXHAUSTED (SLS-0008), outside the validity period SLS-0029, on a
// closed agreement SLS-0052, and the whole PATCH is refused with nothing
// stored. An order that was not released from an agreement draws nothing. The
// draw, a DRAWN version and agreement.drawn.v1 are written together.
func drawAgreementTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, raise decimal.Decimal, actor string, now time.Time) error {
	if !raise.IsPositive() {
		return nil
	}
	var releaseID, agreementID, price string
	err := tx.QueryRow(ctx, `select id::text, agreement_id::text, unit_price::text from agreement_release where tenant_id = $1 and sales_order_id = $2
		order by released_at, id limit 1`, tenantID, o.ID).Scan(&releaseID, &agreementID, &price)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	a, err := loadAgreement(ctx, tx, tenantID, agreementID, true)
	if err != nil {
		return err
	}
	qty, err := commitmentWorth(a.CommitmentType, raise, decimal.RequireFromString(price))
	if err != nil {
		return problem(http.StatusBadRequest, "COM-0001", err.Error())
	}
	if !qty.IsPositive() {
		return nil
	}
	if a.Status == domain.AgreementClosed {
		return agreementClosed(a)
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
	next, status, err := domain.ReleaseAgreement(a.Commitment, a.OverdeliveryPct, a.Released, qty)
	if errors.Is(err, domain.ErrAgreementExhausted) {
		return problem(http.StatusUnprocessableEntity, CodeAgreementExhausted,
			"AGREEMENT_EXHAUSTED: agreement "+a.AgreementNumber+" has "+a.Remaining.String()+" "+a.CommitmentUnit+" remaining; the change asks for "+qty.String()+" more")
	}
	if err != nil {
		return problem(http.StatusBadRequest, "COM-0001", err.Error())
	}
	v := a.Version + 1
	if _, err := tx.Exec(ctx, `update sales_agreement set released_qty = $3::numeric, status = $4, version = $5, updated_at = $6
		where tenant_id = $1 and id = $2`, tenantID, a.ID, next.String(), status, v, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into agreement_draw (tenant_id, id, legal_entity_id, agreement_id, release_id, sales_order_id, quantity, actor, drawn_at)
		values ($1, $2, $3, $4, $5, $6, $7::numeric, $8, $9)`, tenantID, ids.New(), a.LegalEntityID, a.ID, releaseID, o.ID, qty.String(), actor, now); err != nil {
		return err
	}
	diff := []domain.FieldChange{{Field: domain.ReleasedField(a.CommitmentType), Old: a.Released.String(), New: next.String()}}
	if status != a.Status {
		diff = append(diff, domain.FieldChange{Field: "status", Old: a.Status, New: status})
	}
	reason := "quantity of order " + o.OrderNumber + " raised"
	if err := insertAgreementVersion(ctx, tx, tenantID, a, v, "DRAWN", actor, reason, diff, now); err != nil {
		return err
	}
	data := lifecycleData(a, status, a.Commitment, a.OverdeliveryPct, next, a.UnitPrice, diff, v, actor)
	data.Reason, data.SalesOrderID, data.DrawnQty = reason, o.ID, qty.String()
	return enqueue(ctx, tx, "agreement", a.ID, salesevents.AgreementDrawn, tenantID, data)
}

// followReleaseLineTx keeps the commitment in step with the quantity of the
// release line of a released order when PATCH changes it: line 1, which
// ReleaseAgreement creates (a supplementary line 1 is never the release line).
// A lowered quantity gives the difference back to the agreement (the order's
// outstanding release quantity is the ceiling for it), a raised one draws the
// difference; on an AMOUNT agreement both are valued at the release price
// (commitmentWorth), so a price or discount edit on the line moves nothing. A
// removed line 1 is handled by followRemovalsTx.
func followReleaseLineTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, before map[int]Line, actor string, now time.Time) error {
	b, ok := before[1]
	if !ok || b.SupplementaryOf != nil || b.LineStatus == domain.LineCancelled {
		return nil
	}
	var l *Line
	for i := range o.Lines {
		if o.Lines[i].LineNo == 1 {
			l = &o.Lines[i]
		}
	}
	if l == nil || l.LineStatus == domain.LineCancelled || l.Quantity.Equal(b.Quantity) {
		return nil
	}
	if l.Quantity.GreaterThan(b.Quantity) {
		return drawAgreementTx(ctx, tx, tenantID, o, l.Quantity.Sub(b.Quantity), actor, now)
	}
	cut := b.Quantity.Sub(l.Quantity)
	return giveBackReleasesTx(ctx, tx, tenantID, o, &cut, actor, "quantity of order "+o.OrderNumber+" lowered", now)
}
