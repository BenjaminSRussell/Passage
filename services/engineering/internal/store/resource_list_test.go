package store_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
	"github.com/BenjaminSRussell/passage/services/engineering/migrations"
)

type listing struct {
	weld, paint     domain.Capability
	west, east      domain.ResourceGroup
	westCalendar    string
	byCode          map[string]domain.Resource
	capabilityCodes map[string]string
}

func seedListing(t *testing.T, s *store.Postgres, ctx context.Context) listing {
	t.Helper()
	_, wc := loadFixture(t)
	calendar := newCalendar(t, s, ctx, wc)
	l := listing{
		weld: domain.Capability{ID: ids.New(), Code: "WELD", Name: "Welding"}, paint: domain.Capability{ID: ids.New(), Code: "PAINT", Name: "Painting"},
		west: domain.ResourceGroup{ID: ids.New(), Code: "WEST", Name: "West", SiteID: "S1", CalendarID: calendar.ID}, east: domain.ResourceGroup{ID: ids.New(), Code: "EAST", Name: "East", SiteID: "S2"},
		westCalendar: calendar.ID, byCode: map[string]domain.Resource{},
	}
	for _, c := range []domain.Capability{l.weld, l.paint} {
		if _, err := s.CreateCapability(ctx, c, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []domain.ResourceGroup{l.west, l.east} {
		if _, err := s.CreateResourceGroup(ctx, g, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	held := func(c domain.Capability) domain.ResourceCapability {
		return domain.ResourceCapability{CapabilityID: c.ID, Level: 2, EffectiveFrom: day(t, "2026-01-01")}
	}
	for _, r := range []struct {
		code, site, kind, group string
		caps                    []domain.ResourceCapability
	}{
		{"M1", "S1", domain.TypeMachine, l.west.ID, []domain.ResourceCapability{held(l.weld)}},
		{"M2", "S1", domain.TypeTool, "", nil},
		{"M3", "S2", domain.TypeMachine, l.east.ID, []domain.ResourceCapability{held(l.weld), held(l.paint)}},
		{"M4", "S1", domain.TypeMachine, l.west.ID, []domain.ResourceCapability{held(l.paint)}},
		{"M5", "S2", domain.TypeLabor, "", nil},
	} {
		res := machine(r.code, r.site)
		res.Type, res.GroupID, res.Capabilities = r.kind, r.group, r.caps
		if _, err := s.CreateResource(ctx, res, store.Idem{}); err != nil {
			t.Fatal(err)
		}
		l.byCode[r.code] = res
	}
	return l
}

func walkResources(t *testing.T, s *store.Postgres, ctx context.Context, f store.ResourceFilter, limit int) (records []store.ResourceRecord, pages int) {
	t.Helper()
	after := ""
	for range 20 {
		page, err := s.ListResources(ctx, f, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		records, pages = append(records, page.Items...), pages+1
		if page.NextID == "" {
			return records, pages
		}
		after = page.NextID
	}
	t.Fatal("paging did not terminate")
	return nil, 0
}

func codesOf(records []store.ResourceRecord) (codes []string) {
	for _, r := range records {
		codes = append(codes, r.Resource.Code)
	}
	return codes
}

func TestPostgres_ListResources_FiltersAndPagesByCode(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	l := seedListing(t, s, ctx)
	tests := []struct {
		name   string
		filter store.ResourceFilter
		want   []string
	}{
		{"no filter", store.ResourceFilter{}, []string{"M1", "M2", "M3", "M4", "M5"}},
		{"site", store.ResourceFilter{SiteID: "S1"}, []string{"M1", "M2", "M4"}},
		{"type", store.ResourceFilter{Type: domain.TypeMachine}, []string{"M1", "M3", "M4"}},
		{"group", store.ResourceFilter{GroupID: l.west.ID}, []string{"M1", "M4"}},
		{"capability", store.ResourceFilter{CapabilityID: l.weld.ID}, []string{"M1", "M3"}},
		{"site and type", store.ResourceFilter{SiteID: "S2", Type: domain.TypeMachine}, []string{"M3"}},
		{"group and capability", store.ResourceFilter{GroupID: l.west.ID, CapabilityID: l.paint.ID}, []string{"M4"}},
		{"all four", store.ResourceFilter{SiteID: "S1", Type: domain.TypeMachine, GroupID: l.west.ID, CapabilityID: l.weld.ID}, []string{"M1"}},
		{"no match", store.ResourceFilter{SiteID: "S2", Type: domain.TypeTool}, nil},
		{"unknown capability", store.ResourceFilter{CapabilityID: ids.New()}, nil},
	}
	for _, tc := range tests {
		for _, limit := range []int{1, 2, store.MaxPageSize} {
			got, _ := walkResources(t, s, ctx, tc.filter, limit)
			if !slices.Equal(codesOf(got), tc.want) {
				t.Fatalf("%s (limit %d) = %v; want %v", tc.name, limit, codesOf(got), tc.want)
			}
		}
	}
	if got, pages := walkResources(t, s, ctx, store.ResourceFilter{}, 2); pages != 3 || len(got) != 5 {
		t.Fatalf("limit 2 over 5 rows = %d rows in %d pages; want 5 rows in 3 pages", len(got), pages)
	}
	if page, err := s.ListResources(ctx, store.ResourceFilter{}, ids.New(), store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("cursor for an absent row = %+v, %v; want an empty page", page, err)
	}
}

func TestPostgres_ListResources_AttachesCapabilitiesAndEffectiveCalendar(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	l := seedListing(t, s, ctx)
	got, _ := walkResources(t, s, ctx, store.ResourceFilter{}, 2)
	byCode := map[string]store.ResourceRecord{}
	for _, r := range got {
		byCode[r.Resource.Code] = r
	}
	wantCaps := map[string][]string{"M1": {l.weld.ID}, "M2": nil, "M3": {l.weld.ID, l.paint.ID}, "M4": {l.paint.ID}, "M5": nil}
	for code, want := range wantCaps {
		var held []string
		for _, c := range byCode[code].Resource.Capabilities {
			held = append(held, c.CapabilityID)
		}
		slices.Sort(held)
		slices.Sort(want)
		if !slices.Equal(held, want) {
			t.Fatalf("%s capabilities = %v; want %v", code, held, want)
		}
	}
	if c := byCode["M1"].Resource.Capabilities[0]; c.Level != 2 || c.EffectiveFrom.IsZero() {
		t.Fatalf("capability = %+v; want level 2 and a start date", c)
	}
	one, err := s.GetResource(ctx, l.byCode["M1"].ID)
	if err != nil || byCode["M1"].EffectiveCalendarID != l.westCalendar || byCode["M1"].EffectiveCalendarID != one.EffectiveCalendarID || byCode["M3"].EffectiveCalendarID != "" {
		t.Fatalf("effective calendars: M1 = %q, M3 = %q; want the west group's %q and none (get: %q, %v)", byCode["M1"].EffectiveCalendarID, byCode["M3"].EffectiveCalendarID, l.westCalendar, one.EffectiveCalendarID, err)
	}
}

func TestPostgres_ListGroupsAndCapabilities_PageByCodeAndFilterBySite(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	l := seedListing(t, s, ctx)
	var groups []string
	for after, n := "", 0; n < 5; n++ {
		page, err := s.ListResourceGroups(ctx, "", after, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range page.Items {
			groups = append(groups, g.Code)
		}
		if after = page.NextID; after == "" {
			break
		}
	}
	if !slices.Equal(groups, []string{"EAST", "WEST"}) {
		t.Fatalf("groups = %v; want EAST, WEST", groups)
	}
	if page, err := s.ListResourceGroups(ctx, "S1", "", store.MaxPageSize); err != nil || len(page.Items) != 1 || page.Items[0].ID != l.west.ID || page.Items[0].CalendarID != l.westCalendar {
		t.Fatalf("groups on S1 = %+v, %v; want only WEST with its calendar", page, err)
	}
	first, err := s.ListCapabilities(ctx, "", 1)
	if err != nil || len(first.Items) != 1 || first.Items[0].Code != "PAINT" || first.NextID != l.paint.ID {
		t.Fatalf("first capability page = %+v, %v; want PAINT with a cursor", first, err)
	}
	second, err := s.ListCapabilities(ctx, first.NextID, 1)
	if err != nil || len(second.Items) != 1 || second.Items[0].Code != "WELD" || second.NextID != "" {
		t.Fatalf("second capability page = %+v, %v; want WELD and no cursor", second, err)
	}
}

func TestPostgres_Lists_RLSHideOtherTenant(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	seedListing(t, s, principalContext(1))
	intruder := principalContext(2)
	resources, resourcesErr := s.ListResources(intruder, store.ResourceFilter{}, "", store.MaxPageSize)
	groups, groupsErr := s.ListResourceGroups(intruder, "", "", store.MaxPageSize)
	capabilities, capabilitiesErr := s.ListCapabilities(intruder, "", store.MaxPageSize)
	if resourcesErr != nil || groupsErr != nil || capabilitiesErr != nil || len(resources.Items)+len(groups.Items)+len(capabilities.Items) != 0 {
		t.Fatalf("tenant 2 lists = %+v %+v %+v (%v %v %v); want nothing", resources, groups, capabilities, resourcesErr, groupsErr, capabilitiesErr)
	}
	owner, err := s.ListResources(principalContext(1), store.ResourceFilter{}, "", store.MaxPageSize)
	if err != nil || len(owner.Items) != 5 {
		t.Fatalf("tenant 1 list = %d rows, %v; want 5", len(owner.Items), err)
	}
	if page, err := s.ListResources(intruder, store.ResourceFilter{}, owner.Items[0].Resource.ID, store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 with tenant 1 cursor = %+v, %v; want nothing", page, err)
	}
}

func TestPostgres_DetailReadsAndCalendarList(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	l := seedListing(t, s, ctx)
	_, wc := loadFixture(t)
	wc.Code = "AAA"
	second := newCalendar(t, s, ctx, wc)
	if g, err := s.GetResourceGroup(ctx, l.west.ID); err != nil || g.Code != "WEST" || g.CalendarID != l.westCalendar {
		t.Fatalf("group = %+v, %v", g, err)
	}
	if c, err := s.GetCapability(ctx, l.weld.ID); err != nil || c.Code != "WELD" {
		t.Fatalf("capability = %+v, %v", c, err)
	}
	if c, err := s.GetCalendar(ctx, l.westCalendar); err != nil || c.Code != "FIX" || c.Version != 1 {
		t.Fatalf("calendar = %+v, %v", c, err)
	}
	for name, err := range map[string]error{
		"group":      func() error { _, err := s.GetResourceGroup(ctx, ids.New()); return err }(),
		"capability": func() error { _, err := s.GetCapability(ctx, ids.New()); return err }(),
		"calendar":   func() error { _, err := s.GetCalendar(ctx, ids.New()); return err }(),
	} {
		var problem *httpx.Problem
		if !errors.As(err, &problem) || problem.Status != http.StatusNotFound {
			t.Fatalf("missing %s = %v; want a 404 problem", name, err)
		}
	}
	first, err := s.ListCalendars(ctx, "", 1)
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != second.ID || first.NextID != second.ID {
		t.Fatalf("first page = %+v, %v; want AAA with a cursor", first, err)
	}
	rest, err := s.ListCalendars(ctx, first.NextID, store.MaxPageSize)
	if err != nil || len(rest.Items) != 1 || rest.Items[0].ID != l.westCalendar || rest.NextID != "" {
		t.Fatalf("second page = %+v, %v; want FIX only", rest, err)
	}
}

func TestPostgres_DetailReads_RLSHideOtherTenant(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	l := seedListing(t, s, principalContext(1))
	intruder := principalContext(2)
	if _, err := s.GetResourceGroup(intruder, l.west.ID); err == nil {
		t.Fatal("tenant 2 read tenant 1 group")
	}
	if _, err := s.GetCapability(intruder, l.weld.ID); err == nil {
		t.Fatal("tenant 2 read tenant 1 capability")
	}
	if _, err := s.GetCalendar(intruder, l.westCalendar); err == nil {
		t.Fatal("tenant 2 read tenant 1 calendar")
	}
	if page, err := s.ListCalendars(intruder, "", store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 calendars = %+v, %v; want none", page, err)
	}
}
