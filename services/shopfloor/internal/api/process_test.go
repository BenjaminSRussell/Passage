package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const (
	processDuty = "shopfloor.process.create,shopfloor.process.deliver,shopfloor.view"
	orderID     = "0195f0a0-0000-7000-8000-000000000010"
	messageID   = "0195f0a0-0000-7000-8000-000000000011"
)

// processFake keeps process messages in memory so handler behaviour can be tested without Postgres.
type processFake struct {
	fakeStore
	msgs     map[string]store.ProcessMessage
	received time.Time
	filter   store.ProcessFilter
	reasons  []string
}

func (f *processFake) store(in domain.NewMessage, cat domain.Category) store.ProcessMessage {
	m := store.ProcessMessage{ID: messageID, Source: in.Source, SourceMessageID: in.SourceMessageID, CategoryCode: in.CategoryCode,
		OrderID: in.OrderID, PhasePath: in.PhasePath, OccurredAt: in.OccurredAt, ReceivedAt: in.OccurredAt, Status: domain.MessageReceived,
		Values: in.Values}
	for _, d := range cat.Destinations {
		m.Deliveries = append(m.Deliveries, domain.Delivery{Destination: d, Status: domain.DeliveryPending})
	}
	if f.msgs == nil {
		f.msgs = map[string]store.ProcessMessage{}
	}
	f.msgs[messageID] = m
	return m
}

func (f *processFake) IngestProcessMessage(_ context.Context, in domain.NewMessage, cat domain.Category, now time.Time) (store.ProcessIngest, error) {
	f.received = now
	for _, m := range f.msgs {
		if m.Source == in.Source && m.SourceMessageID == in.SourceMessageID {
			return store.ProcessIngest{Message: m, Duplicate: true}, nil
		}
	}
	return store.ProcessIngest{Message: f.store(in, cat)}, nil
}

func (f *processFake) ListProcessMessages(_ context.Context, flt store.ProcessFilter) ([]store.ProcessMessage, error) {
	f.filter = flt
	out := []store.ProcessMessage{}
	for _, m := range f.msgs {
		if flt.Status == "" || m.Status == flt.Status {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *processFake) GetProcessMessage(_ context.Context, id string) (store.ProcessMessage, error) {
	m, ok := f.msgs[id]
	if !ok {
		return store.ProcessMessage{}, httpx.NewProblem(http.StatusNotFound, store.CodeProcessNotFound, "process message not found")
	}
	return m, nil
}

func (f *processFake) ReportDelivery(_ context.Context, id, dest, outcome string, errText *string, now time.Time, _ store.Idem) (store.ProcessMessage, error) {
	m, ok := f.msgs[id]
	if !ok {
		return store.ProcessMessage{}, httpx.NewProblem(http.StatusNotFound, store.CodeProcessNotFound, "process message not found")
	}
	for i := range m.Deliveries {
		if m.Deliveries[i].Destination == dest {
			d, err := m.Deliveries[i].Report(outcome, errText, now)
			if err != nil {
				return m, err
			}
			m.Deliveries[i] = d
		}
	}
	f.msgs[id] = m
	return m, nil
}

func (f *processFake) ReprocessDelivery(_ context.Context, id, dest, reason string, now time.Time, _ store.Idem) (store.ProcessMessage, error) {
	f.reasons = append(f.reasons, reason)
	m := f.msgs[id]
	for i := range m.Deliveries {
		if m.Deliveries[i].Destination == dest {
			d, err := m.Deliveries[i].Reprocess(now)
			if err != nil {
				var de *domain.Error
				errors.As(err, &de)
				return m, httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
			}
			m.Deliveries[i] = d
		}
	}
	f.msgs[id] = m
	return m, nil
}

// The shared fakeStore satisfies Store for the floor tests. Process message calls are
// served by processFake, which overrides these stubs.
var errProcessNotStubbed = errors.New("process message store not stubbed in this test")

func (f *fakeStore) IngestProcessMessage(context.Context, domain.NewMessage, domain.Category, time.Time) (store.ProcessIngest, error) {
	return store.ProcessIngest{}, errProcessNotStubbed
}

func (f *fakeStore) ListProcessMessages(context.Context, store.ProcessFilter) ([]store.ProcessMessage, error) {
	return nil, errProcessNotStubbed
}

func (f *fakeStore) GetProcessMessage(context.Context, string) (store.ProcessMessage, error) {
	return store.ProcessMessage{}, errProcessNotStubbed
}

func (f *fakeStore) ReportDelivery(context.Context, string, string, string, *string, time.Time, store.Idem) (store.ProcessMessage, error) {
	return store.ProcessMessage{}, errProcessNotStubbed
}

func (f *fakeStore) ReprocessDelivery(context.Context, string, string, string, time.Time, store.Idem) (store.ProcessMessage, error) {
	return store.ProcessMessage{}, errProcessNotStubbed
}

// Process message handlers are reached through the same Store interface as the rest of the service.
var _ Store = (*processFake)(nil)

func qualityBody(sourceID string) string {
	return fmt.Sprintf(`{"source":"kiosk","sourceMessageId":%q,"categoryCode":"quality-result","orderId":%q,
		"phasePath":"/proc/unit/op/phase","occurredAt":"2026-10-20T09:00:00Z",
		"values":[{"name":"temp_c","type":"numeric","value":"13","min":"10","max":"12","unit":"Cel"}]}`, sourceID, orderID)
}

func TestProcessMessage_CreateFlagsOutOfLimitAndRoutesDestinations(t *testing.T) {
	fs := &processFake{}
	rec := call(t, newRouter(fs), http.MethodPost, "/api/v1/shopfloor/process-messages", qualityBody("kiosk-1"), processDuty)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out store.ProcessIngest
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Duplicate || len(out.Message.Deliveries) != 3 {
		t.Fatalf("message = %+v", out.Message)
	}
	if len(out.Message.Values) != 1 || !out.Message.Values[0].OutOfLimit {
		t.Fatalf("values = %+v", out.Message.Values)
	}
}

func TestProcessMessage_ReplayReturnsStoredMessage(t *testing.T) {
	fs := &processFake{}
	h := newRouter(fs)
	call(t, h, http.MethodPost, "/api/v1/shopfloor/process-messages", qualityBody("kiosk-2"), processDuty)
	rec := call(t, h, http.MethodPost, "/api/v1/shopfloor/process-messages", qualityBody("kiosk-2"), processDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("replay status %d: %s", rec.Code, rec.Body.String())
	}
	var out store.ProcessIngest
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.Duplicate {
		t.Fatal("replay not flagged as duplicate")
	}
}

func TestProcessMessage_ValidationNamesFields(t *testing.T) {
	bad := `{"source":"fax","sourceMessageId":"","categoryCode":"nope","orderId":"x","occurredAt":"2026-10-20T09:00:00Z",
		"values":[{"name":"a","type":"numeric","value":"1"},{"name":"a","type":"color","value":"1"}]}`
	rec := call(t, newRouter(&processFake{}), http.MethodPost, "/api/v1/shopfloor/process-messages", bad, processDuty)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	var p httpx.Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	for _, field := range []string{"source", "sourceMessageId", "categoryCode", "orderId", "values[1].name", "values[1].type"} {
		if _, ok := p.Errors[field]; !ok {
			t.Errorf("missing field error %s in %v", field, p.Errors)
		}
	}
}

func TestProcessMessage_InvalidValueIsUnprocessable(t *testing.T) {
	body := fmt.Sprintf(`{"source":"edge","sourceMessageId":"e-1","categoryCode":"process-data","orderId":%q,"occurredAt":"2026-10-20T09:00:00Z",
		"values":[{"name":"flag","type":"boolean","value":"maybe"}]}`, orderID)
	rec := call(t, newRouter(&processFake{}), http.MethodPost, "/api/v1/shopfloor/process-messages", body, processDuty)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeInvalidValue {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestProcessMessage_DutiesGateEachEndpoint(t *testing.T) {
	h := newRouter(&processFake{})
	if rec := call(t, h, http.MethodPost, "/api/v1/shopfloor/process-messages", qualityBody("k-3"), "shopfloor.view"); rec.Code != http.StatusForbidden {
		t.Fatalf("create without duty status %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, "/api/v1/shopfloor/process-messages/"+messageID+"/deliveries/kafka:report",
		`{"outcome":"delivered"}`, "shopfloor.view"); rec.Code != http.StatusForbidden {
		t.Fatalf("report without duty status %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/api/v1/shopfloor/process-messages", "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("list without duty status %d", rec.Code)
	}
}

func TestProcessMessage_ListFiltersAndValidates(t *testing.T) {
	h := newRouter(&processFake{})
	call(t, h, http.MethodPost, "/api/v1/shopfloor/process-messages", qualityBody("k-4"), processDuty)
	rec := call(t, h, http.MethodGet, "/api/v1/shopfloor/process-messages?status=received&limit=5", "", processDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", rec.Code, rec.Body.String())
	}
	for _, q := range []string{"status=done", "category=nope", "limit=0", "limit=999"} {
		if rec := call(t, h, http.MethodGet, "/api/v1/shopfloor/process-messages?"+q, "", processDuty); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", q, rec.Code)
		}
	}
}

func TestProcessMessage_GetUnknownIsNotFound(t *testing.T) {
	rec := call(t, newRouter(&processFake{}), http.MethodGet, "/api/v1/shopfloor/process-messages/"+messageID, "", processDuty)
	if rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeProcessNotFound {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestProcessMessage_ReportValidatesOutcomeAndDestination(t *testing.T) {
	fs := &processFake{}
	h := newRouter(fs)
	call(t, h, http.MethodPost, "/api/v1/shopfloor/process-messages", qualityBody("k-5"), processDuty)
	base := "/api/v1/shopfloor/process-messages/" + messageID + "/deliveries/"
	if rec := call(t, h, http.MethodPost, base+"fax:report", `{"outcome":"delivered"}`, processDuty); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown destination status %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, base+"ebr:report", `{"outcome":"maybe"}`, processDuty); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown outcome status %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, base+"ebr:report", `{"outcome":"failed","error":"EBR offline"}`, processDuty); rec.Code != http.StatusOK {
		t.Fatalf("failed report status %d: %s", rec.Code, rec.Body.String())
	}
	m := fs.msgs[messageID]
	for _, d := range m.Deliveries {
		if d.Destination == domain.DestEBR && (d.Status != domain.DeliveryPending || d.Attempts != 1) {
			t.Fatalf("ebr after failure = %+v", d)
		}
	}
}

func TestProcessMessage_ReprocessNeedsDestinationAndReason(t *testing.T) {
	h := newRouter(&processFake{})
	call(t, h, http.MethodPost, "/api/v1/shopfloor/process-messages", qualityBody("k-6"), processDuty)
	url := "/api/v1/shopfloor/process-messages/" + messageID + ":reprocess"
	if rec := call(t, h, http.MethodPost, url, `{"destination":"ebr","reason":""}`, processDuty); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty reason status %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, url, `{"destination":"nope","reason":"retry"}`, processDuty); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad destination status %d", rec.Code)
	}
}

func TestProcessMessage_ReprocessOnlyFromError(t *testing.T) {
	h := newRouter(&processFake{})
	call(t, h, http.MethodPost, "/api/v1/shopfloor/process-messages", qualityBody("k-7"), processDuty)
	url := "/api/v1/shopfloor/process-messages/" + messageID + ":reprocess"
	rec := call(t, h, http.MethodPost, url, `{"destination":"ebr","reason":"EBR restored"}`, processDuty)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeNotReprocessable {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestProcessMessage_StampsServerReceiveTime(t *testing.T) {
	fs := &processFake{}
	server := time.Date(2026, 10, 20, 9, 0, 7, 0, time.UTC)
	rec := call(t, newRouterAt(fs, func() time.Time { return server }), http.MethodPost, "/api/v1/shopfloor/process-messages", qualityBody("k-10"), processDuty)
	if rec.Code != http.StatusCreated || !fs.received.Equal(server) {
		t.Fatalf("status %d, received at %v", rec.Code, fs.received)
	}
}

func TestProcessMessage_ListPassesEveryFilter(t *testing.T) {
	fs := &processFake{}
	h := newRouter(fs)
	rec := call(t, h, http.MethodGet, "/api/v1/shopfloor/process-messages?status=error&category=quality-result&destination=quality&orderId="+orderID+"&limit=20", "", processDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	want := store.ProcessFilter{Status: "error", Category: "quality-result", Destination: "quality", OrderID: orderID, Limit: 20}
	if fs.filter != want {
		t.Fatalf("filter %+v, want %+v", fs.filter, want)
	}
	if rec := call(t, h, http.MethodGet, "/api/v1/shopfloor/process-messages", "", processDuty); rec.Code != http.StatusOK || fs.filter.Limit != defaultProcessList {
		t.Fatalf("default limit %d (status %d)", fs.filter.Limit, rec.Code)
	}
	for _, q := range []string{"destination=fax", "orderId=PO-7"} {
		if rec := call(t, h, http.MethodGet, "/api/v1/shopfloor/process-messages?"+q, "", processDuty); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", q, rec.Code)
		}
	}
}

// Delivery reports and reprocess are commands: they need an Idempotency-Key, and a
// repeated key answers from the stored outcome without touching the delivery again.
func TestProcessMessage_ReportAndReprocessNeedIdempotencyKey(t *testing.T) {
	fs := &processFake{}
	h := newRouter(fs)
	call(t, h, http.MethodPost, "/api/v1/shopfloor/process-messages", qualityBody("k-11"), processDuty)
	report := "/api/v1/shopfloor/process-messages/" + messageID + "/deliveries/quality:report"
	if rec := callWith(t, h, http.MethodPost, report, `{"outcome":"failed"}`, processDuty, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("report without key: %d", rec.Code)
	}
	if rec := callWith(t, h, http.MethodPost, "/api/v1/shopfloor/process-messages/"+messageID+":reprocess", `{"destination":"quality","reason":"QMS back"}`,
		processDuty, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("reprocess without key: %d", rec.Code)
	}
	fs.replays = map[string]*store.Replay{"rep-1": {Status: http.StatusOK, Body: []byte(`{"id":"` + messageID + `"}`)}}
	rec := callWith(t, h, http.MethodPost, report, `{"outcome":"failed","error":"QMS down"}`, processDuty, map[string]string{"Idempotency-Key": "rep-1"})
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %v", rec.Code, rec.Header())
	}
	for _, d := range fs.msgs[messageID].Deliveries {
		if d.Attempts != 0 {
			t.Fatalf("a replayed report changed %s: %+v", d.Destination, d)
		}
	}
}

func TestProcessMessage_ReprocessTrimsReasonAndUnknownIDIsNotFound(t *testing.T) {
	fs := &processFake{}
	h := newRouter(fs)
	call(t, h, http.MethodPost, "/api/v1/shopfloor/process-messages", qualityBody("k-12"), processDuty)
	m := fs.msgs[messageID]
	for i := range m.Deliveries {
		if m.Deliveries[i].Destination == domain.DestQuality {
			m.Deliveries[i].Status, m.Deliveries[i].Attempts = domain.DeliveryError, domain.MaxDeliveryAttempts
		}
	}
	fs.msgs[messageID] = m
	rec := call(t, h, http.MethodPost, "/api/v1/shopfloor/process-messages/"+messageID+":reprocess", `{"destination":"quality","reason":"  QMS restored  "}`, processDuty)
	if rec.Code != http.StatusOK || len(fs.reasons) != 1 || fs.reasons[0] != "QMS restored" {
		t.Fatalf("status %d reasons %q: %s", rec.Code, fs.reasons, rec.Body.String())
	}
	if rec := call(t, h, http.MethodPost, "/api/v1/shopfloor/process-messages/not-a-uuid:reprocess", `{"destination":"quality","reason":"x"}`, processDuty); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/api/v1/shopfloor/process-messages/not-a-uuid", "", processDuty); rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeProcessNotFound {
		t.Fatalf("bad id get: %d", rec.Code)
	}
}
