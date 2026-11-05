package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

type sfFake struct {
	orderLE, cardLE map[string]string
	lookupErr       error
	postErr         error
	adjustErr       error
	posted          []store.ShopfloorJobCard
	postedAs        []tenant.Principal
	adjusted        []string
	adjustedAs      []tenant.Principal
}

func (f *sfFake) ShopfloorOrderLegalEntity(_ context.Context, orderRef string) (string, bool, error) {
	le, ok := f.orderLE[orderRef]
	return le, ok, f.lookupErr
}

func (f *sfFake) ShopfloorCardLegalEntity(_ context.Context, reg string) (string, bool, error) {
	le, ok := f.cardLE[reg]
	return le, ok, f.lookupErr
}

func (f *sfFake) PostShopfloorJobCard(ctx context.Context, in store.ShopfloorJobCard) (store.Journal, bool, error) {
	p, _ := tenant.FromContext(ctx)
	f.posted, f.postedAs = append(f.posted, in), append(f.postedAs, p)
	return store.Journal{ID: ids.New(), Posted: f.postErr == nil}, f.postErr == nil, f.postErr
}

func (f *sfFake) AdjustShopfloorJobCard(ctx context.Context, reg string, hours decimal.Decimal, _ string) (bool, error) {
	p, _ := tenant.FromContext(ctx)
	f.adjusted, f.adjustedAs = append(f.adjusted, reg+"="+hours.String()), append(f.adjustedAs, p)
	return true, f.adjustErr
}

var sfTenant = testkit.TenantID(7301)

func sfCtx(t *testing.T) context.Context {
	t.Helper()
	p, err := tenant.ServicePrincipal(ShopfloorConsumerName, sfTenant, "")
	if err != nil {
		t.Fatal(err)
	}
	return tenant.With(context.Background(), p)
}

func sfEvent(t *testing.T, typ string, data map[string]any) events.Event {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return events.Event{ID: ids.New(), Type: typ, Source: ShopfloorSource, TenantID: sfTenant, Data: raw}
}

func stopData(reg, order string) map[string]any {
	return map[string]any{"registrationId": reg, "workerId": ids.New(), "kind": "job-stop", "hours": "2.00",
		"orderRef": order, "operationRef": "10", "costRate": "26.00", "costRateCurrency": "USD", "flags": []string{}}
}

// AC-25.1: a closed job-stop posts its job card as the consumer principal pinned to the
// order's legal entity, at the worker rate the event carries.
func TestShopfloorConsumer_PostsJobCardPinnedToTheOrdersLegalEntity(t *testing.T) {
	order, le, reg := ids.New(), ids.New(), ids.New()
	f := &sfFake{orderLE: map[string]string{order: le}}
	h := ShopfloorConsumerHandlers(f)[RegistrationPostedType]
	evt := sfEvent(t, RegistrationPostedType, stopData(reg, order))
	if err := h.Handle(sfCtx(t), nil, evt); err != nil {
		t.Fatal(err)
	}
	if len(f.posted) != 1 {
		t.Fatalf("posted %d", len(f.posted))
	}
	got, as := f.posted[0], f.postedAs[0]
	if got.RegistrationID != reg || got.OrderRef != order || got.OperationRef != "10" || !got.Hours.Equal(decimal.RequireFromString("2")) ||
		!got.CostRate.Equal(decimal.RequireFromString("26")) || got.CostRateCurrency != "USD" || got.TraceID != evt.ID || got.Actor != "service:production-shopfloor" {
		t.Fatalf("job card %+v", got)
	}
	if as.LegalEntityID != le || as.TenantID != sfTenant || !as.HasDuty(DutyShopfloorJobCard) || as.HasDuty(tenant.DutyAllScope) {
		t.Fatalf("posted as %+v, want the consumer pinned to %s", as, le)
	}
}

func TestShopfloorConsumer_IgnoresRegistrationsWithoutOrderLabor(t *testing.T) {
	f := &sfFake{orderLE: map[string]string{}}
	h := ShopfloorConsumerHandlers(f)[RegistrationPostedType]
	clock := stopData(ids.New(), ids.New())
	clock["kind"] = "clock-out"
	open := stopData(ids.New(), ids.New())
	open["hours"] = nil
	noOrder := stopData(ids.New(), "")
	for _, d := range []map[string]any{clock, open, noOrder} {
		if err := h.Handle(sfCtx(t), nil, sfEvent(t, RegistrationPostedType, d)); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.posted) != 0 {
		t.Fatalf("posted %d", len(f.posted))
	}
}

// Sweep rule: a refusal a retry cannot fix is parked; a transient failure is retried with backoff.
func TestShopfloorConsumer_ParksRefusalsAndRetriesTheRest(t *testing.T) {
	order, le := ids.New(), ids.New()
	h := func(f *sfFake) eventing.Handler { return ShopfloorConsumerHandlers(f)[RegistrationPostedType] }
	stop := func() events.Event { return sfEvent(t, RegistrationPostedType, stopData(ids.New(), order)) }

	unknown := &sfFake{orderLE: map[string]string{}}
	if err := h(unknown).Handle(sfCtx(t), nil, stop()); !eventing.IsPermanent(err) || !errors.Is(err, store.ErrJobCardRefused) {
		t.Errorf("unknown order: %v, want parked", err)
	}
	refused := &sfFake{orderLE: map[string]string{order: le}, postErr: store.ErrJobCardRefused}
	if err := h(refused).Handle(sfCtx(t), nil, stop()); !eventing.IsPermanent(err) {
		t.Errorf("refused card: %v, want parked", err)
	}
	down := &sfFake{orderLE: map[string]string{order: le}, postErr: errors.New("connection reset")}
	if err := h(down).Handle(sfCtx(t), nil, stop()); err == nil || eventing.IsPermanent(err) {
		t.Errorf("transient post failure: %v, want a retry", err)
	}
	lookupDown := &sfFake{lookupErr: errors.New("connection reset")}
	if err := h(lookupDown).Handle(sfCtx(t), nil, stop()); err == nil || eventing.IsPermanent(err) {
		t.Errorf("transient lookup failure: %v, want a retry", err)
	}

	foreign := stop()
	foreign.Source = "sales"
	planner := tenant.With(context.Background(), tenant.Principal{TenantID: sfTenant, UserID: "service:" + PlanningConsumerName, Duties: map[string]struct{}{DutyPlannedIntake: {}}})
	bad := events.Event{ID: ids.New(), Type: RegistrationPostedType, Source: ShopfloorSource, TenantID: sfTenant, Data: []byte("[")}
	for name, tc := range map[string]struct {
		ctx context.Context
		evt events.Event
	}{
		"foreign source": {sfCtx(t), foreign},
		"no duty":        {planner, stop()},
		"not json":       {sfCtx(t), bad},
	} {
		if err := h(&sfFake{orderLE: map[string]string{order: le}}).Handle(tc.ctx, nil, tc.evt); !eventing.IsPermanent(err) {
			t.Errorf("%s: %v, want parked", name, err)
		}
	}
}

// A correction re-books the registration's card in the card's legal entity; one that
// overtakes its registration.posted is retried until the card is there.
func TestShopfloorConsumer_CorrectionRebooksTheCard(t *testing.T) {
	reg, le := ids.New(), ids.New()
	f := &sfFake{cardLE: map[string]string{reg: le}}
	h := ShopfloorConsumerHandlers(f)[RegistrationCorrectedType]
	corrected := func(reg, kind string) events.Event {
		return sfEvent(t, RegistrationCorrectedType, map[string]any{"registrationId": reg, "workerId": ids.New(), "kind": kind, "hours": "0.00", "reason": "kiosk stop", "correctedBy": "system:offline-replay"})
	}
	if err := h.Handle(sfCtx(t), nil, corrected(reg, "job-stop")); err != nil {
		t.Fatal(err)
	}
	if len(f.adjusted) != 1 || f.adjusted[0] != reg+"=0" || f.adjustedAs[0].LegalEntityID != le {
		t.Fatalf("adjusted %v as %+v", f.adjusted, f.adjustedAs)
	}
	if err := h.Handle(sfCtx(t), nil, corrected(ids.New(), "clock-in")); err != nil || len(f.adjusted) != 1 {
		t.Fatalf("clock correction: %v, adjusted %v", err, f.adjusted)
	}
	if err := h.Handle(sfCtx(t), nil, corrected(ids.New(), "job-stop")); err == nil || eventing.IsPermanent(err) {
		t.Fatalf("correction before its card: %v, want a retry", err)
	}
	f.adjustErr = store.ErrJobCardRefused
	if err := h.Handle(sfCtx(t), nil, corrected(reg, "job-stop")); !eventing.IsPermanent(err) {
		t.Fatalf("refused adjust: %v, want parked", err)
	}
}
