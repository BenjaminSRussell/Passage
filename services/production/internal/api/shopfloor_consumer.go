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
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

const (
	// RegistrationPostedType is the shop floor's registration.posted.v1 (w25): a registration
	// was stored; a closed job-stop carries its hours, order, operation and worker rate.
	RegistrationPostedType  = "dev.passage.shopfloor.registration.posted.v1"
	RegistrationPostedTopic = "passage.shopfloor.registration"
	// RegistrationCorrectedType is the shop floor's registration.corrected.v1: a supervisor
	// correction, or a replayed kiosk stop superseding a server stop, changed a registration's hours.
	RegistrationCorrectedType  = "dev.passage.shopfloor.registration.corrected.v1"
	RegistrationCorrectedTopic = "passage.shopfloor.registration-corrected"
	// ShopfloorSource is the CloudEvents source the shop floor writes on its events.
	ShopfloorSource = "shopfloor"
	// ShopfloorConsumerName is the consumer (and Kafka group) that posts shop-floor labor as job
	// cards, and the service principal it runs as, service:production-shopfloor.
	ShopfloorConsumerName = "production-shopfloor"
	// DutyShopfloorJobCard lets the consumer post job cards from registrations. Only its service
	// principal holds it.
	DutyShopfloorJobCard = store.DutyShopfloorJobCard
)

// ShopfloorStore is the persistence the shop-floor consumer needs.
type ShopfloorStore interface {
	ShopfloorOrderLegalEntity(ctx context.Context, orderRef string) (string, bool, error)
	ShopfloorCardLegalEntity(ctx context.Context, registrationID string) (string, bool, error)
	PostShopfloorJobCard(ctx context.Context, in store.ShopfloorJobCard) (store.Journal, bool, error)
	AdjustShopfloorJobCard(ctx context.Context, registrationID string, newHours decimal.Decimal, traceID string) (bool, error)
}

type registrationPostedData struct {
	RegistrationID   string           `json:"registrationId"`
	Kind             string           `json:"kind"`
	Hours            *decimal.Decimal `json:"hours"`
	OrderRef         string           `json:"orderRef"`
	OperationRef     string           `json:"operationRef"`
	CostRate         *decimal.Decimal `json:"costRate"`
	CostRateCurrency string           `json:"costRateCurrency"`
}

type registrationCorrectedData struct {
	RegistrationID string           `json:"registrationId"`
	Kind           string           `json:"kind"`
	Hours          *decimal.Decimal `json:"hours"`
}

// ShopfloorConsumerHandlers posts shop-floor labor to the production order (AC-25.1, BR-25.9):
// a closed job-stop in registration.posted becomes a posted job card at the worker's cost rate,
// and a registration.corrected re-books that card at the new hours. The order's legal entity is
// read first and the card is posted as the service principal pinned to it, so the WIP rows and
// their GL posting stay in that legal entity's ledger. A refusal that a retry cannot fix (an
// unknown order or operation, an order that no longer takes cards) is parked on the dead-letter
// topic; anything else is returned and retried with the consumer's backoff. The store calls are
// idempotent by registration id, so a redelivery after a crash books nothing twice.
// Register it with a consumer named ShopfloorConsumerName.
func ShopfloorConsumerHandlers(s ShopfloorStore) map[string]eventing.Handler {
	h := shopfloorConsumer{store: s}
	return map[string]eventing.Handler{
		RegistrationPostedType:    eventing.HandlerFunc(h.registrationPosted),
		RegistrationCorrectedType: eventing.HandlerFunc(h.registrationCorrected),
	}
}

type shopfloorConsumer struct{ store ShopfloorStore }

func (c shopfloorConsumer) check(ctx context.Context, evt events.Event, out any) error {
	p, ok := tenant.FromContext(ctx)
	if !ok || !p.HasDuty(DutyShopfloorJobCard) {
		return eventing.Permanent(errors.New("shop-floor consumer has no principal"))
	}
	if evt.Source != ShopfloorSource {
		return eventing.Permanent(fmt.Errorf("%s from source %q, want %q", evt.Type, evt.Source, ShopfloorSource))
	}
	if err := json.Unmarshal(evt.Data, out); err != nil {
		return eventing.Permanent(fmt.Errorf("%s data: %w", evt.Type, err))
	}
	return nil
}

// pinned is ctx with the consumer's principal narrowed to one legal entity.
func pinned(ctx context.Context, evt events.Event, legalEntityID string) (context.Context, error) {
	p, err := tenant.ServicePrincipal(ShopfloorConsumerName, evt.TenantID, legalEntityID)
	if err != nil {
		return nil, eventing.Permanent(err)
	}
	return tenant.With(ctx, p), nil
}

// classify parks a permanent refusal and leaves any other error to the retry.
func classify(err error) error {
	if errors.Is(err, store.ErrJobCardRefused) {
		return eventing.Permanent(err)
	}
	return err
}

func (c shopfloorConsumer) registrationPosted(ctx context.Context, _ pgx.Tx, evt events.Event) error {
	var d registrationPostedData
	if err := c.check(ctx, evt, &d); err != nil {
		return err
	}
	// Clock punches, absences, open stops and stops on unknown jobs carry no order labor.
	if d.Kind != "job-stop" || d.Hours == nil || d.OrderRef == "" {
		return nil
	}
	le, ok, err := c.store.ShopfloorOrderLegalEntity(ctx, d.OrderRef)
	if err != nil {
		return classify(err)
	}
	if !ok {
		return eventing.Permanent(fmt.Errorf("%w: registration %s names order %q, which production does not hold", store.ErrJobCardRefused, d.RegistrationID, d.OrderRef))
	}
	pctx, err := pinned(ctx, evt, le)
	if err != nil {
		return err
	}
	j, posted, err := c.store.PostShopfloorJobCard(pctx, store.ShopfloorJobCard{
		RegistrationID: d.RegistrationID, Kind: d.Kind, OrderRef: d.OrderRef, OperationRef: d.OperationRef,
		Hours: d.Hours, CostRate: d.CostRate, CostRateCurrency: d.CostRateCurrency,
		TraceID: evt.ID, Actor: tenant.ServiceActor(ShopfloorConsumerName),
	})
	if err != nil {
		slog.WarnContext(ctx, "shop-floor job card not posted", "registrationId", d.RegistrationID, "orderId", d.OrderRef, "err", err, "eventId", evt.ID)
		return classify(err)
	}
	slog.InfoContext(ctx, "shop-floor job card", "registrationId", d.RegistrationID, "orderId", d.OrderRef, "journalId", j.ID, "posted", posted, "eventId", evt.ID)
	return nil
}

func (c shopfloorConsumer) registrationCorrected(ctx context.Context, _ pgx.Tx, evt events.Event) error {
	var d registrationCorrectedData
	if err := c.check(ctx, evt, &d); err != nil {
		return err
	}
	if d.Kind != "job-stop" || d.Hours == nil {
		return nil
	}
	le, ok, err := c.store.ShopfloorCardLegalEntity(ctx, d.RegistrationID)
	if err != nil {
		return classify(err)
	}
	if !ok {
		// The two events travel on different topics, so a correction can overtake the
		// registration.posted that books the card. Retrying with backoff lets the card land
		// first; a registration that never books one (no order) is parked after the retries.
		return fmt.Errorf("registration %s has no shop-floor job card yet", d.RegistrationID)
	}
	pctx, err := pinned(ctx, evt, le)
	if err != nil {
		return err
	}
	changed, err := c.store.AdjustShopfloorJobCard(pctx, d.RegistrationID, *d.Hours, evt.ID)
	if err != nil {
		return classify(err)
	}
	slog.InfoContext(ctx, "shop-floor job card corrected", "registrationId", d.RegistrationID, "hours", d.Hours.String(), "changed", changed, "eventId", evt.ID)
	return nil
}
