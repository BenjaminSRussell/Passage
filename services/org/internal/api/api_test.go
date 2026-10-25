package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	pevents "github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/api"
	orgevents "github.com/BenjaminSRussell/passage/services/org/internal/events"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

type fakeStore struct {
	err                 error
	pingErr             error
	entities            []store.LegalEntity
	byKey               map[string]store.LegalEntity
	refs                map[string]store.ReferenceItem
	ous                 []store.OperatingUnit
	ouCalls             int
	sites               []store.Site
	siteCalls           int
	warehouses          []store.Warehouse
	warehouseCalls      int
	locations           []store.Location
	locationCalls       int
	locationUpdateCalls int
	addresses           []store.Address
	addressCalls        int
	reviseCalls         int
	calendars           []store.FiscalCalendar
	generatedYears      map[string]bool
	generateCalls       int
	generateErr         error
	generatedID         string
	hierarchyNodes      []store.HierarchyNode
	publishCalls        int
	publishBackdated    bool
	publishedBy         string
}

func (f *fakeStore) reference(kind store.ReferenceKind, code string) (store.ReferenceItem, bool) {
	if f.refs == nil {
		minor0, minor2, yes := 0, 2, true
		f.refs = map[string]store.ReferenceItem{
			"currency/USD": {Code: "USD", Name: "US Dollar", NumericCode: "840", MinorUnits: &minor2, Status: "active"},
			"currency/JPY": {Code: "JPY", Name: "Yen", NumericCode: "392", MinorUnits: &minor0, Status: "active"},
			"country/US":   {Code: "US", Name: "United States", RegionRequired: &yes, Status: "active"},
			"region/US-CA": {Code: "US-CA", Name: "California", CountryCode: "US", Status: "active"},
			"language/en":  {Code: "en", Name: "English", Status: "active"},
		}
	}
	item, ok := f.refs[string(kind)+"/"+code]
	return item, ok
}

func (f *fakeStore) ListReference(_ context.Context, kind store.ReferenceKind, filter store.ReferenceFilter) ([]store.ReferenceItem, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.reference(kind, "")
	items := []store.ReferenceItem{}
	for key, item := range f.refs {
		if strings.HasPrefix(key, string(kind)+"/") && (filter.Status == "" || item.Status == filter.Status) && (filter.Country == "" || item.CountryCode == filter.Country) {
			items = append(items, item)
		}
	}
	return items, nil
}

func (f *fakeStore) GetReference(_ context.Context, kind store.ReferenceKind, code string) (store.ReferenceItem, error) {
	if f.err != nil {
		return store.ReferenceItem{}, f.err
	}
	item, ok := f.reference(kind, code)
	if !ok {
		return item, httpx.NewProblem(http.StatusNotFound, store.CodeReferenceNotFound, "unknown code")
	}
	return item, nil
}

func (f *fakeStore) SetReferenceStatus(ctx context.Context, kind store.ReferenceKind, code, status string) (store.ReferenceItem, error) {
	item, err := f.GetReference(ctx, kind, code)
	if err != nil {
		return item, err
	}
	item.Status = status
	f.refs[string(kind)+"/"+code] = item
	return item, nil
}

func (f *fakeStore) CreateLegalEntity(_ context.Context, in store.LegalEntity, key string) (store.LegalEntity, error) {
	if f.err != nil {
		return store.LegalEntity{}, f.err
	}
	if replay, ok := f.byKey[key]; ok && key != "" {
		return replay, nil
	}
	in.ID, in.Status, in.Version, in.CreatedAt = "id-1", "active", 1, time.Unix(0, 0).UTC()
	f.entities = append(f.entities, in)
	if f.byKey == nil {
		f.byKey = map[string]store.LegalEntity{}
	}
	f.byKey[key] = in
	return in, nil
}

func (f *fakeStore) lookup(id string) (*store.LegalEntity, error) {
	if f.err != nil {
		return nil, f.err
	}
	for i := range f.entities {
		if f.entities[i].ID == id {
			return &f.entities[i], nil
		}
	}
	return nil, httpx.NewProblem(http.StatusNotFound, store.CodeLegalEntityNotFound, "legal entity not found")
}

func (f *fakeStore) GetLegalEntity(_ context.Context, id string) (store.LegalEntity, error) {
	e, err := f.lookup(id)
	if err != nil {
		return store.LegalEntity{}, err
	}
	return *e, nil
}

func (f *fakeStore) UpdateLegalEntity(_ context.Context, id string, version int, in store.LegalEntityPatch) (store.LegalEntity, error) {
	e, err := f.lookup(id)
	if err != nil {
		return store.LegalEntity{}, err
	}
	if e.Version != version {
		return store.LegalEntity{}, httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, "current version is 1")
	}
	if in.Name != nil && *in.Name != e.Name {
		e.Name = *in.Name
		e.Version++
	}
	return *e, nil
}

func (f *fakeStore) ListLegalEntityNames(_ context.Context, id string) ([]store.LegalEntityName, error) {
	_, err := f.lookup(id)
	return []store.LegalEntityName{{Name: "Passage US", ValidFrom: time.Unix(0, 0).UTC()}}, err
}

func (f *fakeStore) ListLegalEntities(_ context.Context, _ store.LegalEntityFilter, after string, limit int) (store.LegalEntityPage, error) {
	if f.err != nil {
		return store.LegalEntityPage{}, f.err
	}
	page := store.LegalEntityPage{Items: []store.LegalEntity{}}
	for _, e := range f.entities {
		if e.Code > after {
			page.Items = append(page.Items, e)
		}
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCode = page.Items[limit-1].Code
	}
	return page, nil
}

func (f *fakeStore) CreateOperatingUnit(_ context.Context, in store.OperatingUnit, _ string) (store.OperatingUnit, error) {
	if f.err != nil {
		return store.OperatingUnit{}, f.err
	}
	for _, u := range f.ous {
		if u.Type == in.Type && u.Code == in.Code {
			return store.OperatingUnit{}, httpx.NewProblem(http.StatusConflict, store.CodeOUCodeDuplicate, "duplicate")
		}
	}
	in.ID, in.Status, in.Version = fmt.Sprintf("00000000-0000-0000-0000-%012d", len(f.ous)+1), "active", 1
	f.ous = append(f.ous, in)
	return in, nil
}

func (f *fakeStore) lookupOperatingUnit(id string) (*store.OperatingUnit, error) {
	f.ouCalls++
	if f.err != nil {
		return nil, f.err
	}
	for i := range f.ous {
		if f.ous[i].ID == id {
			return &f.ous[i], nil
		}
	}
	return nil, httpx.NewProblem(http.StatusNotFound, store.CodeOUNotFound, "operating unit not found")
}

func (f *fakeStore) GetOperatingUnit(_ context.Context, id string) (store.OperatingUnit, error) {
	u, err := f.lookupOperatingUnit(id)
	if err != nil {
		return store.OperatingUnit{}, err
	}
	return *u, nil
}

func (f *fakeStore) ListOperatingUnits(_ context.Context, _ store.OperatingUnitFilter, after store.OperatingUnitKey, limit int) (store.OperatingUnitPage, error) {
	if f.err != nil {
		return store.OperatingUnitPage{}, f.err
	}
	page := store.OperatingUnitPage{Items: []store.OperatingUnit{}}
	sorted := slices.Clone(f.ous)
	slices.SortFunc(sorted, func(a, b store.OperatingUnit) int { return strings.Compare(a.Type+"/"+a.Code, b.Type+"/"+b.Code) })
	for _, u := range sorted {
		if u.Type+"/"+u.Code > after.Type+"/"+after.Code {
			page.Items = append(page.Items, u)
		}
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.Next = &store.OperatingUnitKey{Type: page.Items[limit-1].Type, Code: page.Items[limit-1].Code}
	}
	return page, nil
}

func (f *fakeStore) UpdateOperatingUnit(_ context.Context, id string, version int, in store.OperatingUnitPatch) (store.OperatingUnit, error) {
	u, err := f.lookupOperatingUnit(id)
	if err != nil {
		return store.OperatingUnit{}, err
	}
	if u.Version != version {
		return store.OperatingUnit{}, httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, "current version is 1")
	}
	if in.Name != nil && *in.Name != u.Name {
		u.Name = *in.Name
		u.Version++
	}
	return *u, nil
}

func (f *fakeStore) CreateSite(_ context.Context, in store.Site, _ string) (store.Site, error) {
	if f.err != nil {
		return store.Site{}, f.err
	}
	for _, s := range f.sites {
		if s.LegalEntityID == in.LegalEntityID && s.Code == in.Code {
			return store.Site{}, httpx.NewProblem(http.StatusConflict, store.CodeSiteCodeDuplicate, "duplicate")
		}
	}
	in.ID, in.Status, in.Version = fmt.Sprintf("00000000-0000-0000-0000-%012d", len(f.sites)+1), "active", 1
	f.sites = append(f.sites, in)
	return in, nil
}

func (f *fakeStore) lookupSite(id string) (*store.Site, error) {
	f.siteCalls++
	if f.err != nil {
		return nil, f.err
	}
	for i := range f.sites {
		if f.sites[i].ID == id {
			return &f.sites[i], nil
		}
	}
	return nil, httpx.NewProblem(http.StatusNotFound, store.CodeSiteNotFound, "site not found")
}

func (f *fakeStore) GetSite(_ context.Context, id string) (store.Site, error) {
	s, err := f.lookupSite(id)
	if err != nil {
		return store.Site{}, err
	}
	return *s, nil
}

func (f *fakeStore) ListSites(_ context.Context, _ store.SiteFilter, after store.SiteKey, limit int) (store.SitePage, error) {
	if f.err != nil {
		return store.SitePage{}, f.err
	}
	page := store.SitePage{Items: []store.Site{}}
	sorted := slices.Clone(f.sites)
	slices.SortFunc(sorted, func(a, b store.Site) int { return strings.Compare(a.Code+"/"+a.ID, b.Code+"/"+b.ID) })
	for _, s := range sorted {
		if s.Code+"/"+s.ID > after.Code+"/"+after.ID {
			page.Items = append(page.Items, s)
		}
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.Next = &store.SiteKey{Code: page.Items[limit-1].Code, ID: page.Items[limit-1].ID}
	}
	return page, nil
}

func (f *fakeStore) UpdateSite(_ context.Context, id string, version int, in store.SitePatch) (store.Site, error) {
	s, err := f.lookupSite(id)
	if err != nil {
		return store.Site{}, err
	}
	if s.Version != version {
		return store.Site{}, httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, fmt.Sprintf("current version is %d", s.Version))
	}
	if in.Status != nil && *in.Status == "inactive" {
		for _, w := range f.warehouses {
			if w.SiteID == id && w.Status == "active" {
				return store.Site{}, httpx.NewProblem(http.StatusConflict, store.CodeSiteInUse, "active warehouses reference this site")
			}
		}
	}
	if in.Name != nil && *in.Name != s.Name {
		s.Name = *in.Name
		s.Version++
	}
	return *s, nil
}

func (f *fakeStore) CreateWarehouse(_ context.Context, in store.Warehouse, _ string) (store.Warehouse, error) {
	if f.err != nil {
		return store.Warehouse{}, f.err
	}
	var site *store.Site
	for i := range f.sites {
		if f.sites[i].ID == in.SiteID {
			site = &f.sites[i]
		}
	}
	if site == nil {
		return store.Warehouse{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeWarehouseSiteInvalid, "site invalid")
	}
	for _, w := range f.warehouses {
		if w.LegalEntityID == site.LegalEntityID && w.Code == in.Code {
			return store.Warehouse{}, httpx.NewProblem(http.StatusConflict, store.CodeWarehouseCodeDuplicate, "duplicate")
		}
	}
	in.LegalEntityID, in.Status, in.Version = site.LegalEntityID, "active", 1
	in.ID = fmt.Sprintf("00000000-0000-0000-0001-%012d", len(f.warehouses)+1)
	if in.Type == "" {
		in.Type = "standard"
	}
	f.warehouses = append(f.warehouses, in)
	return in, nil
}

func (f *fakeStore) lookupWarehouse(id string) (*store.Warehouse, error) {
	f.warehouseCalls++
	if f.err != nil {
		return nil, f.err
	}
	for i := range f.warehouses {
		if f.warehouses[i].ID == id {
			return &f.warehouses[i], nil
		}
	}
	return nil, httpx.NewProblem(http.StatusNotFound, store.CodeWarehouseNotFound, "warehouse not found")
}

func (f *fakeStore) GetWarehouse(_ context.Context, id string) (store.Warehouse, error) {
	w, err := f.lookupWarehouse(id)
	if err != nil {
		return store.Warehouse{}, err
	}
	return *w, nil
}

func (f *fakeStore) ListWarehouses(_ context.Context, _ store.WarehouseFilter, after store.SiteKey, limit int) (store.WarehousePage, error) {
	if f.err != nil {
		return store.WarehousePage{}, f.err
	}
	page := store.WarehousePage{Items: []store.Warehouse{}}
	sorted := slices.Clone(f.warehouses)
	slices.SortFunc(sorted, func(a, b store.Warehouse) int { return strings.Compare(a.Code+"/"+a.ID, b.Code+"/"+b.ID) })
	for _, w := range sorted {
		if w.Code+"/"+w.ID > after.Code+"/"+after.ID {
			page.Items = append(page.Items, w)
		}
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.Next = &store.SiteKey{Code: page.Items[limit-1].Code, ID: page.Items[limit-1].ID}
	}
	return page, nil
}

func (f *fakeStore) UpdateWarehouse(_ context.Context, id string, version int, in store.WarehousePatch) (store.Warehouse, error) {
	w, err := f.lookupWarehouse(id)
	if err != nil {
		return store.Warehouse{}, err
	}
	if w.Version != version {
		return store.Warehouse{}, httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, fmt.Sprintf("current version is %d", w.Version))
	}
	if in.SiteID != nil {
		for _, s := range f.sites {
			if s.ID == *in.SiteID && s.LegalEntityID != w.LegalEntityID {
				return store.Warehouse{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeWarehouseLegalEntityMismatch, "mismatch")
			}
		}
	}
	if in.Name != nil && *in.Name != w.Name {
		w.Name = *in.Name
		w.Version++
	}
	return *w, nil
}

func (f *fakeStore) CreateLocation(_ context.Context, in store.Location, _ string) (store.Location, error) {
	if f.err != nil {
		return store.Location{}, f.err
	}
	var warehouse *store.Warehouse
	for i := range f.warehouses {
		if f.warehouses[i].ID == in.WarehouseID {
			warehouse = &f.warehouses[i]
		}
	}
	if warehouse == nil {
		return store.Location{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeLocationWarehouseInvalid, "warehouse invalid")
	}
	for _, l := range f.locations {
		if l.WarehouseID == in.WarehouseID && l.Code == in.Code {
			return store.Location{}, httpx.NewProblem(http.StatusConflict, store.CodeLocationCodeDuplicate, "duplicate")
		}
	}
	in.LegalEntityID, in.Status, in.Version = warehouse.LegalEntityID, "active", 1
	in.ID = fmt.Sprintf("00000000-0000-0000-0002-%012d", len(f.locations)+1)
	if in.Type == "" {
		in.Type = "storage"
	}
	f.locations = append(f.locations, in)
	return in, nil
}

func (f *fakeStore) GetLocation(_ context.Context, id string) (store.Location, error) {
	f.locationCalls++
	if f.err != nil {
		return store.Location{}, f.err
	}
	for _, l := range f.locations {
		if l.ID == id {
			return l, nil
		}
	}
	return store.Location{}, httpx.NewProblem(http.StatusNotFound, store.CodeLocationNotFound, "location not found")
}

func (f *fakeStore) UpdateLocation(_ context.Context, id string, version int, in store.LocationPatch) (store.Location, error) {
	f.locationUpdateCalls++
	for i := range f.locations {
		l := &f.locations[i]
		if l.ID != id {
			continue
		}
		if l.Version != version {
			return store.Location{}, httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, fmt.Sprintf("current version is %d", l.Version))
		}
		if in.Name != nil && *in.Name != l.Name {
			l.Name = *in.Name
			l.Version++
		}
		return *l, nil
	}
	return store.Location{}, httpx.NewProblem(http.StatusNotFound, store.CodeLocationNotFound, "location not found")
}

func (f *fakeStore) ListLocations(_ context.Context, _ store.LocationFilter, after store.SiteKey, limit int) (store.LocationPage, error) {
	if f.err != nil {
		return store.LocationPage{}, f.err
	}
	page := store.LocationPage{Items: []store.Location{}}
	sorted := slices.Clone(f.locations)
	slices.SortFunc(sorted, func(a, b store.Location) int { return strings.Compare(a.Code+"/"+a.ID, b.Code+"/"+b.ID) })
	for _, l := range sorted {
		if l.Code+"/"+l.ID > after.Code+"/"+after.ID {
			page.Items = append(page.Items, l)
		}
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.Next = &store.SiteKey{Code: page.Items[limit-1].Code, ID: page.Items[limit-1].ID}
	}
	return page, nil
}

func (f *fakeStore) CreateAddress(_ context.Context, in store.Address, _ string) (store.Address, error) {
	if f.err != nil {
		return store.Address{}, f.err
	}
	in.ID = fmt.Sprintf("00000000-0000-0000-0003-%012d", len(f.addresses)+1)
	in.VersionID, in.Formatted, in.ValidFrom = in.ID, "formatted", "2026-01-01"
	f.addresses = append(f.addresses, in)
	return in, nil
}

func (f *fakeStore) GetAddress(_ context.Context, id string) (store.Address, error) {
	f.addressCalls++
	for _, a := range f.addresses {
		if a.ID == id {
			return a, nil
		}
	}
	return store.Address{}, httpx.NewProblem(http.StatusNotFound, store.CodeAddressNotFound, "address not found")
}

func (f *fakeStore) ReviseAddress(_ context.Context, id, versionID string, next store.Address) (store.Address, error) {
	f.reviseCalls++
	if f.err != nil {
		return store.Address{}, f.err
	}
	for i, a := range f.addresses {
		if a.ID != id {
			continue
		}
		if a.VersionID != versionID {
			return store.Address{}, httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, "current version is "+a.VersionID)
		}
		next.ID, next.Formatted, next.ValidFrom = a.ID, "formatted", "2026-02-01"
		next.VersionID = fmt.Sprintf("00000000-0000-0000-0005-%012d", f.reviseCalls)
		f.addresses[i] = next
		return next, nil
	}
	return store.Address{}, httpx.NewProblem(http.StatusNotFound, store.CodeAddressNotFound, "address not found")
}

func (f *fakeStore) GetAddressVersion(_ context.Context, versionID string) (store.Address, error) {
	f.addressCalls++
	for _, a := range f.addresses {
		if a.VersionID == versionID {
			return a, nil
		}
	}
	return store.Address{}, httpx.NewProblem(http.StatusNotFound, store.CodeAddressNotFound, "address not found")
}

func (f *fakeStore) ListAddresses(_ context.Context, filter store.AddressFilter, afterID string, limit int) (store.AddressPage, error) {
	page := store.AddressPage{Items: []store.Address{}}
	for _, a := range f.addresses {
		if a.OwnerType != filter.OwnerType || a.OwnerID != filter.OwnerID || a.ID <= afterID {
			continue
		}
		if len(page.Items) == limit {
			page.NextID = page.Items[limit-1].ID
			break
		}
		page.Items = append(page.Items, a)
	}
	return page, nil
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func newRouter(f *fakeStore) (*chi.Mux, *api.API) {
	a := api.New(f)
	r := httpx.NewRouter()
	a.Register(r)
	return r, a
}

func do(r http.Handler, method, path, body, duties string, withTenant bool, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if withTenant {
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-Duties", duties)
	}
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const (
	legalEntities   = "/v1/org/legal-entities"
	allDuties       = "org.read,org.legal-entity.write"
	referenceDuties = "org.read,org.reference.write"
	validBody       = `{"code":"US01","name":"Passage US","country":"US","currency":"USD","timeZone":"UTC"}`
)

func TestLegalEntities(t *testing.T) {
	tests := []struct {
		name       string
		store      *fakeStore
		method     string
		body       string
		duties     string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"create ok", &fakeStore{}, http.MethodPost, validBody, allDuties, true, http.StatusCreated, `"code":"US01"`},
		{"create without tenant", &fakeStore{}, http.MethodPost, validBody, "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"create malformed body", &fakeStore{}, http.MethodPost, `{`, allDuties, true, http.StatusBadRequest, "COM-0001"},
		{"create unknown field", &fakeStore{}, http.MethodPost, `{"code":"US01","x":1}`, allDuties, true, http.StatusBadRequest, "COM-0001"},
		{"create bad country and currency", &fakeStore{}, http.MethodPost, `{"code":"US01","name":"n","country":"USA","currency":"US"}`, allDuties, true, http.StatusBadRequest, `"currency"`},
		{"create missing time zone", &fakeStore{}, http.MethodPost, `{"code":"US01","name":"n","country":"US","currency":"USD"}`, allDuties, true, http.StatusBadRequest, `"timeZone"`},
		{"create unknown time zone", &fakeStore{}, http.MethodPost, `{"code":"US01","name":"n","country":"US","currency":"USD","timeZone":"Mars/Base"}`, allDuties, true, http.StatusBadRequest, `"timeZone"`},
		{"create local time zone", &fakeStore{}, http.MethodPost, `{"code":"US01","name":"n","country":"US","currency":"USD","timeZone":"Local"}`, allDuties, true, http.StatusBadRequest, `"timeZone"`},
		{"create numeric time zone", &fakeStore{}, http.MethodPost, `{"code":"US01","name":"n","country":"US","currency":"USD","timeZone":5}`, allDuties, true, http.StatusBadRequest, "COM-0001"},
		{"create blank name", &fakeStore{}, http.MethodPost, `{"code":"US01","name":"  ","country":"US","currency":"USD","timeZone":"UTC"}`, allDuties, true, http.StatusBadRequest, `"name"`},
		{"create store error hides detail", &fakeStore{err: errors.New("pg secret detail")}, http.MethodPost, validBody, allDuties, true, http.StatusInternalServerError, "COM-0500"},
		{"create duplicate code", &fakeStore{err: httpx.NewProblem(http.StatusConflict, store.CodeLegalEntityCode, "exists")}, http.MethodPost, validBody, allDuties, true, http.StatusConflict, store.CodeLegalEntityCode},
		{"create without duty", &fakeStore{}, http.MethodPost, validBody, "org.read", true, http.StatusForbidden, "AUTH-0003"},
		{"list empty is array", &fakeStore{}, http.MethodGet, "", allDuties, true, http.StatusOK, `"items":[]`},
		{"list store error", &fakeStore{err: errors.New("boom")}, http.MethodGet, "", allDuties, true, http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, legalEntities, tc.body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if tc.wantStatus == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("internal detail leaked: %s", rec.Body.String())
			}
		})
	}
}

const knownID, unknownID = "0190b6c0-0000-7000-8000-000000000001", "0190b6c0-0000-7000-8000-000000000002"

func seededStore() *fakeStore {
	return &fakeStore{entities: []store.LegalEntity{{ID: knownID, Code: "US01", Name: "Passage US", Version: 1}}}
}

func TestLegalEntityLifecycleRoutes(t *testing.T) {
	byID := legalEntities + "/" + knownID
	tests := []struct {
		name       string
		store      *fakeStore
		method     string
		path       string
		body       string
		duties     string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"get ok", seededStore(), http.MethodGet, byID, "", allDuties, true, http.StatusOK, `"code":"US01"`},
		{"get unknown id", seededStore(), http.MethodGet, legalEntities + "/" + unknownID, "", allDuties, true, http.StatusNotFound, "ORG-LE-NOT-FOUND"},
		{"get without tenant", seededStore(), http.MethodGet, byID, "", "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"patch name", seededStore(), http.MethodPatch, byID, `{"version":1,"name":"Passage USA"}`, allDuties, true, http.StatusOK, `"version":2`},
		{"patch no change keeps version", seededStore(), http.MethodPatch, byID, `{"version":1,"name":"Passage US"}`, allDuties, true, http.StatusOK, `"version":1`},
		{"patch unknown field code", seededStore(), http.MethodPatch, byID, `{"version":1,"code":"X1"}`, allDuties, true, http.StatusBadRequest, "COM-0001"},
		{"patch bad primary address id", seededStore(), http.MethodPatch, byID, `{"version":1,"primaryAddressId":"abc"}`, allDuties, true, http.StatusBadRequest, `"primaryAddressId"`},
		{"patch clearing primary address id accepted", seededStore(), http.MethodPatch, byID, `{"version":1,"primaryAddressId":""}`, allDuties, true, http.StatusOK, `"version":1`},
		{"patch missing version", seededStore(), http.MethodPatch, byID, `{"name":"x"}`, allDuties, true, http.StatusBadRequest, `"version"`},
		{"patch bad status", seededStore(), http.MethodPatch, byID, `{"version":1,"status":"deleted"}`, allDuties, true, http.StatusBadRequest, `"status"`},
		{"patch stale version", seededStore(), http.MethodPatch, byID, `{"version":7,"name":"x"}`, allDuties, true, http.StatusConflict, "ORG-VERSION-CONFLICT"},
		{"patch unknown id", seededStore(), http.MethodPatch, legalEntities + "/" + unknownID, `{"version":1}`, allDuties, true, http.StatusNotFound, "ORG-LE-NOT-FOUND"},
		{"patch without write duty", seededStore(), http.MethodPatch, byID, `{"version":1}`, "org.read", true, http.StatusForbidden, "AUTH-0003"},
		{"patch store error hides detail", &fakeStore{err: errors.New("pg secret detail")}, http.MethodPatch, byID, `{"version":1}`, allDuties, true, http.StatusInternalServerError, "COM-0500"},
		{"get malformed id never reaches the store", &fakeStore{err: errors.New("store reached")}, http.MethodGet, legalEntities + "/nope", "", allDuties, true, http.StatusNotFound, "ORG-LE-NOT-FOUND"},
		{"patch malformed id never reaches the store", &fakeStore{err: errors.New("store reached")}, http.MethodPatch, legalEntities + "/nope", `{"version":1}`, allDuties, true, http.StatusNotFound, "ORG-LE-NOT-FOUND"},
		{"names", seededStore(), http.MethodGet, byID + "/names", "", allDuties, true, http.StatusOK, `"items":[{"name":"Passage US"`},
		{"names unknown id", seededStore(), http.MethodGet, legalEntities + "/" + unknownID + "/names", "", allDuties, true, http.StatusNotFound, "ORG-LE-NOT-FOUND"},
		{"list bad status", &fakeStore{}, http.MethodGet, legalEntities + "?status=bogus", "", allDuties, true, http.StatusBadRequest, "COM-0001"},
		{"create upper-cases the code", &fakeStore{}, http.MethodPost, legalEntities, `{"code":"us01","name":"Passage US","country":"US","currency":"USD","timeZone":"UTC"}`, allDuties, true, http.StatusCreated, `"code":"US01"`},
		{"create defaults the language", &fakeStore{}, http.MethodPost, legalEntities, `{"code":"US01","name":"Passage US","country":"US","currency":"USD","timeZone":"UTC"}`, allDuties, true, http.StatusCreated, `"defaultLanguage":"en-US"`},
		{"create keeps the registration number", &fakeStore{}, http.MethodPost, legalEntities, `{"code":"US01","name":"Passage US","country":"US","currency":"USD","timeZone":"UTC","registrationNumber":"R-1"}`, allDuties, true, http.StatusCreated, `"registrationNumber":"R-1"`},
		{"create reserved code", &fakeStore{}, http.MethodPost, legalEntities, `{"code":"ALL","name":"n","country":"US","currency":"USD","timeZone":"UTC"}`, allDuties, true, http.StatusBadRequest, "ORG-LE-CODE-RESERVED"},
		{"create bad default language", &fakeStore{}, http.MethodPost, legalEntities, `{"code":"US01","name":"n","country":"US","currency":"USD","timeZone":"UTC","defaultLanguage":"english"}`, allDuties, true, http.StatusBadRequest, `"defaultLanguage"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, tc.path, tc.body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if tc.wantStatus == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("internal detail leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestLegalEntityUpdatedCounterCountsOnlyVersionBumps(t *testing.T) {
	r, a := newRouter(seededStore())
	byID := legalEntities + "/" + knownID
	do(r, http.MethodPatch, byID, `{"version":1,"name":"Passage US"}`, allDuties, true)
	do(r, http.MethodPatch, byID, `{"version":9,"name":"Other"}`, allDuties, true)
	do(r, http.MethodPatch, byID, `{"version":1,"name":"Passage USA"}`, allDuties, true)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "org_legal_entities_updated_total 1") {
		t.Fatalf("want exactly one counted update in %s", rec.Body.String())
	}
}

func TestCreateLegalEntity_ReplayReturnsOriginal(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	post := func(body string) *httptest.ResponseRecorder {
		return do(r, http.MethodPost, legalEntities, body, allDuties, true, "Idempotency-Key", "key-1")
	}
	first, replay := post(validBody), post(validBody)
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated || first.Body.String() != replay.Body.String() || len(f.entities) != 1 {
		t.Fatalf("replay must return the original entity: %d %s / %d %s, rows %d", first.Code, first.Body, replay.Code, replay.Body, len(f.entities))
	}
}

func TestLegalEntities_InvalidLegalEntityHeader(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		rec := do(r, method, legalEntities, validBody, allDuties, true, "X-Passage-Legal-Entity", "foo")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
			t.Fatalf("%s: got %d %s; want 400 COM-0001", method, rec.Code, rec.Body.String())
		}
	}
}

func TestConvert(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"kilograms to grams", "from=kg&to=g&qty=2.5", true, http.StatusOK, `"quantity":"2500"`},
		{"offset conversion", "from=C&to=F&qty=100", true, http.StatusOK, `"quantity":"212"`},
		{"without tenant", "from=kg&to=g&qty=1", false, http.StatusUnauthorized, "AUTH-0001"},
		{"missing qty", "from=kg&to=g", true, http.StatusBadRequest, "COM-0001"},
		{"non numeric qty", "from=kg&to=g&qty=abc", true, http.StatusBadRequest, "COM-0001"},
		{"huge exponent qty", "from=kg&to=g&qty=1e999", true, http.StatusBadRequest, "COM-0001"},
		{"missing from", "to=g&qty=1", true, http.StatusBadRequest, "COM-0001"},
		{"unknown unit", "from=kg&to=furlong&qty=1", true, http.StatusUnprocessableEntity, "ORG-UOM-NO-PATH"},
		{"cross class", "from=kg&to=m&qty=1", true, http.StatusUnprocessableEntity, "ORG-UOM-NO-PATH"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{})
			rec := do(r, http.MethodGet, "/v1/org/uom/convert?"+tc.query, "", "", tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestProbes(t *testing.T) {
	down := &fakeStore{pingErr: errors.New("db down")}
	tests := []struct {
		name       string
		store      *fakeStore
		path       string
		wantStatus int
	}{
		{"readyz ok", &fakeStore{}, "/readyz", http.StatusOK},
		{"readyz fails closed when database is down", down, "/readyz", http.StatusServiceUnavailable},
		{"healthz stays up when database is down", down, "/healthz", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, http.MethodGet, tc.path, "", "", false)
			if rec.Code != tc.wantStatus {
				t.Fatalf("got %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusServiceUnavailable && !strings.Contains(rec.Body.String(), `"status":"unavailable"`) {
				t.Fatalf("unexpected body %s", rec.Body)
			}
		})
	}
}

func TestMetricsCountBusinessOperations(t *testing.T) {
	f := &fakeStore{}
	r, a := newRouter(f)
	do(r, http.MethodPost, legalEntities, validBody, allDuties, true)
	f.err = errors.New("boom")
	do(r, http.MethodPost, legalEntities, validBody, allDuties, true)

	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{
		"# TYPE org_legal_entities_created_total counter",
		"org_legal_entities_created_total 1",
		"org_legal_entities_failed_total 1",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics missing %q in %s", want, rec.Body.String())
		}
	}
}

func TestOpenAPIDocumentsEveryRoute(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := newRouter(&fakeStore{})
	_ = chi.Walk(r, func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.Contains(string(spec), "\n  "+route+":") {
			t.Errorf("openapi.yaml does not document %s", route)
		}
		return nil
	})
}

func TestAsyncAPIDeclaresEveryEvent(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`x-passage-type: (\S+)`).FindAllStringSubmatch(string(spec), -1) {
		declared[m[1]] = true
	}
	for _, typ := range orgevents.All() {
		if !pevents.ValidType(typ) || !declared[typ] {
			t.Errorf("%s must be valid and declared in asyncapi.yaml", typ)
		}
		delete(declared, typ)
	}
	for typ := range declared {
		t.Errorf("asyncapi.yaml declares %s which events.All() does not list", typ)
	}
}

func TestScopedCallerCannotCreateLegalEntity(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	rec := do(r, http.MethodPost, legalEntities, validBody, allDuties, true, "X-Passage-Legal-Entity", testkit.TenantID(2))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
		t.Fatalf("got %d %s; want 403 AUTH-0003", rec.Code, rec.Body.String())
	}
}

func TestListLegalEntitiesPagesByCursor(t *testing.T) {
	f := &fakeStore{entities: []store.LegalEntity{{Code: "A1"}, {Code: "B2"}, {Code: "C3"}}}
	r, _ := newRouter(f)
	first := do(r, http.MethodGet, legalEntities+"?limit=2", "", allDuties, true)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"B2"`) || strings.Contains(first.Body.String(), `"C3"`) || !strings.Contains(first.Body.String(), `"nextCursor":"QjI"`) {
		t.Fatalf("first page = %d %s", first.Code, first.Body.String())
	}
	second := do(r, http.MethodGet, legalEntities+"?limit=2&cursor=QjI", "", allDuties, true)
	if !strings.Contains(second.Body.String(), `"C3"`) || strings.Contains(second.Body.String(), "nextCursor") {
		t.Fatalf("second page = %d %s", second.Code, second.Body.String())
	}
	for _, bad := range []string{"?limit=0", "?limit=101", "?limit=x", "?cursor=!!"} {
		if rec := do(r, http.MethodGet, legalEntities+bad, "", allDuties, true); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d; want 400", bad, rec.Code)
		}
	}
}

func TestReference(t *testing.T) {
	tests := []struct {
		name       string
		store      *fakeStore
		method     string
		path       string
		body       string
		duties     string
		withTenant bool
		headers    []string
		wantStatus int
		wantBody   string
	}{
		{"list countries", &fakeStore{}, http.MethodGet, "/v1/org/countries", "", referenceDuties, true, nil, http.StatusOK, `"items":[{"code":"US"`},
		{"list currencies", &fakeStore{}, http.MethodGet, "/v1/org/currencies?status=active", "", referenceDuties, true, nil, http.StatusOK, `"numericCode":"840"`},
		{"list regions by country", &fakeStore{}, http.MethodGet, "/v1/org/regions?country=US", "", referenceDuties, true, nil, http.StatusOK, `"countryCode":"US"`},
		{"list regions with no match is empty array", &fakeStore{}, http.MethodGet, "/v1/org/regions?country=ZZ", "", referenceDuties, true, nil, http.StatusOK, `"items":[]`},
		{"list languages", &fakeStore{}, http.MethodGet, "/v1/org/languages", "", referenceDuties, true, nil, http.StatusOK, `"code":"en"`},
		{"list bad status", &fakeStore{}, http.MethodGet, "/v1/org/currencies?status=bogus", "", referenceDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"list bad country shape", &fakeStore{}, http.MethodGet, "/v1/org/regions?country=usa", "", referenceDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"list country on non-region collection", &fakeStore{}, http.MethodGet, "/v1/org/currencies?country=US", "", referenceDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"list without tenant", &fakeStore{}, http.MethodGet, "/v1/org/countries", "", "", false, nil, http.StatusUnauthorized, "AUTH-0001"},
		{"list without duty", &fakeStore{}, http.MethodGet, "/v1/org/countries", "", "", true, nil, http.StatusForbidden, "AUTH-0003"},
		{"list store error hides detail", &fakeStore{err: errors.New("pg secret detail")}, http.MethodGet, "/v1/org/countries", "", referenceDuties, true, nil, http.StatusInternalServerError, "COM-0500"},
		{"get JPY has zero minor units", &fakeStore{}, http.MethodGet, "/v1/org/currencies/JPY", "", referenceDuties, true, nil, http.StatusOK, `"minorUnits":0`},
		{"get unknown code", &fakeStore{}, http.MethodGet, "/v1/org/currencies/ZZZ", "", referenceDuties, true, nil, http.StatusNotFound, store.CodeReferenceNotFound},
		{"get without tenant", &fakeStore{}, http.MethodGet, "/v1/org/currencies/USD", "", "", false, nil, http.StatusUnauthorized, "AUTH-0001"},
		{"get store error hides detail", &fakeStore{err: errors.New("pg secret detail")}, http.MethodGet, "/v1/org/currencies/USD", "", referenceDuties, true, nil, http.StatusInternalServerError, "COM-0500"},
		{"patch deactivates", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"inactive"}`, referenceDuties, true, nil, http.StatusOK, `"status":"inactive"`},
		{"patch repeat is idempotent", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"active"}`, referenceDuties, true, nil, http.StatusOK, `"status":"active"`},
		{"patch invalid status", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"deleted"}`, referenceDuties, true, nil, http.StatusBadRequest, `"status"`},
		{"patch empty object", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{}`, referenceDuties, true, nil, http.StatusBadRequest, `"status"`},
		{"patch wrong type", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":5}`, referenceDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"patch unknown field", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"active","x":1}`, referenceDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"patch malformed", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{`, referenceDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"patch without write duty", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"inactive"}`, "org.read", true, nil, http.StatusForbidden, "AUTH-0003"},
		{"patch legal-entity scoped", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"inactive"}`, referenceDuties, true, []string{"X-Passage-Legal-Entity", testkit.TenantID(2)}, http.StatusForbidden, "AUTH-0003"},
		{"patch without tenant", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"inactive"}`, "", false, nil, http.StatusUnauthorized, "AUTH-0001"},
		{"patch unknown code", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/ZZZ", `{"status":"inactive"}`, referenceDuties, true, nil, http.StatusNotFound, store.CodeReferenceNotFound},
		{"patch store error hides detail", &fakeStore{err: errors.New("pg secret detail")}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"inactive"}`, referenceDuties, true, nil, http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, tc.path, tc.body, tc.duties, tc.withTenant, tc.headers...)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if tc.wantStatus == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("internal detail leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestReference_PatchCountsOnlySuccessfulChanges(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	do(r, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"inactive"}`, referenceDuties, true)
	do(r, http.MethodPatch, "/v1/org/currencies/ZZZ", `{"status":"inactive"}`, referenceDuties, true)
	do(r, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"deleted"}`, referenceDuties, true)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"# TYPE org_reference_status_changed_total counter", "org_reference_status_changed_total 1"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics missing %q in %s", want, rec.Body.String())
		}
	}
}

func TestReference_DeactivatedRowIsFilteredByStatus(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	do(r, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"inactive"}`, referenceDuties, true)
	rec := do(r, http.MethodGet, "/v1/org/currencies?status=inactive", "", referenceDuties, true)
	if !strings.Contains(rec.Body.String(), `"code":"USD"`) || strings.Contains(rec.Body.String(), `"code":"JPY"`) {
		t.Fatalf("inactive list = %s", rec.Body.String())
	}
}

func (f *fakeStore) CreateHierarchyType(_ context.Context, in store.HierarchyType, _ string) (store.HierarchyType, error) {
	if f.err != nil {
		return store.HierarchyType{}, f.err
	}
	in.ID, in.Status, in.Version = hierarchyTypeID, "active", 1
	return in, nil
}

func (f *fakeStore) CreateHierarchyDraft(_ context.Context, typeID, _ string) (store.HierarchyVersion, error) {
	if f.err != nil {
		return store.HierarchyVersion{}, f.err
	}
	return store.HierarchyVersion{ID: hierarchyVersionID, HierarchyTypeID: typeID, VersionNo: 1, Status: "draft", Version: 1, Nodes: []store.HierarchyNode{}}, nil
}

func (f *fakeStore) ReplaceHierarchyNodes(_ context.Context, id string, version int, nodes []store.HierarchyNode) (store.HierarchyVersion, error) {
	if f.err != nil {
		return store.HierarchyVersion{}, f.err
	}
	f.hierarchyNodes = nodes
	return store.HierarchyVersion{ID: id, VersionNo: 1, Status: "draft", NodeCount: len(nodes), Version: version + 1, Nodes: nodes}, nil
}

func (f *fakeStore) PublishHierarchyVersion(_ context.Context, id string, version int, effectiveFrom time.Time, reason, _, publishedBy string, backdated bool) (store.HierarchyVersion, error) {
	f.publishCalls, f.publishBackdated, f.publishedBy = f.publishCalls+1, backdated, publishedBy
	if f.err != nil {
		return store.HierarchyVersion{}, f.err
	}
	from := effectiveFrom.Format("2006-01-02")
	return store.HierarchyVersion{ID: id, VersionNo: 1, Status: "published", EffectiveFrom: &from, Version: version + 1, PublishedBy: &publishedBy, PublishReason: &reason, Nodes: []store.HierarchyNode{}}, nil
}

func (f *fakeStore) HierarchyAsOf(_ context.Context, typeID string, date time.Time, orgUnitID string) (store.HierarchyAsOf, error) {
	if f.err != nil {
		return store.HierarchyAsOf{}, f.err
	}
	return store.HierarchyAsOf{HierarchyTypeID: typeID, Date: date.Format("2006-01-02"), OrgUnitID: orgUnitID, Ancestors: []store.AsOfRelative{}, Descendants: []store.AsOfRelative{}}, nil
}
