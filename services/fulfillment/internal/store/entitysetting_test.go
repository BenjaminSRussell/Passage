package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func TestLegalEntitySetting_DefaultSaveVersionReplayAndEvent(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f := released(t, s, ctx, 3)

	// Nothing saved: both false at version 0, and the entity is listed from its shipment.
	got, err := s.GetLegalEntitySetting(ctx, f.legalEntity)
	if err != nil || got.Version != 0 || got.CogsAtShipment || got.AsnEnabled {
		t.Fatalf("default = %+v, %v", got, err)
	}
	if list, err := s.ListLegalEntitySettings(ctx); err != nil || len(list) != 1 || list[0].LegalEntityID != f.legalEntity || list[0].Version != 0 {
		t.Fatalf("default list = %+v, %v", list, err)
	}
	if _, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 1, store.LegalEntitySettingInput{CogsAtShipment: true, CanChangeCogs: true}, idem("early")); problemCode(err) != store.CodeVersion {
		t.Fatalf("save at a version that is not 0: %v", err)
	}
	key := idem("first")
	saved, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 0, store.LegalEntitySettingInput{CogsAtShipment: true, CanChangeCogs: true}, key)
	if err != nil || saved.Version != 1 || !saved.CogsAtShipment || saved.AsnEnabled || saved.UpdatedAt == nil {
		t.Fatalf("first save = %+v, %v", saved, err)
	}
	if replay, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 0, store.LegalEntitySettingInput{CogsAtShipment: true, CanChangeCogs: true}, key); err != nil || replay.Version != 1 {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if n := countEvents(t, admin, fulevents.LegalEntitySettingChanged); n != 1 {
		t.Fatalf("events after a save and its replay = %d, want 1", n)
	}
	if _, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 0, store.LegalEntitySettingInput{AsnEnabled: true}, idem("stale")); problemCode(err) != store.CodeVersion {
		t.Fatalf("stale save: %v", err)
	}
	next, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 1, store.LegalEntitySettingInput{CogsAtShipment: false, AsnEnabled: true, CanChangeCogs: true}, idem("next"))
	if err != nil || next.Version != 2 || next.CogsAtShipment || !next.AsnEnabled {
		t.Fatalf("second save = %+v, %v", next, err)
	}
	if got := outboxData(t, admin, fulevents.LegalEntitySettingChanged, "{previous_cogs_at_shipment}"); got != "true" {
		t.Fatalf("previous cogs in event = %q, want true", got)
	}
	if got := outboxData(t, admin, fulevents.LegalEntitySettingChanged, "{asn_enabled}"); got != "true" {
		t.Fatalf("asn in event = %q, want true", got)
	}

	// Another tenant sees the default and saves its own row for the same id.
	if got, err := s.GetLegalEntitySetting(ctxFor(2, ""), f.legalEntity); err != nil || got.Version != 0 || got.AsnEnabled {
		t.Fatalf("other tenant get = %+v, %v", got, err)
	}
	if got, err := s.SaveLegalEntitySetting(ctxFor(2, ""), f.legalEntity, 0, store.LegalEntitySettingInput{CogsAtShipment: true, CanChangeCogs: true}, idem("t2")); err != nil || got.Version != 1 {
		t.Fatalf("other tenant save = %+v, %v", got, err)
	}
	if got, _ := s.GetLegalEntitySetting(ctx, f.legalEntity); !got.AsnEnabled || got.CogsAtShipment {
		t.Fatalf("tenant 1 setting changed by tenant 2: %+v", got)
	}
}

// setCogsPolicy saves cogs_at_shipment for a legal entity (with the ledger
// setup duty) unless it already has that value, keeping asn_enabled.
func setCogsPolicy(t *testing.T, s *store.Postgres, ctx context.Context, legalEntity string, on bool) {
	t.Helper()
	cur, err := s.GetLegalEntitySetting(ctx, legalEntity)
	if err != nil {
		t.Fatal(err)
	}
	if cur.CogsAtShipment == on {
		return
	}
	if _, err := s.SaveLegalEntitySetting(ctx, legalEntity, cur.Version,
		store.LegalEntitySettingInput{CogsAtShipment: on, AsnEnabled: cur.AsnEnabled, CanChangeCogs: true}, idem("cogs-policy")); err != nil {
		t.Fatal(err)
	}
}

// Changing cogs_at_shipment is an accounting policy change: fulfillment.warehouse.release
// alone may change asn_enabled but not cogs_at_shipment.
func TestLegalEntitySetting_CogsChangeNeedsTheLedgerSetupDuty(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f := released(t, s, ctx, 3)
	if _, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 0, store.LegalEntitySettingInput{CogsAtShipment: true}, idem("no-duty")); problemCode(err) != store.CodeDutyMissing {
		t.Fatalf("cogs change without the duty: %v, want %s", err, store.CodeDutyMissing)
	}
	if n := countEvents(t, admin, fulevents.LegalEntitySettingChanged); n != 0 {
		t.Fatalf("events after a refused save = %d", n)
	}
	asn, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 0, store.LegalEntitySettingInput{AsnEnabled: true}, idem("asn-only"))
	if err != nil || asn.Version != 1 || !asn.AsnEnabled || asn.CogsAtShipment {
		t.Fatalf("asn change without the duty = %+v, %v", asn, err)
	}
	cogs, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 1, store.LegalEntitySettingInput{CogsAtShipment: true, AsnEnabled: true, CanChangeCogs: true}, idem("finance"))
	if err != nil || cogs.Version != 2 || !cogs.CogsAtShipment {
		t.Fatalf("cogs change with the duty = %+v, %v", cogs, err)
	}
	// Keeping cogs_at_shipment as it is needs no duty.
	if got, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 2, store.LegalEntitySettingInput{CogsAtShipment: true}, idem("keep")); err != nil || got.AsnEnabled || !got.CogsAtShipment {
		t.Fatalf("asn off keeping cogs = %+v, %v", got, err)
	}
}

func TestLegalEntitySetting_Scope(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	f := released(t, s, ctx, 3)
	other := ids.New()
	if _, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 0, store.LegalEntitySettingInput{AsnEnabled: true}, idem("ok")); err != nil {
		t.Fatal(err)
	}

	// A principal scoped to another legal entity sees neither the entity nor its
	// setting, and cannot save one for it.
	scoped := ctxFor(1, other)
	if list, err := s.ListLegalEntitySettings(scoped); err != nil || len(list) != 0 {
		t.Fatalf("other legal entity list = %+v, %v", list, err)
	}
	if got, err := s.GetLegalEntitySetting(scoped, f.legalEntity); err != nil || got.Version != 0 || got.AsnEnabled {
		t.Fatalf("other legal entity get = %+v, %v", got, err)
	}
	if _, err := s.SaveLegalEntitySetting(scoped, f.legalEntity, 0, store.LegalEntitySettingInput{CogsAtShipment: true, CanChangeCogs: true}, idem("sneak")); problemCode(err) != store.CodeLegalEntityScope {
		t.Fatalf("save outside the scope: %v, want %s", err, store.CodeLegalEntityScope)
	}
	own := ctxFor(1, f.legalEntity)
	if got, err := s.GetLegalEntitySetting(own, f.legalEntity); err != nil || !got.AsnEnabled || got.Version != 1 {
		t.Fatalf("own legal entity get = %+v, %v", got, err)
	}
	if list, _ := s.ListLegalEntitySettings(own); len(list) != 1 {
		t.Fatalf("own list = %+v", list)
	}
}

// A post that names neither cogsAtShipment nor asnEnabled takes the settings of
// the shipment's legal entity; a named value wins over the setting.
func TestPostPackingSlip_TakesTheLegalEntitySettings(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")

	// No saved setting: Dr 1405, no ASN, as before this table existed.
	plain := released(t, s, ctx, 3)
	packed := pickAndPack(t, s, ctx, plain.shipment)
	slip, err := s.PostPackingSlip(ctx, plain.shipment.ID, packed.Shipment.Version,
		store.PostInput{PostingDate: today(), Currency: "USD", CogsFromSetting: true, AsnFromSetting: true, Costs: costs(plain.shipment, "2")}, idem("plain"))
	if err != nil || slip.Journal[0].Account != "1405" {
		t.Fatalf("default post = %+v, %v", slip.Journal, err)
	}
	if n := countEvents(t, admin, fulevents.ASNIssued); n != 0 {
		t.Fatalf("asn events without a setting = %d", n)
	}

	set := released(t, s, ctx, 3)
	if _, err := s.SaveLegalEntitySetting(ctx, set.legalEntity, 0, store.LegalEntitySettingInput{CogsAtShipment: true, AsnEnabled: true, CanChangeCogs: true}, idem("set")); err != nil {
		t.Fatal(err)
	}
	packed = pickAndPack(t, s, ctx, set.shipment)
	slip, err = s.PostPackingSlip(ctx, set.shipment.ID, packed.Shipment.Version,
		store.PostInput{PostingDate: today(), Currency: "USD", CogsFromSetting: true, AsnFromSetting: true, Costs: costs(set.shipment, "2")}, idem("set-post"))
	if err != nil || slip.Journal[0].Account != "5000" {
		t.Fatalf("post with COGS at shipment set = %+v, %v", slip.Journal, err)
	}
	if n := countEvents(t, admin, fulevents.ASNIssued); n != 1 {
		t.Fatalf("asn events with the setting = %d, want 1", n)
	}

	// A named asnEnabled wins over the setting; a named cogsAtShipment must
	// match the policy (FUL-0059) and posts when it does.
	named := released(t, s, ctx, 3)
	if _, err := s.SaveLegalEntitySetting(ctx, named.legalEntity, 0, store.LegalEntitySettingInput{CogsAtShipment: true, AsnEnabled: true, CanChangeCogs: true}, idem("named")); err != nil {
		t.Fatal(err)
	}
	packed = pickAndPack(t, s, ctx, named.shipment)
	if _, err := s.PostPackingSlip(ctx, named.shipment.ID, packed.Shipment.Version,
		store.PostInput{PostingDate: today(), Currency: "USD", AsnFromSetting: true, Costs: costs(named.shipment, "2")}, idem("contradicts")); problemCode(err) != store.CodeCogsPolicy {
		t.Fatalf("post naming cogsAtShipment=false against the policy: %v, want %s", err, store.CodeCogsPolicy)
	}
	if n := countEvents(t, admin, fulevents.PackingSlipPosted); n != 2 {
		t.Fatalf("slips after a refused post = %d, want 2", n)
	}
	slip, err = s.PostPackingSlip(ctx, named.shipment.ID, packed.Shipment.Version,
		store.PostInput{PostingDate: today(), Currency: "USD", CogsAtShipment: true, Costs: costs(named.shipment, "2")}, idem("named-post"))
	if err != nil || slip.Journal[0].Account != "5000" {
		t.Fatalf("post naming the policy value = %+v, %v", slip.Journal, err)
	}
	if n := countEvents(t, admin, fulevents.ASNIssued); n != 1 {
		t.Fatalf("asn events after a post naming asnEnabled=false = %d, want still 1", n)
	}

	// A post without a saved setting that names cogsAtShipment=true is refused
	// too: the default policy is physical posting (Dr 1405).
	unset := released(t, s, ctx, 3)
	packed = pickAndPack(t, s, ctx, unset.shipment)
	if _, err := s.PostPackingSlip(ctx, unset.shipment.ID, packed.Shipment.Version,
		store.PostInput{PostingDate: today(), Currency: "USD", CogsAtShipment: true, Costs: costs(unset.shipment, "2")}, idem("unset")); problemCode(err) != store.CodeCogsPolicy {
		t.Fatalf("post naming cogsAtShipment=true without a policy: %v", err)
	}
}
