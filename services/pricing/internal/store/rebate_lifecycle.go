package store

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// AgreementRevision is a change to an ACTIVE rebate agreement at an expected version. Nil
// fields stay as they are.
type AgreementRevision struct {
	ID      string
	Version int
	Name    *string
	ValidTo *time.Time
	Tiers   *[]domain.Tier
	// Scope replaces the product scope; an empty ProductScope takes every invoice line again.
	Scope *ProductScope
}

// ProductScope is the product or product group an agreement is limited to (at most one set).
type ProductScope struct {
	ProductID      string
	ProductGroupID string
}

func agreementEventData(a domain.RebateAgreement) pevents.RebateAgreementData {
	return pevents.RebateAgreementData{AgreementID: a.ID, LegalEntityID: a.LegalEntityID, PartyRole: domain.PartyRoleOf(a), Status: a.Status,
		ValidFrom: dayStr(a.ValidFrom), ValidTo: dayStr(a.ValidTo), Version: a.Version,
		ProductID: a.ProductID, ProductGroupID: a.ProductGroupID}
}

// lockAgreement reads the agreement under a row lock and returns the date of its latest
// accrued invoice line. The per-period delta rows of a retroactive recompute (invoice line
// retro:...) are dated on the activation day, not on an invoice, so they do not count.
func lockAgreement(ctx context.Context, tx pgx.Tx, id string) (domain.RebateAgreement, *time.Time, error) {
	a, err := loadAgreement(ctx, tx, `where id = $1 for update`, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, nil, agreementNotFound(id)
	}
	if err != nil {
		return a, nil, err
	}
	var last *time.Time
	if err := tx.QueryRow(ctx, `select max(invoice_date) from rebate_accrual where agreement_id = $1 and invoice_line_id not like 'retro:%'`, id).Scan(&last); err != nil {
		return a, nil, err
	}
	return a, last, nil
}

func agreementStateError(a domain.RebateAgreement) error {
	return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "rebate agreement "+a.ID+" is "+a.Status+"; only an ACTIVE agreement can change")
}

// ReviseAgreement changes the name, end date or tiers of an ACTIVE agreement at its version
// (412 conflict_version otherwise). The end date may not fall before an invoice already
// accrued (PRC-0048) and the tiers may not change once the agreement has accruals (PRC-0049,
// the accrued amounts were computed at the old tiers). Writes rebateagreement.revised in the
// same transaction; a repeated Idempotency-Key replays the first response.
func (s *Postgres) ReviseAgreement(ctx context.Context, rev AgreementRevision, idempotencyKey string) (domain.RebateAgreement, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RebateAgreement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, last, err := lockAgreement(ctx, tx, rev.ID)
		if err != nil {
			return err
		}
		hash := commandHash("agreement-revise", rev.ID, strconv.Itoa(rev.Version), ptrStr(rev.Name), ptrDay(rev.ValidTo), tiersHash(rev.Tiers), scopeHash(rev.Scope))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if cur.Version != rev.Version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "rebate agreement version does not match")
		}
		if cur.Status != domain.AgreementActive {
			return agreementStateError(cur)
		}
		next := cur
		if rev.Name != nil {
			next.Name = *rev.Name
		}
		if rev.ValidTo != nil {
			next.ValidTo = *rev.ValidTo
		}
		if rev.Tiers != nil {
			if last != nil {
				return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeAgreementHasAccruals,
					"tiers cannot change after invoices have accrued; close this agreement and create a new one")
			}
			next.Tiers = *rev.Tiers
		}
		if rev.Scope != nil {
			next.ProductID, next.ProductGroupID = rev.Scope.ProductID, rev.Scope.ProductGroupID
			if next.ProductID != cur.ProductID || next.ProductGroupID != cur.ProductGroupID {
				// Any accrual row counts, a replayed retroactive line included: it joined the
				// basis under the scope that was in force.
				var accrued bool
				if err := tx.QueryRow(ctx, `select exists (select 1 from rebate_accrual where agreement_id = $1)`, rev.ID).Scan(&accrued); err != nil {
					return err
				}
				if accrued {
					return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeScopeHasAccruals,
						"the product scope cannot change after invoices have accrued; close this agreement and create a new one")
				}
			}
		}
		if e := domain.ValidateAgreement(next); e != nil {
			return httpx.NewProblem(http.StatusUnprocessableEntity, e.Code, e.Detail)
		}
		if last != nil && dayStr(next.ValidTo) < dayStr(*last) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeAgreementEndsEarly,
				"validTo "+dayStr(next.ValidTo)+" is before the invoice of "+dayStr(*last)+" that this agreement already accrued")
		}
		tiers, err := marshalTiers(next.Tiers)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update rebate_agreement set name = $2, valid_to = $3, tiers = $4, product_id = nullif($5, '')::uuid,
			product_group_id = nullif($6, '')::uuid, version = version + 1 where id = $1`,
			rev.ID, next.Name, next.ValidTo, tiers, next.ProductID, next.ProductGroupID); err != nil {
			return err
		}
		next.Version = cur.Version + 1
		out = next
		if err := enqueueEvent(ctx, tx, "rebateagreement", out.ID, pevents.RebateAgreementRevised, agreementEventData(out)); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// CloseAgreement ends an ACTIVE agreement on closeDate (BR-26.8): valid_to becomes the close
// date, so invoices dated later no longer accrue, and the status becomes CLOSED. The close
// date must lie within the validity and not before an invoice already accrued (PRC-0048).
// 409 PRC-0031 when already closed; 412 on a stale version; writes rebateagreement.closed in
// the same transaction; a repeated Idempotency-Key replays the first response.
func (s *Postgres) CloseAgreement(ctx context.Context, id string, version int, closeDate time.Time, idempotencyKey string) (domain.RebateAgreement, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RebateAgreement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, last, err := lockAgreement(ctx, tx, id)
		if err != nil {
			return err
		}
		hash := commandHash("agreement-close", id, strconv.Itoa(version), dayStr(closeDate))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "rebate agreement version does not match")
		}
		if cur.Status != domain.AgreementActive {
			return agreementStateError(cur)
		}
		if d := dayStr(closeDate); d < dayStr(cur.ValidFrom) || d > dayStr(cur.ValidTo) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation,
				"closeDate must be within the agreement validity "+dayStr(cur.ValidFrom)+" to "+dayStr(cur.ValidTo))
		}
		if last != nil && dayStr(closeDate) < dayStr(*last) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeAgreementEndsEarly,
				"closeDate "+dayStr(closeDate)+" is before the invoice of "+dayStr(*last)+" that this agreement already accrued")
		}
		if _, err := tx.Exec(ctx, `update rebate_agreement set status = 'CLOSED', valid_to = $2, closed_at = now(), version = version + 1 where id = $1`,
			id, closeDate); err != nil {
			return err
		}
		cur.Status, cur.ValidTo, cur.Version = domain.AgreementClosed, closeDate, cur.Version+1
		out = cur
		if err := enqueueEvent(ctx, tx, "rebateagreement", out.ID, pevents.RebateAgreementClosed, agreementEventData(out)); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// ReopenAgreement makes a CLOSED agreement ACTIVE again with validTo as its new last day
// (BR-26.8 reversed): the end date must be later than the close date it carries now (422
// COM-0001), so invoices dated after the old close date accrue again up to validTo. The
// accruals already booked stay as they are. 409 PRC-0031 when the agreement is not CLOSED; 412
// on a stale version; writes rebateagreement.reopened in the same transaction; a repeated
// Idempotency-Key replays the first response.
func (s *Postgres) ReopenAgreement(ctx context.Context, id string, version int, validTo time.Time, idempotencyKey string) (domain.RebateAgreement, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RebateAgreement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, _, err := lockAgreement(ctx, tx, id)
		if err != nil {
			return err
		}
		hash := commandHash("agreement-reopen", id, strconv.Itoa(version), dayStr(validTo))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "rebate agreement version does not match")
		}
		if cur.Status != domain.AgreementClosed {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "rebate agreement "+cur.ID+" is "+cur.Status+"; only a CLOSED agreement can be reopened")
		}
		if dayStr(validTo) <= dayStr(cur.ValidTo) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation,
				"validTo must be after the close date "+dayStr(cur.ValidTo))
		}
		next := cur
		next.Status, next.ValidTo = domain.AgreementActive, validTo
		if e := domain.ValidateAgreement(next); e != nil {
			return httpx.NewProblem(http.StatusUnprocessableEntity, e.Code, e.Detail)
		}
		if _, err := tx.Exec(ctx, `update rebate_agreement set status = 'ACTIVE', valid_to = $2, closed_at = null, version = version + 1 where id = $1`,
			id, validTo); err != nil {
			return err
		}
		next.Version = cur.Version + 1
		out = next
		if err := enqueueEvent(ctx, tx, "rebateagreement", out.ID, pevents.RebateAgreementReopened, agreementEventData(out)); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}
