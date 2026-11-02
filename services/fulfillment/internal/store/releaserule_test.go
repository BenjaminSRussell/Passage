package store_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func TestReleaseRule_DefaultSaveVersionReplayAndEvent(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f := releasedDefault(t, s, ctx, 3)

	// Nothing saved: the section 5.28.13 defaults at version 0, listed from the shipment.
	got, err := s.GetReleaseRule(ctx, f.warehouse)
	if err != nil || got.Saved() || got.HorizonDays != 2 || !got.RequireFullATP || got.WaveMaxLines != 200 || got.WaveMaxOrders != 50 ||
		got.LegalEntityID == nil || *got.LegalEntityID != f.legalEntity {
		t.Fatalf("default = %+v, %v", got, err)
	}
	if list, err := s.ListReleaseRules(ctx); err != nil || len(list) != 1 || list[0].WarehouseID != f.warehouse || list[0].Version != 0 || list[0].WaveMaxOrders != 50 {
		t.Fatalf("default list = %+v, %v", list, err)
	}
	in := store.ReleaseRuleInput{HorizonDays: 7, RequireFullATP: false, WaveMaxLines: 120, WaveMaxOrders: 30}
	if _, err := s.SaveReleaseRule(ctx, f.warehouse, f.legalEntity, 1, in, idem("early")); problemCode(err) != store.CodeSettingVersion {
		t.Fatalf("save at a version that is not 0: %v", err)
	}
	key := idem("first")
	saved, err := s.SaveReleaseRule(ctx, f.warehouse, f.legalEntity, 0, in, key)
	if err != nil || saved.Version != 1 || saved.HorizonDays != 7 || saved.RequireFullATP || saved.UpdatedAt == nil {
		t.Fatalf("first save = %+v, %v", saved, err)
	}
	if replay, err := s.SaveReleaseRule(ctx, f.warehouse, f.legalEntity, 0, in, key); err != nil || replay.Version != 1 {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if n := countEvents(t, admin, fulevents.WarehouseReleaseRuleChanged); n != 1 {
		t.Fatalf("events after a save and its replay = %d, want 1", n)
	}
	if _, err := s.SaveReleaseRule(ctx, f.warehouse, f.legalEntity, 0, in, idem("stale")); problemCode(err) != store.CodeSettingVersion {
		t.Fatalf("stale save: %v", err)
	}
	if got, err := s.GetReleaseRule(ctx, f.warehouse); err != nil || !got.Saved() || got.HorizonDays != 7 || got.WaveMaxLines != 120 {
		t.Fatalf("read back = %+v, %v", got, err)
	}
	next, err := s.SaveReleaseRule(ctx, f.warehouse, f.legalEntity, 1, store.ReleaseRuleInput{HorizonDays: 1, RequireFullATP: true, WaveMaxLines: 10, WaveMaxOrders: 2}, idem("next"))
	if err != nil || next.Version != 2 || next.WaveMaxOrders != 2 {
		t.Fatalf("second save = %+v, %v", next, err)
	}
	if got := outboxData(t, admin, fulevents.WarehouseReleaseRuleChanged, "{previous_release_horizon_days}"); got != "7" {
		t.Fatalf("previous horizon in event = %q, want 7", got)
	}
	if got := outboxData(t, admin, fulevents.WarehouseReleaseRuleChanged, "{wave_max_orders}"); got != "2" {
		t.Fatalf("wave_max_orders in event = %q, want 2", got)
	}
	if list, err := s.ListReleaseRules(ctx); err != nil || len(list) != 1 || list[0].Version != 2 || list[0].HorizonDays != 1 {
		t.Fatalf("list after saves = %+v, %v", list, err)
	}

	// Another tenant sees the defaults and keeps its own row for the same warehouse id.
	if got, err := s.GetReleaseRule(ctxFor(2, ""), f.warehouse); err != nil || got.Saved() || got.HorizonDays != 2 {
		t.Fatalf("other tenant get = %+v, %v", got, err)
	}
	if got, err := s.SaveReleaseRule(ctxFor(2, ""), f.warehouse, f.legalEntity, 0, in, idem("t2")); err != nil || got.Version != 1 {
		t.Fatalf("other tenant save = %+v, %v", got, err)
	}
	if got, _ := s.GetReleaseRule(ctx, f.warehouse); got.Version != 2 || got.HorizonDays != 1 {
		t.Fatalf("tenant 1 rule changed by tenant 2: %+v", got)
	}
}

// A warehouse belongs to one legal entity: its rule carries it, is scoped by it
// and cannot be saved under another one.
func TestReleaseRule_LegalEntityScope(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	f := releasedDefault(t, s, ctx, 10)
	other := ids.New()
	in := store.ReleaseRuleInput{HorizonDays: 3, RequireFullATP: true, WaveMaxLines: 100, WaveMaxOrders: 20}

	if _, err := s.SaveReleaseRule(ctx, f.warehouse, other, 0, in, idem("wrong")); problemCode(err) != store.CodeWarehouseLegalEntity {
		t.Fatalf("save under a legal entity the warehouse's shipments do not name: %v, want %s", err, store.CodeWarehouseLegalEntity)
	}
	if _, err := s.SaveReleaseRule(ctx, f.warehouse, f.legalEntity, 0, in, idem("right")); err != nil {
		t.Fatal(err)
	}
	scoped := ctxFor(1, other)
	if list, err := s.ListReleaseRules(scoped); err != nil || len(list) != 0 {
		t.Fatalf("other legal entity list = %+v, %v", list, err)
	}
	if got, err := s.GetReleaseRule(scoped, f.warehouse); err != nil || got.Saved() || got.LegalEntityID != nil {
		t.Fatalf("other legal entity get = %+v, %v", got, err)
	}
	if _, err := s.SaveReleaseRule(scoped, f.warehouse, other, 0, in, idem("sneak")); problemCode(err) != store.CodeWarehouseLegalEntity {
		t.Fatalf("other legal entity overwrite: %v, want %s", err, store.CodeWarehouseLegalEntity)
	}
	if got, err := s.GetReleaseRule(ctxFor(1, f.legalEntity), f.warehouse); err != nil || got.Version != 1 || got.HorizonDays != 3 {
		t.Fatalf("own legal entity get = %+v, %v", got, err)
	}

	// A warehouse without shipments keeps the legal entity of its first save.
	bare, le := ids.New(), ids.New()
	if _, err := s.SaveReleaseRule(ctx, bare, le, 0, in, idem("bare")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveReleaseRule(ctx, bare, other, 1, in, idem("move")); problemCode(err) != store.CodeWarehouseLegalEntity {
		t.Fatalf("moving a warehouse to another legal entity: %v", err)
	}
}

// The database refuses values outside the section 5.28.13 ranges.
func TestReleaseRule_DatabaseChecksRanges(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	for name, in := range map[string]store.ReleaseRuleInput{
		"horizon":    {HorizonDays: 31, RequireFullATP: true, WaveMaxLines: 1, WaveMaxOrders: 1},
		"wave lines": {HorizonDays: 1, RequireFullATP: true, WaveMaxLines: 0, WaveMaxOrders: 1},
		"orders":     {HorizonDays: 1, RequireFullATP: true, WaveMaxLines: 1, WaveMaxOrders: 501},
	} {
		if _, err := s.SaveReleaseRule(ctx, ids.New(), ids.New(), 0, in, idem(name)); err == nil {
			t.Errorf("%s: out-of-range value saved", name)
		}
	}
}

// Two first saves of the same warehouse at the same moment (both read version
// 0): the per-warehouse lock serialises them, so one row is written and the
// other caller is stale (412), with one event. The admin holds the lock until
// both wait on it, so the two really overlap.
func TestReleaseRule_ConcurrentFirstSaves(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	wh, le := ids.New(), ids.New()
	bg := context.Background()
	hold, err := admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(bg) }()
	if _, err := hold.Exec(bg, `select pg_advisory_xact_lock(hashtextextended($1 || '/release-rule/' || $2, 0))`, testkit.TenantID(1), wh); err != nil {
		t.Fatal(err)
	}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.SaveReleaseRule(ctx, wh, le, 0, store.ReleaseRuleInput{HorizonDays: i, RequireFullATP: true, WaveMaxLines: 10, WaveMaxOrders: 5}, idem("first"))
		}(i)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var waiting int
		if err := admin.QueryRow(bg, `select count(*) from pg_stat_activity where wait_event_type = 'Lock' and wait_event = 'advisory' and query like '%/release-rule/%'`).Scan(&waiting); err != nil {
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
	if err := admin.QueryRow(bg, `select count(*), max(version) from warehouse_release_rule where warehouse_id = $1`, wh).Scan(&rows, &version); err != nil {
		t.Fatal(err)
	}
	if ok != 1 || stale != 1 || rows != 1 || version != 1 || countEvents(t, admin, fulevents.WarehouseReleaseRuleChanged) != 1 {
		t.Fatalf("ok %d stale %d rows %d version %d events %d, want 1 1 1 1 1", ok, stale, rows, version, countEvents(t, admin, fulevents.WarehouseReleaseRuleChanged))
	}
}

// A warehouse belongs to one legal entity across its settings: a release rule
// cannot be saved under another legal entity than the warehouse's short pick
// setting, nor the other way round.
func TestReleaseRule_AgreesWithTheWarehouseSetting(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	in := store.ReleaseRuleInput{HorizonDays: 3, RequireFullATP: true, WaveMaxLines: 100, WaveMaxOrders: 20}

	bySetting, le, other := ids.New(), ids.New(), ids.New()
	if _, err := s.SaveWarehouseSetting(ctx, bySetting, le, 0, domain.ActionCancelRemainder, idem("ws")); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetReleaseRule(ctx, bySetting); err != nil || got.LegalEntityID == nil || *got.LegalEntityID != le {
		t.Fatalf("rule of a warehouse with a setting = %+v, %v, want its legal entity", got, err)
	}
	if list, err := s.ListReleaseRules(ctx); err != nil || len(list) != 1 || list[0].WarehouseID != bySetting {
		t.Fatalf("list = %+v, %v, want the warehouse of the setting", list, err)
	}
	if _, err := s.SaveReleaseRule(ctx, bySetting, other, 0, in, idem("rr-other")); problemCode(err) != store.CodeWarehouseLegalEntity {
		t.Fatalf("rule under another legal entity than the setting: %v, want %s", err, store.CodeWarehouseLegalEntity)
	}
	if _, err := s.SaveReleaseRule(ctx, bySetting, le, 0, in, idem("rr-own")); err != nil {
		t.Fatal(err)
	}

	byRule := ids.New()
	if _, err := s.SaveReleaseRule(ctx, byRule, le, 0, in, idem("rr")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveWarehouseSetting(ctx, byRule, other, 0, domain.ActionCancelRemainder, idem("ws-other")); problemCode(err) != store.CodeWarehouseLegalEntity {
		t.Fatalf("setting under another legal entity than the rule: %v, want %s", err, store.CodeWarehouseLegalEntity)
	}
}

// Without a saved rule a warehouse reads the process configuration for the
// horizon and the ATP rule (what a release run applies), and the first save
// reports those as the previous values.
func TestReleaseRule_DefaultsFollowTheConfiguration(t *testing.T) {
	s, admin := setup(t)
	s = s.WithReleaseDefaults(5, false)
	ctx := ctxFor(1, "")
	f := releasedDefault(t, s, ctx, 1)
	got, err := s.GetReleaseRule(ctx, f.warehouse)
	if err != nil || got.Saved() || got.HorizonDays != 5 || got.RequireFullATP || got.WaveMaxLines != 200 || got.WaveMaxOrders != 50 {
		t.Fatalf("default = %+v, %v, want 5 days, partial ATP, 200, 50", got, err)
	}
	if list, err := s.ListReleaseRules(ctx); err != nil || len(list) != 1 || list[0].HorizonDays != 5 || list[0].RequireFullATP {
		t.Fatalf("default list = %+v, %v", list, err)
	}
	if _, err := s.SaveReleaseRule(ctx, f.warehouse, f.legalEntity, 0, store.ReleaseRuleInput{HorizonDays: 1, RequireFullATP: true, WaveMaxLines: 9, WaveMaxOrders: 3}, idem("s")); err != nil {
		t.Fatal(err)
	}
	if h, atp := outboxData(t, admin, fulevents.WarehouseReleaseRuleChanged, "{previous_release_horizon_days}"),
		outboxData(t, admin, fulevents.WarehouseReleaseRuleChanged, "{previous_release_requires_full_atp}"); h != "5" || atp != "false" {
		t.Fatalf("previous in the first event = %s, %s, want 5, false", h, atp)
	}
}
