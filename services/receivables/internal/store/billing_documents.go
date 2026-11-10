package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

const (
	CodeBillingNumberTaken = "AR-BILLING-NUMBER-TAKEN"
	CodeBillingMismatch    = "AR-BILLING-MISMATCH"
	sourceKeyIndex         = "open_item_source_key"
	documentNumberKey      = "open_item_tenant_id_legal_entity_id_document_number_key"
)

// BillingDocument is a posted billing invoice or credit note and the open items it opens (see domain.PlanInvoiceItems and
// PlanCreditItem). SourceType is INVOICE or CREDIT_NOTE, OriginalID the invoice a credit note credits.
type BillingDocument struct {
	SourceType    string
	ID            string
	OriginalID    string
	LegalEntityID string
	CustomerID    string
	Currency      string
	DocumentDate  string
	Items         []domain.PlannedItem
}

// ApplyBillingDocument opens the document's items in one transaction and reports how many it created. A document already
// applied creates nothing (0, nil), whether it comes again or races itself: (source type, document id, installment) is
// unique. A document number already taken by an item that does not come from this document (entered by hand, imported, opened
// for an RMA credit) is refused, 409 AR-BILLING-NUMBER-TAKEN, and a credit note whose customer, legal entity or currency
// differs from its original invoice's items is refused, 422 AR-BILLING-MISMATCH; the caller parks both.
func (s *Postgres) ApplyBillingDocument(ctx context.Context, d BillingDocument) (int, error) {
	p, _ := tenant.FromContext(ctx)
	created := 0
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		created = 0
		var existing int
		if err := tx.QueryRow(ctx, `select count(*) from open_item where source_type = $1 and source_doc_id = $2`, d.SourceType, d.ID).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			return nil
		}
		if d.OriginalID != "" {
			var customer, currency, entity string
			err := tx.QueryRow(ctx, `select customer_id::text, currency, legal_entity_id::text from open_item
				where source_type = 'INVOICE' and source_doc_id = $1 limit 1`, d.OriginalID).Scan(&customer, &currency, &entity)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil && (customer != d.CustomerID || currency != d.Currency || entity != d.LegalEntityID) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeBillingMismatch,
					"the credit note's customer, legal entity or currency differs from the invoice it credits")
			}
		}
		for _, it := range d.Items {
			var original any
			if d.OriginalID != "" {
				original = d.OriginalID
			}
			if _, err := tx.Exec(ctx, `insert into open_item (tenant_id, id, legal_entity_id, customer_id, source_type, document_number,
				document_date, due_date, currency, amount, open_amount, status, source_doc_id, installment_no, original_doc_id)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10, 'OPEN', $11, $12, $13)`,
				p.TenantID, ids.New(), d.LegalEntityID, d.CustomerID, d.SourceType, it.DocumentNumber, d.DocumentDate, it.DueDate,
				d.Currency, it.Amount.String(), d.ID, it.InstallmentNo, original); err != nil {
				return err
			}
			created++
		}
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case sourceKeyIndex:
			return 0, nil
		case documentNumberKey:
			if s.billingDocumentApplied(ctx, d) {
				return 0, nil
			}
			return 0, httpx.NewProblem(http.StatusConflict, CodeBillingNumberTaken,
				"an open item with this document number already exists and does not come from this billing document")
		}
	}
	return created, err
}

// billingDocumentApplied tells a document that raced itself (the other delivery committed first, so the unique number is its
// own) from a number taken by an item of another origin.
func (s *Postgres) billingDocumentApplied(ctx context.Context, d BillingDocument) bool {
	var n int
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from open_item where source_type = $1 and source_doc_id = $2`, d.SourceType, d.ID).Scan(&n)
	})
	return err == nil && n > 0
}
