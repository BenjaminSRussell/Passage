package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/api"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func orderScheduledEvent(t *testing.T, eventID, tenantID, orderID string, version int, jobs []map[string]any) events.Event {
	t.Helper()
	return orderEventWithReason(t, eventID, tenantID, orderID, "schedule", version, jobs)
}

func orderEventWithReason(t *testing.T, eventID, tenantID, orderID, reason string, version int, jobs []map[string]any) events.Event {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"orderId": orderID, "orderNumber": "PO-100", "legalEntityId": ids.New(), "siteId": ids.New(), "status": "Scheduled", "reason": reason,
		"schedulingMethod": "jobs", "direction": "forward", "start": monday(6).Format(time.RFC3339), "end": monday(18).Format(time.RFC3339),
		"startDate": "2031-03-03", "endDate": "2031-03-03", "deliveryDate": "2031-03-07", "jobs": jobs, "orderVersion": version, "actor": "planner",
		"occurredAt": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	return events.Event{SpecVersion: "1.0", ID: eventID, Source: "/passage/production", Type: api.OrderScheduledType, Time: time.Now().UTC(),
		DataContentType: "application/json", Data: data, TenantID: tenantID}
}

func orderScheduledRecord(t *testing.T, offset int64, evt events.Event) eventing.Record {
	t.Helper()
	value, err := json.Marshal(evt)
	if err != nil {
		t.Fatal(err)
	}
	return eventing.Record{Topic: api.OrderScheduledTopic, Offset: offset, Value: value, Headers: map[string]string{"tenantid": evt.TenantID}}
}

// orderIntakeCtx is the principal the order intake runs as once
// service:scheduling-order-import is seeded in the service principal table.
func orderIntakeCtx(tenantID string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, UserID: tenant.ServiceActor(api.OrderImportConsumerName),
		Duties: map[string]struct{}{api.DutyOrderImport: {}}})
}

func TestOrderIntake_RegistersOperationsAndIgnoresAnOlderVersion(t *testing.T) {
	st, a, _, count := newRigWith(t, (*api.API).OrderConsumerHandlers)
	ctx := orderIntakeCtx(testkit.TenantID(1))
	res, _, err := st.CreateResource(ctx, store.Resource{Code: "PRESS-1", Name: "Press", Finite: true, TimeZone: "UTC",
		Shifts: []domain.Shift{{Weekday: time.Monday, StartMinute: 8 * 60, EndMinute: 16 * 60}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	orderID := ids.New()
	jobs := []map[string]any{
		{"jobId": ids.New(), "operNumber": 10, "seq": 1, "jobType": "setup", "resourceId": res.ID, "plannedStart": monday(6).Format(time.RFC3339), "plannedEnd": monday(7).Format(time.RFC3339), "plannedHours": "0.5"},
		{"jobId": ids.New(), "operNumber": 10, "seq": 2, "jobType": "process", "resourceId": res.ID, "plannedStart": monday(7).Format(time.RFC3339), "plannedEnd": monday(9).Format(time.RFC3339), "plannedHours": "2"},
		{"jobId": ids.New(), "operNumber": 20, "seq": 1, "jobType": "process", "resourceId": ids.New(), "plannedStart": monday(9).Format(time.RFC3339), "plannedEnd": monday(10).Format(time.RFC3339), "plannedHours": "1"},
	}
	handle := a.OrderConsumerHandlers()[api.OrderScheduledType]
	if err := handle.Handle(ctx, nil, orderScheduledEvent(t, ids.New(), testkit.TenantID(1), orderID, 2, jobs)); err != nil {
		t.Fatal(err)
	}
	if err := handle.Handle(ctx, nil, orderScheduledEvent(t, ids.New(), testkit.TenantID(1), orderID, 1, jobs[:1])); err != nil {
		t.Fatalf("an older version must be acknowledged, not failed: %v", err)
	}

	if n := count(`select count(*) from scheduling_operation where order_id = $1::uuid`, orderID); n != 1 {
		t.Fatalf("%d operations, want only operation 10 (20 names a resource scheduling does not know)", n)
	}
	if n := count(`select minutes from scheduling_operation where order_id = $1::uuid and oper_number = 10`, orderID); n != 120 {
		t.Fatalf("operation 10 has %d minutes, want 120", n)
	}
	if n := count(`select setup_minutes from scheduling_operation where order_id = $1::uuid and oper_number = 10`, orderID); n != 30 {
		t.Fatalf("operation 10 has %d setup minutes, want 30", n)
	}
	imp, err := st.GetOrderImport(ctx, orderID)
	if err != nil || imp.Status != store.ImportPartial || imp.OrderVersion != 2 || len(imp.Skipped) != 1 || imp.Skipped[0].OperNumber != 20 {
		t.Fatalf("import = %+v err=%v, want partial at version 2 with operation 20 skipped", imp, err)
	}
	if n := count(`select count(*) from outbox where type = 'dev.passage.scheduling.orderimport.recorded.v1' and aggregate_id = $1`, orderID); n != 1 {
		t.Fatalf("%d import events, want 1 (the older version changes nothing)", n)
	}
}

func TestOrderIntake_DowntimePrincipalCannotImportOrders(t *testing.T) {
	_, _, run, count := newRigWith(t, (*api.API).OrderConsumerHandlers)
	orderID := ids.New()
	evt := orderScheduledEvent(t, ids.New(), testkit.TenantID(1), orderID, 1, []map[string]any{
		{"operNumber": 10, "seq": 1, "jobType": "process", "resourceId": ids.New(), "plannedHours": "1"},
	})
	src, dlq := run(orderScheduledRecord(t, 1, evt))
	if len(dlq.sent) != 1 || dlq.sent[0].reason != "permanent" || len(src.acked) != 1 {
		t.Fatalf("dead-lettered %+v acked %v, want the event refused for service:scheduling-downtime", dlq.sent, src.acked)
	}
	if n := count(`select count(*) from scheduling_order_import where order_id = $1::uuid`, orderID); n != 0 {
		t.Fatalf("%d import rows written under the downtime principal", n)
	}
}

func TestOrderIntake_MalformedEventIsPermanent(t *testing.T) {
	_, a, _, _ := newRigWith(t, (*api.API).OrderConsumerHandlers)
	handle := a.OrderConsumerHandlers()[api.OrderScheduledType]
	ctx := orderIntakeCtx(testkit.TenantID(1))
	bad := map[string]events.Event{
		"order id":    orderScheduledEvent(t, ids.New(), testkit.TenantID(1), "not-a-uuid", 1, nil),
		"resource id": orderScheduledEvent(t, ids.New(), testkit.TenantID(1), ids.New(), 1, []map[string]any{{"operNumber": 10, "jobType": "process", "resourceId": "press", "plannedHours": "1"}}),
		"version":     orderScheduledEvent(t, ids.New(), testkit.TenantID(1), ids.New(), -1, nil),
	}
	for name, evt := range bad {
		if err := handle.Handle(ctx, nil, evt); err == nil || !eventing.IsPermanent(err) {
			t.Errorf("%s: err = %v, want a permanent failure", name, err)
		}
	}
}

func TestOrderIntakeSeeded_NeedsItsOwnPrincipalWithTheImportDuty(t *testing.T) {
	cases := []struct {
		name  string
		table map[string][]string
		want  bool
	}{
		{"seeded", map[string][]string{api.OrderImportConsumerName: {api.DutyOrderImport}}, true},
		{"absent", map[string][]string{api.DowntimeConsumerName: {api.DutyDowntimeApply}}, false},
		{"principal without the duty", map[string][]string{api.OrderImportConsumerName: {api.DutyDowntimeApply}}, false},
		{"duty on another principal", map[string][]string{api.DowntimeConsumerName: {api.DutyOrderImport}}, false},
	}
	for _, c := range cases {
		if got := orderIntakeSeeded(c.table); got != c.want {
			t.Errorf("%s: orderIntakeSeeded = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOrderIntake_IgnoresProductionsEchoOfScheduledDates(t *testing.T) {
	st, a, _, count := newRigWith(t, (*api.API).OrderConsumerHandlers)
	ctx := orderIntakeCtx(testkit.TenantID(1))
	res, _, err := st.CreateResource(ctx, store.Resource{Code: "PRESS-1", Name: "Press", Finite: true, TimeZone: "UTC",
		Shifts: []domain.Shift{{Weekday: time.Monday, StartMinute: 8 * 60, EndMinute: 16 * 60}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	handle := a.OrderConsumerHandlers()[api.OrderScheduledType]
	orderID := ids.New()
	first := []map[string]any{{"operNumber": 10, "seq": 1, "jobType": "process", "resourceId": res.ID, "plannedHours": "2"}}
	if err := handle.Handle(ctx, nil, orderScheduledEvent(t, ids.New(), testkit.TenantID(1), orderID, 1, first)); err != nil {
		t.Fatal(err)
	}
	// Production writes reason "finite" when it applies scheduling's own
	// dates; a newer version with other hours must still change nothing.
	echo := []map[string]any{{"operNumber": 10, "seq": 1, "jobType": "process", "resourceId": res.ID, "plannedHours": "5"}}
	if err := handle.Handle(ctx, nil, orderEventWithReason(t, ids.New(), testkit.TenantID(1), orderID, "finite", 2, echo)); err != nil {
		t.Fatalf("a finite echo must be acknowledged, not failed: %v", err)
	}
	if n := count(`select minutes from scheduling_operation where order_id = $1::uuid and oper_number = 10`, orderID); n != 120 {
		t.Fatalf("operation 10 has %d minutes, want the 120 of the schedule event", n)
	}
	if n := count(`select order_version from scheduling_order_import where order_id = $1::uuid`, orderID); n != 1 {
		t.Fatalf("import row at version %d, want 1: the finite echo records nothing", n)
	}
	if n := count(`select count(*) from outbox where type = 'dev.passage.scheduling.orderimport.recorded.v1' and aggregate_id = $1`, orderID); n != 1 {
		t.Fatalf("%d import events, want 1", n)
	}
	// An order scheduling never saw is not created by its echo either.
	other := ids.New()
	if err := handle.Handle(ctx, nil, orderEventWithReason(t, ids.New(), testkit.TenantID(1), other, "finite", 1, echo)); err != nil {
		t.Fatal(err)
	}
	if n := count(`select count(*) from scheduling_operation where order_id = $1::uuid`, other); n != 0 {
		t.Fatalf("%d operations registered from a finite echo", n)
	}
}

func orderStatusChangedEvent(t *testing.T, orderID, to string, dates bool) events.Event {
	t.Helper()
	body := map[string]any{"orderId": orderID, "from": "Scheduled", "to": to, "startDate": nil, "endDate": nil}
	if dates {
		body["startDate"], body["endDate"] = "2031-03-03", "2031-03-04"
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return events.Event{SpecVersion: "1.0", ID: ids.New(), Source: "/passage/production", Type: api.OrderStatusChangedType, Time: time.Now().UTC(),
		DataContentType: "application/json", Data: data, TenantID: testkit.TenantID(1)}
}

func TestOrderIntake_WithdrawsAnOrderProductionTookOutOfSchedule(t *testing.T) {
	st, a, _, count := newRigWith(t, (*api.API).OrderConsumerHandlers)
	ctx := orderIntakeCtx(testkit.TenantID(1))
	res, _, err := st.CreateResource(ctx, store.Resource{Code: "PRESS-1", Name: "Press", Finite: true, TimeZone: "UTC",
		Shifts: []domain.Shift{{Weekday: time.Monday, StartMinute: 8 * 60, EndMinute: 16 * 60}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	handlers := a.OrderConsumerHandlers()
	orderID := ids.New()
	jobs := []map[string]any{{"operNumber": 10, "seq": 1, "jobType": "process", "resourceId": res.ID, "plannedHours": "2"}}
	if err := handlers[api.OrderScheduledType].Handle(ctx, nil, orderScheduledEvent(t, ids.New(), testkit.TenantID(1), orderID, 1, jobs)); err != nil {
		t.Fatal(err)
	}
	withdraw := handlers[api.OrderStatusChangedType]
	// A dated event, a journal post's auto-start or report as finished (null
	// dates on a still dated order), an order never received, and a reset
	// older than the schedule event already imported change nothing.
	stale := orderStatusChangedEvent(t, orderID, "Estimated", false)
	stale.Time = time.Now().UTC().Add(-time.Hour)
	for name, evt := range map[string]events.Event{
		"dated start":        orderStatusChangedEvent(t, orderID, "Started", true),
		"auto-start":         orderStatusChangedEvent(t, orderID, "Started", false),
		"report as finished": orderStatusChangedEvent(t, orderID, "ReportedAsFinished", false),
		"unknown order":      orderStatusChangedEvent(t, ids.New(), "Created", false),
		"older reset":        stale,
	} {
		if err := withdraw.Handle(ctx, nil, evt); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if n := count(`select count(*) from scheduling_operation where order_id = $1::uuid`, orderID); n != 1 {
			t.Fatalf("%s: %d operations, want 1", name, n)
		}
	}
	if err := withdraw.Handle(ctx, nil, orderStatusChangedEvent(t, orderID, "Estimated", false)); err != nil {
		t.Fatal(err)
	}
	if n := count(`select count(*) from scheduling_operation where order_id = $1::uuid`, orderID); n != 0 {
		t.Fatalf("%d operations left after the withdrawal, want 0", n)
	}
	imp, err := st.GetOrderImport(ctx, orderID)
	if err != nil || imp.Status != store.ImportWithdrawn || imp.Removed != 1 || imp.WithdrawnTo == nil || *imp.WithdrawnTo != "Estimated" {
		t.Fatalf("import = %+v err=%v, want withdrawn to Estimated with 1 removed", imp, err)
	}
	if n := count(`select count(*) from outbox where type = 'dev.passage.scheduling.orderimport.recorded.v1' and aggregate_id = $1`, orderID); n != 2 {
		t.Fatalf("%d import events, want 2 (import and withdrawal)", n)
	}
	// A schedule event older than the withdrawal (delivered late) does not
	// bring the order back; a newer one does.
	late := orderScheduledEvent(t, ids.New(), testkit.TenantID(1), orderID, 2, jobs)
	late.Time = time.Now().UTC().Add(-time.Hour)
	if err := handlers[api.OrderScheduledType].Handle(ctx, nil, late); err != nil {
		t.Fatal(err)
	}
	if n := count(`select count(*) from scheduling_operation where order_id = $1::uuid`, orderID); n != 0 {
		t.Fatalf("%d operations after a schedule event older than the withdrawal, want 0", n)
	}
	if err := handlers[api.OrderScheduledType].Handle(ctx, nil, orderScheduledEvent(t, ids.New(), testkit.TenantID(1), orderID, 2, jobs)); err != nil {
		t.Fatal(err)
	}
	if imp, err := st.GetOrderImport(ctx, orderID); err != nil || imp.Status != store.ImportImported || imp.WithdrawnAt != nil {
		t.Fatalf("after a newer schedule event: import = %+v err=%v, want imported", imp, err)
	}
}

func TestOrderIntake_WithdrawalNeedsTheImportDutyAndAnOrderId(t *testing.T) {
	_, a, _, _ := newRigWith(t, (*api.API).OrderConsumerHandlers)
	withdraw := a.OrderConsumerHandlers()[api.OrderStatusChangedType]
	noDuty := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: tenant.ServiceActor(api.DowntimeConsumerName),
		Duties: map[string]struct{}{api.DutyDowntimeApply: {}}})
	if err := withdraw.Handle(noDuty, nil, orderStatusChangedEvent(t, ids.New(), "Created", false)); err == nil || !eventing.IsPermanent(err) {
		t.Errorf("without the duty: err = %v, want a permanent failure", err)
	}
	if err := withdraw.Handle(orderIntakeCtx(testkit.TenantID(1)), nil, orderStatusChangedEvent(t, "nope", "Created", false)); err == nil || !eventing.IsPermanent(err) {
		t.Errorf("bad order id: err = %v, want a permanent failure", err)
	}
}
