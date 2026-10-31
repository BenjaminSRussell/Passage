package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

func notBlockedIfNoRow(err error, id string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return &domain.Error{Code: domain.CodeNotAuditBlocked, Detail: fmt.Sprintf("invoice %s is not blocked for audit", id)}
	}
	return err
}

func (s *Postgres) RejectedInvoiceCount(ctx context.Context, vendorID, number string) (int, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM receiving_vendor_invoice WHERE tenant_id = $1 AND vendor_id = $2 AND status = 'AUDIT_REJECTED'
			AND (lower(invoice_number) = lower($3) OR left(lower(invoice_number), length($3) + 2) = lower($3) || '-r')`, tenantID, vendorID, number).Scan(&n)
	})
	return n, err
}

func sameActor(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	ca, okA := ids.Canonical(a)
	cb, okB := ids.Canonical(b)
	if okA && okB {
		return ca == cb
	}
	return strings.EqualFold(a, b)
}

func CheckRelease(inv Invoice, legalEntityID, by string) error {
	switch {
	case inv.LegalEntityID != legalEntityID:
		return &domain.Error{Code: domain.CodeInvalidReceipt, Detail: "legalEntityId differs from the legal entity of the invoice"}
	case inv.Status != domain.InvoiceStatusAuditBlocked:
		return &domain.Error{Code: domain.CodeNotAuditBlocked, Detail: fmt.Sprintf("invoice %s is not blocked for audit", inv.ID)}
	case strings.TrimSpace(inv.PostedBy) == "":
		return &domain.Error{Code: domain.CodeAuditSelfRelease, Detail: "the invoice has no recorded poster, so segregation of duties cannot be shown; it cannot be decided here"}
	case sameActor(inv.PostedBy, by):
		return &domain.Error{Code: domain.CodeAuditSelfRelease, Detail: "the user who posted an invoice cannot release or reject its audit block"}
	}
	return nil
}

func (s *Postgres) ReleaseInvoice(ctx context.Context, id, legalEntityID, by string, fn ReleaseFunc) (Invoice, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Invoice{}, err
	}
	id, ok := ids.Canonical(id)
	if !ok {
		return Invoice{}, ErrInvoiceNotFound
	}
	inv := Invoice{ID: id}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, tenantID+"|release|"+id); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT legal_entity_id::text, vendor_id::text, invoice_number, invoice_date, posting_date, currency, ledger_id::text, fiscal_period_id::text,
			ledger_entry_id, total, created_at, source, status, posted_by, COALESCE(one_time_payee_id::text, '') FROM receiving_vendor_invoice WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id).
			Scan(&inv.LegalEntityID, &inv.VendorID, &inv.InvoiceNumber, &inv.InvoiceDate, &inv.PostingDate, &inv.Currency, &inv.LedgerID, &inv.FiscalPeriodID,
				&inv.LedgerEntryID, &inv.Total, &inv.CreatedAt, &inv.Source, &inv.Status, &inv.PostedBy, &inv.OneTimePayeeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvoiceNotFound
		}
		if err != nil {
			return err
		}
		if err := CheckRelease(inv, legalEntityID, by); err != nil {
			return err
		}
		if err := loadInvoiceLines(ctx, tx, tenantID, &inv); err != nil {
			return err
		}
		outcome, err := fn(inv)
		if err != nil {
			return err
		}
		inv.LedgerEntryID, inv.Status, inv.ReleasedBy = outcome.LedgerEntryID, domain.InvoiceStatusPosted, by
		if err := tx.QueryRow(ctx, `UPDATE receiving_vendor_invoice SET ledger_entry_id = $3, status = 'POSTED', released_by = $4, released_at = now()
			WHERE tenant_id = $1 AND id = $2 AND status = 'AUDIT_BLOCKED' RETURNING released_at`, tenantID, id, inv.LedgerEntryID, by).Scan(&inv.ReleasedAt); err != nil {
			return notBlockedIfNoRow(err, id)
		}
		if err := markPostingsDone(ctx, tx, tenantID, id, outcome.LedgerEntryID != ""); err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, invoiceAggregate, id, outcome.Event)
	})
	return inv, err
}

func loadInvoiceLines(ctx context.Context, tx pgx.Tx, tenantID string, inv *Invoice) error {
	rows, err := tx.Query(ctx, `SELECT receipt_id::text, receipt_line_no, qty, unit_price, amount, standard_cost, COALESCE(grir_cleared, 0)
		FROM receiving_vendor_invoice_line WHERE tenant_id = $1 AND invoice_id = $2 ORDER BY line_no`, tenantID, inv.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var l InvoiceLine
		if err := rows.Scan(&l.ReceiptID, &l.ReceiptLineNo, &l.Qty, &l.UnitPrice, &l.Amount, &l.StandardCost, &l.GrirCleared); err != nil {
			return err
		}
		inv.Lines = append(inv.Lines, l)
	}
	return rows.Err()
}

func (s *Postgres) RejectInvoice(ctx context.Context, id, legalEntityID, by, reason string, fn RejectFunc) (Invoice, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Invoice{}, err
	}
	id, ok := ids.Canonical(id)
	if !ok {
		return Invoice{}, ErrInvoiceNotFound
	}
	inv := Invoice{ID: id}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, tenantID+"|release|"+id); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT legal_entity_id::text, vendor_id::text, invoice_number, invoice_date, posting_date, currency, ledger_id::text, fiscal_period_id::text,
			ledger_entry_id, total, created_at, source, status, posted_by FROM receiving_vendor_invoice WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id).
			Scan(&inv.LegalEntityID, &inv.VendorID, &inv.InvoiceNumber, &inv.InvoiceDate, &inv.PostingDate, &inv.Currency, &inv.LedgerID, &inv.FiscalPeriodID,
				&inv.LedgerEntryID, &inv.Total, &inv.CreatedAt, &inv.Source, &inv.Status, &inv.PostedBy)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvoiceNotFound
		}
		if err != nil {
			return err
		}
		if err := CheckRelease(inv, legalEntityID, by); err != nil {
			return err
		}
		if err := loadInvoiceLines(ctx, tx, tenantID, &inv); err != nil {
			return err
		}
		evt, err := fn(inv)
		if err != nil {
			return err
		}
		order := make([]int, len(inv.Lines))
		for i := range order {
			order[i] = i
		}
		slices.SortStableFunc(order, func(a, b int) int {
			x, y := inv.Lines[a], inv.Lines[b]
			return strings.Compare(fmt.Sprintf("%s|%09d", x.ReceiptID, x.ReceiptLineNo), fmt.Sprintf("%s|%09d", y.ReceiptID, y.ReceiptLineNo))
		})
		for _, i := range order {
			l := inv.Lines[i]
			if _, err := tx.Exec(ctx, `UPDATE receiving_grir_open_item SET invoiced_qty = invoiced_qty - $4::numeric, cleared_amount = cleared_amount - $5::numeric,
				status = CASE WHEN invoiced_qty - $4::numeric >= received_qty THEN 'CLEARED' WHEN invoiced_qty - $4::numeric <= 0 THEN 'OPEN' ELSE 'PARTIAL' END,
				cleared_at = CASE WHEN invoiced_qty - $4::numeric >= received_qty THEN cleared_at END
				WHERE tenant_id = $1 AND receipt_id = $2 AND line_no = $3`, tenantID, l.ReceiptID, l.ReceiptLineNo, l.Qty, l.GrirCleared); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE receiving_receipt_line SET invoiced_qty = invoiced_qty - $4::numeric WHERE tenant_id = $1 AND receipt_id = $2 AND line_no = $3`,
				tenantID, l.ReceiptID, l.ReceiptLineNo, l.Qty); err != nil {
				return err
			}
		}
		inv.Status, inv.RejectedBy, inv.RejectReason = domain.InvoiceStatusAuditRejected, by, reason
		if err := tx.QueryRow(ctx, `UPDATE receiving_vendor_invoice SET status = 'AUDIT_REJECTED', rejected_by = $3, rejected_at = now(), reject_reason = $4
			WHERE tenant_id = $1 AND id = $2 AND status = 'AUDIT_BLOCKED' RETURNING rejected_at`, tenantID, id, by, reason).Scan(&inv.RejectedAt); err != nil {
			return notBlockedIfNoRow(err, id)
		}
		return outbox.Enqueue(ctx, tx, invoiceAggregate, id, evt)
	})
	return inv, err
}

func (s *Postgres) SetErsVendor(ctx context.Context, legalEntityID, vendorID string, enabled bool, by string, fn ErsVendorFunc) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, strings.ToLower(tenantID+"|ers|"+legalEntityID+"|"+vendorID)); err != nil {
			return err
		}
		old := false
		err := tx.QueryRow(ctx, `SELECT enabled FROM receiving_ers_vendor WHERE tenant_id = $1 AND legal_entity_id = $2 AND vendor_id = $3`, tenantID, legalEntityID, vendorID).Scan(&old)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		evt, err := fn(old)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO receiving_ers_vendor (tenant_id, legal_entity_id, vendor_id, enabled, updated_by) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (tenant_id, legal_entity_id, vendor_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			tenantID, legalEntityID, vendorID, enabled, by); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO receiving_ers_vendor_history (tenant_id, legal_entity_id, vendor_id, old_enabled, new_enabled, changed_by) VALUES ($1, $2, $3, $4, $5, $6)`,
			tenantID, legalEntityID, vendorID, old, enabled, by); err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "ersvendor", vendorID, evt)
	})
}

func (s *Postgres) ErsVendor(ctx context.Context, legalEntityID, vendorID string) (bool, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return false, err
	}
	enabled := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT enabled FROM receiving_ers_vendor WHERE tenant_id = $1 AND legal_entity_id = $2 AND vendor_id = $3`, tenantID, legalEntityID, vendorID).Scan(&enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return enabled, err
}

func (s *Postgres) SetAuditPolicy(ctx context.Context, legalEntityID string, policy domain.AuditPolicy, by string, fn AuditPolicyFunc) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, strings.ToLower(tenantID+"|auditrate|"+legalEntityID)); err != nil {
			return err
		}
		old, err := loadAuditPolicy(ctx, tx, tenantID, legalEntityID)
		if err != nil {
			return err
		}
		policy.RuleVersion = old.RuleVersion
		if old.RuleVersion == 0 || !old.RatePct.Equal(policy.RatePct) || !old.Threshold.Equal(policy.Threshold) || old.ThresholdCurrency != policy.ThresholdCurrency {
			policy.RuleVersion++
		}
		evt, err := fn(old, policy)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO receiving_audit_policy (tenant_id, legal_entity_id, sample_rate_pct, threshold_amount, threshold_currency, rule_version, updated_by)
			VALUES ($1, $2, $3, NULLIF($4, 0), NULLIF($5, ''), $6, $7)
			ON CONFLICT (tenant_id, legal_entity_id) DO UPDATE SET sample_rate_pct = EXCLUDED.sample_rate_pct, threshold_amount = EXCLUDED.threshold_amount,
			threshold_currency = EXCLUDED.threshold_currency, rule_version = EXCLUDED.rule_version, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			tenantID, legalEntityID, policy.RatePct, policy.Threshold, policy.ThresholdCurrency, policy.RuleVersion, by); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO receiving_audit_policy_history (tenant_id, legal_entity_id, old_rate_pct, new_rate_pct, changed_by, old_threshold_amount, new_threshold_amount, rule_version)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, 0), NULLIF($7, 0), $8)`,
			tenantID, legalEntityID, old.RatePct, policy.RatePct, by, old.Threshold, policy.Threshold, policy.RuleVersion); err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "auditpolicy", legalEntityID, evt)
	})
}

func loadAuditPolicy(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) (domain.AuditPolicy, error) {
	var p domain.AuditPolicy
	err := tx.QueryRow(ctx, `SELECT sample_rate_pct, COALESCE(threshold_amount, 0), COALESCE(threshold_currency, ''), rule_version
		FROM receiving_audit_policy WHERE tenant_id = $1 AND legal_entity_id = $2`, tenantID, legalEntityID).Scan(&p.RatePct, &p.Threshold, &p.ThresholdCurrency, &p.RuleVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AuditPolicy{}, nil
	}
	return p, err
}

func (s *Postgres) AuditPolicy(ctx context.Context, legalEntityID string) (domain.AuditPolicy, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return domain.AuditPolicy{}, err
	}
	var p domain.AuditPolicy
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		p, err = loadAuditPolicy(ctx, tx, tenantID, legalEntityID)
		return err
	})
	return p, err
}

func (s *Postgres) ConfirmErsPrice(ctx context.Context, legalEntityID, receiptID string, lineNo int, price decimal.Decimal, by string) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO receiving_ers_price_confirmation (tenant_id, legal_entity_id, receipt_id, line_no, confirmed_price, confirmed_by)
			SELECT tenant_id, legal_entity_id, receipt_id, line_no, $5, $6 FROM receiving_receipt_line
			WHERE tenant_id = $1 AND legal_entity_id = $2 AND receipt_id = $3 AND line_no = $4
			ON CONFLICT (tenant_id, receipt_id, line_no) DO UPDATE SET confirmed_price = EXCLUDED.confirmed_price, confirmed_by = EXCLUDED.confirmed_by, confirmed_at = now()`,
			tenantID, legalEntityID, receiptID, lineNo, price, by)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Postgres) ErsPriceConfirmations(ctx context.Context, receiptID string) (map[int]decimal.Decimal, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int]decimal.Decimal{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT line_no, confirmed_price FROM receiving_ers_price_confirmation WHERE tenant_id = $1 AND receipt_id = $2`, tenantID, receiptID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var lineNo int
			var price decimal.Decimal
			if err := rows.Scan(&lineNo, &price); err != nil {
				return err
			}
			out[lineNo] = price
		}
		return rows.Err()
	})
	return out, err
}
