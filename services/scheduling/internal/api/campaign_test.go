package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

const (
	campID = "00000000-0000-0000-0000-0000000000c1"
	ordB   = "00000000-0000-0000-0000-0000000000d1"
	ordC   = "00000000-0000-0000-0000-0000000000d2"
	ordD   = "00000000-0000-0000-0000-0000000000d3"
	resF1  = "00000000-0000-0000-0000-0000000000b2"
)

// Slice 2 fake store methods: they record what reached the store and answer
// from f.campaign, or fail with f.err.

func (f *fakeStore) CreateNetwork(_ context.Context, in store.Network, _ store.Idem) (store.Network, bool, error) {
	f.lastNetwork = in
	in.ID, in.Version = ids.New(), 1
	return in, !f.replay, f.err
}

func (f *fakeStore) ListNetworks(context.Context) ([]store.Network, error) {
	return []store.Network{}, f.err
}

func (f *fakeStore) GetNetwork(_ context.Context, id string) (store.Network, error) {
	return store.Network{ID: id, Code: "MIXING", Edges: []domain.NetworkEdge{}}, f.err
}

func (f *fakeStore) CreateSetupMatrix(_ context.Context, in store.SetupMatrix, _ store.Idem) (store.SetupMatrix, bool, error) {
	f.lastMatrix = in
	in.ID, in.Version = ids.New(), 1
	return in, !f.replay, f.err
}

func (f *fakeStore) ListSetupMatrices(context.Context, string) ([]store.SetupMatrix, error) {
	return []store.SetupMatrix{}, f.err
}

func (f *fakeStore) GetSetupMatrix(_ context.Context, id string) (store.SetupMatrix, error) {
	return store.SetupMatrix{ID: id}, f.err
}

func (f *fakeStore) CreateCleanoutRule(_ context.Context, in store.CleanoutRule, _ store.Idem) (store.CleanoutRule, bool, error) {
	f.lastRule = in
	in.ID = ids.New()
	return in, !f.replay, f.err
}

func (f *fakeStore) ListCleanoutRules(context.Context) ([]store.CleanoutRule, error) {
	return []store.CleanoutRule{}, f.err
}

func (f *fakeStore) CreateCapacityBlock(_ context.Context, in store.CapacityBlock, _ store.Idem) (store.CapacityBlock, bool, error) {
	f.lastBlock = in
	in.ID = ids.New()
	return in, !f.replay, f.err
}

func (f *fakeStore) ListCapacityBlocks(context.Context, string) ([]store.CapacityBlock, error) {
	return []store.CapacityBlock{}, f.err
}

func (f *fakeStore) CreateCampaign(_ context.Context, in store.Campaign, _ store.Idem) (store.Campaign, bool, error) {
	f.lastCampaign = in
	in.ID, in.Status, in.Version = campID, domain.CampaignProposed, 1
	return in, !f.replay, f.err
}

func (f *fakeStore) ListCampaigns(_ context.Context, filter store.CampaignFilter) ([]store.Campaign, error) {
	f.lastFilter = filter
	return []store.Campaign{}, f.err
}

func (f *fakeStore) GetCampaign(context.Context, string) (store.Campaign, error) {
	return f.campaign, f.err
}

func (f *fakeStore) command(cmd store.CampaignCommand, idem store.Idem) (store.Campaign, bool, error) {
	f.lastCmd, f.lastIdem = cmd, idem
	if f.err != nil {
		return store.Campaign{}, false, f.err
	}
	c := f.campaign
	c.Version = cmd.Version + 1
	return c, f.replay, nil
}

func (f *fakeStore) AddCampaignOrder(_ context.Context, cmd store.CampaignCommand, idem store.Idem) (store.Campaign, bool, error) {
	return f.command(cmd, idem)
}

func (f *fakeStore) RemoveCampaignOrder(_ context.Context, cmd store.CampaignCommand, idem store.Idem) (store.Campaign, bool, error) {
	return f.command(cmd, idem)
}

func (f *fakeStore) SequenceCampaign(_ context.Context, cmd store.CampaignCommand, idem store.Idem) (store.Campaign, bool, error) {
	return f.command(cmd, idem)
}

func (f *fakeStore) StartCampaign(_ context.Context, cmd store.CampaignCommand, idem store.Idem) (store.Campaign, bool, error) {
	return f.command(cmd, idem)
}

func doWith(h http.Handler, method, path, body, duties string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", tenantID)
	req.Header.Set("X-Passage-Duties", duties)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("not a problem: %s", rec.Body)
	}
	return p
}

// seededCampaign is the AC-23.12 campaign: RAF 100, 200 and 300 units.
func seededCampaign() store.Campaign {
	return store.Campaign{ID: campID, ResourceID: resID, Status: domain.CampaignPlanned, Version: 3, Orders: []store.CampaignOrder{
		{OrderID: ordB, SetupGroup: "B", Quantity: decimal.NewFromInt(100), RunMinutes: 60, Position: 1},
		{OrderID: ordC, SetupGroup: "C", Quantity: decimal.NewFromInt(200), RunMinutes: 60, Position: 2},
		{OrderID: ordD, SetupGroup: "C", Quantity: decimal.NewFromInt(300), RunMinutes: 60, Position: 3},
	}}
}

func TestCreateNetwork_ValidatesEdgesWithTheDomainRules(t *testing.T) {
	cases := []struct {
		name, body, field string
	}{
		{"no edges", `{"code":"MIXING","name":"Mixing","activeFrom":"2027-01-01","edges":[]}`, "edges"},
		{"self loop", `{"code":"MIXING","name":"Mixing","activeFrom":"2027-01-01","edges":[{"fromResourceId":"` + resID + `","toResourceId":"` + resID + `"}]}`, "edges"},
		{"bad id", `{"code":"MIXING","name":"Mixing","activeFrom":"2027-01-01","edges":[{"fromResourceId":"V1","toResourceId":"` + resID + `"}]}`, "edges"},
		{"wait too long", `{"code":"MIXING","name":"Mixing","activeFrom":"2027-01-01","edges":[{"fromResourceId":"` + resID + `","toResourceId":"` + resF1 + `","maxWaitMinutes":20000}]}`, "edges"},
		{"bad window", `{"code":"MIXING","name":"Mixing","activeFrom":"2027-01-02","activeTo":"2027-01-01","edges":[{"fromResourceId":"` + resID + `","toResourceId":"` + resF1 + `"}]}`, "activeTo"},
		{"lower-case code", `{"code":"mixing","name":"Mixing","activeFrom":"2027-01-01","edges":[{"fromResourceId":"` + resID + `","toResourceId":"` + resF1 + `"}]}`, "code"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(newRouter(&fakeStore{}), http.MethodPost, "/v1/scheduling/networks", tc.body, allDuties, "k1")
			if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors[tc.field] == "" {
				t.Fatalf("status %d, want 400 on %s: %s", rec.Code, tc.field, rec.Body)
			}
		})
	}
}

func TestCreateNetwork_PassesEdgesToTheStore(t *testing.T) {
	f := &fakeStore{}
	body := `{"code":"MIXING","name":" Mixing ","activeFrom":"2027-01-01","edges":[{"fromResourceId":"` + strings.ToUpper(resID) + `","toResourceId":"` + resF1 + `","transferMinutes":5,"maxWaitMinutes":120}]}`
	rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/networks", body, allDuties, "k1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	e := f.lastNetwork.Edges[0]
	if f.lastNetwork.Name != "Mixing" || e.From != resID || e.TransferMinutes != 5 || e.MaxWaitMinutes == nil || *e.MaxWaitMinutes != 120 {
		t.Fatalf("store got %+v", f.lastNetwork)
	}
	if rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/networks", body, "scheduling.read", "k1"); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty created a network: %d", rec.Code)
	}
}

func TestCreateSetupMatrix_DecimalCostAndValidation(t *testing.T) {
	f := &fakeStore{}
	ok := `{"resourceId":"` + resID + `","activeFrom":"2027-01-01","currency":"USD","entries":[{"fromGroup":"A","toGroup":"B","minutes":30,"cost":"12.5"},{"fromGroup":"PEANUT","toGroup":"FREE","minutes":240,"cost":0,"cleaningSetId":"WET"}]}`
	rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/setup-matrices", ok, allDuties, "k1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !f.lastMatrix.Entries[0].Cost.Equal(decimal.RequireFromString("12.5")) || f.lastMatrix.Entries[1].CleaningSetID != "WET" {
		t.Fatalf("entries = %+v", f.lastMatrix.Entries)
	}
	bad := map[string]string{
		"currency":  `{"resourceId":"` + resID + `","activeFrom":"2027-01-01","currency":"usd","entries":[{"fromGroup":"A","toGroup":"B","minutes":1}]}`,
		"entries":   `{"resourceId":"` + resID + `","activeFrom":"2027-01-01","currency":"USD","entries":[{"fromGroup":"A","toGroup":"A","minutes":1}]}`,
		"entries ":  `{"resourceId":"` + resID + `","activeFrom":"2027-01-01","currency":"USD","entries":[{"fromGroup":"A","toGroup":"B","minutes":1,"cost":"0.0000001"}]}`,
		"entries  ": `{"resourceId":"` + resID + `","activeFrom":"2027-01-01","currency":"USD","entries":[{"fromGroup":"A","toGroup":"B","minutes":1},{"fromGroup":"A","toGroup":"B","minutes":2}]}`,
	}
	for field, body := range bad {
		rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/setup-matrices", body, allDuties, "k2")
		if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors[strings.TrimSpace(field)] == "" {
			t.Fatalf("%s: status %d: %s", field, rec.Code, rec.Body)
		}
	}
}

func TestCreateCleanoutRule_NeedsATrigger(t *testing.T) {
	f := &fakeStore{}
	rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/cleanout-rules", `{"setupGroup":"PEANUT","durationMinutes":240,"atCampaignEnd":false}`, allDuties, "k1")
	if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors["atCampaignEnd"] == "" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	rec = do(newRouter(f), http.MethodPost, "/v1/scheduling/cleanout-rules", `{"setupGroup":"PEANUT","durationMinutes":240,"afterBatches":5}`, allDuties, "k1")
	if rec.Code != http.StatusCreated || !f.lastRule.AtCampaignEnd || *f.lastRule.AfterBatches != 5 {
		t.Fatalf("status %d, rule %+v", rec.Code, f.lastRule)
	}
}

func TestCreateCapacityBlock_WeeklyOrDatedNotBoth(t *testing.T) {
	f := &fakeStore{}
	weekly := `{"resourceId":"` + resID + `","code":"MON-WHITE","windows":[{"weekday":1,"startMinute":360,"endMinute":840}],"condition":[{"attribute":"color","op":"=","value":"white"}],"priority":5}`
	rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/capacity-blocks", weekly, allDuties, "k1")
	if rec.Code != http.StatusCreated || f.lastBlock.Priority != 5 || !f.lastBlock.Active || f.lastBlock.Condition[0].Value != "white" {
		t.Fatalf("status %d, block %+v", rec.Code, f.lastBlock)
	}
	bad := map[string]string{
		"windows":   `{"resourceId":"` + resID + `","code":"B1","windows":[{"weekday":1,"startMinute":360,"endMinute":840}],"startAt":"2027-01-04T06:00:00Z","endAt":"2027-01-04T14:00:00Z"}`,
		"windows ":  `{"resourceId":"` + resID + `","code":"B1"}`,
		"condition": `{"resourceId":"` + resID + `","code":"B1","startAt":"2027-01-04T06:00:00Z","endAt":"2027-01-04T14:00:00Z","condition":[{"attribute":"grade","op":">=","value":"two"}]}`,
	}
	for field, body := range bad {
		rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/capacity-blocks", body, allDuties, "k2")
		if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors[strings.TrimSpace(field)] == "" {
			t.Fatalf("%s: status %d: %s", field, rec.Code, rec.Body)
		}
	}
	dated := `{"resourceId":"` + resID + `","code":"B2","startAt":"2027-01-04T07:00:00+01:00","endAt":"2027-01-04T15:00:00+01:00"}`
	if rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/capacity-blocks", dated, allDuties, "k3"); rec.Code != http.StatusCreated ||
		!f.lastBlock.StartAt.Equal(time.Date(2027, 1, 4, 6, 0, 0, 0, time.UTC)) || f.lastBlock.StartAt.Location() != time.UTC {
		t.Fatalf("dated block: %d %+v", rec.Code, f.lastBlock)
	}
}

func TestCreateCampaign_DefaultsAndValidation(t *testing.T) {
	f := &fakeStore{}
	body := `{"resourceId":"` + resID + `","campaignKey":"FAMILY-1","plannedStart":"2027-01-04T06:00:00Z","maxQty":"1000",
		"orders":[{"orderId":"` + ordB + `","setupGroup":"B","quantity":"40.5","runMinutes":100},{"orderId":"` + ordC + `","setupGroup":"C","quantity":60,"runMinutes":120,"routeSetupMinutes":15}]}`
	rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/campaigns", body, allDuties, "k1")
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status %d etag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	c := f.lastCampaign
	if c.SequenceRule != domain.SequenceNearestNeighbor || len(c.Orders) != 2 || !c.Orders[0].Quantity.Equal(decimal.RequireFromString("40.5")) ||
		c.Orders[1].RouteSetupMinutes != 15 || !c.MaxQty.Equal(decimal.NewFromInt(1000)) {
		t.Fatalf("campaign = %+v", c)
	}
	bad := map[string]string{
		"sequenceRule":       `{"resourceId":"` + resID + `","campaignKey":"K","plannedStart":"2027-01-04T06:00:00Z","sequenceRule":"cp-sat"}`,
		"plannedStart":       `{"resourceId":"` + resID + `","campaignKey":"K"}`,
		"orders[0].quantity": `{"resourceId":"` + resID + `","campaignKey":"K","plannedStart":"2027-01-04T06:00:00Z","orders":[{"orderId":"` + ordB + `","setupGroup":"B","quantity":0}]}`,
		"maxQty":             `{"resourceId":"` + resID + `","campaignKey":"K","plannedStart":"2027-01-04T06:00:00Z","maxQty":"-1"}`,
	}
	for field, body := range bad {
		rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/campaigns", body, allDuties, "k2")
		if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors[field] == "" {
			t.Fatalf("%s: status %d: %s", field, rec.Code, rec.Body)
		}
	}
}

// Campaign commands need If-Match and an Idempotency-Key, and the
// fingerprint binds the key to the campaign, command and version.
func TestCampaignCommands_RequireIfMatchAndKey(t *testing.T) {
	f := &fakeStore{campaign: seededCampaign()}
	h := newRouter(f)
	path := "/v1/scheduling/campaigns/" + campID + ":sequence"
	if rec := doWith(h, http.MethodPost, path, "", allDuties, map[string]string{"Idempotency-Key": "k1"}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := doWith(h, http.MethodPost, path, "", allDuties, map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	rec := doWith(h, http.MethodPost, path, "", allDuties, map[string]string{"If-Match": `"3"`, "Idempotency-Key": "k1"})
	if rec.Code != http.StatusOK || f.lastCmd.Version != 3 || f.lastCmd.CampaignID != campID || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("status %d cmd %+v etag %s", rec.Code, f.lastCmd, rec.Header().Get("ETag"))
	}
	seqFP := f.lastIdem.Fingerprint
	doWith(h, http.MethodPost, "/v1/scheduling/campaigns/"+campID+":start", "", allDuties, map[string]string{"If-Match": `"3"`, "Idempotency-Key": "k1"})
	if f.lastIdem.Fingerprint == seqFP {
		t.Fatal("the same key on another command must not share a fingerprint")
	}
	if rec := doWith(h, http.MethodPost, path, "", "scheduling.read,scheduling.campaign.write", map[string]string{"If-Match": `"3"`, "Idempotency-Key": "k9"}); rec.Code != http.StatusForbidden {
		t.Fatalf("sequencing needs the maintain-schedule duty, got %d", rec.Code)
	}
}

func TestCampaignOrders_AddAndRemove(t *testing.T) {
	f := &fakeStore{campaign: seededCampaign()}
	h := newRouter(f)
	rec := doWith(h, http.MethodPost, "/v1/scheduling/campaigns/"+campID+"/orders",
		`{"orderId":"`+strings.ToUpper(ordB)+`","setupGroup":"B","quantity":"5","runMinutes":30}`, allDuties,
		map[string]string{"If-Match": `"3"`, "Idempotency-Key": "k1"})
	if rec.Code != http.StatusOK || f.lastCmd.Order.OrderID != ordB || !f.lastCmd.Order.Quantity.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("add: %d %+v", rec.Code, f.lastCmd)
	}
	rec = doWith(h, http.MethodDelete, "/v1/scheduling/campaigns/"+campID+"/orders/"+ordC, "", allDuties,
		map[string]string{"If-Match": `"4"`, "Idempotency-Key": "k2"})
	if rec.Code != http.StatusOK || f.lastCmd.OrderID != ordC || f.lastCmd.Version != 4 {
		t.Fatalf("remove: %d %+v", rec.Code, f.lastCmd)
	}
	f.err = httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCampaignStarted, "campaign is started")
	rec = doWith(h, http.MethodDelete, "/v1/scheduling/campaigns/"+campID+"/orders/"+ordC, "", allDuties,
		map[string]string{"If-Match": `"4"`, "Idempotency-Key": "k3"})
	if rec.Code != http.StatusUnprocessableEntity || problemOf(t, rec).Code != "SCHED-0011" {
		t.Fatalf("frozen campaign: %d %s", rec.Code, rec.Body)
	}
}

// TestAllocateCampaignCost_AC23_12 covers AC-23.12 through the API: USD 900.00
// over RAF 100, 200 and 300 gives 150.00, 300.00 and 450.00.
func TestAllocateCampaignCost_AC23_12(t *testing.T) {
	f := &fakeStore{campaign: seededCampaign()}
	rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/campaigns/"+campID+":allocate-cost", `{"amount":"900","currency":"USD"}`, allDuties, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out allocationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{ordB: "150.00", ordC: "300.00", ordD: "450.00"}
	if out.Basis != "quantity" || out.Amount != "900.00" || len(out.Lines) != 3 {
		t.Fatalf("response = %+v", out)
	}
	for _, l := range out.Lines {
		if want[l.OrderID] != l.Amount {
			t.Fatalf("line %+v, want %s", l, want[l.OrderID])
		}
	}
}

func TestAllocateCampaignCost_ActualsAndErrors(t *testing.T) {
	f := &fakeStore{campaign: seededCampaign()}
	h := newRouter(f)
	// Actual RAF overrides the planned quantity: 100 / 100 / 0 -> half each.
	rec := do(h, http.MethodPost, "/v1/scheduling/campaigns/"+campID+":allocate-cost",
		`{"amount":"10.01","currency":"USD","actuals":[{"orderId":"`+ordC+`","quantity":"100"},{"orderId":"`+ordD+`","quantity":"0"}]}`, allDuties, "")
	var out allocationResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out.Lines[0].Amount != "5.00" || out.Lines[1].Amount != "5.01" || out.Lines[2].Amount != "0.00" {
		t.Fatalf("status %d lines %+v", rec.Code, out.Lines)
	}
	bad := map[string]string{
		"amount":   `{"amount":"1.001","currency":"USD"}`,
		"currency": `{"amount":"1","currency":"dollars"}`,
		"basis":    `{"amount":"1","currency":"USD","basis":"weight"}`,
		"actuals":  `{"amount":"1","currency":"USD","actuals":[{"orderId":"` + ids.New() + `","quantity":"1"}]}`,
	}
	for field, body := range bad {
		rec := do(h, http.MethodPost, "/v1/scheduling/campaigns/"+campID+":allocate-cost", body, allDuties, "")
		if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors[field] == "" {
			t.Fatalf("%s: status %d: %s", field, rec.Code, rec.Body)
		}
	}
	// Equal basis with every planned quantity: three equal shares of 1.00.
	rec = do(h, http.MethodPost, "/v1/scheduling/campaigns/"+campID+":allocate-cost", `{"amount":"1","currency":"USD","basis":"equal"}`, allDuties, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Lines[0].Amount != "0.34" || out.Lines[1].Amount != "0.33" || out.Lines[2].Amount != "0.33" {
		t.Fatalf("equal split %+v", out.Lines)
	}
}

func TestListCampaigns_Filters(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	if rec := do(h, http.MethodGet, "/v1/scheduling/campaigns?status=started&resourceId="+strings.ToUpper(resID), "", allDuties, ""); rec.Code != http.StatusOK ||
		f.lastFilter.Status != "started" || f.lastFilter.ResourceID != resID || f.lastFilter.Limit != 50 {
		t.Fatalf("filter %+v", f.lastFilter)
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/campaigns?status=running", "", allDuties, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status accepted: %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/campaigns/not-a-uuid", "", allDuties, ""); rec.Code != http.StatusNotFound || problemOf(t, rec).Code != store.CodeCampaignNotFound {
		t.Fatalf("bad id: %d %s", rec.Code, rec.Body)
	}
}
