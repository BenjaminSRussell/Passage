package store_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	tmsevents "github.com/BenjaminSRussell/passage/services/transport/internal/events"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

var truck = domain.Capacity{MaxWeightKg: dec("20000"), MaxVolumeM3: dec("90"), MaxPallets: 26}

func shipments(n int, site, weight string) []domain.LoadShipment {
	out := make([]domain.LoadShipment, n)
	for i := range out {
		out[i] = domain.LoadShipment{ID: ids.New(), ShipFromSiteID: site, ShipToRegion: "US-SE", Mode: domain.ModeTL,
			PickupDate: "2026-06-01", WeightKg: dec(weight), VolumeM3: dec("2.5"), Pallets: 1}
	}
	return out
}

func built(t *testing.T, ships []domain.LoadShipment) []domain.Load {
	t.Helper()
	loads, err := domain.BuildLoads(ships, truck)
	if err != nil {
		t.Fatal(err)
	}
	return loads
}

// TestBuildLoads_AC348StoredReplayedAndIsolated: 30 shipments of 1,600 kg on a
// 20,000 kg rule are stored as 3 loads holding every shipment once; the same
// key replays them, another body under that key is refused, another tenant
// sees none of them.
func TestBuildLoads_AC348StoredReplayedAndIsolated(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	ships := shipments(30, ids.New(), "1600")
	idem := store.Idem{Key: "build-1", Fingerprint: "hash-1"}
	loads, err := s.BuildLoads(ctx, built(t, ships), idem)
	if err != nil {
		t.Fatal(err)
	}
	if len(loads) != 3 {
		t.Fatalf("loads = %d, want 3", len(loads))
	}
	seen := map[string]bool{}
	for _, l := range loads {
		if l.ID == "" || l.Version != 1 || l.Status != domain.LoadPlanned || l.TotalWeightKg.GreaterThan(truck.MaxWeightKg) {
			t.Fatalf("load = %+v", l)
		}
		got, err := s.GetLoad(ctx, l.ID)
		if err != nil || !got.TotalWeightKg.Equal(l.TotalWeightKg) || len(got.ShipmentIDs) != len(l.ShipmentIDs) || !got.Capacity.MaxWeightKg.Equal(dec("20000")) {
			t.Fatalf("stored load = %+v, %v", got, err)
		}
		for _, id := range got.ShipmentIDs {
			seen[id] = true
		}
	}
	if len(seen) != 30 {
		t.Fatalf("stored shipments = %d, want 30", len(seen))
	}

	replay, err := s.BuildLoads(ctx, built(t, ships), idem)
	if err != nil || len(replay) != 3 || replay[0].ID != loads[0].ID || replay[2].ID != loads[2].ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := s.BuildLoads(ctx, built(t, shipments(1, ids.New(), "10")), store.Idem{Key: "build-1", Fingerprint: "hash-2"}); problemCode(t, err) != "idempotency_key_reuse" {
		t.Fatalf("key reuse = %v", err)
	}

	if list, err := s.ListLoads(ctx, store.LoadFilter{Status: domain.LoadPlanned}); err != nil || len(list) != 3 {
		t.Fatalf("list = %d, %v", len(list), err)
	}
	if list, err := s.ListLoads(ctx, store.LoadFilter{Status: "TENDERED"}); err != nil || len(list) != 0 {
		t.Fatalf("tendered list = %d, %v", len(list), err)
	}
	other := tenantCtx(2)
	if list, err := s.ListLoads(other, store.LoadFilter{}); err != nil || len(list) != 0 {
		t.Fatalf("other tenant sees %d loads, %v", len(list), err)
	}
	if _, err := s.GetLoad(other, loads[0].ID); problemCode(t, err) != "TMS-0100" {
		t.Fatalf("other tenant get = %v", err)
	}
}

// TestBuildLoads_ShipmentPlannedOnce: a shipment already on a load is TMS-0116,
// also when two builds race for it; the losing build stores nothing.
func TestBuildLoads_ShipmentPlannedOnce(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	ships := shipments(2, ids.New(), "500")
	if _, err := s.BuildLoads(ctx, built(t, ships[:1]), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BuildLoads(ctx, built(t, ships), store.Idem{}); problemCode(t, err) != domain.CodeShipmentPlanned {
		t.Fatalf("replanned shipment = %v", err)
	}
	if list, _ := s.ListLoads(ctx, store.LoadFilter{}); len(list) != 1 {
		t.Fatalf("loads after refused build = %d, want 1", len(list))
	}

	race := shipments(1, ids.New(), "700")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.BuildLoads(ctx, built(t, race), store.Idem{})
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if problemCode(t, err) != domain.CodeShipmentPlanned {
			t.Fatalf("race error = %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d of 2 racing builds stored the shipment, want 1", ok)
	}
}

// TestDGCheck_AC3413BlocksTender: a FAIL check is stored with its findings and
// shipping papers, and the load cannot be tendered until a later check passes.
func TestDGCheck_AC3413BlocksTender(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	loads, err := s.BuildLoads(ctx, built(t, shipments(1, ids.New(), "800")), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	load := loads[0].ID
	bad := domain.DGRequest{Regulation: domain.Reg49CFR, EmergencyResponsePhone: "+18005551234", Lines: []domain.DGLine{
		{UNNumber: "UN0081", ProperShippingName: "Explosive, blasting, type A", Class: "1.1D", PackingGroup: "II", Quantity: dec("25"), TransportCategory: 1},
		{UNNumber: "UN1203", ProperShippingName: "Gasoline", Class: "3", PackingGroup: "II", Quantity: dec("200"), TransportCategory: 2},
	}}
	check := func(req domain.DGRequest, at time.Time) domain.DGCheck {
		t.Helper()
		result, points, findings := domain.CheckDG(req)
		c, err := s.RecordDGCheck(ctx, domain.DGCheck{LoadID: load, Regulation: req.Regulation, Result: result, ADRPoints: points,
			Findings: findings, ShippingPapers: domain.ShippingPapers(req), CheckedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	failed := check(bad, t0)
	if failed.Result != domain.DGFail || failed.CheckedBy != "user-test" || len(failed.Findings) != 1 || failed.Findings[0].Code != domain.CodeDGSegregation {
		t.Fatalf("failed check = %+v", failed)
	}
	if len(failed.ShippingPapers) != 2 || failed.ShippingPapers[1].UNNumber != "UN1203" || !failed.ShippingPapers[1].Quantity.Equal(dec("200")) {
		t.Fatalf("papers = %+v", failed.ShippingPapers)
	}
	if _, err := s.CreateTender(ctx, sentTender(load, time.Hour, ids.New()), store.Idem{}, t0); problemCode(t, err) != domain.CodeDGSegregation {
		t.Fatalf("tender after FAIL = %v, want TMS-0130", err)
	}
	if list, _ := s.ListTenders(ctx, store.TenderFilter{LoadID: load}); len(list) != 0 {
		t.Fatalf("a tender was stored for a failed DG load: %+v", list)
	}

	split := bad
	split.Lines = bad.Lines[1:]
	check(split, t0.Add(time.Minute))
	checks, err := s.ListDGChecks(ctx, load)
	if err != nil || len(checks) != 2 || checks[0].Result != domain.DGPass || checks[1].ID != failed.ID {
		t.Fatalf("checks newest first = %+v, %v", checks, err)
	}
	if _, err := s.CreateTender(ctx, sentTender(load, time.Hour, ids.New()), store.Idem{}, t0); err != nil {
		t.Fatalf("tender after PASS = %v", err)
	}

	if _, err := s.RecordDGCheck(ctx, domain.DGCheck{LoadID: ids.New(), Regulation: domain.RegADR, Result: domain.DGPass,
		ADRPoints: dec("0"), Findings: []domain.DGFinding{}, ShippingPapers: []domain.PaperLine{}, CheckedAt: t0}); problemCode(t, err) != "TMS-0100" {
		t.Fatalf("check on unknown load = %v", err)
	}
	if _, err := s.RecordDGCheck(tenantCtx(2), domain.DGCheck{LoadID: load, Regulation: domain.RegADR, Result: domain.DGPass,
		ADRPoints: dec("0"), Findings: []domain.DGFinding{}, ShippingPapers: []domain.PaperLine{}, CheckedAt: t0}); problemCode(t, err) != "TMS-0100" {
		t.Fatalf("check on another tenant's load = %v", err)
	}
}

func matchedBill(t *testing.T, s *store.Postgres, invoiced string) domain.FreightBill {
	t.Helper()
	ctx := tenantCtx(1)
	carrier := ids.New()
	bill, err := s.CreateFreightBill(ctx, newBill(carrier, "412.50"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	_, matched, err := s.RegisterCarrierInvoice(ctx, newInvoice(carrier, "INV-"+bill.ID[:8], &bill.ID, invoiced))
	if err != nil {
		t.Fatal(err)
	}
	return *matched
}

// TestDecideFreightBill_BR344: AC-34.4's VARIANCE bill is disputed, then
// approved with a reason; the approval is one outbox event carrying the
// accrued and invoiced amounts, and the decisions are the audit trail.
func TestDecideFreightBill_BR344(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	bill := matchedBill(t, s, "418.00")
	if bill.MatchStatus != domain.MatchVariance {
		t.Fatalf("bill = %s, want VARIANCE", bill.MatchStatus)
	}
	if _, err := s.DecideFreightBill(ctx, bill.ID, domain.ActionDispute, "fuel charged twice", bill.Version+1, store.Idem{}, t0); problemCode(t, err) != store.CodeVersionConflict {
		t.Fatalf("stale version = %v", err)
	}
	disputed, err := s.DecideFreightBill(ctx, bill.ID, domain.ActionDispute, "fuel charged twice", bill.Version, store.Idem{Key: "d-1", Fingerprint: "fp-d"}, t0)
	if err != nil || disputed.MatchStatus != domain.BillDisputed || disputed.Version != bill.Version+1 {
		t.Fatalf("dispute = %+v, %v", disputed, err)
	}
	if replay, err := s.DecideFreightBill(ctx, bill.ID, domain.ActionDispute, "fuel charged twice", bill.Version, store.Idem{Key: "d-1", Fingerprint: "fp-d"}, t0); err != nil || replay.Version != disputed.Version {
		t.Fatalf("dispute replay = %+v, %v", replay, err)
	}
	if _, err := s.DecideFreightBill(ctx, bill.ID, domain.ActionApprove, "", disputed.Version, store.Idem{}, t0); problemCode(t, err) != domain.CodeBillState {
		t.Fatalf("approve a dispute without reason = %v", err)
	}
	approved, err := s.DecideFreightBill(ctx, bill.ID, domain.ActionApprove, "carrier confirmed the fuel table", disputed.Version, store.Idem{}, t0.Add(time.Hour))
	if err != nil || approved.MatchStatus != domain.BillApproved {
		t.Fatalf("approve = %+v, %v", approved, err)
	}
	if _, err := s.DecideFreightBill(ctx, bill.ID, domain.ActionApprove, "again", approved.Version, store.Idem{}, t0); problemCode(t, err) != domain.CodeBillState {
		t.Fatalf("second approval = %v", err)
	}

	decisions, err := s.ListBillDecisions(ctx, bill.ID)
	if err != nil || len(decisions) != 2 {
		t.Fatalf("decisions = %+v, %v", decisions, err)
	}
	if d := decisions[0]; d.Action != domain.ActionDispute || d.FromStatus != domain.MatchVariance || d.ToStatus != domain.BillDisputed || d.DecidedBy != "user-test" {
		t.Fatalf("dispute decision = %+v", d)
	}
	if d := decisions[1]; d.Action != domain.ActionApprove || d.FromStatus != domain.BillDisputed || d.Reason != "carrier confirmed the fuel table" {
		t.Fatalf("approve decision = %+v", d)
	}

	evts := eventsOf(t, admin, tmsevents.FreightBillApproved)
	if len(evts) != 1 || evts[0].Subject != bill.ID {
		t.Fatalf("approved events = %+v", evts)
	}
	var p tmsevents.FreightBillApprovedPayload
	if err := json.Unmarshal(evts[0].Data, &p); err != nil {
		t.Fatal(err)
	}
	if p.ExpectedTotal != "412.50" || p.InvoicedTotal != "418.00" || p.Variance != "5.50" || p.FromStatus != domain.BillDisputed || p.CarrierInvoiceID == "" || p.ApprovedBy != "user-test" {
		t.Fatalf("approved payload = %+v", p)
	}
	if len(eventsOf(t, admin, tmsevents.FreightBillMatched)) != 1 {
		t.Fatal("a decision wrote a second matched event")
	}

	if _, err := s.DecideFreightBill(tenantCtx(2), bill.ID, domain.ActionApprove, "x", approved.Version, store.Idem{}, t0); problemCode(t, err) != "TMS-0100" {
		t.Fatalf("other tenant decision = %v", err)
	}
	if list, err := s.ListBillDecisions(tenantCtx(2), bill.ID); err != nil || len(list) != 0 {
		t.Fatalf("other tenant sees %d decisions, %v", len(list), err)
	}
}

// TestDecideFreightBill_MatchedApprovesWithoutReason and an UNMATCHED bill
// cannot be decided at all.
func TestDecideFreightBill_MatchedAndUnmatched(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	bill := matchedBill(t, s, "414.00")
	if bill.MatchStatus != domain.MatchMatched {
		t.Fatalf("bill = %s, want MATCHED", bill.MatchStatus)
	}
	if _, err := s.DecideFreightBill(ctx, bill.ID, domain.ActionDispute, "no", bill.Version, store.Idem{}, t0); problemCode(t, err) != domain.CodeBillState {
		t.Fatalf("dispute a matched bill = %v", err)
	}
	if approved, err := s.DecideFreightBill(ctx, bill.ID, domain.ActionApprove, "", bill.Version, store.Idem{}, t0); err != nil || approved.MatchStatus != domain.BillApproved {
		t.Fatalf("approve matched = %+v, %v", approved, err)
	}
	open, err := s.CreateFreightBill(ctx, newBill(ids.New(), "100"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideFreightBill(ctx, open.ID, domain.ActionApprove, "x", open.Version, store.Idem{}, t0); problemCode(t, err) != domain.CodeBillState {
		t.Fatalf("approve unmatched = %v", err)
	}
}
