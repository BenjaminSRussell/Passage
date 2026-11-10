package index

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/search/internal/store"
)

const (
	ConsumerName = "search-index"

	PartyTopic          = "passage.party.party"
	ProductTopic        = "passage.product.product"
	PurchaseOrderTopic  = "passage.procurement.purchase_order"
	ReceiptTopic        = "passage.receiving.receipt"
	VendorInvoiceTopic  = "passage.receiving.vendorinvoice"
	SalesOrderTopic     = "passage.sales.order"
	ShipmentTopic       = "passage.fulfillment.shipment"
	BillingInvoiceTopic = "passage.billing.invoice"

	PartyCreatedType       = "dev.passage.party.party.created.v1"
	PartyUpdatedType       = "dev.passage.party.party.updated.v1"
	PartyMergedType        = "dev.passage.party.party.merged.v1"
	ProductCreatedType     = "dev.passage.product.product.created.v1"
	ProductUpdatedType     = "dev.passage.product.product.updated.v1"
	PurchaseOrderConfirmed = "dev.passage.procurement.po.confirmed.v1"
	PurchaseOrderCancelled = "dev.passage.procurement.po.cancelled.v1"

	ReceiptPostedType         = "dev.passage.receiving.receipt.posted.v1"
	ReceiptCorrectedType      = "dev.passage.receiving.receipt.corrected.v1"
	VendorInvoicePostedType   = "dev.passage.payables.invoice.posted.v1"
	SalesOrderConfirmedType   = "dev.passage.sales.order.confirmed.v1"
	PackingSlipPostedType     = "dev.passage.fulfillment.packingslip.posted.v1"
	PackingSlipCancelledType  = "dev.passage.fulfillment.packingslip.cancelled.v1"
	CustomerInvoicePostedType = "dev.passage.billing.invoice.posted.v1"
	CreditNotePostedType      = "dev.passage.billing.creditnote.posted.v1"

	receivingSource   = "/passage/receiving/receiving"
	salesSource       = "sales"
	fulfillmentSource = "fulfillment"
	billingSource     = "billing"
	statusPosted      = "POSTED"
	statusCancelled   = "CANCELLED"
	statusConfirmed   = "CONFIRMED"

	partySource         = "/passage/party/party"
	productSource       = "passage://product"
	purchasingSource    = "/passage/procurement/purchasing"
	statusMerged        = "MERGED"
	statusPOConfirmed   = "CONFIRMED"
	statusPOCancelled   = "CANCELLED"
	maxMergedPartiesLen = 100
)

var confirmedBefore = map[string]bool{"CONFIRMED": true, "FINALIZED": true}

type Applier interface {
	Apply(ctx context.Context, r store.Record) (bool, error)
}

type partyData struct {
	PartyID     string `json:"party_id"`
	PartyNumber string `json:"party_number"`
	PartyType   string `json:"party_type"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Version     int    `json:"version"`
}

type partyMergedData struct {
	SurvivorPartyID string   `json:"survivorPartyId"`
	MergedPartyIDs  []string `json:"mergedPartyIds"`
	Survivor        struct {
		PartyNumber string `json:"partyNumber"`
		PartyType   string `json:"partyType"`
		Name        string `json:"name"`
		Status      string `json:"status"`
		Version     int    `json:"version"`
	} `json:"survivor"`
}

type productData struct {
	ProductID      string `json:"productId"`
	ProductNumber  string `json:"productNumber"`
	Name           string `json:"name"`
	ProductType    string `json:"productType"`
	LifecycleState string `json:"lifecycleState"`
	Version        int    `json:"version"`
}

type purchaseOrderData struct {
	POID          string `json:"po_id"`
	Number        string `json:"number"`
	LegalEntityID string `json:"legal_entity_id"`
	Currency      string `json:"currency"`
	Total         string `json:"total"`
	VersionNo     int    `json:"version_no"`
	PriorStatus   string `json:"prior_status"`
}

// Handlers indexes party, product and purchase order events. Register them
// with a consumer named ConsumerName with Idempotent set: every write is an
// ordered upsert, so a redelivery changes nothing.
func Handlers(s Applier) map[string]eventing.Handler {
	h := func(f func(context.Context, Applier, events.Event) error) eventing.Handler {
		return eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error { return f(ctx, s, evt) })
	}
	return map[string]eventing.Handler{
		PartyCreatedType:          h(indexParty),
		PartyUpdatedType:          h(indexParty),
		PartyMergedType:           h(removeMergedParties),
		ProductCreatedType:        h(indexProduct),
		ProductUpdatedType:        h(indexProduct),
		PurchaseOrderConfirmed:    h(indexPurchaseOrder),
		PurchaseOrderCancelled:    h(indexPurchaseOrder),
		ReceiptPostedType:         h(indexReceiptPosted),
		ReceiptCorrectedType:      h(indexReceiptCorrected),
		VendorInvoicePostedType:   h(indexVendorInvoice),
		SalesOrderConfirmedType:   h(indexSalesOrder),
		PackingSlipPostedType:     h(indexPackingSlipPosted),
		PackingSlipCancelledType:  h(indexPackingSlipCancelled),
		CustomerInvoicePostedType: h(indexBillingDocument),
		CreditNotePostedType:      h(indexBillingDocument),
	}
}

func decode(evt events.Event, wantSource string, into any) error {
	if evt.Source != wantSource {
		return eventing.Permanent(fmt.Errorf("%s from source %q, want %q", evt.Type, evt.Source, wantSource))
	}
	if evt.Time.IsZero() {
		return eventing.Permanent(fmt.Errorf("%s %s has no time", evt.Type, evt.ID))
	}
	if err := json.Unmarshal(evt.Data, into); err != nil {
		return eventing.Permanent(fmt.Errorf("%s data: %w", evt.Type, err))
	}
	return nil
}

func apply(ctx context.Context, s Applier, evt events.Event, r store.Record) error {
	r.At = evt.Time.UTC()
	applied, err := s.Apply(ctx, r)
	if err != nil {
		return fmt.Errorf("index %s %s: %w", r.Type, r.ID, err)
	}
	slog.InfoContext(ctx, "search record indexed", "type", r.Type, "recordId", r.ID, "applied", applied, "eventId", evt.ID)
	return nil
}

func indexParty(ctx context.Context, s Applier, evt events.Event) error {
	var d partyData
	if err := decode(evt, partySource, &d); err != nil {
		return err
	}
	if !ids.Valid(d.PartyID) || d.PartyNumber == "" {
		return eventing.Permanent(fmt.Errorf("%s needs party_id and party_number", evt.Type))
	}
	return apply(ctx, s, evt, store.Record{Type: store.TypeParty, ID: d.PartyID, Number: d.PartyNumber, Title: d.Name, Subtitle: d.PartyType, Status: d.Status, Version: d.Version})
}

func removeMergedParties(ctx context.Context, s Applier, evt events.Event) error {
	var d partyMergedData
	if err := decode(evt, partySource, &d); err != nil {
		return err
	}
	if d.SurvivorPartyID != "" && !ids.Valid(d.SurvivorPartyID) {
		return eventing.Permanent(fmt.Errorf("%s names survivor %q, not an id", evt.Type, d.SurvivorPartyID))
	}
	if len(d.MergedPartyIDs) == 0 || len(d.MergedPartyIDs) > maxMergedPartiesLen {
		return eventing.Permanent(fmt.Errorf("%s names %d merged parties", evt.Type, len(d.MergedPartyIDs)))
	}
	for _, id := range d.MergedPartyIDs {
		if !ids.Valid(id) {
			return eventing.Permanent(fmt.Errorf("%s names merged party %q, not an id", evt.Type, id))
		}
		if err := apply(ctx, s, evt, store.Record{Type: store.TypeParty, ID: id, Status: statusMerged, Removed: true}); err != nil {
			return err
		}
	}
	// A merge without the survivor's data still takes the losers out; the survivor is indexed by its own party events.
	if d.SurvivorPartyID == "" || d.Survivor.PartyNumber == "" {
		return nil
	}
	return apply(ctx, s, evt, store.Record{Type: store.TypeParty, ID: d.SurvivorPartyID, Number: d.Survivor.PartyNumber, Title: d.Survivor.Name,
		Subtitle: d.Survivor.PartyType, Status: d.Survivor.Status, Version: d.Survivor.Version})
}

func indexProduct(ctx context.Context, s Applier, evt events.Event) error {
	var d productData
	if err := decode(evt, productSource, &d); err != nil {
		return err
	}
	if !ids.Valid(d.ProductID) || d.ProductNumber == "" {
		return eventing.Permanent(fmt.Errorf("%s needs productId and productNumber", evt.Type))
	}
	return apply(ctx, s, evt, store.Record{Type: store.TypeProduct, ID: d.ProductID, Number: d.ProductNumber, Title: d.Name, Subtitle: d.ProductType, Status: d.LifecycleState, Version: d.Version})
}

func indexPurchaseOrder(ctx context.Context, s Applier, evt events.Event) error {
	var d purchaseOrderData
	if err := decode(evt, purchasingSource, &d); err != nil {
		return err
	}
	if !ids.Valid(d.POID) || !ids.Valid(d.LegalEntityID) || d.Number == "" {
		return eventing.Permanent(fmt.Errorf("%s needs po_id, legal_entity_id and number", evt.Type))
	}
	scoped, err := tenant.ServicePrincipal(ConsumerName, evt.TenantID, d.LegalEntityID)
	if err != nil {
		return eventing.Permanent(err)
	}
	r := store.Record{Type: store.TypePurchaseOrder, ID: d.POID, LegalEntityID: d.LegalEntityID, Number: d.Number, Title: d.Number, Status: statusPOConfirmed, Version: d.VersionNo}
	if evt.Type == PurchaseOrderCancelled {
		r.Status = statusPOCancelled
		r.UpdateOnly = !confirmedBefore[d.PriorStatus]
	} else if d.Currency != "" {
		r.Subtitle = d.Currency + " " + d.Total
	}
	return apply(tenant.With(ctx, scoped), s, evt, r)
}

// scoped indexes one record of a legal-entity-scoped type under a service
// principal limited to that entity. Version is zero for these types: the
// source events carry none, so the event time orders them.
func scoped(ctx context.Context, s Applier, evt events.Event, r store.Record) error {
	if !ids.Valid(r.ID) || !ids.Valid(r.LegalEntityID) || r.Number == "" {
		return eventing.Permanent(fmt.Errorf("%s needs an id, a legal entity and a number", evt.Type))
	}
	p, err := tenant.ServicePrincipal(ConsumerName, evt.TenantID, r.LegalEntityID)
	if err != nil {
		return eventing.Permanent(err)
	}
	return apply(tenant.With(ctx, p), s, evt, r)
}

func subtitle(currency, amount string) string {
	if currency == "" || amount == "" {
		return ""
	}
	return currency + " " + amount
}

func indexReceiptPosted(ctx context.Context, s Applier, evt events.Event) error {
	var d struct {
		ReceiptID     string `json:"receipt_id"`
		ReceiptNumber string `json:"receipt_number"`
		LegalEntityID string `json:"legal_entity_id"`
		PostingDate   string `json:"posting_date"`
	}
	if err := decode(evt, receivingSource, &d); err != nil {
		return err
	}
	return scoped(ctx, s, evt, store.Record{Type: store.TypeReceipt, ID: d.ReceiptID, LegalEntityID: d.LegalEntityID, Number: d.ReceiptNumber,
		Title: d.ReceiptNumber, Subtitle: d.PostingDate, Status: statusPosted})
}

// indexReceiptCorrected marks a cancelled receipt; a correction leaves the
// row as it is.
func indexReceiptCorrected(ctx context.Context, s Applier, evt events.Event) error {
	var d struct {
		ReceiptID     string `json:"receipt_id"`
		ReceiptNumber string `json:"receipt_number"`
		LegalEntityID string `json:"legal_entity_id"`
		Kind          string `json:"kind"`
	}
	if err := decode(evt, receivingSource, &d); err != nil {
		return err
	}
	if d.Kind != "CANCELLATION" {
		return nil
	}
	return scoped(ctx, s, evt, store.Record{Type: store.TypeReceipt, ID: d.ReceiptID, LegalEntityID: d.LegalEntityID, Number: d.ReceiptNumber,
		Status: statusCancelled, UpdateOnly: true})
}

func indexVendorInvoice(ctx context.Context, s Applier, evt events.Event) error {
	var d struct {
		InvoiceID     string `json:"invoice_id"`
		InvoiceNumber string `json:"invoice_number"`
		LegalEntityID string `json:"legal_entity_id"`
		Currency      string `json:"currency"`
		Total         string `json:"total"`
	}
	if err := decode(evt, receivingSource, &d); err != nil {
		return err
	}
	return scoped(ctx, s, evt, store.Record{Type: store.TypeVendorInvoice, ID: d.InvoiceID, LegalEntityID: d.LegalEntityID, Number: d.InvoiceNumber,
		Title: d.InvoiceNumber, Subtitle: subtitle(d.Currency, d.Total), Status: statusPosted})
}

func indexSalesOrder(ctx context.Context, s Applier, evt events.Event) error {
	var d struct {
		OrderID       string `json:"order_id"`
		OrderNumber   string `json:"order_number"`
		LegalEntityID string `json:"legal_entity_id"`
		Currency      string `json:"currency"`
		NetTotal      string `json:"net_total"`
		Version       int    `json:"version"`
	}
	if err := decode(evt, salesSource, &d); err != nil {
		return err
	}
	return scoped(ctx, s, evt, store.Record{Type: store.TypeSalesOrder, ID: d.OrderID, LegalEntityID: d.LegalEntityID, Number: d.OrderNumber,
		Title: d.OrderNumber, Subtitle: subtitle(d.Currency, d.NetTotal), Status: statusConfirmed, Version: d.Version})
}

type packingSlipData struct {
	PackingSlipID     string `json:"packing_slip_id"`
	PackingSlipNumber string `json:"packing_slip_number"`
	LegalEntityID     string `json:"legal_entity_id"`
	PostingDate       string `json:"posting_date"`
}

func indexPackingSlipPosted(ctx context.Context, s Applier, evt events.Event) error {
	var d packingSlipData
	if err := decode(evt, fulfillmentSource, &d); err != nil {
		return err
	}
	return scoped(ctx, s, evt, store.Record{Type: store.TypePackingSlip, ID: d.PackingSlipID, LegalEntityID: d.LegalEntityID, Number: d.PackingSlipNumber,
		Title: d.PackingSlipNumber, Subtitle: d.PostingDate, Status: statusPosted})
}

func indexPackingSlipCancelled(ctx context.Context, s Applier, evt events.Event) error {
	var d packingSlipData
	if err := decode(evt, fulfillmentSource, &d); err != nil {
		return err
	}
	return scoped(ctx, s, evt, store.Record{Type: store.TypePackingSlip, ID: d.PackingSlipID, LegalEntityID: d.LegalEntityID, Number: d.PackingSlipNumber,
		Status: statusCancelled, UpdateOnly: true})
}

// indexBillingDocument indexes a posted customer invoice or credit note. A
// credit note carries credit_note_id and its own number; both are customer
// documents opened on the invoice page.
func indexBillingDocument(ctx context.Context, s Applier, evt events.Event) error {
	var d struct {
		InvoiceID     string `json:"invoice_id"`
		CreditNoteID  string `json:"credit_note_id"`
		Number        string `json:"number"`
		LegalEntityID string `json:"legal_entity_id"`
		Currency      string `json:"currency"`
		Totals        struct {
			Gross string `json:"gross"`
		} `json:"totals"`
	}
	if err := decode(evt, billingSource, &d); err != nil {
		return err
	}
	id := d.InvoiceID
	if id == "" {
		id = d.CreditNoteID
	}
	return scoped(ctx, s, evt, store.Record{Type: store.TypeCustomerInvoice, ID: id, LegalEntityID: d.LegalEntityID, Number: d.Number,
		Title: d.Number, Subtitle: subtitle(d.Currency, d.Totals.Gross), Status: statusPosted})
}
