package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	tmsevents "github.com/BenjaminSRussell/passage/services/transport/internal/events"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

var t0 = time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)

type event struct {
	Type    string
	Subject string
	Data    json.RawMessage
}

// events returns the outbox rows of one type, oldest first, read as the admin role.
func eventsOf(t *testing.T, admin *pgxpool.Pool, typ string) []event {
	t.Helper()
	rows, err := admin.Query(context.Background(), `select type, payload from outbox where type = $1 order by created_at, id`, typ)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []event
	for rows.Next() {
		var e event
		var raw []byte
		if err := rows.Scan(&e.Type, &raw); err != nil {
			t.Fatal(err)
		}
		var env struct {
			Subject string          `json:"subject"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		e.Subject, e.Data = env.Subject, env.Data
		out = append(out, e)
	}
	return out
}

func tenderPayload(t *testing.T, e event) tmsevents.TenderPayload {
	t.Helper()
	var p tmsevents.TenderPayload
	if err := json.Unmarshal(e.Data, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func sentTender(load string, timeout time.Duration, carriers ...string) domain.Tender {
	return sentAt(t0, load, timeout, carriers...)
}

func sentAt(at time.Time, load string, timeout time.Duration, carriers ...string) domain.Tender {
	tender, _ := domain.NewTender(load, carriers, timeout)
	tender.Send(at)
	return tender
}

// TestTender_AC343 drives AC-34.3 through the store with a test clock: carrier 1
// ignores the 10-minute tender, carrier 2 declines, carrier 3 accepts. Every
// outcome is an outbox event in the same transaction as the state change.
func TestTender_AC343(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	c1, c2, c3 := ids.New(), ids.New(), ids.New()
	in := sentTender(seedLoad(t, s, ctx), 10*time.Minute, c1, c2, c3)
	in.Currency, in.Rates = "USD", map[string]decimal.Decimal{c3: dec("412.5")}
	created, err := s.CreateTender(ctx, in, store.Idem{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Status != domain.TenderPending || created.Version != 1 || len(created.Attempts) != 1 {
		t.Fatalf("created = %+v", created)
	}

	swept, err := s.ExpireTenders(ctx, t0.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(swept) != 1 || swept[0].Attempts[1].CarrierID != c2 {
		t.Fatalf("sweep = %+v", swept)
	}
	if again, err := s.ExpireTenders(ctx, t0.Add(11*time.Minute)); err != nil || len(again) != 0 {
		t.Fatalf("second sweep = %+v, %v; carrier 2 is inside its deadline", again, err)
	}
	if _, err := s.RespondTender(ctx, created.ID, c2, false, t0.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	accepted, err := s.RespondTender(ctx, created.ID, c3, true, t0.Add(15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != domain.TenderAccepted || accepted.AcceptedCarrier != c3 || accepted.Version != 4 {
		t.Fatalf("accepted = %+v", accepted)
	}
	stored, err := s.GetTender(ctx, created.ID)
	if err != nil || stored.Status != domain.TenderAccepted || len(stored.Attempts) != 3 || !stored.Rates[c3].Equal(dec("412.5")) {
		t.Fatalf("stored = %+v, %v", stored, err)
	}

	expired := eventsOf(t, admin, tmsevents.TenderExpired)
	declined := eventsOf(t, admin, tmsevents.TenderDeclined)
	acc := eventsOf(t, admin, tmsevents.TenderAccepted)
	if len(expired) != 1 || len(declined) != 1 || len(acc) != 1 {
		t.Fatalf("events: %d expired, %d declined, %d accepted", len(expired), len(declined), len(acc))
	}
	if p := tenderPayload(t, expired[0]); p.CarrierID != c1 || p.Rank != 1 || p.Response != domain.ResponseExpired || p.Rate != "" {
		t.Fatalf("expired payload = %+v", p)
	}
	if p := tenderPayload(t, declined[0]); p.CarrierID != c2 || p.Rank != 2 {
		t.Fatalf("declined payload = %+v", p)
	}
	if p := tenderPayload(t, acc[0]); p.CarrierID != c3 || p.Rank != 3 || p.Rate != "412.50" || p.Currency != "USD" || acc[0].Subject != created.ID {
		t.Fatalf("accepted payload = %+v subject %s", p, acc[0].Subject)
	}

	// A repeated acceptance replays; it writes nothing.
	if replay, err := s.RespondTender(ctx, created.ID, c3, true, t0.Add(16*time.Minute)); err != nil || replay.Version != 4 {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if n := len(eventsOf(t, admin, tmsevents.TenderAccepted)); n != 1 {
		t.Fatalf("replay wrote %d accepted events", n)
	}
}

// TestTender_LateAcceptanceSavesTheCascade: BR-34.3's late acceptance is
// TMS-0105, but the expiry it uncovered is saved with its event.
func TestTender_LateAcceptanceSavesTheCascade(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	c1, c2 := ids.New(), ids.New()
	created, err := s.CreateTender(ctx, sentTender(seedLoad(t, s, ctx), 10*time.Minute, c1, c2), store.Idem{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RespondTender(ctx, created.ID, c1, true, t0.Add(11*time.Minute)); problemCode(t, err) != domain.CodeTenderExpired {
		t.Fatalf("late acceptance = %v", err)
	}
	stored, _ := s.GetTender(ctx, created.ID)
	if a, ok := stored.Pending(); !ok || a.CarrierID != c2 || stored.Version != 2 {
		t.Fatalf("stored = %+v, want carrier 2 pending", stored)
	}
	if n := len(eventsOf(t, admin, tmsevents.TenderExpired)); n != 1 {
		t.Fatalf("expired events = %d", n)
	}
	if _, err := s.RespondTender(ctx, created.ID, c1, false, t0.Add(12*time.Minute)); problemCode(t, err) != domain.CodeTenderExpired {
		t.Fatalf("second late response = %v", err)
	}
	if _, err := s.RespondTender(ctx, created.ID, ids.New(), true, t0.Add(12*time.Minute)); problemCode(t, err) != domain.CodeTenderNotPending {
		t.Fatalf("unknown carrier = %v", err)
	}
	if stored, _ := s.GetTender(ctx, created.ID); stored.Version != 2 {
		t.Fatalf("rejected responses changed the tender: version %d", stored.Version)
	}
}

// TestTender_OneOpenTenderPerLoad: a load with a pending tender inside its
// deadline, or with an accepted tender, refuses another (TMS-0114). A pending
// tender that has run out of carriers by time is failed first and no longer blocks.
func TestTender_OneOpenTenderPerLoad(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	load, c1 := seedLoad(t, s, ctx), ids.New()
	first, err := s.CreateTender(ctx, sentTender(load, 10*time.Minute, c1), store.Idem{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTender(ctx, sentAt(t0.Add(5*time.Minute), load, 10*time.Minute, ids.New()), store.Idem{}, t0.Add(5*time.Minute)); problemCode(t, err) != domain.CodeTenderOpen {
		t.Fatalf("second pending tender = %v", err)
	}
	second, err := s.CreateTender(ctx, sentAt(t0.Add(30*time.Minute), load, 10*time.Minute, c1), store.Idem{}, t0.Add(30*time.Minute))
	if err != nil {
		t.Fatalf("re-tender after the first failed by time: %v", err)
	}
	if failed, _ := s.GetTender(ctx, first.ID); failed.Status != domain.TenderFailed || !failed.PlannerTask {
		t.Fatalf("first tender = %+v, want TENDER_FAILED with a planner task", failed)
	}
	if n := len(eventsOf(t, admin, tmsevents.TenderExpired)); n != 1 {
		t.Fatalf("expired events = %d", n)
	}
	if _, err := s.RespondTender(ctx, second.ID, c1, true, t0.Add(31*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTender(ctx, sentAt(t0.Add(time.Hour), load, 10*time.Minute, ids.New()), store.Idem{}, t0.Add(time.Hour)); problemCode(t, err) != domain.CodeTenderOpen {
		t.Fatalf("tender after acceptance = %v", err)
	}
	list, err := s.ListTenders(ctx, store.TenderFilter{LoadID: load})
	if err != nil || len(list) != 2 || list[0].ID != second.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if failed, _ := s.ListTenders(ctx, store.TenderFilter{Status: domain.TenderFailed}); len(failed) != 1 || failed[0].ID != first.ID {
		t.Fatalf("failed filter = %+v", failed)
	}
}

func TestTender_IdempotentAndTenantIsolated(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	in := sentTender(seedLoad(t, s, ctx), 10*time.Minute, ids.New())
	key := store.Idem{Key: "tender-1", Fingerprint: "a"}
	first, err := s.CreateTender(ctx, in, key, t0)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CreateTender(ctx, in, key, t0.Add(time.Minute))
	if err != nil || again.ID != first.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if _, err := s.CreateTender(ctx, in, store.Idem{Key: "tender-1", Fingerprint: "b"}, t0); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("reuse = %v", err)
	}
	other := tenantCtx(2)
	if _, err := s.GetTender(other, first.ID); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("tenant 2 read = %v", err)
	}
	if list, _ := s.ListTenders(other, store.TenderFilter{}); len(list) != 0 {
		t.Fatalf("tenant 2 list = %+v", list)
	}
	if swept, _ := s.ExpireTenders(other, t0.Add(time.Hour)); len(swept) != 0 {
		t.Fatalf("tenant 2 sweep advanced %+v", swept)
	}
	if _, err := s.RespondTender(other, first.ID, in.Carriers[0], true, t0); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("tenant 2 respond = %v", err)
	}
}

// TestTracking_IngestReplayAndTimeline covers the source+ref idempotency key,
// the outbox row per stored event and the occurred_at timeline order (BR-34.9).
func TestTracking_IngestReplayAndTimeline(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	load := seedLoad(t, s, ctx)
	d1 := domain.TrackingEvent{LoadID: load, StopNo: 2, Code: "D1", OccurredAt: t0.Add(3 * time.Hour), Location: "Dallas TX", Source: "EDI214", Ref: "ISA-1"}
	saved, inserted, err := s.RecordTrackingEvent(ctx, d1)
	if err != nil || !inserted || saved.ID == "" || !saved.OccurredAt.Equal(d1.OccurredAt) || saved.ReceivedAt.IsZero() {
		t.Fatalf("saved = %+v inserted %v err %v", saved, inserted, err)
	}
	replay, inserted, err := s.RecordTrackingEvent(ctx, d1)
	if err != nil || inserted || replay.ID != saved.ID {
		t.Fatalf("replay = %+v inserted %v err %v", replay, inserted, err)
	}
	changed := d1
	changed.Code = "X1"
	if _, _, err := s.RecordTrackingEvent(ctx, changed); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("different report under the same key = %v", err)
	}
	af := domain.TrackingEvent{LoadID: load, StopNo: 1, Code: "AF", OccurredAt: t0, Source: "API", Ref: "a-1"}
	if _, _, err := s.RecordTrackingEvent(ctx, af); err != nil {
		t.Fatal(err)
	}
	timeline, err := s.ListTrackingEvents(ctx, store.TrackingFilter{LoadID: load})
	if err != nil || len(timeline) != 2 || timeline[0].Code != "AF" || timeline[1].Code != "D1" {
		t.Fatalf("timeline = %+v, %v", timeline, err)
	}
	if recent, _ := s.ListTrackingEvents(ctx, store.TrackingFilter{}); len(recent) != 2 || recent[0].Code != "AF" {
		t.Fatalf("recent = %+v, want the newest received first", recent)
	}
	stored := eventsOf(t, admin, tmsevents.TrackingEventRecorded)
	if len(stored) != 2 || stored[0].Subject != load {
		t.Fatalf("tracking events = %+v", stored)
	}
	var p tmsevents.TrackingPayload
	if err := json.Unmarshal(stored[0].Data, &p); err != nil || p.Code != "D1" || p.StopNo != 2 || p.OccurredAt != "2026-06-01T11:00:00Z" {
		t.Fatalf("payload = %+v, %v", p, err)
	}
	if other, _ := s.ListTrackingEvents(tenantCtx(2), store.TrackingFilter{LoadID: load}); len(other) != 0 {
		t.Fatalf("tenant 2 sees %+v", other)
	}
	// Tenant 2 may use the same source and ref for its own report.
	changed.LoadID = seedLoad(t, s, tenantCtx(2))
	if _, inserted, err := s.RecordTrackingEvent(tenantCtx(2), changed); err != nil || !inserted {
		t.Fatalf("tenant 2 ingest = %v %v", inserted, err)
	}
}

func newVoyage() domain.Voyage {
	return domain.NormalizeVoyage(domain.Voyage{
		Reference:  "MAEU-2611E",
		Currency:   "USD",
		Legs:       []domain.Leg{{From: "CNSHA", To: "USLAX", Mode: "OCEAN"}},
		Containers: []domain.Container{{Number: "CSQU3054383", Type: "40HC", Seal: "778812"}},
		Folios:     []domain.Folio{{FolioNo: "F-1", Forwarder: "Kuehne", Containers: []string{"CSQU3054383"}}},
		CostLines: []domain.VoyageCostLine{
			{CostType: "FREIGHT", Amount: dec("1000.00"), AllocationMethod: domain.MethodWeight},
			{CostType: "DUTY", Amount: dec("45.55"), AllocationMethod: domain.MethodValue},
		},
		ReceiptLines: []domain.VoyageReceiptLine{
			{Line: 1, Value: dec("100"), Weight: dec("1")},
			{Line: 2, Value: dec("100"), Weight: dec("1")},
			{Line: 3, Value: dec("250"), Weight: dec("1")},
		},
	})
}

// TestVoyage_AllocateAC346AndReconcile posts a voyage and checks AC-34.6 and the
// PRD reconciliation "Σ landed cost posted = Σ voyage actual costs": the event's
// receipt lines sum to the voyage cost total to the cent.
func TestVoyage_AllocateAC346AndReconcile(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	created, err := s.CreateVoyage(ctx, newVoyage(), store.Idem{Key: "v-1", Fingerprint: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != domain.VoyageEstimated || created.Version != 1 || len(created.Containers) != 1 || len(created.Allocations) != 0 {
		t.Fatalf("created = %+v", created)
	}
	if again, err := s.CreateVoyage(ctx, newVoyage(), store.Idem{Key: "v-1", Fingerprint: "a"}); err != nil || again.ID != created.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if _, err := s.CreateVoyage(ctx, newVoyage(), store.Idem{Key: "v-1", Fingerprint: "b"}); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("reuse = %v", err)
	}

	posted, err := s.AllocateVoyage(ctx, created.ID, t0)
	if err != nil {
		t.Fatal(err)
	}
	if posted.Status != domain.VoyagePosted || posted.PostedAt == nil || !posted.PostedAt.Equal(t0) || posted.Version != 2 {
		t.Fatalf("posted = %+v", posted)
	}
	freight := posted.CostAllocations[0]
	for i, want := range []string{"333.33", "333.33", "333.34"} {
		if !freight.Shares[i].Amount.Equal(dec(want)) {
			t.Fatalf("freight share %d = %s, want %s", i, freight.Shares[i].Amount, want)
		}
	}
	events := eventsOf(t, admin, tmsevents.LandedCostPosted)
	if len(events) != 1 || events[0].Subject != created.ID {
		t.Fatalf("landedcost events = %+v", events)
	}
	var p tmsevents.LandedCostPostedPayload
	if err := json.Unmarshal(events[0].Data, &p); err != nil {
		t.Fatal(err)
	}
	sum := decimal.Zero
	for _, l := range p.ReceiptLines {
		sum = sum.Add(dec(l.Amount))
	}
	if p.Total != "1045.55" || !sum.Equal(dec("1045.55")) || len(p.ReceiptLines) != 3 {
		t.Fatalf("payload = %+v (sum %s), want 1045.55", p, sum)
	}

	// A retry returns the posted voyage and writes nothing.
	if replay, err := s.AllocateVoyage(ctx, created.ID, t0.Add(time.Hour)); err != nil || replay.Version != 2 || !replay.PostedAt.Equal(t0) {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if n := len(eventsOf(t, admin, tmsevents.LandedCostPosted)); n != 1 {
		t.Fatalf("retry wrote %d events", n)
	}
	list, err := s.ListVoyages(ctx, store.VoyageFilter{Status: domain.VoyagePosted})
	if err != nil || len(list) != 1 || !list[0].CostTotal.Equal(dec("1045.55")) || list[0].Containers != 1 || list[0].ReceiptLines != 3 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if est, _ := s.ListVoyages(ctx, store.VoyageFilter{Status: domain.VoyageEstimated}); len(est) != 0 {
		t.Fatalf("estimated filter = %+v", est)
	}
}

func TestVoyage_TenantIsolated(t *testing.T) {
	s, _ := setup(t)
	created, err := s.CreateVoyage(tenantCtx(1), newVoyage(), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	other := tenantCtx(2)
	if _, err := s.GetVoyage(other, created.ID); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("tenant 2 get = %v", err)
	}
	if _, err := s.AllocateVoyage(other, created.ID, t0); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("tenant 2 allocate = %v", err)
	}
	if list, _ := s.ListVoyages(other, store.VoyageFilter{}); len(list) != 0 {
		t.Fatalf("tenant 2 list = %+v", list)
	}
	if v, err := s.GetVoyage(tenantCtx(1), created.ID); err != nil || v.Status != domain.VoyageEstimated {
		t.Fatalf("tenant 1 get = %+v, %v", v, err)
	}
}
