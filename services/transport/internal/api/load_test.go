package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/transport/internal/api"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

const siteID = "0190f0a0-0000-7000-8000-0000000000aa"

type fakeDecision struct {
	id, action, reason string
	version            int
	idem               store.Idem
	at                 time.Time
}

func (f *fakeStore) BuildLoads(_ context.Context, loads []domain.Load, idem store.Idem) ([]domain.Load, error) {
	f.buildIdem = idem
	if f.buildErr != nil {
		return nil, f.buildErr
	}
	for i := range loads {
		loads[i].ID = fmt.Sprintf("0190f0a0-0000-7000-8000-%012d", i+1)
		loads[i].Version = 1
	}
	f.builtLoads = loads
	return loads, nil
}

func (f *fakeStore) ListLoads(_ context.Context, filter store.LoadFilter) ([]domain.Load, error) {
	f.loadFilter = filter
	return []domain.Load{f.load}, nil
}

func (f *fakeStore) GetLoad(_ context.Context, _ string) (domain.Load, error) {
	return f.load, f.loadErr
}

func (f *fakeStore) RecordDGCheck(_ context.Context, in domain.DGCheck) (domain.DGCheck, error) {
	if f.loadErr != nil {
		return domain.DGCheck{}, f.loadErr
	}
	in.ID, in.CheckedBy = "dg-1", "user-test"
	f.recordedDG = in
	return in, nil
}

func (f *fakeStore) ListDGChecks(_ context.Context, _ string) ([]domain.DGCheck, error) {
	return f.dgChecks, nil
}

func (f *fakeStore) DecideFreightBill(_ context.Context, id, action, reason string, version int, idem store.Idem, now time.Time) (domain.FreightBill, error) {
	f.decision = fakeDecision{id: id, action: action, reason: reason, version: version, idem: idem, at: now}
	if f.decideErr != nil {
		return domain.FreightBill{}, f.decideErr
	}
	b := f.bill
	b.ID, b.Version = id, version+1
	b.MatchStatus = domain.BillApproved
	if action == domain.ActionDispute {
		b.MatchStatus = domain.BillDisputed
	}
	return b, nil
}

func (f *fakeStore) ListBillDecisions(_ context.Context, _ string) ([]domain.Decision, error) {
	return f.decisions, nil
}

func shipmentJSON(n int, weight string) string {
	return fmt.Sprintf(`{"id":"0190f0a0-0000-7000-8000-%012d","shipFromSiteId":%q,"shipToRegion":" us-se ","mode":"tl","pickupDate":"2026-06-01","weightKg":%q,"volumeM3":"2","pallets":1}`,
		n, siteID, weight)
}

const capacityJSON = `{"maxWeightKg":"20000","maxVolumeM3":"90","maxPallets":30}`

// TestBuildLoads_AC348 is AC-34.8 through the API: 30 shipments of 1,600 kg
// (48,000 kg) under a 20,000 kg TL rule give 3 loads, none over capacity.
func TestBuildLoads_AC348(t *testing.T) {
	st := &fakeStore{}
	parts := make([]string, 30)
	for i := range parts {
		parts[i] = shipmentJSON(i+1, "1600")
	}
	body := `{"capacity":` + capacityJSON + `,"shipments":[` + strings.Join(parts, ",") + `]}`
	rec := doWith(t, router(st), http.MethodPost, base+"/loads:build", body, map[string]string{"Idempotency-Key": "build-1"}, api.DutyPlan)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Items []domain.Load `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 3 {
		t.Fatalf("loads = %d, want 3", len(resp.Items))
	}
	total := dec("0")
	for _, l := range resp.Items {
		if l.TotalWeightKg.GreaterThan(dec("20000")) {
			t.Fatalf("load over capacity: %s kg", l.TotalWeightKg)
		}
		if l.Mode != domain.ModeTL || l.ShipToRegion != "US-SE" || l.Status != domain.LoadPlanned {
			t.Fatalf("load not normalised: %+v", l)
		}
		total = total.Add(l.TotalWeightKg)
	}
	if !total.Equal(dec("48000")) {
		t.Fatalf("total weight = %s, want 48000", total)
	}
	if st.buildIdem.Key != "build-1" || st.buildIdem.Fingerprint == "" {
		t.Fatalf("idempotency not passed: %+v", st.buildIdem)
	}
}

func TestBuildLoads_Rejections(t *testing.T) {
	cases := []struct {
		name, body, code, field string
		status                  int
	}{
		{"no shipments", `{"capacity":` + capacityJSON + `,"shipments":[]}`, "COM-0001", "shipments", http.StatusBadRequest},
		{"no capacity", `{"shipments":[` + shipmentJSON(1, "10") + `]}`, "COM-0001", "capacity.maxWeightKg", http.StatusBadRequest},
		{"duplicate shipment", `{"capacity":` + capacityJSON + `,"shipments":[` + shipmentJSON(1, "10") + `,` + shipmentJSON(1, "20") + `]}`, "COM-0001", "shipments[1].id", http.StatusBadRequest},
		{"too heavy alone", `{"capacity":` + capacityJSON + `,"shipments":[` + shipmentJSON(1, "20000.5") + `]}`, domain.CodeShipmentTooLarge, "", http.StatusUnprocessableEntity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, router(&fakeStore{}), http.MethodPost, base+"/loads:build", c.body, api.DutyPlan)
			p := problem(t, rec)
			if rec.Code != c.status || p.Code != c.code {
				t.Fatalf("status = %d code = %s, want %d %s (%s)", rec.Code, p.Code, c.status, c.code, rec.Body.String())
			}
			if c.field != "" && p.Errors[c.field] == "" {
				t.Fatalf("no error on %s: %v", c.field, p.Errors)
			}
		})
	}
}

func TestBuildLoads_StoreConflictPassesThrough(t *testing.T) {
	st := &fakeStore{buildErr: httpx.NewProblem(http.StatusConflict, domain.CodeShipmentPlanned, "shipment is already on a load")}
	body := `{"capacity":` + capacityJSON + `,"shipments":[` + shipmentJSON(1, "10") + `]}`
	rec := do(t, router(st), http.MethodPost, base+"/loads:build", body, api.DutyPlan)
	if rec.Code != http.StatusConflict || problem(t, rec).Code != domain.CodeShipmentPlanned {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestListAndGetLoad(t *testing.T) {
	st := &fakeStore{load: domain.Load{ID: loadID, Status: domain.LoadPlanned, Version: 3},
		dgChecks: []domain.DGCheck{{ID: "dg-1", LoadID: loadID, Result: domain.DGWarn}}}
	h := router(st)
	if rec := do(t, h, http.MethodGet, base+"/loads?status=BOGUS", "", api.DutyView); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status filter = %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, base+"/loads?status=PLANNED", "", api.DutyView); rec.Code != http.StatusOK || st.loadFilter.Status != "PLANNED" {
		t.Fatalf("list = %d filter %+v", rec.Code, st.loadFilter)
	}
	rec := do(t, h, http.MethodGet, base+"/loads/"+loadID, "", api.DutyView)
	var got struct {
		ID       string           `json:"id"`
		DGChecks []domain.DGCheck `json:"dgChecks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("get = %d %s", rec.Code, rec.Body.String())
	}
	if got.ID != loadID || len(got.DGChecks) != 1 || got.DGChecks[0].Result != domain.DGWarn || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("detail = %+v", got)
	}
	if rec := do(t, h, http.MethodGet, base+"/loads/not-a-uuid", "", api.DutyView); rec.Code != http.StatusNotFound || problem(t, rec).Code != domain.CodeNotFound {
		t.Fatalf("malformed id = %d", rec.Code)
	}
}

const dgLine = `{"unNumber":"un %s","properShippingName":"%s","class":"%s","packingGroup":"ii","quantity":"%s","transportCategory":%d}`

// TestCheckLoadDG_AC3413 is AC-34.13 through the API: a class 1.1D explosive with
// a class 3 flammable liquid under 49 CFR fails with the segregation rule.
func TestCheckLoadDG_AC3413(t *testing.T) {
	st := &fakeStore{}
	body := `{"regulation":"49cfr","emergencyResponsePhone":"+1 8005551234","lines":[` +
		fmt.Sprintf(dgLine, "0081", "Explosive, blasting, type A", "1.1d", "50", 1) + `,` +
		fmt.Sprintf(dgLine, "1203", "Gasoline", "3", "200", 2) + `]}`
	rec := do(t, router(st), http.MethodPost, base+"/loads/"+loadID+":dg-check", body, api.DutyPlan)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var got domain.DGCheck
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Result != domain.DGFail || len(got.Findings) != 1 || got.Findings[0].RuleID != domain.RuleSeg49CFR || got.Findings[0].Code != domain.CodeDGSegregation {
		t.Fatalf("check = %+v", got)
	}
	if st.recordedDG.LoadID != loadID || st.recordedDG.Regulation != domain.Reg49CFR || st.recordedDG.CheckedAt.IsZero() {
		t.Fatalf("recorded = %+v", st.recordedDG)
	}
	if p := got.ShippingPapers; len(p) != 2 || p[0].UNNumber != "UN0081" || p[0].Class != "1.1D" || p[0].PackingGroup != "II" || p[0].EmergencyResponsePhone != "+18005551234" {
		t.Fatalf("shipping papers = %+v", p)
	}
}

func TestCheckLoadDG_ValidationAndUnknownLoad(t *testing.T) {
	bad := `{"regulation":"IMDG","emergencyResponsePhone":"call us","lines":[{"unNumber":"1203","class":"3D","quantity":"0","transportCategory":7}]}`
	rec := do(t, router(&fakeStore{}), http.MethodPost, base+"/loads/"+loadID+":dg-check", bad, api.DutyPlan)
	p := problem(t, rec)
	for _, field := range []string{"regulation", "emergencyResponsePhone", "lines[0].unNumber", "lines[0].class", "lines[0].quantity", "lines[0].transportCategory", "lines[0].properShippingName"} {
		if p.Errors[field] == "" {
			t.Errorf("no error on %s: %v", field, p.Errors)
		}
	}
	st := &fakeStore{loadErr: store.NotFound("load")}
	good := `{"regulation":"ADR","emergencyResponsePhone":"+442079460000","lines":[` + fmt.Sprintf(dgLine, "1203", "Gasoline", "3", "10", 2) + `]}`
	if rec := do(t, router(st), http.MethodPost, base+"/loads/"+loadID+":dg-check", good, api.DutyPlan); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown load = %d", rec.Code)
	}
}

type stubProvider map[string]string

func (s stubProvider) Screen(_ context.Context, p domain.ScreeningParty) (string, error) {
	if st, ok := s[p.Name]; ok {
		return st, nil
	}
	return "", errors.New("provider timeout")
}

func screenRouter(st store.Store, p domain.ScreeningProvider) http.Handler {
	r := chi.NewRouter()
	api.New(st).WithScreeningProvider(p).Register(r)
	return r
}

// TestScreenLoad_AC3416Hold is the hold half of AC-34.16: a ship-to party
// returning POTENTIAL_MATCH holds the load with TMS-0133.
func TestScreenLoad_AC3416Hold(t *testing.T) {
	st := &fakeStore{load: domain.Load{ID: loadID}}
	h := screenRouter(st, stubProvider{"Acme GmbH": domain.ScreenClear, "Northwind": domain.ScreenPotentialMatch})
	body := `{"parties":[{"role":"sold_to","name":"Acme GmbH","country":"de"},{"role":"SHIP_TO","name":"Northwind","country":"US"}]}`
	rec := do(t, h, http.MethodPost, base+"/loads/"+loadID+":screen", body, api.DutyPlan)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		CrossBorder bool                    `json:"crossBorder"`
		Decision    string                  `json:"decision"`
		Parties     []domain.PartyScreening `json:"parties"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.CrossBorder || got.Decision != domain.DecisionHold || got.Parties[1].Code != domain.CodeScreeningHold || got.Parties[0].Role != "SOLD_TO" {
		t.Fatalf("screening = %+v", got)
	}
}

func TestScreenLoad_NoProviderWarnsCrossBorder(t *testing.T) {
	st := &fakeStore{load: domain.Load{ID: loadID}}
	body := `{"crossBorder":true,"parties":[{"role":"CONSIGNEE","name":"Contoso"}]}`
	rec := do(t, router(st), http.MethodPost, base+"/loads/"+loadID+":screen", body, api.DutyPlan)
	if !strings.Contains(rec.Body.String(), `"decision":"WARN"`) || !strings.Contains(rec.Body.String(), domain.CodeNotScreened) {
		t.Fatalf("no-provider screening = %d %s", rec.Code, rec.Body.String())
	}
	bad := `{"parties":[{"role":"BUYER","name":""}]}`
	if rec := do(t, router(st), http.MethodPost, base+"/loads/"+loadID+":screen", bad, api.DutyPlan); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad parties = %d", rec.Code)
	}
}

func TestDecideFreightBill_PassesVersionReasonAndKey(t *testing.T) {
	st := &fakeStore{bill: domain.FreightBill{MatchStatus: domain.MatchVariance}}
	rec := doWith(t, router(st), http.MethodPost, base+"/freight-bills/"+billID+":approve", `{"reason":" carrier added a fuel correction "}`,
		map[string]string{"If-Match": `"3"`, "Idempotency-Key": "approve-1"}, api.DutyApprove)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("approve = %d etag %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	d := st.decision
	if d.id != billID || d.action != domain.ActionApprove || d.reason != "carrier added a fuel correction" || d.version != 3 || d.idem.Key != "approve-1" || d.at.IsZero() {
		t.Fatalf("decision = %+v", d)
	}
}

func TestDecideFreightBill_Rejections(t *testing.T) {
	url := base + "/freight-bills/" + billID
	cases := []struct {
		name, path, body, ifMatch, code string
		duty                            string
		status                          int
	}{
		{"approve without If-Match", url + ":approve", `{}`, "", "COM-0428", api.DutyApprove, http.StatusPreconditionRequired},
		{"approve with a bad If-Match", url + ":approve", `{}`, "abc", "COM-0001", api.DutyApprove, http.StatusBadRequest},
		{"dispute without a reason", url + ":dispute", `{"reason":"  "}`, "1", "COM-0001", api.DutyReconcile, http.StatusBadRequest},
		{"reason too long", url + ":dispute", `{"reason":"` + strings.Repeat("x", 501) + `"}`, "1", "COM-0001", api.DutyReconcile, http.StatusBadRequest},
		{"reconcile duty cannot approve", url + ":approve", `{}`, "1", "AUTH-0002", api.DutyReconcile, http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			header := map[string]string{}
			if c.ifMatch != "" {
				header["If-Match"] = c.ifMatch
			}
			rec := doWith(t, router(&fakeStore{}), http.MethodPost, c.path, c.body, header, c.duty)
			if rec.Code != c.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, c.status, rec.Body.String())
			}
			if c.status != http.StatusForbidden && problem(t, rec).Code != c.code {
				t.Fatalf("code = %s, want %s", problem(t, rec).Code, c.code)
			}
		})
	}
	st := &fakeStore{decideErr: httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "changed")}
	rec := doWith(t, router(st), http.MethodPost, url+":dispute", `{"reason":"fuel billed twice"}`, map[string]string{"If-Match": "1"}, api.DutyReconcile)
	if rec.Code != http.StatusPreconditionFailed || st.decision.action != domain.ActionDispute {
		t.Fatalf("version conflict = %d %+v", rec.Code, st.decision)
	}
}

func TestGetFreightBill_ListsDecisions(t *testing.T) {
	st := &fakeStore{bill: domain.FreightBill{ID: billID, MatchStatus: domain.BillDisputed, Version: 3},
		decisions: []domain.Decision{{ID: "d1", Action: domain.ActionDispute, FromStatus: domain.MatchVariance, ToStatus: domain.BillDisputed, Reason: "fuel"}}}
	rec := do(t, router(st), http.MethodGet, base+"/freight-bills/"+billID, "", api.DutyView)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` || !strings.Contains(rec.Body.String(), `"decisions":[{"id":"d1"`) {
		t.Fatalf("detail = %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	st.decisions = nil
	if rec := do(t, router(st), http.MethodGet, base+"/freight-bills/"+billID, "", api.DutyView); !strings.Contains(rec.Body.String(), `"decisions":[]`) {
		t.Fatalf("empty decisions should be [], got %s", rec.Body.String())
	}
}

// TestTransferRule_BR3411: FOB transfers at origin and opens a goods-in-transit
// order (TMS-0121); DDP transfers at receipt; an unknown rule is a 400.
func TestTransferRule_BR3411(t *testing.T) {
	h := router(&fakeStore{})
	rec := do(t, h, http.MethodGet, base+"/transfer-rules/fob", "", api.DutyView)
	var rule domain.TransferRule
	if err := json.Unmarshal(rec.Body.Bytes(), &rule); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("FOB = %d %s", rec.Code, rec.Body.String())
	}
	if rule.Incoterm != "FOB" || rule.Event != domain.TransferOriginShipment || !rule.GoodsInTransit || rule.Code != domain.CodeOwnershipMoved {
		t.Fatalf("FOB rule = %+v", rule)
	}
	rec = do(t, h, http.MethodGet, base+"/transfer-rules/DDP", "", api.DutyView)
	if !strings.Contains(rec.Body.String(), `"event":"DESTINATION_RECEIPT"`) || !strings.Contains(rec.Body.String(), `"goodsInTransit":false`) {
		t.Fatalf("DDP = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, base+"/transfer-rules/XYZ", "", api.DutyView); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "incoterm") {
		t.Fatalf("unknown incoterm = %d %s", rec.Code, rec.Body.String())
	}
}
