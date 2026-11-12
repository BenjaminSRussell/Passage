// Package autosend is the event-driven outbound of the EDI engine (#35, 35-1): when fulfillment publishes an advance ship notice
// (dev.passage.fulfillment.asn.issued.v1) or billing posts an invoice (dev.passage.billing.invoice.posted.v1), every active partner
// of that legal entity that asked for the document (partner_auto_outbound) gets its 856 or DESADV, or its 810 or INVOIC, generated
// from the source document exactly as POST /partners/{id}/outbound:from-source would.
//
// The consumer runs as the service principal edi-outbound in the legal entity of the event, with the one duty
// edi.outbound.from-event. Each document is generated under the same idempotency key the route uses (source, partner, set, source
// document), so a redelivered event, a replay from the dead-letter topic and a route call made by hand all yield one interchange.
package autosend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

const (
	// ConsumerName is the consumer (and Kafka group) and the service principal it runs as, service:edi-outbound.
	ConsumerName = "edi-outbound"
	// Duty is the only duty of that principal.
	Duty = "edi.outbound.from-event"

	ASNIssuedType      = "dev.passage.fulfillment.asn.issued.v1"
	InvoicePostedType  = "dev.passage.billing.invoice.posted.v1"
	FulfillmentSource  = "fulfillment"
	BillingSource      = "billing"
	FulfillmentTopic   = "passage.fulfillment.asn"
	BillingTopic       = "passage.billing.invoice"
	DeadLetterTopic    = "passage.edi.outbound.dlq"
	maxPartnersPerSend = 200
)

// Sender generates the outbound document of one source document for one partner. API.SendFromSource implements it.
type Sender interface {
	SendFromSource(ctx context.Context, partnerID, transactionSet, sourceID string, slip *domain.SlipRef) (store.OutboundResult, bool, error)
}

// Partners finds the partners of a legal entity that receive the given sets automatically.
type Partners interface {
	AutoPartners(ctx context.Context, legalEntityID string, want []string) ([]store.AutoPartner, error)
}

type asnData struct {
	ShipmentID        string `json:"shipment_id"`
	LegalEntityID     string `json:"legal_entity_id"`
	PackingSlipID     string `json:"packing_slip_id"`
	PackingSlipNumber string `json:"packing_slip_number"`
	Containers        []struct {
		SSCC string `json:"sscc"`
	} `json:"containers"`
}

// slip is the packing slip the notice is for. An event that names a slip always gets a notice for that slip and never the whole
// shipment's (which would send a slip's containers twice, once per slip): the SSCCs it lists are the notice's containers. ok is false
// when the event names a slip that lists no container, which has nothing to notify; a nil slip with ok true is a legacy event that names
// no slip at all, sent as the whole shipment.
func (d asnData) slip() (slip *domain.SlipRef, ok bool, err error) {
	if d.PackingSlipID == "" {
		return nil, true, nil
	}
	if !ids.Valid(d.PackingSlipID) {
		return nil, false, fmt.Errorf("asn.issued names the packing slip %q, which is not an id", d.PackingSlipID)
	}
	s := &domain.SlipRef{ID: d.PackingSlipID, Number: d.PackingSlipNumber}
	for _, c := range d.Containers {
		if c.SSCC != "" {
			s.SSCCs = append(s.SSCCs, c.SSCC)
		}
	}
	if len(s.SSCCs) == 0 {
		return nil, false, nil
	}
	return s, true, nil
}

type invoiceData struct {
	InvoiceID     string `json:"invoice_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Customer      string `json:"customer"`
}

// EInvoicer builds (and where registered submits) the e-invoices of a posted invoice. API.AutoEInvoice implements it; a Sender that
// also implements it makes the consumer e-invoice the customers registered under a mandate (35-2).
type EInvoicer interface {
	AutoEInvoice(ctx context.Context, legalEntityID, customerID, invoiceID string) error
}

// Handlers returns the consumer's handlers by event type. Register them with a consumer named ConsumerName with Idempotent set.
func Handlers(p Partners, s Sender) map[string]eventing.Handler {
	return map[string]eventing.Handler{
		ASNIssuedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			var d asnData
			if err := json.Unmarshal(evt.Data, &d); err != nil {
				return eventing.Permanent(fmt.Errorf("asn.issued data: %w", err))
			}
			slip, ok, err := d.slip()
			if err != nil {
				return eventing.Permanent(err)
			}
			if !ok {
				slog.InfoContext(ctx, "edi outbound skipped: the packing slip lists no container", "shipment", d.ShipmentID, "slip", d.PackingSlipID)
				return nil
			}
			return send(ctx, p, s, evt, FulfillmentSource, d.LegalEntityID, d.ShipmentID, slip, []string{"856", "DESADV"})
		}),
		InvoicePostedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			var d invoiceData
			if err := json.Unmarshal(evt.Data, &d); err != nil {
				return eventing.Permanent(fmt.Errorf("invoice.posted data: %w", err))
			}
			if err := send(ctx, p, s, evt, BillingSource, d.LegalEntityID, d.InvoiceID, nil, []string{"810", "INVOIC"}); err != nil {
				return err
			}
			return autoEInvoice(ctx, s, evt, d)
		}),
	}
}

func send(ctx context.Context, p Partners, s Sender, evt events.Event, source, legalEntityID, sourceID string, slip *domain.SlipRef, sets []string) error {
	actor, ok := tenant.FromContext(ctx)
	if !ok || !actor.HasDuty(Duty) {
		return eventing.Permanent(errors.New("outbound consumer has no principal with " + Duty))
	}
	if evt.Source != source {
		return eventing.Permanent(fmt.Errorf("%s from source %q, want %q", evt.Type, evt.Source, source))
	}
	if !ids.Valid(legalEntityID) || !ids.Valid(sourceID) {
		return eventing.Permanent(fmt.Errorf("%s names no legal entity or no source document", evt.Type))
	}
	scoped, err := tenant.ServicePrincipal(ConsumerName, actor.TenantID, legalEntityID)
	if err != nil {
		return eventing.Permanent(err)
	}
	ctx = tenant.With(ctx, scoped)
	partners, err := p.AutoPartners(ctx, legalEntityID, sets)
	if err != nil {
		return err
	}
	if len(partners) > maxPartnersPerSend {
		partners = partners[:maxPartnersPerSend]
	}
	var retry error
	for _, ap := range partners {
		for _, set := range ap.TransactionSets {
			_, created, err := s.SendFromSource(ctx, ap.PartnerID, set, sourceID, slip)
			switch kind := classify(err); kind {
			case outcomeSent:
				slog.InfoContext(ctx, "edi outbound sent from event", "type", evt.Type, "partner", ap.PartnerID, "set", set, "source", sourceID, "created", created)
			case outcomeSkip:
				// The document is not this partner's (another customer), is not final, or cannot be mapped: no retry changes that.
				slog.WarnContext(ctx, "edi outbound skipped", "type", evt.Type, "partner", ap.PartnerID, "set", set, "source", sourceID, "reason", err.Error())
			default:
				slog.ErrorContext(ctx, "edi outbound failed", "type", evt.Type, "partner", ap.PartnerID, "set", set, "source", sourceID, "err", err)
				if retry == nil {
					retry = err
				}
			}
		}
	}
	// The other partners were sent first; the retry repeats them as no-ops (same idempotency key) and tries the failed one again.
	return retry
}

// autoEInvoice builds the e-invoices of the posted invoice for the mandates its customer is registered under.
func autoEInvoice(ctx context.Context, s Sender, evt events.Event, d invoiceData) error {
	ei, ok := s.(EInvoicer)
	if !ok || !ids.Valid(d.Customer) {
		return nil
	}
	actor, ok := tenant.FromContext(ctx)
	if !ok {
		return eventing.Permanent(errors.New("outbound consumer has no principal"))
	}
	scoped, err := tenant.ServicePrincipal(ConsumerName, actor.TenantID, d.LegalEntityID)
	if err != nil {
		return eventing.Permanent(err)
	}
	err = ei.AutoEInvoice(tenant.With(ctx, scoped), d.LegalEntityID, d.Customer, d.InvoiceID)
	if classify(err) == outcomeSkip {
		slog.WarnContext(ctx, "e-invoice skipped", "type", evt.Type, "invoice", d.InvoiceID, "reason", err.Error())
		return nil
	}
	return err
}

type outcome int

const (
	outcomeSent outcome = iota
	outcomeSkip
	outcomeRetry
)

// SkipCodes are the problem codes that mean this document is not sendable to this partner, whatever the retries: the document
// belongs to another customer (EDI-0011), the partner has no trade profile or EDIFACT identity (EDI-0012, EDI-0017), the
// document cannot be mapped (EDI-0007), the partner is inactive (EDI-0008) or a body was sent under the key before (idempotency).
var SkipCodes = map[string]bool{"EDI-0007": true, "EDI-0008": true, "EDI-0011": true, "EDI-0012": true, "EDI-0017": true, "idempotency_key_reuse": true,
	"EDI-0027": true, "EDI-0030": true, "EDI-0031": true}

func classify(err error) outcome {
	if err == nil {
		return outcomeSent
	}
	var pr *httpx.Problem
	if errors.As(err, &pr) && SkipCodes[pr.Code] {
		return outcomeSkip
	}
	return outcomeRetry
}
