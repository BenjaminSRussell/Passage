package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	siteCreatedType = "dev.passage.org.site.created.v1"
	siteUpdatedType = "dev.passage.org.site.updated.v1"
)

func newSite(legalEntityID, code string) store.Site {
	return store.Site{LegalEntityID: legalEntityID, Code: code, Name: "Site " + code}
}

func mustCreateSite(t *testing.T, s *store.Postgres, ctx context.Context, in store.Site) store.Site {
	t.Helper()
	created, err := s.CreateSite(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func scopedTo(legalEntityID string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: legalEntityID})
}

func TestPostgres_SiteCreateWritesOneEventAndReplayWritesNone(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("US01"))
	first, err := s.CreateSite(ctx, newSite(a.ID, "S1"), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateSite(ctx, newSite(a.ID, "S1"), "key-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v; want the original site", replay, err)
	}
	changed := newSite(a.ID, "S1")
	changed.Name = "Other"
	if _, err := s.CreateSite(ctx, changed, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("replay with a different name error = %v", err)
	}
	events := unitEvents(t, ctx, pool, siteCreatedType)
	if len(events) != 1 || events[0]["id"] != first.ID || events[0]["legal_entity_id"] != a.ID {
		t.Fatalf("created events = %v; want exactly one for the site", events)
	}
	for _, key := range []string{"parent_id", "type", "address_id"} {
		if value, present := events[0][key]; !present || value != nil {
			t.Fatalf("%s = %v, present %v; want present and null", key, value, present)
		}
	}
}

func TestPostgres_SiteTimeZoneDefaultsToTheLegalEntity(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	in := newEntity("US01")
	in.TimeZone = "America/New_York"
	a := mustCreate(t, s, ctx, in)
	if got := mustCreateSite(t, s, ctx, newSite(a.ID, "S1")); got.TimeZone != "America/New_York" {
		t.Fatalf("default time zone = %q", got.TimeZone)
	}
	berlin := newSite(a.ID, "S2")
	berlin.TimeZone = "Europe/Berlin"
	if got := mustCreateSite(t, s, ctx, berlin); got.TimeZone != "Europe/Berlin" {
		t.Fatalf("explicit time zone = %q", got.TimeZone)
	}
}

func TestPostgres_SiteCodeIsUniquePerLegalEntity(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a, b := mustCreate(t, s, ctx, newEntity("US01")), mustCreate(t, s, ctx, newEntity("US02"))
	mustCreateSite(t, s, ctx, newSite(a.ID, "S1"))
	mustCreateSite(t, s, ctx, newSite(b.ID, "S1"))
	if _, err := s.CreateSite(ctx, newSite(a.ID, "S1"), ""); problemCode(t, err) != store.CodeSiteCodeDuplicate {
		t.Fatalf("duplicate code error = %v", err)
	}
}

func TestPostgres_SiteTenantIsolation(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("US01"))
	site := mustCreateSite(t, s, ctx, newSite(a.ID, "S1"))
	other := principalContext(2)
	if _, err := s.GetSite(other, site.ID); problemCode(t, err) != store.CodeSiteNotFound {
		t.Fatalf("tenant 2 get error = %v", err)
	}
	if page, err := s.ListSites(other, store.SiteFilter{}, store.SiteKey{}, store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 list = %+v, %v; want empty", page, err)
	}
}

func TestPostgres_SiteLegalEntityMustBeActiveAndVisible(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	b := mustCreate(t, s, ctx, newEntity("US02"))
	if _, err := s.CreateSite(ctx, newSite(testkit.TenantID(9), "S1"), ""); problemCode(t, err) != store.CodeSiteLegalEntityInvalid {
		t.Fatalf("unknown legal entity error = %v", err)
	}
	inactive := "inactive"
	if _, err := s.UpdateLegalEntity(ctx, b.ID, 1, store.LegalEntityPatch{Status: &inactive}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSite(ctx, newSite(b.ID, "S2"), ""); problemCode(t, err) != store.CodeSiteLegalEntityInvalid {
		t.Fatalf("inactive legal entity error = %v", err)
	}
}

func TestPostgres_SiteScopedCaller(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a, b := mustCreate(t, s, ctx, newEntity("US01")), mustCreate(t, s, ctx, newEntity("US02"))
	siteA, siteB := mustCreateSite(t, s, ctx, newSite(a.ID, "S1")), mustCreateSite(t, s, ctx, newSite(b.ID, "S2"))
	scoped := scopedTo(a.ID)
	if _, err := s.CreateSite(scoped, newSite(b.ID, "S3"), ""); problemCode(t, err) != store.CodeSiteLegalEntityInvalid {
		t.Fatalf("create under another legal entity error = %v", err)
	}
	page, err := s.ListSites(scoped, store.SiteFilter{}, store.SiteKey{}, store.MaxPageSize)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != siteA.ID {
		t.Fatalf("scoped list = %+v, %v; want only A's site", page, err)
	}
	if _, err := s.GetSite(scoped, siteB.ID); problemCode(t, err) != store.CodeSiteNotFound {
		t.Fatalf("get of B's site error = %v", err)
	}
	name := "Hijacked"
	if _, err := s.UpdateSite(scoped, siteB.ID, 1, store.SitePatch{Name: &name}); problemCode(t, err) != store.CodeSiteNotFound {
		t.Fatalf("update of B's site error = %v", err)
	}
	if untouched, _ := s.GetSite(ctx, siteB.ID); untouched.Name != "Site S2" || untouched.Version != 1 {
		t.Fatalf("site B changed: %+v", untouched)
	}
}

func TestPostgres_ListSitesFiltersAndPages(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a, b := mustCreate(t, s, ctx, newEntity("US01")), mustCreate(t, s, ctx, newEntity("US02"))
	one, two := mustCreateSite(t, s, ctx, newSite(a.ID, "S1")), mustCreateSite(t, s, ctx, newSite(b.ID, "S1"))
	inactive := "inactive"
	if _, err := s.UpdateSite(ctx, two.ID, 1, store.SitePatch{Status: &inactive}); err != nil {
		t.Fatal(err)
	}
	if page, err := s.ListSites(ctx, store.SiteFilter{LegalEntityID: a.ID}, store.SiteKey{}, store.MaxPageSize); err != nil || len(page.Items) != 1 || page.Items[0].ID != one.ID {
		t.Fatalf("legal entity filter = %+v, %v", page, err)
	}
	if page, err := s.ListSites(ctx, store.SiteFilter{Status: "inactive"}, store.SiteKey{}, store.MaxPageSize); err != nil || len(page.Items) != 1 || page.Items[0].ID != two.ID {
		t.Fatalf("status filter = %+v, %v", page, err)
	}
	seen := map[string]bool{}
	var after store.SiteKey
	for pages := 0; pages < 3; pages++ {
		page, err := s.ListSites(ctx, store.SiteFilter{}, after, 1)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("page %d = %+v, %v", pages, page, err)
		}
		seen[page.Items[0].ID] = true
		if page.Next == nil {
			break
		}
		after = *page.Next
	}
	if len(seen) != 2 || !seen[one.ID] || !seen[two.ID] {
		t.Fatalf("walk saw %v; want both sites with the same code once", seen)
	}
}

func TestPostgres_UpdateSiteVersionsAndEmitsOneEvent(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("US01"))
	site := mustCreateSite(t, s, ctx, newSite(a.ID, "S1"))
	name := "Renamed"
	renamed, err := s.UpdateSite(ctx, site.ID, 1, store.SitePatch{Name: &name})
	if err != nil || renamed.Version != 2 || renamed.Name != name {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}
	events := unitEvents(t, ctx, pool, siteUpdatedType)
	if len(events) != 1 || !reflect.DeepEqual(events[0]["changed_fields"], []any{"name"}) || events[0]["version"] != float64(2) {
		t.Fatalf("updated events = %v", events)
	}
	if _, err := s.UpdateSite(ctx, site.ID, 1, store.SitePatch{Name: &name}); problemCode(t, err) != store.CodeVersionConflict {
		t.Fatalf("stale version error = %v", err)
	}
	same, err := s.UpdateSite(ctx, site.ID, 2, store.SitePatch{Name: &name})
	if err != nil || same.Version != 2 {
		t.Fatalf("no-op = %+v, %v; want version 2", same, err)
	}
	if got := len(unitEvents(t, ctx, pool, siteUpdatedType)); got != 1 {
		t.Fatalf("stale and no-op patches left %d updated events; want 1", got)
	}
	zone, status := "Europe/Berlin", "inactive"
	if _, err := s.UpdateSite(ctx, site.ID, 2, store.SitePatch{TimeZone: &zone, Status: &status}); err != nil {
		t.Fatal(err)
	}
	events = unitEvents(t, ctx, pool, siteUpdatedType)
	if len(events) != 2 || !reflect.DeepEqual(events[1]["changed_fields"], []any{"status", "time_zone"}) {
		t.Fatalf("updated events = %v", events)
	}
	if got := len(unitEvents(t, ctx, pool, siteCreatedType)); got != 1 {
		t.Fatalf("created events = %d; want 1", got)
	}
}
