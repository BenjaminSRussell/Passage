package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

// Transfer mirrors the store's guards: the domain shape check, the version
// and the source status; it records what the handler passed on.
func (f *fakeStore) Transfer(_ context.Context, assetID string, in store.TransferInput, _ string) (store.Transfer, error) {
	f.lastTransfer = in
	pcts := make([]decimal.Decimal, len(in.Parts))
	for i, p := range in.Parts {
		pcts[i] = p.Percentage
	}
	if err := domain.ValidateTransfer(in.Kind, pcts); err != nil {
		return store.Transfer{}, err
	}
	a, ok := f.assets[assetID]
	if !ok {
		return store.Transfer{}, httpx.NewProblem(http.StatusNotFound, store.CodeAssetNotFound, "no asset")
	}
	if a.Version != in.Version {
		return store.Transfer{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	a.Status, a.Version = string(domain.StatusTransferred), a.Version+1
	f.assets[assetID] = a
	out := store.Transfer{ID: ids.New(), Kind: in.Kind, TransferDate: in.Date, SourceAssetID: assetID, Source: &a, Targets: []store.TransferTarget{}}
	for i, p := range in.Parts {
		out.Targets = append(out.Targets, store.TransferTarget{LineNo: i + 1, Percentage: p.Percentage,
			Asset: store.Asset{ID: ids.New(), AssetNumber: p.AssetNumber, Name: p.Name, Status: "Open", Version: 1}})
	}
	return out, nil
}

func (f *fakeStore) ListTransfers(_ context.Context, assetID string) ([]store.Transfer, error) {
	if _, ok := f.assets[assetID]; !ok {
		return nil, httpx.NewProblem(http.StatusNotFound, store.CodeAssetNotFound, "no asset")
	}
	return []store.Transfer{}, nil
}

func (f *fakeStore) CreateGroupAsset(_ context.Context, in store.GroupAssetInput, _ string) (store.GroupAsset, error) {
	f.lastGroup = in
	g := store.GroupAsset{ID: ids.New(), LegalEntityID: in.LegalEntityID, GroupCode: in.GroupCode, Name: in.Name, ClassCode: in.ClassCode,
		BookCode: in.BookCode, TaxYear: in.TaxYear, Currency: "EUR", Method: in.Method, ServiceLifePeriods: in.ServiceLifePeriods,
		Convention: in.Convention, Status: "Open", Basis: decimal.RequireFromString("24000"), MemberCount: len(in.MemberAssetIDs), Version: 1}
	f.groups[g.ID] = g
	return g, nil
}

func (f *fakeStore) ListGroupAssets(_ context.Context, filter store.GroupFilter) (store.GroupPage, error) {
	f.lastGroupFilter = filter
	page := store.GroupPage{Items: []store.GroupAsset{}}
	for _, g := range f.groups {
		page.Items = append(page.Items, g)
	}
	return page, nil
}

func (f *fakeStore) GetGroupAsset(_ context.Context, id string) (store.GroupAsset, error) {
	g, ok := f.groups[id]
	if !ok {
		return store.GroupAsset{}, httpx.NewProblem(http.StatusNotFound, store.CodeGroupNotFound, "no group asset")
	}
	return g, nil
}

const maintainDuties = "AssetView,AssetMaintain"

func transferReq(id, body, duties, etag string, key bool) req {
	headers := map[string]string{}
	if etag != "" {
		headers["If-Match"] = etag
	}
	return req{method: http.MethodPost, path: "/v1/assets/" + id + "/transfer", body: body, duties: duties, key: key, headers: headers}
}

func TestTransfer_SplitPassesPartsAndReturnsTargets(t *testing.T) {
	f := newFake()
	h := server(f)
	a := createAsset(t, h)
	body := `{"kind":"split","transferDate":"2026-06-30","parts":[
		{"assetNumber":" FA-1A ","name":"Van (front)","percentage":"60","quantity":"1"},
		{"assetNumber":"FA-1B","name":"Van (rear)","percentage":"40","classCode":"VEH-PART","barcode":" BC-9 "}]}`
	rec := do(h, transferReq(a.ID, body, maintainDuties, `"1"`, true))
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("status = %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	in := f.lastTransfer
	if in.Kind != domain.TransferKindSplit || in.Version != 1 || in.Date.String() != "2026-06-30" || len(in.Parts) != 2 {
		t.Fatalf("store got %+v", in)
	}
	if in.Parts[0].AssetNumber != "FA-1A" || in.Parts[0].Quantity == nil || !in.Parts[0].Percentage.Equal(decimal.NewFromInt(60)) {
		t.Fatalf("part 0 = %+v", in.Parts[0])
	}
	if in.Parts[1].ClassCode != "VEH-PART" || in.Parts[1].Barcode == nil || *in.Parts[1].Barcode != "BC-9" || in.Parts[1].Quantity != nil {
		t.Fatalf("part 1 = %+v", in.Parts[1])
	}
	var out store.Transfer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Targets) != 2 || out.Source == nil || out.Source.Status != "Transferred" {
		t.Fatalf("response = %+v", out)
	}
}

func TestTransfer_Guards(t *testing.T) {
	f := newFake()
	h := server(f)
	a := createAsset(t, h)
	ok := `{"kind":"transfer","parts":[{"assetNumber":"FA-9","name":"Van","percentage":"100"}]}`
	cases := []struct {
		name   string
		req    req
		status int
		code   string
	}{
		{"without AssetMaintain", transferReq(a.ID, ok, "AssetView,AssetDispose", `"1"`, true), http.StatusForbidden, ""},
		{"without Idempotency-Key", transferReq(a.ID, ok, maintainDuties, `"1"`, false), http.StatusBadRequest, "COM-0001"},
		{"without If-Match", transferReq(a.ID, ok, maintainDuties, "", true), http.StatusPreconditionRequired, store.CodeVersionConflict},
		{"stale If-Match", transferReq(a.ID, ok, maintainDuties, `"7"`, true), http.StatusPreconditionFailed, store.CodeVersionConflict},
		{"unknown asset", transferReq(ids.New(), ok, maintainDuties, `"1"`, true), http.StatusNotFound, store.CodeAssetNotFound},
		{"split shares short of 100%", transferReq(a.ID, `{"kind":"split","parts":[{"assetNumber":"A","name":"A","percentage":"60"},
			{"assetNumber":"B","name":"B","percentage":"30"}]}`, maintainDuties, `"1"`, true), http.StatusUnprocessableEntity, domain.CodeSplitPercentage},
		{"transfer with two targets", transferReq(a.ID, `{"kind":"transfer","parts":[{"assetNumber":"A","name":"A","percentage":"50"},
			{"assetNumber":"B","name":"B","percentage":"50"}]}`, maintainDuties, `"1"`, true), http.StatusUnprocessableEntity, domain.CodeInvalidTransfer},
		{"target legal entity outside the caller's scope", req{method: http.MethodPost, path: "/v1/assets/" + a.ID + "/transfer",
			body:   `{"kind":"transfer","parts":[{"assetNumber":"A","name":"A","percentage":"100","legalEntityId":"` + ids.New() + `"}]}`,
			duties: maintainDuties, key: true, headers: map[string]string{"If-Match": `"1"`, "X-Passage-Legal-Entity": ids.New()}}, http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(h, tc.req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.code != "" && problem(t, rec).Code != tc.code {
				t.Fatalf("code = %q, want %q", problem(t, rec).Code, tc.code)
			}
		})
	}
}

func TestTransfer_ValidationListsFields(t *testing.T) {
	h := server(newFake())
	a := createAsset(t, h)
	body := `{"kind":"move","transferDate":"30/06/2026","parts":[
		{"assetNumber":"","name":"","percentage":"x","legalEntityId":"le","classCode":"lower","barcode":" ","quantity":"0"},
		{"assetNumber":"FA-2","name":"B","percentage":"50"},{"assetNumber":"FA-2","name":"C","percentage":"50"}]}`
	rec := do(h, transferReq(a.ID, body, maintainDuties, `"1"`, true))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	p := problem(t, rec)
	for _, field := range []string{"kind", "transferDate", "parts[0].assetNumber", "parts[0].name", "parts[0].percentage", "parts[0].legalEntityId",
		"parts[0].classCode", "parts[0].barcode", "parts[0].quantity", "parts[2].assetNumber"} {
		if p.Errors[field] == "" {
			t.Errorf("field %s missing from %v", field, p.Errors)
		}
	}
	if p.Errors["parts[1].assetNumber"] != "" {
		t.Errorf("first use of FA-2 flagged: %v", p.Errors)
	}
	if rec := do(h, transferReq(a.ID, `{"kind":"split","parts":[]}`, maintainDuties, `"1"`, true)); rec.Code != http.StatusBadRequest || problem(t, rec).Errors["parts"] == "" {
		t.Fatalf("no parts status = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestListTransfers(t *testing.T) {
	h := server(newFake())
	a := createAsset(t, h)
	if rec := call(h, http.MethodGet, "/v1/assets/"+a.ID+"/transfers", "", "AssetView", false); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := call(h, http.MethodGet, "/v1/assets/not-a-uuid/transfers", "", "AssetView", false); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed id status = %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/assets/"+a.ID+"/transfers", "", "AssetMaintain", false); rec.Code != http.StatusForbidden {
		t.Fatalf("without AssetView status = %d", rec.Code)
	}
}

const groupBody = `{"legalEntityId":"%s","groupCode":"SP-2026","name":"Sammelposten 2026","classCode":"GWG","bookCode":"DE-TAX","taxYear":2026,
	"method":"StraightLine","serviceLifePeriods":60,"convention":"None","memberAssetIds":["%s","%s"]}`

func TestCreateGroupAsset_DutyAndInput(t *testing.T) {
	f := newFake()
	h := server(f)
	m1, m2 := ids.New(), ids.New()
	le := testLE
	full := sprintf(groupBody, le, m1, m2)
	if rec := call(h, http.MethodPost, "/v1/assets/group-assets", full, maintainDuties, true); rec.Code != http.StatusForbidden {
		t.Fatalf("without AssetConfigure status = %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/assets/group-assets", full, "AssetConfigure", false); rec.Code != http.StatusBadRequest {
		t.Fatalf("without Idempotency-Key status = %d", rec.Code)
	}
	rec := call(h, http.MethodPost, "/v1/assets/group-assets", full, "AssetConfigure", true)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	in := f.lastGroup
	if in.LegalEntityID != le || in.BookCode != "DE-TAX" || in.TaxYear != 2026 || in.ServiceLifePeriods != 60 || len(in.MemberAssetIDs) != 2 || in.Name != "Sammelposten 2026" {
		t.Fatalf("store got %+v", in)
	}
	var g store.GroupAsset
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	if rec := call(h, http.MethodGet, "/v1/assets/group-assets/"+g.ID, "", "AssetView", false); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"groupCode":"SP-2026"`) {
		t.Fatalf("get status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := call(h, http.MethodGet, "/v1/assets/group-assets/"+ids.New(), "", "AssetConfigure", false); rec.Code != http.StatusNotFound || problem(t, rec).Code != store.CodeGroupNotFound {
		t.Fatalf("unknown group status = %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/assets/group-assets?legalEntityId="+le, "", "AssetConfigure", false); rec.Code != http.StatusOK || f.lastGroupFilter.LegalEntityID != le || f.lastGroupFilter.Limit != 50 {
		t.Fatalf("list status = %d filter %+v", rec.Code, f.lastGroupFilter)
	}
	if rec := call(h, http.MethodGet, "/v1/assets/group-assets?limit=0", "", "AssetView", false); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit 0 status = %d", rec.Code)
	}
	outside := do(h, req{method: http.MethodPost, path: "/v1/assets/group-assets", body: full, duties: "AssetConfigure", key: true,
		headers: map[string]string{"X-Passage-Legal-Entity": ids.New()}})
	if outside.Code != http.StatusForbidden {
		t.Fatalf("legal entity outside scope status = %d", outside.Code)
	}
}

func TestCreateGroupAsset_ValidationListsFields(t *testing.T) {
	h := server(newFake())
	m := ids.New()
	body := `{"legalEntityId":"x","groupCode":"sp","name":"","classCode":"","bookCode":"tax","taxYear":26,"method":"Pool","serviceLifePeriods":0,
		"convention":"MidMonth1","factor":"-1","memberAssetIds":["` + m + `","` + m + `"]}`
	rec := call(h, http.MethodPost, "/v1/assets/group-assets", body, "AssetConfigure", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	p := problem(t, rec)
	for _, field := range []string{"legalEntityId", "groupCode", "name", "classCode", "bookCode", "taxYear", "method", "serviceLifePeriods",
		"convention", "factor", "memberAssetIds"} {
		if p.Errors[field] == "" {
			t.Errorf("field %s missing from %v", field, p.Errors)
		}
	}
	if rec := call(h, http.MethodPost, "/v1/assets/group-assets", sprintf(groupBody, ids.New(), "nope", m), "AssetConfigure", true); rec.Code != http.StatusBadRequest ||
		problem(t, rec).Errors["memberAssetIds"] == "" {
		t.Fatalf("malformed member status = %d", rec.Code)
	}
}

// sprintf replaces each %s in format with the next argument.
func sprintf(format string, args ...string) string {
	for _, a := range args {
		format = strings.Replace(format, "%s", a, 1)
	}
	return format
}
