package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

func br2211Profile(code string) domain.BufferProfileSpec {
	p := domain.DefaultBufferProfile(code, domain.BufferManufactured)
	p.LeadTimeFactor, p.VariabilityFactor, p.OrderCycleDays = decimal.RequireFromString("0.5"), decimal.RequireFromString("0.5"), 7
	return p
}

// br2211Buffer is the evaluated BR-22.11 buffer of item at site.
func br2211Buffer(t *testing.T, item, site, warehouse, profileID string, onHand int64) store.DdmrpBuffer {
	t.Helper()
	ten := decimal.NewFromInt(10)
	st, err := domain.EvaluateBuffer(domain.BufferInput{Profile: br2211Profile("X").Factors(), ADU: ten, DLT: ten,
		DAF: decimal.NewFromInt(1), OnHand: decimal.NewFromInt(onHand), OpenSupply: decimal.NewFromInt(60),
		Demand: []domain.DayDemand{{Day: 0, Qty: decimal.NewFromInt(20)}}, SpikeHorizonDays: 10})
	if err != nil {
		t.Fatal(err)
	}
	return store.DdmrpBuffer{ItemID: item, SiteID: site, WarehouseID: warehouse, ProfileID: profileID, ADU: ten, DLTDays: ten,
		DAF: decimal.NewFromInt(1), Status: st}
}

// TestBufferProfilesAndBuffers covers FR-22.16 persistence on the database:
// profile create with exact decimals, idempotent replay, unique code, If-Match
// update; the buffer upsert keeping one row per item, site and warehouse; the
// buffer status inquiry filters; the item coverage reference to a profile; and
// tenant isolation under RLS.
func TestBufferProfilesAndBuffers(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.setup.maintain", "planning.read")

	spec := br2211Profile("BUF-MFG")
	spec.MinOrderQty = decimal.RequireFromString("12.000001")
	horizon := 5
	spec.SpikeHorizonDays = &horizon
	created, replayed, err := st.CreateBufferProfile(ctx, store.BufferProfileCreate{IdempotencyKey: "bp-1", InputHash: "h1", Spec: spec})
	if err != nil || replayed || created.Version != 1 {
		t.Fatalf("create = %+v (replayed %v), %v", created, replayed, err)
	}
	got := created.Spec
	if !got.LeadTimeFactor.Equal(spec.LeadTimeFactor) || !got.MinOrderQty.Equal(spec.MinOrderQty) || got.MinOrderQty.String() != "12.000001" ||
		!got.SpikeThresholdPct.Equal(decimal.NewFromInt(50)) || got.SpikeHorizonDays == nil || *got.SpikeHorizonDays != 5 || got.PastDays != 90 {
		t.Fatalf("round trip: %+v", got)
	}
	again, replayed, err := st.CreateBufferProfile(ctx, store.BufferProfileCreate{IdempotencyKey: "bp-1", InputHash: "h1", Spec: spec})
	if err != nil || !replayed || again.ID != created.ID {
		t.Fatalf("replay = %+v (replayed %v), %v", again, replayed, err)
	}
	if _, _, err := st.CreateBufferProfile(ctx, store.BufferProfileCreate{IdempotencyKey: "bp-1", InputHash: "h2", Spec: spec}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	if _, _, err := st.CreateBufferProfile(ctx, store.BufferProfileCreate{Spec: spec}); problemCode(err) != store.CodeProfileExists {
		t.Fatalf("duplicate code: %v", err)
	}
	if _, err := st.GetBufferProfile(ctx, ids.New()); problemCode(err) != store.CodeProfileNotFound {
		t.Fatalf("unknown profile: %v", err)
	}

	spec.SpikeHorizonDays = nil
	spec.VariabilityFactor = decimal.RequireFromString("0.25")
	updated, err := st.UpdateBufferProfile(ctx, created.ID, 1, spec)
	if err != nil || updated.Version != 2 || updated.Spec.SpikeHorizonDays != nil || updated.Spec.VariabilityFactor.String() != "0.25" {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if _, err := st.UpdateBufferProfile(ctx, created.ID, 1, spec); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale update: %v", err)
	}
	other, _, err := st.CreateBufferProfile(ctx, store.BufferProfileCreate{Spec: br2211Profile("BUF-PUR")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateBufferProfile(ctx, other.ID, 1, spec); problemCode(err) != store.CodeProfileExists {
		t.Fatalf("rename onto a used code: %v", err)
	}
	list, err := st.ListBufferProfiles(ctx, 10)
	if err != nil || len(list) != 2 || list[0].Spec.Code != "BUF-MFG" || list[1].Spec.Code != "BUF-PUR" {
		t.Fatalf("list by code = %+v, %v", list, err)
	}

	// One stored buffer per item, site and warehouse: a recalculation replaces it.
	item, site, wh := ids.New(), ids.New(), ids.New()
	first, err := st.UpsertDdmrpBuffer(ctx, br2211Buffer(t, item, site, "", created.ID, 90))
	if err != nil {
		t.Fatal(err)
	}
	s := first.Status
	if first.ProfileCode != "BUF-MFG" || s.Zone != domain.ZoneYellow || !s.PlannedQty.Equal(decimal.NewFromInt(115)) ||
		s.Priority.String() != "0.5306" || !s.NetFlow.Equal(decimal.NewFromInt(130)) || !s.QualifiedDemand.Equal(decimal.NewFromInt(20)) ||
		!s.Zones.TopOfGreen.Equal(decimal.NewFromInt(245)) || first.CalculatedAt == "" {
		t.Fatalf("stored BR-22.11 buffer: %+v", first)
	}
	second, err := st.UpsertDdmrpBuffer(ctx, br2211Buffer(t, item, site, "", created.ID, 300))
	if err != nil || second.ID != first.ID || second.Status.Zone != domain.ZoneGreen {
		t.Fatalf("recalculation of the same scope = %+v, %v; want id %s, green", second, err, first.ID)
	}
	inWarehouse, err := st.UpsertDdmrpBuffer(ctx, br2211Buffer(t, item, site, wh, other.ID, 10))
	if err != nil || inWarehouse.ID == first.ID || inWarehouse.WarehouseID != wh || inWarehouse.Status.Zone != domain.ZoneRed {
		t.Fatalf("warehouse buffer = %+v, %v", inWarehouse, err)
	}
	if _, err := st.UpsertDdmrpBuffer(ctx, br2211Buffer(t, item, site, "", ids.New(), 90)); problemCode(err) != store.CodeProfileNotFound {
		t.Fatalf("unknown profile on a buffer: %v", err)
	}
	all, err := st.ListDdmrpBuffers(ctx, store.BufferQuery{Limit: 10})
	if err != nil || len(all) != 2 || all[0].ID != inWarehouse.ID {
		t.Fatalf("most urgent first = %+v, %v", all, err)
	}
	half := decimal.RequireFromString("0.5")
	red, err := st.ListDdmrpBuffers(ctx, store.BufferQuery{SiteID: site, Zone: domain.ZoneRed, MaxPriority: &half, Limit: 10})
	if err != nil || len(red) != 1 || red[0].ID != inWarehouse.ID {
		t.Fatalf("red at the site under 0.5 = %+v, %v", red, err)
	}
	if none, err := st.ListDdmrpBuffers(ctx, store.BufferQuery{SiteID: ids.New(), Limit: 10}); err != nil || len(none) != 0 {
		t.Fatalf("other site = %+v, %v", none, err)
	}

	// Item coverage now refers to a stored buffer profile.
	group, _, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{Spec: domain.DefaultCoverageGroup("REQ", domain.CoverageRequirement)})
	if err != nil {
		t.Fatal(err)
	}
	cov := domain.DefaultItemCoverage(item, site, group.ID)
	cov.DecouplingPoint, cov.BufferProfileID = true, ids.New()
	if _, _, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{Spec: cov}); problemCode(err) != store.CodeProfileNotFound {
		t.Fatalf("item coverage with an unknown profile: %v; want %s", err, store.CodeProfileNotFound)
	}
	cov.BufferProfileID = created.ID
	if _, _, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{Spec: cov}); err != nil {
		t.Fatalf("item coverage with a stored profile: %v", err)
	}

	// Another tenant sees none of it and may reuse the code.
	tenant2 := testkit.Ctx(testkit.TenantID(2), "planning.setup.maintain", "planning.read")
	if l, err := st.ListBufferProfiles(tenant2, 10); err != nil || len(l) != 0 {
		t.Fatalf("tenant 2 profiles = %+v, %v", l, err)
	}
	if b, err := st.ListDdmrpBuffers(tenant2, store.BufferQuery{Limit: 10}); err != nil || len(b) != 0 {
		t.Fatalf("tenant 2 buffers = %+v, %v", b, err)
	}
	if _, err := st.GetBufferProfile(tenant2, created.ID); problemCode(err) != store.CodeProfileNotFound {
		t.Fatalf("tenant 2 reads tenant 1's profile: %v", err)
	}
	if _, _, err := st.CreateBufferProfile(tenant2, store.BufferProfileCreate{Spec: br2211Profile("BUF-MFG")}); err != nil {
		t.Fatalf("tenant 2 reusing the code: %v", err)
	}
	if _, err := st.UpsertDdmrpBuffer(tenant2, br2211Buffer(t, item, site, "", created.ID, 90)); problemCode(err) != store.CodeProfileNotFound {
		t.Fatalf("tenant 2 buffering on tenant 1's profile: %v", err)
	}
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from ddmrp_buffer`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("ddmrp_buffer rows = %d, %v; want 2", n, err)
	}
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// br2212 is the BR-22.12 arrangement of item at site: vendor A quota 60
// allocated 300, vendor B quota 40 allocated 100, and a zero-quota transfer
// fallback with a per-period maximum.
func br2212(item, site string, from, to time.Time) domain.QuotaArrangementSpec {
	limit, period := decimal.RequireFromString("500.5"), 30
	return domain.QuotaArrangementSpec{ItemID: item, SiteID: site, ValidFrom: from, ValidTo: to, MinSplitQty: decimal.Zero,
		Sources: []domain.QuotaSourceSpec{
			{Type: domain.SourceVendor, VendorID: ids.New(), Quota: decimal.NewFromInt(60), Allocated: decimal.NewFromInt(300), Priority: 2},
			{Type: domain.SourceVendor, VendorID: ids.New(), Quota: decimal.NewFromInt(40), Allocated: decimal.NewFromInt(100), Priority: 1},
			{Type: domain.SourceTransfer, FromWarehouseID: ids.New(), Quota: decimal.Zero, MaxQtyPerPeriod: &limit, PeriodDays: &period},
		}}
}

// TestQuotaArrangements covers FR-22.17 persistence on the database: sources
// kept in the order given with exact decimals, idempotent replay, the overlap
// invariant (inclusive dates, adjacent periods allowed, other sites and items
// unaffected), If-Match update replacing the sources, the list filters and
// tenant isolation under RLS.
func TestQuotaArrangements(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.setup.maintain", "planning.read")
	item, site := ids.New(), ids.New()

	spec := br2212(item, site, day(2026, 11, 1), day(2026, 12, 31))
	created, replayed, err := st.CreateQuotaArrangement(ctx, store.QuotaCreate{IdempotencyKey: "qa-1", InputHash: "h1", Spec: spec})
	if err != nil || replayed || created.Version != 1 || len(created.Spec.Sources) != 3 {
		t.Fatalf("create = %+v (replayed %v), %v", created, replayed, err)
	}
	src := created.Spec.Sources
	if src[0].VendorID != spec.Sources[0].VendorID || src[1].VendorID != spec.Sources[1].VendorID || src[2].FromWarehouseID == "" ||
		src[2].MaxQtyPerPeriod == nil || src[2].MaxQtyPerPeriod.String() != "500.5" || *src[2].PeriodDays != 30 ||
		!src[0].Allocated.Equal(decimal.NewFromInt(300)) || src[0].ID == "" || src[1].MaxQtyPerPeriod != nil {
		t.Fatalf("sources not kept in order with their values: %+v", src)
	}
	if !created.Spec.ValidFrom.Equal(day(2026, 11, 1)) || !created.Spec.ValidTo.Equal(day(2026, 12, 31)) {
		t.Fatalf("validity: %v..%v", created.Spec.ValidFrom, created.Spec.ValidTo)
	}
	again, replayed, err := st.CreateQuotaArrangement(ctx, store.QuotaCreate{IdempotencyKey: "qa-1", InputHash: "h1", Spec: spec})
	if err != nil || !replayed || again.ID != created.ID || len(again.Spec.Sources) != 3 {
		t.Fatalf("replay = %+v (replayed %v), %v", again, replayed, err)
	}
	if _, _, err := st.CreateQuotaArrangement(ctx, store.QuotaCreate{IdempotencyKey: "qa-1", InputHash: "h2", Spec: spec}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}

	for _, tc := range []struct {
		name     string
		from, to time.Time
		code     string
	}{
		{"same last day", day(2026, 12, 31), day(2027, 1, 31), store.CodeQuotaOverlap},
		{"inside", day(2026, 11, 10), day(2026, 11, 20), store.CodeQuotaOverlap},
		{"around", day(2026, 10, 1), day(2027, 1, 31), store.CodeQuotaOverlap},
		{"adjacent before", day(2026, 10, 1), day(2026, 10, 31), ""},
		{"adjacent after", day(2027, 1, 1), day(2027, 6, 30), ""},
	} {
		_, _, err := st.CreateQuotaArrangement(ctx, store.QuotaCreate{Spec: br2212(item, site, tc.from, tc.to)})
		if problemCode(err) != tc.code || (tc.code == "" && err != nil) {
			t.Fatalf("%s: %v; want %q", tc.name, err, tc.code)
		}
	}
	if _, _, err := st.CreateQuotaArrangement(ctx, store.QuotaCreate{Spec: br2212(item, ids.New(), day(2026, 11, 1), day(2026, 12, 31))}); err != nil {
		t.Fatalf("same period at another site: %v", err)
	}

	// Update: replace the sources and move the period; the arrangement itself is no clash.
	upd := br2212(item, site, day(2026, 11, 2), day(2026, 12, 31))
	upd.Split, upd.MinSplitQty = true, decimal.NewFromInt(25)
	upd.Sources = upd.Sources[:2]
	updated, err := st.UpdateQuotaArrangement(ctx, created.ID, 1, upd)
	if err != nil || updated.Version != 2 || !updated.Spec.Split || len(updated.Spec.Sources) != 2 || updated.Spec.Sources[0].ID == src[0].ID {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if _, err := st.UpdateQuotaArrangement(ctx, created.ID, 1, upd); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale update: %v", err)
	}
	clash := upd
	clash.ValidTo = day(2027, 1, 5)
	if _, err := st.UpdateQuotaArrangement(ctx, created.ID, 2, clash); problemCode(err) != store.CodeQuotaOverlap {
		t.Fatalf("update into the next period: %v", err)
	}
	if _, err := st.UpdateQuotaArrangement(ctx, ids.New(), 1, upd); problemCode(err) != store.CodeQuotaNotFound {
		t.Fatalf("update of an unknown arrangement: %v", err)
	}
	var sources int
	if err := admin.QueryRow(context.Background(), `select count(*) from quota_source where arrangement_id = $1`, created.ID).Scan(&sources); err != nil || sources != 2 {
		t.Fatalf("sources after update = %d, %v; want 2", sources, err)
	}

	mid := day(2026, 11, 15)
	list, err := st.ListQuotaArrangements(ctx, store.QuotaQuery{ItemID: item, SiteID: site, ValidOn: &mid, Limit: 10})
	if err != nil || len(list) != 1 || list[0].ID != created.ID || len(list[0].Spec.Sources) != 2 {
		t.Fatalf("valid on 2026-11-15 = %+v, %v", list, err)
	}
	all, err := st.ListQuotaArrangements(ctx, store.QuotaQuery{ItemID: item, Limit: 10})
	if err != nil || len(all) != 4 {
		t.Fatalf("all of the item = %d, %v; want 4", len(all), err)
	}
	if got, err := st.GetQuotaArrangement(ctx, created.ID); err != nil || got.Version != 2 || len(got.Spec.Sources) != 2 {
		t.Fatalf("get = %+v, %v", got, err)
	}

	tenant2 := testkit.Ctx(testkit.TenantID(2), "planning.setup.maintain", "planning.read")
	if l, err := st.ListQuotaArrangements(tenant2, store.QuotaQuery{Limit: 10}); err != nil || len(l) != 0 {
		t.Fatalf("tenant 2 list = %+v, %v", l, err)
	}
	if _, err := st.GetQuotaArrangement(tenant2, created.ID); problemCode(err) != store.CodeQuotaNotFound {
		t.Fatalf("tenant 2 reads tenant 1's arrangement: %v", err)
	}
	if _, _, err := st.CreateQuotaArrangement(tenant2, store.QuotaCreate{Spec: spec}); err != nil {
		t.Fatalf("tenant 2 with the same item, site and period: %v", err)
	}
}
