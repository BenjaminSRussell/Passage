package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func TestOperationRule_DefaultSaveVersionReplayAndEvent(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f := releasedDefault(t, s, ctx, 3)

	got, err := s.GetOperationRule(ctx, f.warehouse)
	if err != nil || got.Saved() || got.PostingDateSource != domain.PostingManual || got.TimeZone != "UTC" || !got.AutoPackEnabled ||
		got.WorkbenchGroupKeyDefault != domain.GroupCustomer || got.LegalEntityID == nil || *got.LegalEntityID != f.legalEntity {
		t.Fatalf("default = %+v, %v", got, err)
	}
	if list, err := s.ListOperationRules(ctx); err != nil || len(list) != 1 || list[0].WarehouseID != f.warehouse || list[0].Version != 0 {
		t.Fatalf("default list = %+v, %v", list, err)
	}
	in := store.OperationRuleInput{PostingDateSource: domain.PostingShipTimestamp, TimeZone: "Europe/Berlin", AutoPackEnabled: false, WorkbenchGroupKeyDefault: domain.GroupZone}
	if _, err := s.SaveOperationRule(ctx, f.warehouse, f.legalEntity, 1, in, idem("early")); problemCode(err) != store.CodeSettingVersion {
		t.Fatalf("save at a version that is not 0: %v", err)
	}
	key := idem("first")
	saved, err := s.SaveOperationRule(ctx, f.warehouse, f.legalEntity, 0, in, key)
	if err != nil || saved.Version != 1 || saved.PostingDateSource != domain.PostingShipTimestamp || saved.AutoPackEnabled || saved.UpdatedAt == nil {
		t.Fatalf("first save = %+v, %v", saved, err)
	}
	if replay, err := s.SaveOperationRule(ctx, f.warehouse, f.legalEntity, 0, in, key); err != nil || replay.Version != 1 {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if n := countEvents(t, admin, fulevents.WarehouseOperationRuleChanged); n != 1 {
		t.Fatalf("events after a save and its replay = %d, want 1", n)
	}
	if _, err := s.SaveOperationRule(ctx, f.warehouse, f.legalEntity, 0, in, idem("stale")); problemCode(err) != store.CodeSettingVersion {
		t.Fatalf("stale save: %v", err)
	}
	next, err := s.SaveOperationRule(ctx, f.warehouse, f.legalEntity, 1, store.OperationRuleInput{PostingDateSource: domain.PostingToday, TimeZone: "UTC",
		AutoPackEnabled: true, WorkbenchGroupKeyDefault: domain.GroupProduct}, idem("next"))
	if err != nil || next.Version != 2 {
		t.Fatalf("second save = %+v, %v", next, err)
	}
	for path, want := range map[string]string{"{previous_posting_date_source}": "SHIP_TIMESTAMP", "{previous_time_zone}": "Europe/Berlin",
		"{previous_auto_pack_enabled}": "false", "{workbench_group_key_default}": "PRODUCT", "{version}": "2"} {
		if got := outboxData(t, admin, fulevents.WarehouseOperationRuleChanged, path); got != want {
			t.Errorf("event %s = %q, want %s", path, got, want)
		}
	}
	// Another tenant sees the defaults for the same warehouse id.
	if got, err := s.GetOperationRule(ctxFor(2, ""), f.warehouse); err != nil || got.Saved() {
		t.Fatalf("other tenant get = %+v, %v", got, err)
	}
	// The release rule list does not lose the warehouse's legal entity when only an operation rule names it.
	bare, le := ids.New(), ids.New()
	if _, err := s.SaveOperationRule(ctx, bare, le, 0, in, idem("bare")); err != nil {
		t.Fatal(err)
	}
	if rr, err := s.GetReleaseRule(ctx, bare); err != nil || rr.LegalEntityID == nil || *rr.LegalEntityID != le {
		t.Fatalf("release rule of a warehouse known by its operation rule = %+v, %v", rr, err)
	}
}

func TestOperationRule_LegalEntityScopeAndChecks(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	f := releasedDefault(t, s, ctx, 3)
	other := ids.New()
	in := store.OperationRuleInput{PostingDateSource: domain.PostingToday, TimeZone: "UTC", AutoPackEnabled: true, WorkbenchGroupKeyDefault: domain.GroupCustomer}
	if _, err := s.SaveOperationRule(ctx, f.warehouse, other, 0, in, idem("wrong")); problemCode(err) != store.CodeWarehouseLegalEntity {
		t.Fatalf("save under another legal entity: %v", err)
	}
	if _, err := s.SaveOperationRule(ctx, f.warehouse, f.legalEntity, 0, in, idem("right")); err != nil {
		t.Fatal(err)
	}
	scoped := ctxFor(1, other)
	if list, err := s.ListOperationRules(scoped); err != nil || len(list) != 0 {
		t.Fatalf("other legal entity list = %+v, %v", list, err)
	}
	if got, err := s.GetOperationRule(scoped, f.warehouse); err != nil || got.Saved() || got.LegalEntityID != nil {
		t.Fatalf("other legal entity get = %+v, %v", got, err)
	}
	if _, err := s.SaveOperationRule(scoped, f.warehouse, other, 0, in, idem("sneak")); problemCode(err) != store.CodeWarehouseLegalEntity {
		t.Fatalf("other legal entity overwrite: %v", err)
	}
	bad := in
	bad.PostingDateSource = "SOMETIME"
	if _, err := s.SaveOperationRule(ctx, ids.New(), ids.New(), 0, bad, idem("source")); err == nil {
		t.Error("the database accepted an unknown posting date source")
	}
	bad = in
	bad.WorkbenchGroupKeyDefault = "COLOUR"
	if _, err := s.SaveOperationRule(ctx, ids.New(), ids.New(), 0, bad, idem("group")); err == nil {
		t.Error("the database accepted an unknown workbench group key")
	}
}

// packedFor picks and packs every line of the fixture's shipment into one container.
func packedFor(t *testing.T, s *store.Postgres, f fixture) store.Shipment {
	t.Helper()
	ctx := ctxFor(1, "")
	picked := pickAll(t, s, ctx, f.shipment)
	var packs []store.PackInput
	for _, l := range picked.Lines {
		packs = append(packs, store.PackInput{ShipmentLineID: l.ID, Qty: l.Planned})
	}
	res, err := s.PackContainer(ctx, picked.ID, picked.Version, "BOX", packs, idem("pack"))
	if err != nil {
		t.Fatalf("PackContainer: %v", err)
	}
	return res.Shipment
}

// BR-28.3: the warehouse's posting_date_source decides the posting date.
func TestOperationRule_PostingDateSource(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	// 23:30 UTC on 1 March 2027 is 2 March in Auckland.
	shippedAt := time.Date(2027, 3, 1, 23, 30, 0, 0, time.UTC)
	date := func(v string) time.Time { d, _ := time.Parse("2006-01-02", v); return d }
	post := func(sh store.Shipment, posted *time.Time) (store.PackingSlip, error) {
		in := store.PostInput{Currency: "EUR", CogsFromSetting: true, AsnFromSetting: true, Costs: costs(sh, "2"), ShippedAt: shippedAt}
		if posted != nil {
			in.PostingDate, in.HasPostingDate = *posted, true
		}
		return s.PostPackingSlip(ctx, sh.ID, sh.Version, in, idem("post"))
	}

	// No rule: MANUAL, a date is required and taken as given.
	f := releasedDefault(t, s, ctx, 2)
	sh := packedFor(t, s, f)
	if _, err := post(sh, nil); problemCode(err) != store.CodeValidation {
		t.Fatalf("MANUAL without a date: %v", err)
	}
	m := date("2027-02-20")
	if slip, err := post(sh, &m); err != nil || slip.PostingDate.Format("2006-01-02") != "2027-02-20" {
		t.Fatalf("MANUAL with a date = %+v, %v", slip, err)
	}

	// SHIP_TIMESTAMP in the warehouse time zone: derived, and a different date is refused.
	f = releasedDefault(t, s, ctx, 2)
	if _, err := s.SaveOperationRule(ctx, f.warehouse, f.legalEntity, 0, store.OperationRuleInput{PostingDateSource: domain.PostingShipTimestamp,
		TimeZone: "Pacific/Auckland", AutoPackEnabled: true, WorkbenchGroupKeyDefault: domain.GroupCustomer}, idem("rule")); err != nil {
		t.Fatal(err)
	}
	sh = packedFor(t, s, f)
	wrong := date("2027-03-01")
	if _, err := post(sh, &wrong); problemCode(err) != store.CodePostingDate {
		t.Fatalf("a date that contradicts SHIP_TIMESTAMP: %v", err)
	}
	slip, err := post(sh, nil)
	if err != nil || slip.PostingDate.Format("2006-01-02") != "2027-03-02" {
		t.Fatalf("SHIP_TIMESTAMP slip = %+v, %v", slip, err)
	}

	// TODAY is the day of the post in the warehouse time zone too, but only a
	// default: a named date overrides it.
	f = releasedDefault(t, s, ctx, 2)
	if _, err := s.SaveOperationRule(ctx, f.warehouse, f.legalEntity, 0, store.OperationRuleInput{PostingDateSource: domain.PostingToday,
		TimeZone: "Pacific/Auckland", AutoPackEnabled: true, WorkbenchGroupKeyDefault: domain.GroupCustomer}, idem("rule2")); err != nil {
		t.Fatal(err)
	}
	sh = packedFor(t, s, f)
	if slip, err := post(sh, nil); err != nil || slip.PostingDate.Format("2006-01-02") != "2027-03-02" {
		t.Fatalf("TODAY slip without a date = %+v, %v", slip, err)
	}
	g := releasedIn(t, s, ctx, f.warehouse, f.legalEntity, 2)
	sh = packedFor(t, s, g)
	override := date("2027-02-27")
	if slip, err := s.PostPackingSlip(ctx, sh.ID, sh.Version, store.PostInput{Currency: "EUR", CogsFromSetting: true, AsnFromSetting: true,
		Costs: costs(sh, "2"), ShippedAt: shippedAt, PostingDate: override, HasPostingDate: true}, idem("post-override")); err != nil || slip.PostingDate.Format("2006-01-02") != "2027-02-27" {
		t.Fatalf("TODAY slip with a named date = %+v, %v", slip, err)
	}
}

// A rule saved for the warehouse under another legal entity does not decide
// the post of a shipment of this one, whatever the caller's scope.
func TestOperationRule_RuleOfAnotherLegalEntityDoesNotApply(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	warehouse, otherLE := ids.New(), ids.New()
	if _, err := s.SaveOperationRule(ctx, warehouse, otherLE, 0, store.OperationRuleInput{PostingDateSource: domain.PostingShipTimestamp,
		TimeZone: "UTC", AutoPackEnabled: false, WorkbenchGroupKeyDefault: domain.GroupCustomer}, idem("other-rule")); err != nil {
		t.Fatal(err)
	}
	f := releasedIn(t, s, ctx, warehouse, ids.New(), 2)
	sh := packedFor(t, s, f)
	named := time.Date(2027, 2, 20, 0, 0, 0, 0, time.UTC)
	slip, err := s.PostPackingSlip(ctx, sh.ID, sh.Version, store.PostInput{Currency: "EUR", CogsFromSetting: true, AsnFromSetting: true,
		Costs: costs(sh, "2"), ShippedAt: time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC), PostingDate: named, HasPostingDate: true}, idem("post"))
	if err != nil || !slip.PostingDate.Equal(named) {
		t.Fatalf("post under the defaults (MANUAL) = %+v, %v", slip, err)
	}
}

// auto_pack_enabled false refuses :auto-pack for that warehouse only.
func TestOperationRule_AutoPackSwitch(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	saveType(t, s, ctx, store.ContainerType{Code: "PALLET", TareKg: decimal.NewFromInt(25)})
	f := releasedDefault(t, s, ctx, 3)
	saveRule(t, s, ctx, store.PackingRule{ProductID: f.shipment.Lines[0].ProductID, ContainerType: "PALLET", QtyPerContainer: decimal.NewFromInt(3), MaxWeightKg: decimal.NewFromInt(1000)})
	picked := pickAll(t, s, ctx, f.shipment)
	off := store.OperationRuleInput{PostingDateSource: domain.PostingManual, TimeZone: "UTC", AutoPackEnabled: false, WorkbenchGroupKeyDefault: domain.GroupCustomer}
	if _, err := s.SaveOperationRule(ctx, f.warehouse, f.legalEntity, 0, off, idem("off")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AutoPack(ctx, picked.ID, picked.Version, nil, idem("ap1")); problemCode(err) != store.CodeAutoPackDisabled {
		t.Fatalf("auto-pack in a warehouse that turned it off: %v", err)
	}
	// Manual packing stays available.
	line := picked.Lines[0]
	if _, err := s.PackContainer(ctx, picked.ID, picked.Version, "PALLET", []store.PackInput{{ShipmentLineID: line.ID, Qty: line.Planned}}, idem("manual")); err != nil {
		t.Fatalf("manual pack with auto-pack off: %v", err)
	}
	on := off
	on.AutoPackEnabled = true
	if _, err := s.SaveOperationRule(ctx, f.warehouse, f.legalEntity, 1, on, idem("on")); err != nil {
		t.Fatal(err)
	}
	g := releasedIn(t, s, ctx, f.warehouse, f.legalEntity, 3)
	saveRule(t, s, ctx, store.PackingRule{ProductID: g.shipment.Lines[0].ProductID, ContainerType: "PALLET", QtyPerContainer: decimal.NewFromInt(3), MaxWeightKg: decimal.NewFromInt(1000)})
	pg := pickAll(t, s, ctx, g.shipment)
	if _, err := s.AutoPack(ctx, pg.ID, pg.Version, nil, idem("ap2")); err != nil {
		t.Fatalf("auto-pack after turning it on: %v", err)
	}
}

// Two first saves of a warehouse at once: the warehouse lock serialises them,
// one creates the rule and the other is refused as stale (not a key violation).
func TestOperationRule_ConcurrentFirstSaves(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	wh, le := ids.New(), ids.New()
	bg := context.Background()
	hold, err := admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(bg) }()
	if _, err := hold.Exec(bg, `select pg_advisory_xact_lock(hashtextextended($1 || '/operation-rule/' || $2, 0))`, testkit.TenantID(1), wh); err != nil {
		t.Fatal(err)
	}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.SaveOperationRule(ctx, wh, le, 0, store.OperationRuleInput{PostingDateSource: domain.PostingToday, TimeZone: "UTC",
				AutoPackEnabled: i == 0, WorkbenchGroupKeyDefault: domain.GroupCustomer}, idem(fmt.Sprintf("first-%d", i)))
		}(i)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var waiting int
		if err := admin.QueryRow(bg, `select count(*) from pg_stat_activity where wait_event_type = 'Lock' and wait_event = 'advisory' and query like '%/operation-rule/%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d saves wait on the warehouse lock, want 2", waiting)
		}
	}
	if err := hold.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	ok, stale := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case problemCode(err) == store.CodeSettingVersion:
			stale++
		default:
			t.Fatalf("concurrent first save: %v", err)
		}
	}
	var rows, version int
	if err := admin.QueryRow(bg, `select count(*), max(version) from warehouse_operation_rule where warehouse_id = $1`, wh).Scan(&rows, &version); err != nil {
		t.Fatal(err)
	}
	if n := countEvents(t, admin, fulevents.WarehouseOperationRuleChanged); ok != 1 || stale != 1 || rows != 1 || version != 1 || n != 1 {
		t.Fatalf("ok %d stale %d rows %d version %d events %d, want 1 1 1 1 1", ok, stale, rows, version, n)
	}
}
