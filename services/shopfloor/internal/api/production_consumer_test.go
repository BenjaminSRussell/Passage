package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

type fakeProduction struct {
	got []store.OperationStatusIn
	err error
}

func (f *fakeProduction) ApplyOperationStatus(_ context.Context, _ pgx.Tx, in store.OperationStatusIn) (bool, error) {
	f.got = append(f.got, in)
	return true, f.err
}

func productionEvent(t *testing.T, data map[string]any) events.Event {
	t.Helper()
	raw, _ := json.Marshal(data)
	return events.Event{ID: "evt-1", Source: OperationStatusSource, Type: OperationStatusType, TenantID: testkit.TenantID(75), Data: raw}
}

// The production consumer stores a finish as finished and a reopen as not finished, and
// refuses (permanently) an event without an order, an operation or a time.
func TestProductionConsumer_StoresStatusAndRefusesBadEvents(t *testing.T) {
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(75), UserID: "service:" + ProductionConsumerName, Duties: map[string]struct{}{"shopfloor.job.sync": {}}})
	f := &fakeProduction{}
	h := ProductionConsumerHandlers(f)[OperationStatusType]
	good := map[string]any{"orderId": jobID, "operNumber": 20, "action": "finish", "from": "started", "to": "finished", "orderVersion": 5,
		"actor": "u1", "occurredAt": "2026-10-26T12:00:00Z"}
	if err := h.Handle(ctx, nil, productionEvent(t, good)); err != nil {
		t.Fatal(err)
	}
	reopen := map[string]any{"orderId": strings.ToUpper(jobID), "operNumber": 20, "action": "reopen", "from": "finished", "to": "started", "orderVersion": 6,
		"occurredAt": "2026-10-26T13:00:00Z"}
	if err := h.Handle(ctx, nil, productionEvent(t, reopen)); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 2 || !f.got[0].Finished || f.got[0].OperNumber != 20 || f.got[0].OrderID != jobID || f.got[0].EventID != "evt-1" || f.got[0].OrderVersion != 5 ||
		f.got[1].Finished || f.got[1].OrderID != jobID {
		t.Fatalf("stored = %+v", f.got)
	}
	for name, bad := range map[string]map[string]any{
		"no order":     {"operNumber": 20, "to": "finished", "orderVersion": 5, "occurredAt": "2026-10-26T12:00:00Z"},
		"no operation": {"orderId": jobID, "to": "finished", "orderVersion": 5, "occurredAt": "2026-10-26T12:00:00Z"},
		"no time":      {"orderId": jobID, "operNumber": 20, "to": "finished", "orderVersion": 5},
		"no version":   {"orderId": jobID, "operNumber": 20, "to": "finished", "occurredAt": "2026-10-26T12:00:00Z"},
	} {
		if err := h.Handle(ctx, nil, productionEvent(t, bad)); !eventing.IsPermanent(err) {
			t.Fatalf("%s: err = %v, want a permanent error", name, err)
		}
	}
	// An event that is not production's is refused before anything is stored.
	n := len(f.got)
	foreign := productionEvent(t, good)
	foreign.Source = "inventory"
	if err := h.Handle(ctx, nil, foreign); !eventing.IsPermanent(err) || len(f.got) != n {
		t.Fatalf("foreign source: err = %v, stored %d", err, len(f.got)-n)
	}
	// A principal without the duty is refused, and a store error is retried (not permanent).
	noDuty := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(75), UserID: "x"})
	if err := h.Handle(noDuty, nil, productionEvent(t, good)); !eventing.IsPermanent(err) {
		t.Fatalf("no duty: %v", err)
	}
	f.err = errors.New("db down")
	if err := h.Handle(ctx, nil, productionEvent(t, good)); err == nil || eventing.IsPermanent(err) {
		t.Fatalf("store error = %v, want a retryable error", err)
	}
}
