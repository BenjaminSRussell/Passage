package store

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

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
// PlanCreditItem). SourceType is INVOICE or CREDIT_NOTE, Number the document number its items are numbered from, OriginalID the
// invoice a credit note credits.
type BillingDocument struct {
	SourceType string
	ID         string
	Number     string
	OriginalID string
	// OriginalNumber is the credited invoice's number, the fallback for an invoice loaded by the opening-item import,
	// which has no billing document id.
	OriginalNumber string
	LegalEntityID  string
	CustomerID     string
	Currency       string
	DocumentDate   string
	Items          []domain.PlannedItem
}

// ErrOriginalInvoiceNotFound is a credit note whose invoice has no open items in its legal entity (yet). The caller retries,
// the invoice may still be on its way, and parks the note when the retries run out.
var ErrOriginalInvoiceNotFound = errors.New("original invoice not found in this legal entity")

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// ApplyBillingDocument opens the document's items in one transaction and reports how many it created. A document already
// applied with the same content creates nothing (0, nil), whether it comes again or races itself: (source type, document id,
// installment) is unique. Refused, so the caller parks the event: a document id already opened with other content or in
// another legal entity (422 AR-BILLING-MISMATCH, as is a credit note whose customer or currency differs from its invoice's), and
// a document number, or an installment number "<number>/<n>", already taken by an item that does not come from this document
// (entered by hand, imported, opened for an RMA credit): 409 AR-BILLING-NUMBER-TAKEN. A credit note whose invoice has no items
// in the legal entity returns ErrOriginalInvoiceNotFound.
func (s *Postgres) ApplyBillingDocument(ctx context.Context, d BillingDocument) (int, error) {
	p, _ := tenant.FromContext(ctx)
	created := 0
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		created = 0
		opened, err := openedWithSameContent(ctx, tx, d)
		if err != nil || opened {
			return err
		}
		if d.OriginalID != "" {
			var customer, currency string
			err := tx.QueryRow(ctx, `select customer_id::text, currency from open_item
				where source_type = 'INVOICE' and (source_doc_id = $1 or ($2 <> '' and source_doc_id is null and legal_entity_id = $3
				  and (document_number = $2 or document_number like $4 escape '\')))
				order by (source_doc_id is null) limit 1`, d.OriginalID, d.OriginalNumber, d.LegalEntityID, likeEscaper.Replace(d.OriginalNumber)+"/%").Scan(&customer, &currency)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrOriginalInvoiceNotFound
			}
			if err != nil {
				return err
			}
			if customer != d.CustomerID || currency != d.Currency {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeBillingMismatch,
					"the credit note's customer or currency differs from the invoice it credits")
			}
		}
		var taken bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from open_item
			where (document_number = $1 or document_number like $2 escape '\') and source_doc_id is distinct from $3::uuid)`,
			d.Number, likeEscaper.Replace(d.Number)+"/%", d.ID).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return numberTaken()
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
			if s.billingDocumentApplied(ctx, d) {
				return 0, nil
			}
			return 0, httpx.NewProblem(http.StatusUnprocessableEntity, CodeBillingMismatch,
				"this billing document id was already opened in another legal entity")
		case documentNumberKey:
			if s.billingDocumentApplied(ctx, d) {
				return 0, nil
			}
			return 0, numberTaken()
		}
	}
	return created, err
}

func numberTaken() error {
	return httpx.NewProblem(http.StatusConflict, CodeBillingNumberTaken,
		"an open item with this document number (or its installment numbers) already exists and does not come from this billing document")
}

// openedWithSameContent tells a document already opened here, whose items match the event's, from one not opened yet; a document
// id opened with other items, customer or currency is a conflict, not a repeat.
func openedWithSameContent(ctx context.Context, tx pgx.Tx, d BillingDocument) (bool, error) {
	rows, err := tx.Query(ctx, `select document_number, installment_no, customer_id::text, currency, amount::text from open_item
		where source_type = $1 and source_doc_id = $2 order by installment_no`, d.SourceType, d.ID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	n := 0
	same := true
	for rows.Next() {
		var number, customer, currency, amount string
		var installment int
		if err := rows.Scan(&number, &installment, &customer, &currency, &amount); err != nil {
			return false, err
		}
		if n >= len(d.Items) {
			same = false
		} else if it := d.Items[n]; it.DocumentNumber != number || it.InstallmentNo != installment || customer != d.CustomerID ||
			currency != d.Currency || !decimal.RequireFromString(amount).Equal(it.Amount) {
			same = false
		}
		n++
	}
	if err := rows.Err(); err != nil || n == 0 {
		return false, err
	}
	if !same || n != len(d.Items) {
		return false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeBillingMismatch,
			"this billing document id was already opened with different items, customer or currency")
	}
	return true, nil
}

// lockInvoiceItems locks and returns the open items of the invoice an RMA credit returns against: the items of the billing
// invoice id (one per installment), else the single item with the invoice number that carries no source id (entered by hand or
// imported). The first item anchors the credit; the amount is the sum of the installments.
func lockInvoiceItems(ctx context.Context, tx pgx.Tx, tenantID string, in RMACredit) (OpenItem, []string, decimal.Decimal, error) {
	billingID := ""
	if c, ok := ids.Canonical(in.InvoiceID); ok {
		billingID = c
	}
	rows, err := tx.Query(ctx, `select `+openItemColumns+` from open_item
		where tenant_id = $1 and legal_entity_id = $2 and source_type = 'INVOICE'
		and (source_doc_id = nullif($3, '')::uuid or document_number = $4 or (source_doc_id is not null and document_number like $5 escape '\'))
		order by installment_no nulls first, document_number for update`, tenantID, in.LegalEntityID, billingID, in.InvoiceNumber, likeEscaper.Replace(in.InvoiceNumber)+"/%")
	if err != nil {
		return OpenItem{}, nil, decimal.Zero, err
	}
	defer rows.Close()
	var anchor OpenItem
	var itemIDs []string
	total := decimal.Zero
	for rows.Next() {
		it, err := scanOpenItem(rows)
		if err != nil {
			return OpenItem{}, nil, decimal.Zero, err
		}
		if len(itemIDs) == 0 {
			anchor = it
		}
		itemIDs = append(itemIDs, it.ID)
		total = total.Add(it.Amount)
	}
	if err := rows.Err(); err != nil {
		return OpenItem{}, nil, decimal.Zero, err
	}
	if len(itemIDs) == 0 {
		return OpenItem{}, nil, decimal.Zero, httpx.NewProblem(http.StatusUnprocessableEntity, CodeRMAInvoice, "the original invoice is not an open item of this legal entity")
	}
	return anchor, itemIDs, total, nil
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
