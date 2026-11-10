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

	PartyTopic         = "passage.party.party"
	ProductTopic       = "passage.product.product"
	PurchaseOrderTopic = "passage.procurement.po"

	PartyCreatedType       = "dev.passage.party.party.created.v1"
	PartyUpdatedType       = "dev.passage.party.party.updated.v1"
	PartyMergedType        = "dev.passage.party.party.merged.v1"
	ProductCreatedType     = "dev.passage.product.product.created.v1"
	ProductUpdatedType     = "dev.passage.product.product.updated.v1"
	PurchaseOrderConfirmed = "dev.passage.procurement.po.confirmed.v1"
	PurchaseOrderCancelled = "dev.passage.procurement.po.cancelled.v1"

	partySource         = "/passage/party/party"
	productSource       = "passage://product"
	purchasingSource    = "/passage/procurement/purchasing"
	statusMerged        = "MERGED"
	statusPOConfirmed   = "CONFIRMED"
	statusPOCancelled   = "CANCELLED"
	maxMergedPartiesLen = 100
)

type Applier interface {
	Apply(ctx context.Context, r store.Record) (bool, error)
}

type partyData struct {
	PartyID     string `json:"party_id"`
	PartyNumber string `json:"party_number"`
	PartyType   string `json:"party_type"`
	Name        string `json:"name"`
	Status      string `json:"status"`
}

type partyMergedData struct {
	MergedPartyIDs []string `json:"mergedPartyIds"`
}

type productData struct {
	ProductID      string `json:"productId"`
	ProductNumber  string `json:"productNumber"`
	Name           string `json:"name"`
	ProductType    string `json:"productType"`
	LifecycleState string `json:"lifecycleState"`
}

type purchaseOrderData struct {
	POID          string `json:"po_id"`
	Number        string `json:"number"`
	LegalEntityID string `json:"legal_entity_id"`
	Currency      string `json:"currency"`
	Total         string `json:"total"`
}

// Handlers indexes party, product and purchase order events. Register them
// with a consumer named ConsumerName with Idempotent set: every write is an
// ordered upsert, so a redelivery changes nothing.
func Handlers(s Applier) map[string]eventing.Handler {
	h := func(f func(context.Context, Applier, events.Event) error) eventing.Handler {
		return eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error { return f(ctx, s, evt) })
	}
	return map[string]eventing.Handler{
		PartyCreatedType:       h(indexParty),
		PartyUpdatedType:       h(indexParty),
		PartyMergedType:        h(removeMergedParties),
		ProductCreatedType:     h(indexProduct),
		ProductUpdatedType:     h(indexProduct),
		PurchaseOrderConfirmed: h(indexPurchaseOrder),
		PurchaseOrderCancelled: h(indexPurchaseOrder),
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
	return apply(ctx, s, evt, store.Record{Type: store.TypeParty, ID: d.PartyID, Number: d.PartyNumber, Title: d.Name, Subtitle: d.PartyType, Status: d.Status})
}

func removeMergedParties(ctx context.Context, s Applier, evt events.Event) error {
	var d partyMergedData
	if err := decode(evt, partySource, &d); err != nil {
		return err
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
	return nil
}

func indexProduct(ctx context.Context, s Applier, evt events.Event) error {
	var d productData
	if err := decode(evt, productSource, &d); err != nil {
		return err
	}
	if !ids.Valid(d.ProductID) || d.ProductNumber == "" {
		return eventing.Permanent(fmt.Errorf("%s needs productId and productNumber", evt.Type))
	}
	return apply(ctx, s, evt, store.Record{Type: store.TypeProduct, ID: d.ProductID, Number: d.ProductNumber, Title: d.Name, Subtitle: d.ProductType, Status: d.LifecycleState})
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
	r := store.Record{Type: store.TypePurchaseOrder, ID: d.POID, LegalEntityID: d.LegalEntityID, Number: d.Number, Title: d.Number, Status: statusPOConfirmed}
	if evt.Type == PurchaseOrderCancelled {
		r.Status = statusPOCancelled
	} else if d.Currency != "" {
		r.Subtitle = d.Currency + " " + d.Total
	}
	return apply(tenant.With(ctx, scoped), s, evt, r)
}
