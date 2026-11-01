package store_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func importReq(orderID string, version int, jobs ...domain.ProductionJob) store.OrderImportRequest {
	return store.OrderImportRequest{
		Order:   domain.ProductionOrder{OrderID: orderID, OrderNumber: "PO-1", Start: at(0, 6, 0), Jobs: jobs},
		End:     at(2, 0, 0),
		Version: version,
		Reason:  "schedule",
		EventID: ids.New(),
	}
}

func pjob(oper, seq int, kind, resource, hours string) domain.ProductionJob {
	return domain.ProductionJob{OperNumber: oper, Seq: seq, JobType: kind, ResourceID: resource, Hours: hours}
}

func TestImportProductionOrder_RegistersUpdatesAndRemovesOperations(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	order := ids.New()

	first, err := s.ImportProductionOrder(ctx, importReq(order, 3,
		pjob(10, 1, "process", m.ID, "2"), pjob(20, 1, "process", m.ID, "1"), pjob(30, 1, "process", ids.New(), "1"), pjob(40, 1, "process", m.ID, "1")))
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != store.ImportPartial || first.Created != 3 || first.OperationsTotal != 4 || len(first.Skipped) != 1 || first.Skipped[0].OperNumber != 30 || first.Skipped[0].Code != domain.SkipUnknownResource {
		t.Fatalf("first = %+v, want 3 created and operation 30 skipped for its resource", first)
	}
	ops, err := s.ListOperations(ctx, store.OperationFilter{OrderID: order, Limit: 10})
	if err != nil || len(ops) != 3 || ops[0].Minutes != 120 || ops[0].ReadyAt == nil || ops[1].ReadyAt != nil {
		t.Fatalf("ops %+v err=%v, want 10, 20 and 40", ops, err)
	}
	var events int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = 'dev.passage.scheduling.orderimport.recorded.v1'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("%d outbox events, err=%v, want 1", events, err)
	}

	// Place the order, firm operation 20, then receive a re-dated order.
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FirmJob(ctx, ops[1].ID, true, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	second, err := s.ImportProductionOrder(ctx, importReq(order, 4,
		pjob(10, 1, "process", m.ID, "3"), pjob(20, 1, "process", m.ID, "5"), pjob(30, 1, "process", m.ID, "1")))
	if err != nil {
		t.Fatal(err)
	}
	// 10 changed and was placed: updated, its job dropped. 20 is firmed: kept. 30 now has a known resource: created. 40 left the route: removed.
	if second.Status != store.ImportImported || second.Updated != 1 || second.Created != 1 || second.Removed != 1 || second.KeptFirmed != 1 || !second.ReplanRequired {
		t.Fatalf("second = %+v", second)
	}
	ops, err = s.ListOperations(ctx, store.OperationFilter{OrderID: order, Limit: 10})
	if err != nil || len(ops) != 3 {
		t.Fatalf("ops %+v err=%v, want 10, 20, 30", ops, err)
	}
	if ops[0].Minutes != 180 || ops[0].Status != "open" || ops[1].Minutes != 60 || ops[1].Status != "scheduled" || ops[2].Minutes != 60 {
		t.Fatalf("ops = %+v, want 10 updated and open, 20 untouched while firmed", ops)
	}
	var jobs int
	if err := admin.QueryRow(context.Background(), `select count(*) from scheduled_job where order_id = $1::uuid and oper_number = 10`, order).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("%d jobs left for the changed operation, err=%v", jobs, err)
	}

	// The same event again changes nothing.
	again, err := s.ImportProductionOrder(ctx, importReq(order, 4,
		pjob(10, 1, "process", m.ID, "3"), pjob(20, 1, "process", m.ID, "5"), pjob(30, 1, "process", m.ID, "1")))
	if err != nil || again.Updated != 0 || again.Created != 0 || again.Removed != 0 || again.Unchanged != 2 || again.ReplanRequired {
		t.Fatalf("again = %+v err=%v, want everything unchanged", again, err)
	}
}

func TestImportProductionOrder_OlderVersionIsIgnoredAndRowsAreTenantScoped(t *testing.T) {
	s, _, _ := setup(t)
	one, two := tenantCtx(1), tenantCtx(2)
	m := mustResource(t, one, s, "M1", true)
	order := ids.New()
	if _, err := s.ImportProductionOrder(one, importReq(order, 5, pjob(10, 1, "process", m.ID, "1"))); err != nil {
		t.Fatal(err)
	}
	stale, err := s.ImportProductionOrder(one, importReq(order, 4, pjob(10, 1, "process", m.ID, "9"), pjob(20, 1, "process", m.ID, "1")))
	if err != nil || !stale.Stale || stale.OrderVersion != 5 {
		t.Fatalf("stale = %+v err=%v, want the stored row back, flagged stale", stale, err)
	}
	ops, _ := s.ListOperations(one, store.OperationFilter{OrderID: order, Limit: 10})
	if len(ops) != 1 || ops[0].Minutes != 60 {
		t.Fatalf("ops = %+v, the older event changed the plan", ops)
	}
	if _, err := s.GetOrderImport(two, order); problemCode(err) != store.CodeOrderImportNotFound {
		t.Fatalf("other tenant read the import: %v", err)
	}
	if list, err := s.ListOrderImports(two, store.OrderImportFilter{Limit: 10}); err != nil || len(list) != 0 {
		t.Fatalf("other tenant list = %+v err=%v", list, err)
	}
	// An order whose every operation is unusable is recorded as rejected.
	rejected, err := s.ImportProductionOrder(one, importReq(ids.New(), 1, pjob(10, 1, "setup", m.ID, "1")))
	if err != nil || rejected.Status != store.ImportRejected {
		t.Fatalf("rejected = %+v err=%v", rejected, err)
	}
	list, err := s.ListOrderImports(one, store.OrderImportFilter{Status: store.ImportRejected, Limit: 10})
	if err != nil || len(list) != 1 || list[0].OrderID != rejected.OrderID {
		t.Fatalf("rejected list = %+v err=%v", list, err)
	}
	got, err := s.GetOrderImport(one, order)
	if err != nil || got.Status != store.ImportImported || got.OperationsTotal != 1 {
		t.Fatalf("get = %+v err=%v", got, err)
	}
}

func TestImportProductionOrder_DueIsTheEndOfTheDeliveryDayWhereTheOrderFinishes(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	utc := mustResource(t, ctx, s, "CUT", true)
	tokyo, _, err := s.CreateResource(ctx, store.Resource{Code: "PACK-T", Name: "Tokyo packing", Finite: true, TimeZone: "Asia/Tokyo", Shifts: weekdays(8*60, 16*60)}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	order := ids.New()
	req := importReq(order, 1, pjob(10, 1, "process", utc.ID, "1"), pjob(20, 1, "process", tokyo.ID, "1"), pjob(30, 1, "process", ids.New(), "1"))
	delivery := time.Date(2031, 3, 7, 0, 0, 0, 0, time.UTC)
	req.Order.DeliveryDate = &delivery
	if _, err := s.ImportProductionOrder(ctx, req); err != nil {
		t.Fatal(err)
	}
	ops, err := s.ListOperations(ctx, store.OperationFilter{OrderID: order, Limit: 10})
	if err != nil || len(ops) != 2 {
		t.Fatalf("ops %+v err=%v", ops, err)
	}
	// The order finishes on the Tokyo resource (operation 30 is not registrable),
	// so 7 March ends at midnight Tokyo time, 15:00 UTC, for every operation.
	want := time.Date(2031, 3, 7, 15, 0, 0, 0, time.UTC)
	for _, op := range ops {
		if op.DueAt == nil || !op.DueAt.Equal(want) {
			t.Fatalf("operation %d due %v, want %v", op.OperNumber, op.DueAt, want)
		}
	}
}

func TestImportProductionOrder_ConcurrentDeliveriesKeepTheNewestVersion(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	order := ids.New()
	older := importReq(order, 1, pjob(10, 1, "process", m.ID, "1"), pjob(20, 1, "process", m.ID, "1"))
	newer := importReq(order, 2, pjob(10, 1, "process", m.ID, "4"))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, req := range []store.OrderImportRequest{older, newer} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.ImportProductionOrder(ctx, req)
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errs = %v; the second delivery must wait for the first, not collide with it", errs)
	}
	got, err := s.GetOrderImport(ctx, order)
	if err != nil || got.OrderVersion != 2 {
		t.Fatalf("import = %+v err=%v, want version 2 whichever delivery ran first", got, err)
	}
	ops, err := s.ListOperations(ctx, store.OperationFilter{OrderID: order, Limit: 10})
	if err != nil || len(ops) != 1 || ops[0].Minutes != 240 {
		t.Fatalf("ops %+v err=%v, want only version 2's operation 10 at 240 minutes", ops, err)
	}
}
