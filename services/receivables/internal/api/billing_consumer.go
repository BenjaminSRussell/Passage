package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

// The billing consumer (#30, 30-1) opens the receivables of billing's posted documents. invoice.posted opens one open item per
// installment of the invoice (or one for the gross), creditnote.posted one credit item of minus the credit note's gross linked
// to the invoice it credits. Each document is keyed by (source type, billing document id, installment) in the store, so a
// replay, a redelivery or a second event for the same document creates nothing. An event the store or the plan refuses (a bad
// amount, installments that do not add up to the gross, a number already taken by an item entered by hand or imported, a
// credit note for another customer or currency than its invoice) is parked on the dead-letter topic with the reason logged,
// never dropped and never half-applied; a database failure is retried with back-off first.
//
// An RMA credit and a billing credit note for the same return do not both open a credit item: the RMA credit reserves the
// returned invoice lines in billing before it opens its item (billing's credit-reservation counts against the invoice exactly
// like a posted credit note), so billing refuses a credit note for the room an RMA credit holds, and no credit note event
// exists for that return. Only credit notes billing posted reach this consumer.
const (
	// BillingConsumerName is the consumer (and Kafka group) and the service principal it runs as, service:receivables-billing.
	BillingConsumerName = "receivables-billing"
	// BillingInvoiceTopic carries billing's invoice.posted and creditnote.posted (billing routes its outbox by aggregate, so
	// invoices and credit notes share the topic; the consumer skips the other types).
	BillingInvoiceTopic = "passage.billing.invoice"
	BillingSource       = "billing"

	InvoicePostedType    = "dev.passage.billing.invoice.posted.v1"
	CreditNotePostedType = "dev.passage.billing.creditnote.posted.v1"

	dutyBillingOpenItem = "receivables.openitem.from-billing"
)

// BillingStore is what the consumer needs of the store.
type BillingStore interface {
	ApplyBillingDocument(ctx context.Context, d store.BillingDocument) (int, error)
}

type billingTotals struct {
	Gross string `json:"gross"`
}

type invoicePostedData struct {
	InvoiceID     string        `json:"invoice_id"`
	Number        string        `json:"number"`
	LegalEntityID string        `json:"legal_entity_id"`
	Customer      string        `json:"customer"`
	Currency      string        `json:"currency"`
	InvoiceDate   string        `json:"invoice_date"`
	DueDate       string        `json:"due_date"`
	Totals        billingTotals `json:"totals"`
	Installments  []struct {
		No      int    `json:"no"`
		Amount  string `json:"amount"`
		DueDate string `json:"due_date"`
	} `json:"installments"`
}

type creditNotePostedData struct {
	CreditNoteID      string        `json:"credit_note_id"`
	Number            string        `json:"number"`
	OriginalInvoiceID string        `json:"original_invoice_id"`
	LegalEntityID     string        `json:"legal_entity_id"`
	Customer          string        `json:"customer"`
	Currency          string        `json:"currency"`
	CreditDate        string        `json:"credit_date"`
	PostingDate       string        `json:"posting_date"`
	Totals            billingTotals `json:"totals"`
}

// BillingConsumerHandlers handles invoice.posted and creditnote.posted. Register it with a consumer named BillingConsumerName
// with Idempotent set: the store keys each document itself, and the inbox records the event once it is applied.
func BillingConsumerHandlers(s BillingStore) map[string]eventing.Handler {
	return map[string]eventing.Handler{
		InvoicePostedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			return openInvoice(ctx, s, evt)
		}),
		CreditNotePostedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			return openCreditNote(ctx, s, evt)
		}),
	}
}

func billingScope(ctx context.Context, evt events.Event, legalEntityID string) (context.Context, error) {
	actor, ok := tenant.FromContext(ctx)
	if !ok || !actor.HasDuty(dutyBillingOpenItem) {
		return nil, eventing.Permanent(errors.New("billing consumer has no principal with " + dutyBillingOpenItem))
	}
	if evt.Source != BillingSource {
		return nil, eventing.Permanent(fmt.Errorf("%s from source %q, want %q", evt.Type, evt.Source, BillingSource))
	}
	if !ids.Valid(legalEntityID) {
		return nil, eventing.Permanent(fmt.Errorf("%s has no legal_entity_id", evt.Type))
	}
	scoped, err := tenant.ServicePrincipal(BillingConsumerName, actor.TenantID, legalEntityID)
	if err != nil {
		return nil, eventing.Permanent(err)
	}
	return tenant.With(ctx, scoped), nil
}

func canonicalIDs(evt events.Event, named map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(named))
	for what, id := range named {
		c, ok := ids.Canonical(id)
		if !ok {
			return nil, eventing.Permanent(fmt.Errorf("%s: %s %q is not an id", evt.Type, what, id))
		}
		out[what] = c
	}
	return out, nil
}

func openInvoice(ctx context.Context, s BillingStore, evt events.Event) error {
	var d invoicePostedData
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return eventing.Permanent(fmt.Errorf("invoice.posted data: %w", err))
	}
	ctx, err := billingScope(ctx, evt, d.LegalEntityID)
	if err != nil {
		return err
	}
	c, err := canonicalIDs(evt, map[string]string{"invoice_id": d.InvoiceID, "legal_entity_id": d.LegalEntityID, "customer": d.Customer})
	if err != nil {
		return err
	}
	gross, err := decimal.NewFromString(d.Totals.Gross)
	if err != nil {
		return eventing.Permanent(fmt.Errorf("invoice.posted %s: gross %q is not an amount", d.Number, d.Totals.Gross))
	}
	installments := make([]domain.BillingInstallment, len(d.Installments))
	for i, in := range d.Installments {
		a, err := decimal.NewFromString(in.Amount)
		if err != nil {
			return eventing.Permanent(fmt.Errorf("invoice.posted %s: installment %d amount %q is not an amount", d.Number, in.No, in.Amount))
		}
		installments[i] = domain.BillingInstallment{No: in.No, Amount: a, DueDate: in.DueDate}
	}
	items, err := domain.PlanInvoiceItems(d.Number, d.Currency, gross, d.DueDate, installments)
	if err != nil {
		return eventing.Permanent(fmt.Errorf("invoice.posted %s: %w", d.Number, err))
	}
	return apply(ctx, s, evt, store.BillingDocument{SourceType: "INVOICE", ID: c["invoice_id"], LegalEntityID: c["legal_entity_id"],
		CustomerID: c["customer"], Currency: d.Currency, DocumentDate: d.InvoiceDate, Items: items}, d.Number)
}

func openCreditNote(ctx context.Context, s BillingStore, evt events.Event) error {
	var d creditNotePostedData
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return eventing.Permanent(fmt.Errorf("creditnote.posted data: %w", err))
	}
	ctx, err := billingScope(ctx, evt, d.LegalEntityID)
	if err != nil {
		return err
	}
	named := map[string]string{"credit_note_id": d.CreditNoteID, "legal_entity_id": d.LegalEntityID, "customer": d.Customer}
	if d.OriginalInvoiceID != "" {
		named["original_invoice_id"] = d.OriginalInvoiceID
	}
	c, err := canonicalIDs(evt, named)
	if err != nil {
		return err
	}
	gross, err := decimal.NewFromString(d.Totals.Gross)
	if err != nil {
		return eventing.Permanent(fmt.Errorf("creditnote.posted %s: gross %q is not an amount", d.Number, d.Totals.Gross))
	}
	item, err := domain.PlanCreditItem(d.Number, d.Currency, gross, d.PostingDate)
	if err != nil {
		return eventing.Permanent(fmt.Errorf("creditnote.posted %s: %w", d.Number, err))
	}
	return apply(ctx, s, evt, store.BillingDocument{SourceType: "CREDIT_NOTE", ID: c["credit_note_id"], OriginalID: c["original_invoice_id"],
		LegalEntityID: c["legal_entity_id"], CustomerID: c["customer"], Currency: d.Currency, DocumentDate: d.CreditDate,
		Items: []domain.PlannedItem{item}}, d.Number)
}

func apply(ctx context.Context, s BillingStore, evt events.Event, doc store.BillingDocument, number string) error {
	if _, err := store.ParseDay(doc.DocumentDate); err != nil {
		return eventing.Permanent(fmt.Errorf("%s %s: the document date %q is not a date", evt.Type, number, doc.DocumentDate))
	}
	created, err := s.ApplyBillingDocument(ctx, doc)
	var p *httpx.Problem
	switch {
	case err == nil:
		slog.InfoContext(ctx, "billing document opened in receivables", "type", evt.Type, "document", number, "documentId", doc.ID,
			"legalEntityId", doc.LegalEntityID, "openItems", created, "replay", created == 0)
		return nil
	case errors.As(err, &p) && p.Status >= 400 && p.Status < 500:
		return eventing.Permanent(fmt.Errorf("%s %s: %s %s", evt.Type, number, p.Code, p.Detail))
	}
	return fmt.Errorf("%s %s: %w", evt.Type, number, err)
}
