package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/domain"
)

func exceptionNotOpen(invoiceID, status string) *domain.Error {
	return &domain.Error{Code: domain.CodeExceptionNotOpen, Detail: "the exception of invoice " + invoiceID + " is " + status + ", not OPEN"}
}

func (s *Postgres) SetMatchingPolicy(ctx context.Context, mp domain.MatchingPolicy, by string) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	p := mp.Policy
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO receiving_matching_policy (tenant_id, id, legal_entity_id, vendor_id, item_id, match_level, price_tol_pct, price_tol_abs, qty_tol_pct,
			total_tol_pct, total_tol_abs, small_diff_abs, price_tol_mode, owner_user_id, updated_by)
			VALUES ($1, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
			ON CONFLICT (tenant_id, id) DO UPDATE SET match_level = EXCLUDED.match_level, price_tol_pct = EXCLUDED.price_tol_pct, price_tol_abs = EXCLUDED.price_tol_abs,
			qty_tol_pct = EXCLUDED.qty_tol_pct, total_tol_pct = EXCLUDED.total_tol_pct, total_tol_abs = EXCLUDED.total_tol_abs, small_diff_abs = EXCLUDED.small_diff_abs,
			price_tol_mode = EXCLUDED.price_tol_mode, owner_user_id = EXCLUDED.owner_user_id, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			tenantID, mp.ID, mp.LegalEntityID, mp.VendorID, mp.ItemID, p.MatchLevel, p.PriceTolPct, p.PriceTolAbs, p.QtyTolPct, p.TotalTolPct, p.TotalTolAbs, p.SmallDiffAbs,
			p.PriceTolMode, mp.OwnerUserID, by)
		return err
	})
}

func (s *Postgres) ListMatchingPolicies(ctx context.Context, legalEntityID string) ([]domain.MatchingPolicy, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.MatchingPolicy
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, legal_entity_id::text, COALESCE(vendor_id::text, ''), COALESCE(item_id::text, ''), owner_user_id, match_level, price_tol_pct, price_tol_abs,
			qty_tol_pct, total_tol_pct, total_tol_abs, small_diff_abs, price_tol_mode
			FROM receiving_matching_policy WHERE tenant_id = $1 AND legal_entity_id = $2`, tenantID, legalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var mp domain.MatchingPolicy
			p := &mp.Policy
			if err := rows.Scan(&mp.ID, &mp.LegalEntityID, &mp.VendorID, &mp.ItemID, &mp.OwnerUserID, &p.MatchLevel, &p.PriceTolPct, &p.PriceTolAbs, &p.QtyTolPct, &p.TotalTolPct,
				&p.TotalTolAbs, &p.SmallDiffAbs, &p.PriceTolMode); err != nil {
				return err
			}
			out = append(out, mp)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) ListExceptions(ctx context.Context, legalEntityID, status string) ([]InvoiceException, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var out []InvoiceException
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT invoice_id::text, legal_entity_id::text, assignee, reasons, status, created_at, COALESCE(decided_by, ''), decided_at, decision_reason
			FROM receiving_invoice_exception WHERE tenant_id = $1 AND status = $2 AND ($3 = '' OR legal_entity_id = $3::uuid)
			ORDER BY created_at, invoice_id LIMIT $4`, tenantID, status, legalEntityID, defaultListLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e InvoiceException
			if err := rows.Scan(&e.InvoiceID, &e.LegalEntityID, &e.Assignee, &e.Reasons, &e.Status, &e.CreatedAt, &e.DecidedBy, &e.DecidedAt, &e.DecisionReason); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

func insertException(ctx context.Context, tx pgx.Tx, tenantID string, in Invoice, ex domain.MatchException) error {
	_, err := tx.Exec(ctx, `INSERT INTO receiving_invoice_exception (tenant_id, invoice_id, legal_entity_id, assignee, reasons) VALUES ($1, $2, $3, $4, $5)`,
		tenantID, in.ID, in.LegalEntityID, ex.Assignee, ex.Reasons)
	return err
}

func checkAssignee(ctx context.Context, tx pgx.Tx, tenantID, invoiceID, by string) error {
	var assignee, status string
	err := tx.QueryRow(ctx, `SELECT assignee, status FROM receiving_invoice_exception WHERE tenant_id = $1 AND invoice_id = $2 FOR UPDATE`, tenantID, invoiceID).Scan(&assignee, &status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrInvoiceNotFound
	case err != nil:
		return err
	case status != domain.ExceptionOpen:
		return exceptionNotOpen(invoiceID, status)
	case !sameActor(assignee, by):
		return &domain.Error{Code: domain.CodeExceptionNotAssignee, Detail: "only the owner of the matching policy can decide this exception"}
	}
	return nil
}

func closeException(ctx context.Context, tx pgx.Tx, tenantID, invoiceID, status, by, reason string) error {
	_, err := tx.Exec(ctx, `UPDATE receiving_invoice_exception SET status = $3, decided_by = $4, decided_at = now(), decision_reason = $5
		WHERE tenant_id = $1 AND invoice_id = $2 AND status = 'OPEN'`, tenantID, invoiceID, status, by, reason)
	return err
}
