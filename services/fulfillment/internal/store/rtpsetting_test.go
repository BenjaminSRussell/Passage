package store_test

import (
	"context"
	"testing"

	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// returnableSlip packs the shipment's lines into returnable pallets (one per
// unit) and posts the slip with a posting date days ago.
func returnableSlip(t *testing.T, s *store.Postgres, ctx context.Context, f fixture, daysAgo int) store.PackingSlip {
	t.Helper()
	line := f.shipment.Lines[0]
	saveRule(t, s, ctx, store.PackingRule{ProductID: line.ProductID, ContainerType: "PALLET", QtyPerContainer: d("1"), MaxWeightKg: d("1000")})
	picked := pickAll(t, s, ctx, f.shipment)
	res, err := s.AutoPack(ctx, picked.ID, picked.Version, nil, idem("ap"))
	if err != nil {
		t.Fatalf("AutoPack: %v", err)
	}
	slip, err := s.PostPackingSlip(ctx, picked.ID, res.Shipment.Version, store.PostInput{PostingDate: today().AddDate(0, 0, -daysAgo), Currency: "EUR",
		CogsFromSetting: true, Costs: costs(f.shipment, "1")}, idem("post"))
	if err != nil {
		t.Fatalf("PostPackingSlip: %v", err)
	}
	return slip
}

// FR-28.16, BR-28.13: rtp_tracking_enabled and rtp_charge_days are per legal
// entity. An entity that does not track raises no balance and its slip remembers
// that, so cancelling it later lowers nothing even after the setting is turned
// on; an entity that tracks ages its balance by its own rtp_charge_days.
func TestLegalEntitySetting_ReturnableTrackingAndChargeDays(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	saveType(t, s, ctx, store.ContainerType{Code: "PALLET", TareKg: d("25"), Returnable: true, PackagingMaterial: strp("WOOD")})

	// No saved setting: tracking on, 90 days, version 0 (what every slip did before).
	off := released(t, s, ctx, 3)
	def, err := s.GetLegalEntitySetting(ctx, off.legalEntity)
	if err != nil || !def.RTPTrackingEnabled || def.RTPChargeDays != 90 || def.Version != 0 {
		t.Fatalf("default = %+v, %v", def, err)
	}

	// Tracking off: the post raises nothing and says so in the event.
	saved, err := s.SaveLegalEntitySetting(ctx, off.legalEntity, 0, store.LegalEntitySettingInput{
		RTPTrackingEnabled: store.Change[bool]{Set: true}, RTPChargeDays: store.Change[int]{Set: true, Value: 45}}, idem("rtp-off"))
	if err != nil || saved.RTPTrackingEnabled || saved.RTPChargeDays != 45 {
		t.Fatalf("save = %+v, %v", saved, err)
	}
	if got := outboxData(t, admin, fulevents.LegalEntitySettingChanged, "{rtp_charge_days}"); got != "45" {
		t.Fatalf("rtp_charge_days in event = %q", got)
	}
	if got := outboxData(t, admin, fulevents.LegalEntitySettingChanged, "{previous_rtp_tracking_enabled}"); got != "true" {
		t.Fatalf("previous_rtp_tracking_enabled in event = %q", got)
	}
	slip := returnableSlip(t, s, ctx, off, 10)
	if slip.RTPTracked {
		t.Fatalf("slip of an entity that does not track = %+v", slip)
	}
	if items, err := s.ListReturnableBalances(ctx, off.shipment.CustomerID, 10); err != nil || len(items) != 0 {
		t.Fatalf("balances = %+v, %v; want none", items, err)
	}
	if got := outboxData(t, admin, fulevents.PackingSlipPosted, "{returnables}"); got != "[]" {
		t.Fatalf("posted event returnables = %q, want []", got)
	}
	// Turned on afterwards, the cancel still mirrors the post: nothing to lower, no FUL-0015.
	if _, err := s.SaveLegalEntitySetting(ctx, off.legalEntity, saved.Version, store.LegalEntitySettingInput{
		RTPTrackingEnabled: store.Change[bool]{Set: true, Value: true}}, idem("rtp-on")); err != nil {
		t.Fatal(err)
	}
	if rev, err := s.CancelPackingSlip(ctx, slip.ID, idem("cancel-off")); err != nil || rev.RTPTracked {
		t.Fatalf("cancel = %+v, %v", rev, err)
	}

	// Tracking on with 30 days: the balance is raised and ages by the entity's days.
	fast := released(t, s, ctx, 2)
	if _, err := s.SaveLegalEntitySetting(ctx, fast.legalEntity, 0, store.LegalEntitySettingInput{
		RTPTrackingEnabled: store.Change[bool]{Set: true, Value: true}, RTPChargeDays: store.Change[int]{Set: true, Value: 30}}, idem("fast")); err != nil {
		t.Fatal(err)
	}
	fastSlip := returnableSlip(t, s, ctx, fast, 40)
	if !fastSlip.RTPTracked {
		t.Fatalf("slip of an entity that tracks = %+v", fastSlip)
	}
	// An entity with no saved setting ages by 90 days.
	plain := released(t, s, ctx, 4)
	returnableSlip(t, s, ctx, plain, 40)

	rows, err := s.ReturnableAging(ctx, "")
	if err != nil || len(rows) != 2 {
		t.Fatalf("aging = %+v, %v; want the two tracked customers", rows, err)
	}
	for _, r := range rows {
		if len(r.Shipped) != 1 {
			t.Fatalf("aging row %+v", r)
		}
		want := 90
		if r.CustomerID == fast.shipment.CustomerID {
			want = 30
		}
		if r.Shipped[0].ChargeDays != want {
			t.Fatalf("customer %s charge days = %d, want %d", r.CustomerID, r.Shipped[0].ChargeDays, want)
		}
	}
	// The untracked slip never appears in the aging of its customer.
	if rows, err := s.ReturnableAging(ctx, off.shipment.CustomerID); err != nil || len(rows) != 0 {
		t.Fatalf("aging of the untracked customer = %+v, %v", rows, err)
	}

	// A caller scoped to another legal entity sees the customer's balance (it is
	// per customer and container type) but none of the fast entity's slips or
	// its charge days, so nothing of that entity ages or is due in its view.
	scoped, err := s.ReturnableAging(ctxFor(1, plain.legalEntity), fast.shipment.CustomerID)
	if err != nil || len(scoped) != 1 || len(scoped[0].Shipped) != 0 {
		t.Fatalf("aging seen from another legal entity = %+v, %v", scoped, err)
	}

	// Turning tracking off leaves the balances already raised as they are: the
	// tracked slip still ages it by the entity's days, and cancelling that slip
	// still lowers it, because the slip remembers it tracked.
	before, _ := s.GetLegalEntitySetting(ctx, fast.legalEntity)
	if _, err := s.SaveLegalEntitySetting(ctx, fast.legalEntity, before.Version, store.LegalEntitySettingInput{
		RTPTrackingEnabled: store.Change[bool]{Set: true, Value: false}}, idem("fast-off")); err != nil {
		t.Fatal(err)
	}
	rows, err = s.ReturnableAging(ctx, fast.shipment.CustomerID)
	if err != nil || len(rows) != 1 || len(rows[0].Shipped) != 1 || rows[0].Shipped[0].ChargeDays != 30 || !rows[0].Balance.Equal(d("2")) {
		t.Fatalf("aging after tracking off = %+v, %v; want the tracked slip still aging", rows, err)
	}
	if rev, err := s.CancelPackingSlip(ctx, fastSlip.ID, idem("cancel-tracked")); err != nil || !rev.RTPTracked {
		t.Fatalf("cancel of a tracked slip after tracking off = %+v, %v", rev, err)
	}
	if items, err := s.ListReturnableBalances(ctx, fast.shipment.CustomerID, 10); err != nil || len(items) != 1 || !items[0].Balance.IsZero() {
		t.Fatalf("balance after cancelling the tracked slip = %+v, %v; want 0", items, err)
	}

	// Another tenant sees the defaults, and an unsaved field of a later save is kept.
	if got, err := s.GetLegalEntitySetting(ctxFor(2, ""), fast.legalEntity); err != nil || got.RTPChargeDays != 90 || got.Version != 0 {
		t.Fatalf("tenant 2 sees %+v, %v", got, err)
	}
	cur, _ := s.GetLegalEntitySetting(ctx, fast.legalEntity)
	kept, err := s.SaveLegalEntitySetting(ctx, fast.legalEntity, cur.Version, store.LegalEntitySettingInput{AsnEnabled: true}, idem("old-client"))
	if err != nil || kept.RTPChargeDays != 30 || kept.RTPTrackingEnabled { // both as saved (30 days, off)
		t.Fatalf("save without the rtp fields = %+v, %v", kept, err)
	}
}
