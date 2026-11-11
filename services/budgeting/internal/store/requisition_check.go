package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
)

// ActiveRule is the control rule in force for a legal entity on a date: the one with the latest effective date on or before
// it (the newest version of its code). ok is false when the legal entity has no rule yet, so nothing is budget controlled.
func (s *Postgres) ActiveRule(ctx context.Context, legalEntityID, date string) (Rule, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var r Rule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		r, err = scanRule(tx.QueryRow(ctx, `select `+ruleColumns+` from budget_control_rule
			where tenant_id = $1 and legal_entity_id = $2 and effective_from <= $3::date order by effective_from desc, code, version desc limit 1`,
			p.TenantID, legalEntityID, date))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Rule{}, false, nil
	}
	return r, err == nil, err
}

// ReleasePreEncumbranceByLine gives back the open pre-encumbrance of a requisition line (the line was cancelled). A line
// that was never checked, was converted into a PO line or is already released has nothing to give back: 404 BUD-0002.
func (s *Postgres) ReleasePreEncumbranceByLine(ctx context.Context, key, hash, requisitionLineID, documentID string) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	var encumbranceID string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id::text from encumbrance where tenant_id = $1 and source_line_id = $2 and stage = $3`,
			p.TenantID, requisitionLineID, domain.StagePre).Scan(&encumbranceID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, httpx.NewProblem(http.StatusNotFound, CodeEncumbranceNotFound, "no open pre-encumbrance for this requisition line")
	}
	if err != nil {
		return 0, nil, err
	}
	return s.ReleaseEncumbrance(ctx, key, hash, ReleaseRequest{EncumbranceID: encumbranceID, DocumentID: documentID, Reason: ReleaseCancelled})
}
