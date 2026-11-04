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
	"github.com/shopspring/decimal"

	pevents "github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/api"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
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
	ouFilter            store.OperatingUnitFilter
	compareArgs         [2]int
	ouCalls             int
	sites               []store.Site
	siteCalls           int
	replayCreates       bool
	warehouses          []store.Warehouse
	warehouseCalls      int
	locations           []store.Location
	locationCalls       int
	locationUpdateCalls int
	addresses           []store.Address
	addressCalls        int
	addressFilter       store.AddressFilter
	asOfArg             string
	reviseCalls         int
	calendars           []store.FiscalCalendar
	generatedYears      map[string]bool
	generateCalls       int
	updateCalls         int
	generateErr         error
	generatedID         string
	hierarchyNodes      []store.HierarchyNode
	hierarchyTypes      []store.HierarchyType
	typeFilter          store.HierarchyTypeFilter
	typeAfter           string
	deleteCalls         int
	publishCalls        int
	publishBackdated    bool
	publishedBy         string
	draftEditor         string
	replaceEditor       string
	hierarchyVersions   []store.HierarchyVersionSummary
	versionFilter       store.HierarchyVersionFilter
	versionAfter        int
	roundingPut         store.CurrencyRounding
	roundingPutCalls    int
	roundingChanged     bool
	translations        map[string]store.NameTranslation
	roundings           []store.CurrencyRounding
	resolveArgs         [3]string
	resolved            store.RoundingRule
	units               []store.Unit
	unitCreated         store.Unit
	unitCreateKey       string
	unitStatusCalls     []string
	conversions         []store.Conversion
	conversionKeys      map[string]store.Conversion
	conversionCreates   int
	roundingDeleteArgs  [3]string
	roundingDeleteCalls int
	teamTypes           []store.TeamType
	teamTypeKeys        map[string]store.TeamType
	teamTypeFilter      store.TeamTypeFilter
	teamTypeAfter       string
	teams               []store.Team
	teamKeys            map[string]store.Team
	teamFilter          store.TeamFilter
	teamMembers         []store.TeamMember
	teamMemberKeys      map[string]store.TeamMember
	teamMemberToday     time.Time
	teamMemberFilter    store.TeamMemberFilter
}

func (f *fakeStore) seedUnits() {
	if f.units != nil {
		return
	}
	f.units = []store.Unit{
		{Code: "C", Name: "Celsius", Class: "temperature", DecimalPrecision: 2, System: true, Status: "active"},
		{Code: "F", Name: "Fahrenheit", Class: "temperature", DecimalPrecision: 2, System: true, Status: "active"},
		{Code: "K", Name: "Kelvin", Class: "temperature", DecimalPrecision: 2, System: true, Status: "active"},
		{Code: "g", Name: "Gram", Class: "mass", DecimalPrecision: 0, System: true, Status: "active"},
		{Code: "kg", Name: "Kilogram", Class: "mass", DecimalPrecision: 3, System: true, Status: "active"},
		{Code: "lb", Name: "Pound", Class: "mass", DecimalPrecision: 3, System: true, Status: "active"},
	}
}

func (f *fakeStore) unit(code string) (*store.Unit, error) {
	f.seedUnits()
	for i := range f.units {
		if f.units[i].Code == code {
			return &f.units[i], nil
		}
	}
	return nil, httpx.NewProblem(http.StatusNotFound, store.CodeUnitNotFound, "no unit with code "+code)
}

func (f *fakeStore) ListUnits(_ context.Context, filter store.UnitFilter) ([]store.Unit, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.seedUnits()
	items := []store.Unit{}
	for _, u := range f.units {
		if (filter.Class == "" || u.Class == filter.Class) && (filter.Status == "" || u.Status == filter.Status) {
			items = append(items, u)
		}
	}
	return items, nil
}

func (f *fakeStore) GetUnit(_ context.Context, code string) (store.Unit, error) {
	if f.err != nil {
		return store.Unit{}, f.err
	}
	u, err := f.unit(code)
	if err != nil {
		return store.Unit{}, err
	}
	return *u, nil
}

func (f *fakeStore) CreateUnit(_ context.Context, in store.Unit, key string) (store.Unit, error) {
	if f.err != nil {
		return store.Unit{}, f.err
	}
	if _, err := f.unit(in.Code); err == nil {
		return store.Unit{}, httpx.NewProblem(http.StatusConflict, store.CodeUnitCodeDuplicate, "duplicate")
	}
	in.System, in.Status = false, "active"
	f.unitCreated, f.unitCreateKey = in, key
	f.units = append(f.units, in)
	return in, nil
}

func (f *fakeStore) SetUnitStatus(_ context.Context, code, status string) (store.Unit, error) {
	u, err := f.unit(code)
	if err != nil {
		return store.Unit{}, err
	}
	u.Status = status
	f.unitStatusCalls = append(f.unitStatusCalls, code+"="+status)
	return *u, nil
}

func (f *fakeStore) ConversionGraph(context.Context) ([]domain.Conversion, map[string]int32, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	f.seedUnits()
	precision := map[string]int32{}
	for _, u := range f.units {
		if u.Status == "active" {
			precision[u.Code] = int32(u.DecimalPrecision)
		}
	}
	var convs []domain.Conversion
	for _, c := range f.allConversions() {
		_, from := precision[c.From]
		_, to := precision[c.To]
		if from && to {
			convs = append(convs, c)
		}
	}
	return convs, precision, nil
}

func (f *fakeStore) allConversions() []domain.Conversion {
	convs := domain.StandardConversions()
	for _, c := range f.conversions {
		convs = append(convs, domain.Conversion{From: c.FromCode, To: c.ToCode, Class: c.Class, Factor: c.Factor, Offset: c.Offset})
	}
	return convs
}

func (f *fakeStore) ListConversions(_ context.Context, filter store.ConversionFilter) ([]store.Conversion, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.seedUnits()
	items := []store.Conversion{}
	for _, c := range domain.StandardConversions() {
		items = append(items, store.Conversion{ID: "sys-" + c.From + "-" + c.To, FromCode: c.From, ToCode: c.To, Class: c.Class, Factor: c.Factor, Offset: c.Offset, System: true})
	}
	items = append(items, f.conversions...)
	out := []store.Conversion{}
	for _, c := range items {
		from, errFrom := f.unit(c.FromCode)
		to, errTo := f.unit(c.ToCode)
		if errFrom != nil || errTo != nil {
			continue
		}
		c.Status = "inactive"
		if from.Status == "active" && to.Status == "active" {
			c.Status = "active"
		}
		if (filter.Class == "" || c.Class == filter.Class) && (filter.Unit == "" || c.FromCode == filter.Unit || c.ToCode == filter.Unit) && (filter.Status == "" || c.Status == filter.Status) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeStore) CreateConversion(_ context.Context, in store.Conversion, key string) (store.Conversion, bool, error) {
	if f.err != nil {
		return store.Conversion{}, false, f.err
	}
	if replay, ok := f.conversionKeys[key]; ok && key != "" {
		if replay.FromCode != in.FromCode || replay.ToCode != in.ToCode || !replay.Factor.Equal(in.Factor) || !replay.Offset.Equal(in.Offset) {
			return store.Conversion{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reuse")
		}
		return replay, false, nil
	}
	from, err := f.unit(in.FromCode)
	if err != nil {
		return store.Conversion{}, false, err
	}
	to, err := f.unit(in.ToCode)
	if err != nil {
		return store.Conversion{}, false, err
	}
	switch {
	case from.Status != "active" || to.Status != "active":
		return store.Conversion{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeUnitInactive, "inactive")
	case from.Class != to.Class:
		return store.Conversion{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeClassMismatch, "class")
	}
	for _, c := range f.allConversions() {
		if (c.From == in.FromCode && c.To == in.ToCode) || (c.From == in.ToCode && c.To == in.FromCode) {
			return store.Conversion{}, false, httpx.NewProblem(http.StatusConflict, store.CodeConversionDuplicate, "duplicate")
		}
	}
	in.ID, in.Class, in.Status = fmt.Sprintf("conv-%d", len(f.conversions)+1), from.Class, "active"
	f.conversions = append(f.conversions, in)
	f.conversionCreates++
	if key != "" {
		if f.conversionKeys == nil {
			f.conversionKeys = map[string]store.Conversion{}
		}
		f.conversionKeys[key] = in
	}
	return in, true, nil
}

func (f *fakeStore) DeleteCurrencyRounding(_ context.Context, currency, purpose string, legalEntityID *string) error {
	f.roundingDeleteCalls++
	f.roundingDeleteArgs = [3]string{currency, purpose}
	if legalEntityID != nil {
		f.roundingDeleteArgs[2] = *legalEntityID
	}
	return f.err
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
	if in.FiscalCalendarID != nil && *in.FiscalCalendarID != "" {
		e.FiscalCalendarID = in.FiscalCalendarID
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

func (f *fakeStore) ListOperatingUnits(_ context.Context, filter store.OperatingUnitFilter, after store.OperatingUnitKey, limit int) (store.OperatingUnitPage, error) {
	f.ouFilter = filter
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
			if f.replayCreates {
				s.Replayed = true
				return s, nil
			}
			return store.Site{}, httpx.NewProblem(http.StatusConflict, store.CodeSiteCodeDuplicate, "duplicate")
		}
	}
	in.ID, in.Status, in.Version = fmt.Sprintf("00000000-0000-0000-0000-%012d", len(f.sites)+1), "active", 1
	if in.DefaultWarehouse != nil {
		for _, w := range f.warehouses {
			if w.LegalEntityID == in.LegalEntityID && w.Code == in.DefaultWarehouse.Code {
				return store.Site{}, httpx.NewProblem(http.StatusConflict, store.CodeWarehouseCodeDuplicate, "duplicate")
			}
		}
		created := *in.DefaultWarehouse
		created.ID, created.LegalEntityID, created.SiteID, created.Status, created.Version = fmt.Sprintf("00000000-0000-0000-0001-%012d", len(f.warehouses)+1), in.LegalEntityID, in.ID, "active", 1
		f.warehouses = append(f.warehouses, created)
		in.DefaultWarehouseID = &created.ID
	}
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
	if in.DefaultWarehouseID != nil {
		if *in.DefaultWarehouseID == "" {
			s.DefaultWarehouseID = nil
		} else if !f.activeWarehouseOf(id, *in.DefaultWarehouseID) {
			return store.Site{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeSiteDefaultWarehouseInvalid, "invalid")
		} else {
			s.DefaultWarehouseID = in.DefaultWarehouseID
		}
		s.Version++
	}
	if in.Name != nil && *in.Name != s.Name {
		s.Name = *in.Name
		s.Version++
	}
	return *s, nil
}

func (f *fakeStore) activeWarehouseOf(siteID, warehouseID string) bool {
	for _, w := range f.warehouses {
		if w.ID == warehouseID && w.SiteID == siteID && w.Status == "active" {
			return true
		}
	}
	return false
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
	for _, s := range f.sites {
		if s.DefaultWarehouseID != nil && *s.DefaultWarehouseID == id && ((in.Status != nil && *in.Status == "inactive") || (in.SiteID != nil && *in.SiteID != w.SiteID)) {
			return store.Warehouse{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeWarehouseIsDefault, "is default")
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

func (f *fakeStore) GetAddressAsOf(_ context.Context, id, asOf string) (store.Address, error) {
	f.addressCalls++
	f.asOfArg = asOf
	for _, a := range f.addresses {
		if a.ID == id && asOf >= a.ValidFrom {
			return a, nil
		}
	}
	return store.Address{}, httpx.NewProblem(http.StatusNotFound, store.CodeAddressNotFound, "address not found")
}

func (f *fakeStore) ListAddressVersions(_ context.Context, id string) ([]store.AddressVersion, error) {
	f.addressCalls++
	if f.err != nil {
		return nil, f.err
	}
	for _, a := range f.addresses {
		if a.ID == id {
			return []store.AddressVersion{{VersionID: a.VersionID, ValidFrom: a.ValidFrom}}, nil
		}
	}
	return nil, httpx.NewProblem(http.StatusNotFound, store.CodeAddressNotFound, "address not found")
}

func (f *fakeStore) ListAddresses(_ context.Context, filter store.AddressFilter, afterID string, limit int) (store.AddressPage, error) {
	f.addressFilter = filter
	page := store.AddressPage{Items: []store.Address{}}
	for _, a := range f.addresses {
		if filter.OwnerType != "" && (a.OwnerType != filter.OwnerType || a.OwnerID != filter.OwnerID) || a.ID <= afterID {
			continue
		}
		if filter.Query != "" && !strings.Contains(strings.ToLower(a.City+" "+a.Street1), strings.ToLower(filter.Query)) {
			continue
		}
		if filter.CountryCode != "" && a.CountryCode != filter.CountryCode {
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
	return doWithoutTenantAdmin(r, method, path, body, duties+",tenant.administer", withTenant, headers...)
}

func doWithoutTenantAdmin(r http.Handler, method, path, body, duties string, withTenant bool, headers ...string) *httptest.ResponseRecorder {
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
		{"patch bad fiscal calendar id", seededStore(), http.MethodPatch, byID, `{"version":1,"fiscalCalendarId":"abc"}`, allDuties, true, http.StatusBadRequest, `"fiscalCalendarId"`},
		{"patch numeric fiscal calendar id", seededStore(), http.MethodPatch, byID, `{"version":1,"fiscalCalendarId":7}`, allDuties, true, http.StatusBadRequest, "COM-0001"},
		{"patch fiscal calendar id assigned", seededStore(), http.MethodPatch, byID, `{"version":1,"fiscalCalendarId":"0190b6c0-0000-7000-8000-0000000000c1"}`, allDuties, true, http.StatusOK, `"fiscalCalendarId":"0190b6c0-0000-7000-8000-0000000000c1"`},
		{"patch clearing fiscal calendar id accepted", seededStore(), http.MethodPatch, byID, `{"version":1,"fiscalCalendarId":""}`, allDuties, true, http.StatusOK, `"version":1`},
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
		{"display rounds to the target precision", "from=lb&to=kg&qty=10", true, http.StatusOK, `"quantity":"4.5359237","displayQuantity":"4.536"`},
		{"display rounds to whole grams", "from=kg&to=g&qty=2.5004", true, http.StatusOK, `"displayQuantity":"2500"`},
		{"offset conversion", "from=C&to=F&qty=100", true, http.StatusOK, `"quantity":"212"`},
		{"without tenant", "from=kg&to=g&qty=1", false, http.StatusUnauthorized, "AUTH-0001"},
		{"missing qty", "from=kg&to=g", true, http.StatusBadRequest, "COM-0001"},
		{"non numeric qty", "from=kg&to=g&qty=abc", true, http.StatusBadRequest, "COM-0001"},
		{"huge exponent qty", "from=kg&to=g&qty=1e999", true, http.StatusBadRequest, "COM-0001"},
		{"missing from", "to=g&qty=1", true, http.StatusBadRequest, "COM-0001"},
		{"unknown unit", "from=kg&to=furlong&qty=1", true, http.StatusUnprocessableEntity, "ORG-UOM-NO-PATH"},
		{"cross class", "from=kg&to=m&qty=1", true, http.StatusUnprocessableEntity, "ORG-UOM-NO-PATH"},
		{"cross class between known units", "from=kg&to=C&qty=1", true, http.StatusUnprocessableEntity, "ORG-UOM-NO-PATH"},
		{"case mismatch", "from=KG&to=g&qty=1", true, http.StatusUnprocessableEntity, "ORG-UOM-NO-PATH"},
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

func TestConvert_StoreErrorIs500(t *testing.T) {
	r, _ := newRouter(&fakeStore{err: errors.New("db down")})
	if rec := do(r, http.MethodGet, "/v1/org/uom/convert?from=kg&to=g&qty=1", "", "", true); rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d %s; want 500", rec.Code, rec.Body.String())
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
		"# TYPE org_team_types_created_total counter",
		"org_team_types_created_total 0",
		"# TYPE org_teams_created_total counter",
		"org_teams_created_total 0",
		"# TYPE org_team_members_added_total counter",
		"org_team_members_added_total 0",
		"# TYPE org_teams_updated_total counter",
		"org_teams_updated_total 0",
		"# TYPE org_team_members_ended_total counter",
		"org_team_members_ended_total 0",
		"# TYPE org_team_members_removed_total counter",
		"org_team_members_removed_total 0",
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

var specOperation = regexp.MustCompile(`^    (get|put|post|patch|delete|head|options):$`)

func declaredOperations(spec string) map[string]bool {
	declared := map[string]bool{}
	inPaths, path, operation := false, "", ""
	for _, line := range strings.Split(spec, "\n") {
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line != "" && !strings.HasPrefix(line, " "):
			inPaths = false
		case !inPaths:
		case strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":") && !strings.HasPrefix(line, "   "):
			path, operation = strings.TrimSuffix(strings.TrimSpace(line), ":"), ""
		case specOperation.MatchString(line):
			operation = path + " " + specOperation.FindStringSubmatch(line)[1]
			declared[operation] = false
		case operation != "" && strings.HasPrefix(line, "      x-passage-duty: "):
			declared[operation] = true
		}
	}
	return declared
}

func parityProblems(served map[string]bool, spec string) []string {
	declared := declaredOperations(spec)
	var problems []string
	for operation := range served {
		if hasDuty, ok := declared[operation]; !ok {
			problems = append(problems, "not documented: "+operation)
		} else if !hasDuty {
			problems = append(problems, "no x-passage-duty: "+operation)
		}
	}
	for operation := range declared {
		if !served[operation] {
			problems = append(problems, "not served: "+operation)
		}
	}
	slices.Sort(problems)
	return problems
}

func TestOpenAPIDocumentsEveryOperation(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := newRouter(&fakeStore{})
	served := map[string]bool{}
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served[route+" "+strings.ToLower(method)] = true
		return nil
	})
	if len(served) < 40 {
		t.Fatalf("router walk yielded %d operations; the walk is not seeing the routes", len(served))
	}
	for _, problem := range parityProblems(served, string(spec)) {
		t.Error(problem)
	}

	t.Run("detects drift in both directions", func(t *testing.T) {
		const small = "openapi: 3.1.0\npaths:\n  /a:\n    get:\n      x-passage-duty: org.read\n    put:\n      operationId: noDuty\n  /b:\n    post:\n      x-passage-duty: org.read\ncomponents:\n  schemas: {}\n"
		got := parityProblems(map[string]bool{"/a get": true, "/a put": true, "/c get": true}, small)
		want := []string{"no x-passage-duty: /a put", "not documented: /c get", "not served: /b post"}
		if !slices.Equal(got, want) {
			t.Fatalf("problems = %v; want %v", got, want)
		}
		if extra := parityProblems(map[string]bool{"/a get": true, "/a put": true, "/b post": true}, strings.Replace(small, "noDuty", "noDuty\n      x-passage-duty: org.read", 1)); len(extra) != 0 {
			t.Fatalf("a matching spec must have no problems: %v", extra)
		}
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

func (f *fakeStore) HierarchyVersionDraftKeys(context.Context, string) (map[string]string, error) {
	return map[string]string{}, f.err
}

func (f *fakeStore) TreeLimits() domain.TreeLimits { return domain.DefaultTreeLimits }

func (f *fakeStore) CreateHierarchyDraft(_ context.Context, typeID, _, editedBy string) (store.HierarchyVersion, error) {
	f.draftEditor = editedBy
	if f.err != nil {
		return store.HierarchyVersion{}, f.err
	}
	return store.HierarchyVersion{ID: hierarchyVersionID, HierarchyTypeID: typeID, VersionNo: 1, Status: "draft", Version: 1, Nodes: []store.HierarchyNode{}}, nil
}

func (f *fakeStore) ReplaceHierarchyNodes(_ context.Context, id string, version int, nodes []store.HierarchyNode, editedBy string) (store.HierarchyVersion, error) {
	f.replaceEditor = editedBy
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

func (f *fakeStore) CompareHierarchyVersions(_ context.Context, typeID string, fromNo, toNo int) (store.HierarchyComparison, error) {
	f.compareArgs = [2]int{fromNo, toNo}
	if f.err != nil {
		return store.HierarchyComparison{}, f.err
	}
	return store.HierarchyComparison{HierarchyTypeID: typeID, FromVersionNo: fromNo, ToVersionNo: toNo, Added: []store.HierarchyChange{}, Removed: []store.HierarchyChange{}, Moved: []store.HierarchyMove{}}, nil
}

func (f *fakeStore) GetHierarchyType(_ context.Context, id string) (store.HierarchyType, error) {
	if f.err != nil {
		return store.HierarchyType{}, f.err
	}
	return store.HierarchyType{ID: id, Code: "REP", Name: "Reporting", Purposes: []string{"reporting"}, Status: "active", Version: 1}, nil
}

func (f *fakeStore) ListHierarchyTypes(_ context.Context, filter store.HierarchyTypeFilter, afterCode string, limit int) (store.HierarchyTypePage, error) {
	f.typeFilter, f.typeAfter = filter, afterCode
	if f.err != nil {
		return store.HierarchyTypePage{}, f.err
	}
	page := store.HierarchyTypePage{Items: append([]store.HierarchyType{}, f.hierarchyTypes...)}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCode = page.Items[limit-1].Code
	}
	return page, nil
}

func (f *fakeStore) ListHierarchyVersions(_ context.Context, _ string, filter store.HierarchyVersionFilter, after, limit int) (store.HierarchyVersionPage, error) {
	f.versionFilter, f.versionAfter = filter, after
	if f.err != nil {
		return store.HierarchyVersionPage{}, f.err
	}
	page := store.HierarchyVersionPage{Items: append([]store.HierarchyVersionSummary{}, f.hierarchyVersions...)}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextVersionNo = page.Items[limit-1].VersionNo
	}
	return page, nil
}

func (f *fakeStore) GetHierarchyVersion(_ context.Context, id string) (store.HierarchyVersion, error) {
	if f.err != nil {
		return store.HierarchyVersion{}, f.err
	}
	return store.HierarchyVersion{ID: id, VersionNo: 1, Status: "draft", Version: 1, Nodes: []store.HierarchyNode{}}, nil
}

func (f *fakeStore) DeleteHierarchyDraft(context.Context, string, int) error {
	f.deleteCalls++
	return f.err
}

func (f *fakeStore) PutCurrencyRounding(_ context.Context, in store.CurrencyRounding) (store.CurrencyRounding, bool, error) {
	if f.err != nil {
		return store.CurrencyRounding{}, false, f.err
	}
	f.roundingPutCalls++
	f.roundingPut = in
	in.ID = "id-1"
	return in, f.roundingChanged, nil
}

func (f *fakeStore) ListCurrencyRoundings(context.Context, string) ([]store.CurrencyRounding, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.roundings == nil {
		return []store.CurrencyRounding{}, nil
	}
	return f.roundings, nil
}

func (f *fakeStore) ResolveCurrencyRounding(_ context.Context, currency, purpose, legalEntityID string) (store.RoundingRule, error) {
	if f.err != nil {
		return store.RoundingRule{}, f.err
	}
	f.resolveArgs = [3]string{currency, purpose, legalEntityID}
	if f.resolved.Unit.IsZero() {
		return store.RoundingRule{Unit: decimal.New(1, -2), Method: domain.HalfEven, Source: store.RoundingSourceDefault}, nil
	}
	return f.resolved, nil
}

func (f *fakeStore) CreateTeamType(_ context.Context, in store.TeamType, key string) (store.TeamType, bool, error) {
	if f.err != nil {
		return store.TeamType{}, false, f.err
	}
	if replay, ok := f.teamTypeKeys[key]; ok && key != "" {
		if replay.Code != in.Code || replay.Name != in.Name || !slices.Equal(replay.MemberKinds, in.MemberKinds) {
			return store.TeamType{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reuse")
		}
		return replay, false, nil
	}
	for _, t := range f.teamTypes {
		if t.Code == in.Code {
			return store.TeamType{}, false, httpx.NewProblem(http.StatusConflict, store.CodeTeamTypeCodeDuplicate, "duplicate")
		}
	}
	in.ID = fmt.Sprintf("00000000-0000-0000-0000-%012d", len(f.teamTypes)+1)
	f.teamTypes = append(f.teamTypes, in)
	if key != "" {
		if f.teamTypeKeys == nil {
			f.teamTypeKeys = map[string]store.TeamType{}
		}
		f.teamTypeKeys[key] = in
	}
	return in, true, nil
}

func (f *fakeStore) GetTeamType(_ context.Context, id string) (store.TeamType, error) {
	if f.err != nil {
		return store.TeamType{}, f.err
	}
	for _, t := range f.teamTypes {
		if t.ID == id {
			return t, nil
		}
	}
	return store.TeamType{}, httpx.NewProblem(http.StatusNotFound, store.CodeTeamTypeNotFound, "team type not found")
}

func (f *fakeStore) ListTeamTypes(_ context.Context, filter store.TeamTypeFilter, afterCode string, limit int) (store.TeamTypePage, error) {
	f.teamTypeFilter, f.teamTypeAfter = filter, afterCode
	if f.err != nil {
		return store.TeamTypePage{}, f.err
	}
	page := store.TeamTypePage{Items: []store.TeamType{}}
	for _, t := range f.teamTypes {
		if t.Code > afterCode && (filter.MemberKind == "" || slices.Contains(t.MemberKinds, filter.MemberKind)) {
			page.Items = append(page.Items, t)
		}
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCode = page.Items[limit-1].Code
	}
	return page, nil
}

func (f *fakeStore) CreateTeam(_ context.Context, in store.Team, key string) (store.Team, bool, error) {
	if f.err != nil {
		return store.Team{}, false, f.err
	}
	if replay, ok := f.teamKeys[key]; ok && key != "" {
		if replay.Code != in.Code || replay.Name != in.Name {
			return store.Team{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reuse")
		}
		return replay, false, nil
	}
	for _, t := range f.teams {
		if t.Code == in.Code {
			return store.Team{}, false, httpx.NewProblem(http.StatusConflict, store.CodeTeamCodeDuplicate, "duplicate")
		}
	}
	in.ID = fmt.Sprintf("00000000-0000-0000-0000-%012d", len(f.teams)+1)
	in.Status, in.Version = "active", 1
	f.teams = append(f.teams, in)
	if key != "" {
		if f.teamKeys == nil {
			f.teamKeys = map[string]store.Team{}
		}
		f.teamKeys[key] = in
	}
	return in, true, nil
}

func (f *fakeStore) GetTeam(_ context.Context, id string) (store.Team, error) {
	if f.err != nil {
		return store.Team{}, f.err
	}
	for _, t := range f.teams {
		if t.ID == id {
			return t, nil
		}
	}
	return store.Team{}, httpx.NewProblem(http.StatusNotFound, store.CodeTeamNotFound, "team not found")
}

func (f *fakeStore) ListTeams(_ context.Context, filter store.TeamFilter, afterCode string, limit int) (store.TeamPage, error) {
	f.teamFilter = filter
	if f.err != nil {
		return store.TeamPage{}, f.err
	}
	page := store.TeamPage{Items: []store.Team{}}
	for _, t := range f.teams {
		if t.Code > afterCode {
			page.Items = append(page.Items, t)
		}
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCode = page.Items[limit-1].Code
	}
	return page, nil
}

func (f *fakeStore) teamKnown(id string) bool {
	return slices.ContainsFunc(f.teams, func(t store.Team) bool { return t.ID == id })
}

func (f *fakeStore) AddTeamMember(_ context.Context, teamID string, in store.TeamMember, today time.Time, key string) (store.TeamMember, bool, error) {
	f.teamMemberToday = today
	if f.err != nil {
		return store.TeamMember{}, false, f.err
	}
	if replay, ok := f.teamMemberKeys[key]; ok && key != "" {
		if replay.MemberID != in.MemberID {
			return store.TeamMember{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reuse")
		}
		return replay, false, nil
	}
	if !f.teamKnown(teamID) {
		return store.TeamMember{}, false, httpx.NewProblem(http.StatusNotFound, store.CodeTeamNotFound, "team not found")
	}
	in.ID = fmt.Sprintf("00000000-0000-0000-0000-%012d", len(f.teamMembers)+1)
	in.TeamID, in.Source = teamID, "manual"
	f.teamMembers = append(f.teamMembers, in)
	if key != "" {
		if f.teamMemberKeys == nil {
			f.teamMemberKeys = map[string]store.TeamMember{}
		}
		f.teamMemberKeys[key] = in
	}
	return in, true, nil
}

func (f *fakeStore) UpdateTeam(_ context.Context, id string, version int, in store.TeamPatch) (store.Team, error) {
	if f.err != nil {
		return store.Team{}, f.err
	}
	for i, t := range f.teams {
		if t.ID != id {
			continue
		}
		if t.Version != version {
			return store.Team{}, httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, "current version is 2")
		}
		if in.Name != nil && *in.Name != t.Name {
			t.Name, t.Version = *in.Name, t.Version+1
		}
		f.teams[i] = t
		return t, nil
	}
	return store.Team{}, httpx.NewProblem(http.StatusNotFound, store.CodeTeamNotFound, "team not found")
}

func (f *fakeStore) memberIndex(teamID, memberRowID string) (int, error) {
	if !f.teamKnown(teamID) {
		return -1, httpx.NewProblem(http.StatusNotFound, store.CodeTeamNotFound, "team not found")
	}
	i := slices.IndexFunc(f.teamMembers, func(m store.TeamMember) bool { return m.TeamID == teamID && m.ID == memberRowID })
	if i < 0 {
		return -1, httpx.NewProblem(http.StatusNotFound, store.CodeTeamMemberNotFound, "team member not found")
	}
	return i, nil
}

func (f *fakeStore) EndTeamMember(_ context.Context, teamID, memberRowID string, validTo time.Time) (store.TeamMember, bool, error) {
	if f.err != nil {
		return store.TeamMember{}, false, f.err
	}
	i, err := f.memberIndex(teamID, memberRowID)
	if err != nil {
		return store.TeamMember{}, false, err
	}
	end := validTo.Format("2006-01-02")
	changed := f.teamMembers[i].ValidTo == nil || *f.teamMembers[i].ValidTo != end
	f.teamMembers[i].ValidTo = &end
	return f.teamMembers[i], changed, nil
}

func (f *fakeStore) RemoveTeamMember(_ context.Context, teamID, memberRowID string) error {
	if f.err != nil {
		return f.err
	}
	i, err := f.memberIndex(teamID, memberRowID)
	if err != nil {
		return err
	}
	f.teamMembers = slices.Delete(f.teamMembers, i, i+1)
	return nil
}

func (f *fakeStore) ListTeamMembers(_ context.Context, teamID string, filter store.TeamMemberFilter, afterID string, limit int) (store.TeamMemberPage, error) {
	f.teamMemberFilter = filter
	if f.err != nil {
		return store.TeamMemberPage{}, f.err
	}
	if !f.teamKnown(teamID) {
		return store.TeamMemberPage{}, httpx.NewProblem(http.StatusNotFound, store.CodeTeamNotFound, "team not found")
	}
	page := store.TeamMemberPage{Items: []store.TeamMember{}}
	for _, m := range f.teamMembers {
		if m.ID > afterID {
			page.Items = append(page.Items, m)
		}
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextID = page.Items[limit-1].ID
	}
	return page, nil
}

func (f *fakeStore) translationKey(ownerType, ownerID, language string) string {
	return ownerType + "/" + ownerID + "/" + language
}

func (f *fakeStore) PutNameTranslation(_ context.Context, ownerType, ownerID, language, name string) (store.NameTranslation, bool, error) {
	if f.err != nil {
		return store.NameTranslation{}, false, f.err
	}
	if f.translations == nil {
		f.translations = map[string]store.NameTranslation{}
	}
	key := f.translationKey(ownerType, ownerID, language)
	existing, found := f.translations[key]
	row := store.NameTranslation{OwnerType: ownerType, OwnerID: ownerID, Language: language, Name: name, Changed: !found || existing.Name != name}
	f.translations[key] = row
	return row, !found, nil
}

func (f *fakeStore) ListNameTranslations(_ context.Context, ownerType, ownerID string) ([]store.NameTranslation, error) {
	if f.err != nil {
		return nil, f.err
	}
	items := []store.NameTranslation{}
	for _, row := range f.translations {
		if row.OwnerType == ownerType && row.OwnerID == ownerID {
			items = append(items, row)
		}
	}
	slices.SortFunc(items, func(a, b store.NameTranslation) int { return strings.Compare(a.Language, b.Language) })
	return items, nil
}

func (f *fakeStore) DeleteNameTranslation(_ context.Context, ownerType, ownerID, language string) error {
	if f.err != nil {
		return f.err
	}
	key := f.translationKey(ownerType, ownerID, language)
	if _, found := f.translations[key]; !found {
		return httpx.NewProblem(http.StatusNotFound, store.CodeTranslationNotFound, "translation not found")
	}
	delete(f.translations, key)
	return nil
}

func (f *fakeStore) ResolveOrgUnitName(_ context.Context, ownerType, ownerID, tag string) (store.NameResolution, error) {
	if f.err != nil {
		return store.NameResolution{}, f.err
	}
	byLanguage := map[string]string{}
	for _, row := range f.translations {
		if row.OwnerType == ownerType && row.OwnerID == ownerID {
			byLanguage[row.Language] = row.Name
		}
	}
	name, language, source := domain.ResolveName("Base name", byLanguage, tag)
	return store.NameResolution{OwnerType: ownerType, OwnerID: ownerID, Language: language, Name: name, Source: source}, nil
}

func TestNilTenantIsUnauthorized(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	rec := do(r, http.MethodPost, legalEntities, validBody, allDuties, true, "X-Passage-Tenant", ids.Nil)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "AUTH-0001") {
		t.Fatalf("nil tenant = %d %s", rec.Code, rec.Body)
	}
}

func TestScopedCallerCannotChangeReferenceStatus(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	rec := do(r, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"inactive"}`, referenceDuties, true, "X-Passage-Legal-Entity", testkit.TenantID(2))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
		t.Fatalf("got %d %s; want 403 AUTH-0003", rec.Code, rec.Body.String())
	}
}

func TestTenantLevelWritesNeedTheTenantAdminDuty(t *testing.T) {
	routes := []struct {
		name, method, path, duties string
	}{
		{"create legal entity", http.MethodPost, legalEntities, allDuties},
		{"patch reference status", http.MethodPatch, "/v1/org/currencies/USD", referenceDuties},
		{"put rounding rule", http.MethodPut, chfCash, referenceDuties},
		{"delete rounding rule", http.MethodDelete, chfCash, referenceDuties},
		{"create unit", http.MethodPost, "/v1/org/uom/units", referenceDuties},
		{"create fiscal calendar", http.MethodPost, fiscalPath, fiscalDuties},
		{"create hierarchy type", http.MethodPost, "/v1/org/hierarchy-types", hierarchyDuties},
		{"create team", http.MethodPost, "/v1/org/teams", teamDuties},
	}
	for _, tt := range routes {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{})
			rec := doWithoutTenantAdmin(r, tt.method, tt.path, `{}`, tt.duties, true)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") || !strings.Contains(rec.Body.String(), "tenant.administer") {
				t.Fatalf("no claim and %s without tenant.administer: %d %s", tt.duties, rec.Code, rec.Body)
			}
			rec = doWithoutTenantAdmin(r, tt.method, tt.path, `{}`, tt.duties+",tenant.administer", true, "X-Passage-Legal-Entity", entityA)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") || !strings.Contains(rec.Body.String(), "legal-entity-scoped") {
				t.Fatalf("scoped caller with tenant.administer: %d %s", rec.Code, rec.Body)
			}
			rec = do(r, tt.method, tt.path, `{}`, tt.duties, true)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("tenant admin was refused: %s", rec.Body)
			}
		})
	}
}
