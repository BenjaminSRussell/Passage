package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// label records a carrier label on the shipment, as :carrier-label does.
func label(t *testing.T, s *store.Postgres, ctx context.Context, shipmentID, carrier, tracking string) {
	t.Helper()
	if _, _, err := s.RecordTracking(ctx, shipmentID, store.TrackingEvent{Provider: carrier, EventKey: "label:" + tracking, TrackingNumber: tracking,
		Status: "LABELLED", Description: "label", EventTime: time.Now().UTC()}); err != nil {
		t.Fatalf("label %s: %v", tracking, err)
	}
}

// shipIn releases, packs and posts one order of the legal entity in the warehouse.
func shipIn(t *testing.T, s *store.Postgres, ctx context.Context, warehouse, legalEntity string) store.Shipment {
	t.Helper()
	f := releasedIn(t, s, ctx, warehouse, legalEntity, 4)
	packed := pickAndPack(t, s, ctx, f.shipment)
	if _, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(f.shipment, "2")}, idem("post")); err != nil {
		t.Fatalf("post: %v", err)
	}
	sh, err := s.GetShipment(ctx, f.shipment.ID)
	if err != nil {
		t.Fatal(err)
	}
	return sh
}

func manifestRows(t *testing.T, admin *pgxpool.Pool, id string) (active, total int) {
	t.Helper()
	if err := admin.QueryRow(context.Background(), `select count(*) filter (where active), count(*) from carrier_manifest_line where manifest_id = $1`, id).Scan(&active, &total); err != nil {
		t.Fatal(err)
	}
	return
}

func TestManifest_ClosesShippedParcelsOnlyAndNumbersThem(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	wh, le := ids.New(), ids.New()
	a := shipIn(t, s, ctx, wh, le)
	b := shipIn(t, s, ctx, wh, le)
	waiting := releasedIn(t, s, ctx, wh, le, 3) // released, labelled, but not shipped
	other := shipIn(t, s, ctx, ids.New(), le)   // another warehouse
	label(t, s, ctx, a.ID, "ups", "1Z0001")
	label(t, s, ctx, a.ID, "ups", "1Z0002") // two parcels on one shipment
	label(t, s, ctx, b.ID, "ups", "1Z0003")
	label(t, s, ctx, b.ID, "dhl", "DHL01") // another carrier
	label(t, s, ctx, waiting.shipment.ID, "ups", "1Z0004")
	label(t, s, ctx, other.ID, "ups", "1Z0005")

	cands, err := s.ManifestCandidates(ctx, store.ManifestCandidateFilter{WarehouseID: wh, Carrier: "ups", Limit: 100})
	if err != nil || len(cands) != 4 {
		t.Fatalf("candidates = %d, %v", len(cands), err)
	}
	ready := 0
	for _, c := range cands {
		if c.Ready {
			ready++
		} else if c.ShipmentID != waiting.shipment.ID {
			t.Errorf("parcel %s is not ready but its shipment shipped", c.TrackingNumber)
		}
	}
	if ready != 3 {
		t.Fatalf("ready parcels = %d, want 3", ready)
	}

	in := store.ManifestInput{WarehouseID: wh, LegalEntityID: le, Carrier: "ups", PickupDate: today(), ProviderManifestID: "UPS-77"}
	key := idem("close")
	m, err := s.CloseManifest(ctx, in, key)
	if err != nil || m.Number != "MF-0000001" || m.Status != "CLOSED" || m.ParcelCount != 3 || m.ShipmentCount != 2 || len(m.Parcels) != 3 ||
		m.ProviderManifestID == nil || *m.ProviderManifestID != "UPS-77" || m.PickupDate != today().Format("2006-01-02") || m.Version != 1 {
		t.Fatalf("close = %+v, %v", m, err)
	}
	if replay, err := s.CloseManifest(ctx, in, key); err != nil || replay.ID != m.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if n := countEvents(t, admin, fulevents.ManifestClosed); n != 1 {
		t.Fatalf("manifest.closed events after a close and its replay = %d, want 1", n)
	}
	if got := outboxData(t, admin, fulevents.ManifestClosed, "{parcel_count}"); got != "3" {
		t.Errorf("event parcel_count = %q", got)
	}

	// The waiting parcel is the only one left in this warehouse for UPS, and it cannot be manifested yet.
	left, err := s.ManifestCandidates(ctx, store.ManifestCandidateFilter{WarehouseID: wh, Carrier: "ups", Limit: 100})
	if err != nil || len(left) != 1 || left[0].Ready || left[0].TrackingNumber != "1Z0004" || left[0].ShipmentStatus != string(domain.StatusReleased) {
		t.Fatalf("left = %+v, %v", left, err)
	}
	if _, err := s.CloseManifest(ctx, in, idem("again")); problemCode(err) != store.CodeManifestEmpty {
		t.Fatalf("second close with nothing ready: %v", err)
	}

	// DHL is its own manifest, with the next number of the legal entity.
	d, err := s.CloseManifest(ctx, store.ManifestInput{WarehouseID: wh, LegalEntityID: le, Carrier: "dhl", PickupDate: today()}, idem("dhl"))
	if err != nil || d.Number != "MF-0000002" || d.ParcelCount != 1 || d.ProviderManifestID != nil {
		t.Fatalf("dhl = %+v, %v", d, err)
	}

	got, err := s.GetManifest(ctx, m.ID)
	if err != nil || len(got.Parcels) != 3 || !got.Parcels[0].Active {
		t.Fatalf("get = %+v, %v", got, err)
	}
	list, err := s.ListManifests(ctx, store.ManifestFilter{WarehouseID: wh, Limit: 10})
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d, %v", len(list), err)
	}
	if list, _ := s.ListManifests(ctx, store.ManifestFilter{Carrier: "dhl", Limit: 10}); len(list) != 1 || list[0].ID != d.ID || len(list[0].Parcels) != 0 {
		t.Fatalf("carrier filter = %+v", list)
	}
	from, to := today().AddDate(0, 0, 1), today().AddDate(0, 0, 2)
	if list, _ := s.ListManifests(ctx, store.ManifestFilter{PickupFrom: &from, PickupTo: &to, Limit: 10}); len(list) != 0 {
		t.Fatalf("date filter = %d, want 0", len(list))
	}
}

func TestManifest_CancelFreesParcelsAndRefusesRepeats(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	wh, le := ids.New(), ids.New()
	a := shipIn(t, s, ctx, wh, le)
	label(t, s, ctx, a.ID, "ups", "1Z1")
	in := store.ManifestInput{WarehouseID: wh, LegalEntityID: le, Carrier: "ups", PickupDate: today()}
	m, err := s.CloseManifest(ctx, in, idem("close"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelManifest(ctx, m.ID, 2, idem("stale")); problemCode(err) != store.CodeVersion {
		t.Fatalf("stale cancel: %v", err)
	}
	key := idem("cancel")
	c, err := s.CancelManifest(ctx, m.ID, 1, key)
	if err != nil || c.Status != "CANCELLED" || c.Version != 2 || c.CancelledAt == nil || len(c.Parcels) != 1 || c.Parcels[0].Active {
		t.Fatalf("cancel = %+v, %v", c, err)
	}
	if replay, err := s.CancelManifest(ctx, m.ID, 1, key); err != nil || replay.Version != 2 {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := s.CancelManifest(ctx, m.ID, 2, idem("twice")); problemCode(err) != store.CodeStatus {
		t.Fatalf("cancel of a cancelled manifest: %v", err)
	}
	if n := countEvents(t, admin, fulevents.ManifestCancelled); n != 1 {
		t.Fatalf("manifest.cancelled events = %d, want 1", n)
	}
	if active, total := manifestRows(t, admin, m.ID); active != 0 || total != 1 {
		t.Fatalf("lines after cancel: %d active of %d", active, total)
	}
	// The freed parcel goes on the next manifest, which gets the next number.
	n, err := s.CloseManifest(ctx, in, idem("reclose"))
	if err != nil || n.Number != "MF-0000002" || n.ParcelCount != 1 {
		t.Fatalf("reclose = %+v, %v", n, err)
	}
	if _, err := s.CancelManifest(ctx, ids.New(), 1, idem("none")); problemCode(err) != store.CodeManifestNotFound {
		t.Fatalf("unknown manifest: %v", err)
	}
	if _, err := s.GetManifest(ctx, ids.New()); problemCode(err) != store.CodeManifestNotFound {
		t.Fatalf("unknown get: %v", err)
	}
}

// Two closes racing for the same warehouse and carrier: one takes the parcel, the other finds nothing left.
func TestManifest_ConcurrentCloseTakesAParcelOnce(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	wh, le := ids.New(), ids.New()
	a := shipIn(t, s, ctx, wh, le)
	label(t, s, ctx, a.ID, "ups", "1Z9")
	in := store.ManifestInput{WarehouseID: wh, LegalEntityID: le, Carrier: "ups", PickupDate: today()}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.CloseManifest(ctx, in, idem(fmt.Sprintf("race%d", i)))
		}()
	}
	wg.Wait()
	ok, empty := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case problemCode(err) == store.CodeManifestEmpty:
			empty++
		default:
			t.Errorf("unexpected: %v", err)
		}
	}
	if ok != 1 || empty != 3 {
		t.Fatalf("closes ok=%d empty=%d, want 1 and 3", ok, empty)
	}
}

func TestManifest_TenantAndLegalEntityScope(t *testing.T) {
	s, _ := setup(t)
	all := ctxFor(1, "")
	wh, le, other := ids.New(), ids.New(), ids.New()
	a := shipIn(t, s, all, wh, le)
	label(t, s, all, a.ID, "ups", "1Z7")
	in := store.ManifestInput{WarehouseID: wh, LegalEntityID: le, Carrier: "ups", PickupDate: today()}

	// A caller scoped to another legal entity sees no parcel and cannot close a manifest for this one.
	scoped := ctxFor(1, other)
	if c, err := s.ManifestCandidates(scoped, store.ManifestCandidateFilter{Limit: 10}); err != nil || len(c) != 0 {
		t.Fatalf("scoped candidates = %+v, %v", c, err)
	}
	if _, err := s.CloseManifest(scoped, in, idem("scoped")); problemCode(err) != store.CodeManifestEmpty {
		t.Fatalf("scoped close: %v", err)
	}
	// Another tenant sees nothing either.
	if c, err := s.ManifestCandidates(ctxFor(2, ""), store.ManifestCandidateFilter{Limit: 10}); err != nil || len(c) != 0 {
		t.Fatalf("other tenant candidates = %+v, %v", c, err)
	}
	m, err := s.CloseManifest(all, in, idem("ok"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetManifest(scoped, m.ID); problemCode(err) != store.CodeManifestNotFound {
		t.Fatalf("scoped get: %v", err)
	}
	if _, err := s.GetManifest(ctxFor(2, ""), m.ID); problemCode(err) != store.CodeManifestNotFound {
		t.Fatalf("other tenant get: %v", err)
	}
	if l, err := s.ListManifests(scoped, store.ManifestFilter{Limit: 10}); err != nil || len(l) != 0 {
		t.Fatalf("scoped list = %+v, %v", l, err)
	}
	if _, err := s.CancelManifest(scoped, m.ID, 1, idem("sc")); problemCode(err) != store.CodeManifestNotFound {
		t.Fatalf("scoped cancel: %v", err)
	}
}

// scan records a carrier tracking event on the parcel, as a webhook or :carrier-track does.
func scan(t *testing.T, s *store.Postgres, ctx context.Context, shipmentID, carrier, tracking, status, key string, at time.Time) {
	t.Helper()
	if _, _, err := s.RecordTracking(ctx, shipmentID, store.TrackingEvent{Provider: carrier, EventKey: key, TrackingNumber: tracking,
		Status: status, Description: status, EventTime: at}); err != nil {
		t.Fatalf("scan %s %s: %v", tracking, status, err)
	}
}

// Only parcels still in the warehouse are candidates: not an old label, not one the carrier has scanned, not one of a
// delivered shipment; a label recorded twice (bought, then reported again by the carrier) is one parcel.
func TestManifest_CandidatesLeaveOutStaleAndCollectedParcels(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	wh, le := ids.New(), ids.New()
	now := time.Now().UTC()
	fresh := shipIn(t, s, ctx, wh, le)
	label(t, s, ctx, fresh.ID, "ups", "1ZA")
	scan(t, s, ctx, fresh.ID, "ups", "1ZA", "LABELLED", "carrier:1ZA:1", now) // the same label reported again
	label(t, s, ctx, fresh.ID, "ups", "1ZB")
	scan(t, s, ctx, fresh.ID, "ups", "1ZB", "IN_TRANSIT", "carrier:1ZB:2", now) // already collected
	old := shipIn(t, s, ctx, wh, le)
	scan(t, s, ctx, old.ID, "ups", "1ZOLD", "LABELLED", "label:1ZOLD", now.AddDate(0, 0, -store.ManifestLookbackDays-1))
	delivered := shipIn(t, s, ctx, wh, le)
	label(t, s, ctx, delivered.ID, "ups", "1ZC")
	label(t, s, ctx, delivered.ID, "ups", "1ZD")
	scan(t, s, ctx, delivered.ID, "ups", "1ZC", "DELIVERED", "carrier:1ZC:9", now) // the shipment is DELIVERED; 1ZD went with it

	cands, err := s.ManifestCandidates(ctx, store.ManifestCandidateFilter{WarehouseID: wh, Limit: 100})
	if err != nil || len(cands) != 1 || cands[0].TrackingNumber != "1ZA" || !cands[0].Ready {
		t.Fatalf("candidates = %+v, %v; want only 1ZA", cands, err)
	}
	m, err := s.CloseManifest(ctx, store.ManifestInput{WarehouseID: wh, LegalEntityID: le, Carrier: "ups", PickupDate: today()}, idem("close"))
	if err != nil || m.ParcelCount != 1 || m.Parcels[0].TrackingNumber != "1ZA" {
		t.Fatalf("close = %+v, %v", m, err)
	}
}

// Once the carrier has scanned a parcel on it, the manifest is the record of the handover and is not cancelled.
func TestManifest_CancelRefusedOnceTheCarrierCollected(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	wh, le := ids.New(), ids.New()
	a := shipIn(t, s, ctx, wh, le)
	label(t, s, ctx, a.ID, "ups", "1ZX")
	m, err := s.CloseManifest(ctx, store.ManifestInput{WarehouseID: wh, LegalEntityID: le, Carrier: "ups", PickupDate: today()}, idem("close"))
	if err != nil {
		t.Fatal(err)
	}
	scan(t, s, ctx, a.ID, "ups", "1ZX", "IN_TRANSIT", "carrier:1ZX:1", time.Now().UTC())
	if _, err := s.CancelManifest(ctx, m.ID, 1, idem("cancel")); problemCode(err) != store.CodeManifestCollected {
		t.Fatalf("cancel after pickup: %v, want %s", err, store.CodeManifestCollected)
	}
	if got, err := s.GetManifest(ctx, m.ID); err != nil || got.Status != store.ManifestClosed || !got.Parcels[0].Active {
		t.Fatalf("manifest after the refused cancel = %+v, %v", got, err)
	}
	if n := countEvents(t, admin, fulevents.ManifestCancelled); n != 0 {
		t.Fatalf("manifest.cancelled events = %d, want 0", n)
	}
}
