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

func TestImportProductionOrder_ResolvesEngineeringMappingAndReimports(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	other := mustResource(t, ctx, s, "M2", true)
	engM, engOther := ids.New(), ids.New()
	order := ids.New()

	// Nothing is mapped yet: the engineering ids are unknown to scheduling.
	first, err := s.ImportProductionOrder(ctx, importReq(order, 1,
		pjob(10, 1, "process", m.ID, "2"), pjob(20, 1, "process", engOther, "1"), pjob(20, 2, "process", engM, "1")))
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != store.ImportPartial || first.Created != 1 || len(first.Skipped) != 1 {
		t.Fatalf("first = %+v, want operation 10 registered and 20 skipped", first)
	}
	_ = other

	// Map both engineering resources (and refuse a second resource for one).
	mv, err := patchEng(ctx, s, m, engM)
	if err != nil {
		t.Fatal(err)
	}
	if mv.EngineeringResourceID == nil || *mv.EngineeringResourceID != engM {
		t.Fatalf("mapped resource = %+v", mv)
	}
	if _, err := patchEng(ctx, s, other, engM); problemCode(err) != store.CodeEngineeringMapped {
		t.Fatalf("two resources mapped to one engineering resource: err=%v", err)
	}
	other, _ = s.GetResource(ctx, other.ID)
	if _, err := patchEng(ctx, s, other, engOther); err != nil {
		t.Fatal(err)
	}

	again, err := s.ReimportProductionOrder(ctx, order)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != store.ImportImported || again.Created != 1 || again.Unchanged != 1 || len(again.Skipped) != 0 {
		t.Fatalf("again = %+v, want operation 20 created and 10 unchanged", again)
	}
	ops, err := s.ListOperations(ctx, store.OperationFilter{OrderID: order, Limit: 10})
	if err != nil || len(ops) != 2 || len(ops[1].ResourceIDs) != 2 || ops[1].ResourceIDs[0] != other.ID || ops[1].ResourceIDs[1] != m.ID {
		t.Fatalf("ops %+v err=%v, want operation 20 on M2 then M1", ops, err)
	}
	// Repeating it changes nothing.
	third, err := s.ReimportProductionOrder(ctx, order)
	if err != nil || third.Created != 0 || third.Updated != 0 || third.Unchanged != 2 {
		t.Fatalf("third = %+v err=%v", third, err)
	}
	// A mapping removed after the operation was registered is not applied
	// until the order is imported again; then the operation is re-resolved.
	other, _ = s.GetResource(ctx, other.ID)
	if _, err := patchEng(ctx, s, other, ""); err != nil {
		t.Fatal(err)
	}
	ops, _ = s.ListOperations(ctx, store.OperationFilter{OrderID: order, Limit: 10})
	if len(ops) != 2 || len(ops[1].ResourceIDs) != 2 {
		t.Fatalf("ops %+v, want operation 20 untouched by the remapping", ops)
	}
	fourth, err := s.ReimportProductionOrder(ctx, order)
	if err != nil || fourth.Updated != 1 || fourth.Unchanged != 1 {
		t.Fatalf("fourth = %+v err=%v, want operation 20 updated", fourth, err)
	}
	ops, _ = s.ListOperations(ctx, store.OperationFilter{OrderID: order, Limit: 10})
	if len(ops) != 2 || len(ops[1].ResourceIDs) != 1 || ops[1].ResourceIDs[0] != m.ID {
		t.Fatalf("ops %+v, want operation 20 on M1 only", ops)
	}
	if _, err := s.ReimportProductionOrder(ctx, ids.New()); problemCode(err) != store.CodeOrderImportNotFound {
		t.Fatalf("reimport of an order never received: err=%v", err)
	}
	// Another tenant neither sees the order nor collides with the mapping.
	if _, err := s.ReimportProductionOrder(tenantCtx(2), order); problemCode(err) != store.CodeOrderImportNotFound {
		t.Fatalf("reimport from another tenant: err=%v", err)
	}
	eng := engM
	if _, _, err := s.CreateResource(tenantCtx(2), store.Resource{Code: "M1", Name: "M1", Finite: true, TimeZone: "UTC", Shifts: weekdays(8*60, 16*60),
		EngineeringResourceID: &eng}, store.Idem{}); err != nil {
		t.Fatalf("same engineering id in another tenant: %v", err)
	}
}

func TestImportProductionOrder_ReimportRacingANewerDeliveryKeepsTheNewest(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	for round := 0; round < 5; round++ {
		order := ids.New()
		if _, err := s.ImportProductionOrder(ctx, importReq(order, 1, pjob(10, 1, "process", m.ID, "1"), pjob(20, 1, "process", m.ID, "1"))); err != nil {
			t.Fatal(err)
		}
		newer := importReq(order, 2, pjob(10, 1, "process", m.ID, "4"))
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); _, errs[0] = s.ReimportProductionOrder(ctx, order) }()
		go func() { defer wg.Done(); _, errs[1] = s.ImportProductionOrder(ctx, newer) }()
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("errs = %v", errs)
		}
		got, err := s.GetOrderImport(ctx, order)
		ops, oerr := s.ListOperations(ctx, store.OperationFilter{OrderID: order, Limit: 10})
		if err != nil || oerr != nil || got.OrderVersion != 2 || len(ops) != 1 || ops[0].Minutes != 240 {
			t.Fatalf("round %d: import %+v ops %+v err=%v/%v, want version 2's single operation whichever ran first", round, got, ops, err, oerr)
		}
	}
}

func TestCreateResource_ConcurrentMappingsToOneEngineeringResource(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	eng := ids.New()
	const n = 4
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = s.CreateResource(ctx, store.Resource{Code: "R" + string(rune('A'+i)), Name: "R", Finite: true, TimeZone: "UTC",
				Shifts: weekdays(8*60, 16*60), EngineeringResourceID: &eng}, store.Idem{})
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case problemCode(err) != store.CodeEngineeringMapped:
			t.Fatalf("a losing create got %v, want %s", err, store.CodeEngineeringMapped)
		}
	}
	if ok != 1 {
		t.Fatalf("%d creates mapped the engineering resource, want exactly 1 (errs %v)", ok, errs)
	}
}

func patchEng(ctx context.Context, s *store.Postgres, r store.Resource, eng string) (store.Resource, error) {
	upd, _, err := s.UpdateResource(ctx, r.ID, r.Version, store.ResourcePatch{EngineeringResourceID: &eng}, time.Now(), store.Idem{})
	return upd.Resource, err
}

func TestWithdrawProductionOrder_RemovesOperationsKeepsFirmedAndImportsAgain(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	order := ids.New()

	// Never received: nothing is written.
	if _, changed, err := s.WithdrawProductionOrder(ctx, store.OrderWithdrawal{OrderID: order, To: "Created", EventID: ids.New()}); err != nil || changed {
		t.Fatalf("withdraw of an unknown order: changed=%v err=%v", changed, err)
	}

	if _, err := s.ImportProductionOrder(ctx, importReq(order, 2, pjob(10, 1, "process", m.ID, "2"), pjob(20, 1, "process", m.ID, "1"), pjob(30, 1, "process", m.ID, "1"))); err != nil {
		t.Fatal(err)
	}
	ops, err := s.ListOperations(ctx, store.OperationFilter{OrderID: order, Limit: 10})
	if err != nil || len(ops) != 3 {
		t.Fatalf("ops %+v err=%v", ops, err)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FirmJob(ctx, ops[1].ID, true, store.Idem{}); err != nil {
		t.Fatal(err)
	}

	res, changed, err := s.WithdrawProductionOrder(ctx, store.OrderWithdrawal{OrderID: order, To: "Estimated", EventID: ids.New()})
	if err != nil || !changed {
		t.Fatalf("withdraw: changed=%v err=%v", changed, err)
	}
	if res.Status != store.ImportWithdrawn || res.Removed != 2 || res.KeptFirmed != 1 || res.WithdrawnAt == nil || res.WithdrawnTo == nil || *res.WithdrawnTo != "Estimated" || res.Reimportable {
		t.Fatalf("withdrawn = %+v", res)
	}
	left, err := s.ListOperations(ctx, store.OperationFilter{OrderID: order, Limit: 10})
	if err != nil || len(left) != 1 || left[0].OperNumber != 20 {
		t.Fatalf("operations left %+v err=%v, want only the firmed operation 20", left, err)
	}
	var jobs int
	if err := admin.QueryRow(context.Background(), `select count(*) from scheduled_job where order_id = $1::uuid and oper_number <> 20`, order).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("%d jobs left for withdrawn operations, err=%v", jobs, err)
	}

	// Withdrawing again changes nothing and writes no second event.
	if _, changed, err := s.WithdrawProductionOrder(ctx, store.OrderWithdrawal{OrderID: order, To: "Created", EventID: ids.New()}); err != nil || changed {
		t.Fatalf("second withdraw: changed=%v err=%v", changed, err)
	}
	var events int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = 'dev.passage.scheduling.orderimport.recorded.v1' and payload::text like '%"withdrawn"%'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("%d withdrawal events, err=%v, want 1", events, err)
	}
	if _, err := s.ReimportProductionOrder(ctx, order); problemCode(err) != store.CodeOrderWithdrawn {
		t.Fatalf("reimport of a withdrawn order: err=%v", err)
	}
	list, err := s.ListOrderImports(ctx, store.OrderImportFilter{Status: store.ImportWithdrawn, Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("withdrawn list %+v err=%v", list, err)
	}
	// Another tenant does not see or withdraw it.
	if _, changed, err := s.WithdrawProductionOrder(tenantCtx(2), store.OrderWithdrawal{OrderID: order, To: "Created", EventID: ids.New()}); err != nil || changed {
		t.Fatalf("withdraw from another tenant: changed=%v err=%v", changed, err)
	}

	// Production schedules the order again: it is imported and no longer withdrawn.
	back, err := s.ImportProductionOrder(ctx, importReq(order, 3, pjob(10, 1, "process", m.ID, "2"), pjob(20, 1, "process", m.ID, "1")))
	if err != nil || back.Status != store.ImportImported || back.Created != 1 || back.WithdrawnAt != nil || back.WithdrawnTo != nil || !back.Reimportable {
		t.Fatalf("back = %+v err=%v", back, err)
	}
}

func TestWithdrawProductionOrder_EventOrderDecidesBetweenWithdrawalAndSchedule(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	order := ids.New()
	t0 := time.Date(2031, 3, 3, 6, 0, 0, 0, time.UTC)

	req := importReq(order, 2, pjob(10, 1, "process", m.ID, "2"))
	req.EventTime = t0
	if _, err := s.ImportProductionOrder(ctx, req); err != nil {
		t.Fatal(err)
	}
	// A reset produced before the schedule event that was already imported.
	old, changed, err := s.WithdrawProductionOrder(ctx, store.OrderWithdrawal{OrderID: order, To: "Estimated", EventID: ids.New(), EventTime: t0.Add(-time.Minute)})
	if err != nil || changed || !old.Stale || old.Status != store.ImportImported {
		t.Fatalf("older withdrawal: %+v changed=%v err=%v, want stale and still imported", old, changed, err)
	}
	if _, changed, err := s.WithdrawProductionOrder(ctx, store.OrderWithdrawal{OrderID: order, To: "Cancelled", EventID: ids.New(), EventTime: t0.Add(time.Minute)}); err != nil || !changed {
		t.Fatalf("newer withdrawal: changed=%v err=%v", changed, err)
	}
	// A schedule event produced before the withdrawal, even with a newer order
	// version than the one imported, does not bring the order back.
	late := importReq(order, 3, pjob(10, 1, "process", m.ID, "2"))
	late.EventTime = t0.Add(30 * time.Second)
	got, err := s.ImportProductionOrder(ctx, late)
	if err != nil || !got.Stale || got.Status != store.ImportWithdrawn {
		t.Fatalf("schedule event older than the withdrawal: %+v err=%v, want stale and withdrawn", got, err)
	}
	if ops, err := s.ListOperations(ctx, store.OperationFilter{OrderID: order, Limit: 10}); err != nil || len(ops) != 0 {
		t.Fatalf("operations %+v err=%v, want none", ops, err)
	}
	next := importReq(order, 4, pjob(10, 1, "process", m.ID, "2"))
	next.EventTime = t0.Add(2 * time.Minute)
	if got, err := s.ImportProductionOrder(ctx, next); err != nil || got.Stale || got.Status != store.ImportImported || got.Created != 1 {
		t.Fatalf("newer schedule event: %+v err=%v, want imported", got, err)
	}
}
