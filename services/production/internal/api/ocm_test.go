package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// ocmFake records the OCM calls and answers with canned results.
type ocmFake struct {
	profiles []store.OCMProfile
	saved    []store.OCMProfileSave
	detects  []store.ChangeDetect
	detected store.ChangeDetectResult
	filters  []store.ChangeFilter
	change   store.OrderChange
	preview  store.ChangePreview
	applies  []store.ChangeApply
	applied  store.ChangeApplyResult
	dismiss  []store.ChangeDismiss
	err      error
}

func (f *fakeStore) ListOCMProfiles(context.Context, string) ([]store.OCMProfile, error) {
	return f.ocm.profiles, f.ocm.err
}

func (f *fakeStore) SaveOCMProfile(_ context.Context, in store.OCMProfileSave) (store.OCMProfile, bool, error) {
	f.ocm.saved = append(f.ocm.saved, in)
	return store.OCMProfile{LegalEntityID: in.LegalEntityID, OCMProfile: in.Profile, Version: in.IfMatch + 1}, in.IfMatch == 0, f.ocm.err
}

func (f *fakeStore) DetectOrderChanges(_ context.Context, in store.ChangeDetect) (store.ChangeDetectResult, error) {
	f.ocm.detects = append(f.ocm.detects, in)
	return f.ocm.detected, f.ocm.err
}

func (f *fakeStore) ListOrderChanges(_ context.Context, cf store.ChangeFilter) (store.ChangePage, error) {
	f.ocm.filters = append(f.ocm.filters, cf)
	return store.ChangePage{Items: []store.OrderChange{f.ocm.change}}, f.ocm.err
}

func (f *fakeStore) GetOrderChange(context.Context, string) (store.OrderChange, error) {
	return f.ocm.change, f.ocm.err
}

func (f *fakeStore) PreviewOrderChange(context.Context, string) (store.ChangePreview, error) {
	return f.ocm.preview, f.ocm.err
}

func (f *fakeStore) ApplyOrderChange(_ context.Context, in store.ChangeApply) (store.ChangeApplyResult, error) {
	f.ocm.applies = append(f.ocm.applies, in)
	return f.ocm.applied, f.ocm.err
}

func (f *fakeStore) DismissOrderChange(_ context.Context, in store.ChangeDismiss) (store.OrderChange, error) {
	f.ocm.dismiss = append(f.ocm.dismiss, in)
	return f.ocm.change, f.ocm.err
}

func detectBody(mut func(m map[string]any)) string {
	m := map[string]any{"ecoId": "ECO-1042", "productId": ids.New(),
		"bom":        []any{map[string]any{"lineNo": "10", "itemId": ids.New(), "qtyPer": "2", "unitCost": "9.5"}},
		"operations": []any{map[string]any{"operNumber": 10, "resourceId": ids.New(), "runTimeHPer": "0.4", "laborRate": "30"}}}
	if mut != nil {
		mut(m)
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

func TestOCMProfiles_SaveValidatesAndDefaultsToAll(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	rec := do(h, call{method: http.MethodPut, path: "/v1/production/ocm-profiles", duties: []string{DutySetupWrite},
		body: `{"mode":"automatic","surchargeFixed":"25","surchargePct":"10"}`})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	got := f.ocm.saved[0]
	if got.Profile.ItemGroup != domain.AllKey || got.Profile.SiteID != domain.AllKey || got.LegalEntityID != entityID || got.Actor != "planner-1" {
		t.Fatalf("saved %+v", got)
	}
	for name, body := range map[string]string{"mode": `{"mode":"sometimes"}`, "surchargePct": `{"mode":"manual","surchargePct":"1001"}`,
		"surchargeFixed": `{"mode":"manual","surchargeFixed":"-1"}`, "siteId": `{"mode":"manual","siteId":"not-a-uuid"}`} {
		rec := do(h, call{method: http.MethodPut, path: "/v1/production/ocm-profiles", duties: []string{DutySetupWrite}, body: body})
		if p := expectProblem(t, rec, http.StatusBadRequest, validationCode); p.Errors[name] == "" {
			t.Errorf("%s: no field error in %v", name, p.Errors)
		}
	}
	expectProblem(t, do(h, call{method: http.MethodPut, path: "/v1/production/ocm-profiles", duties: []string{DutyRead}, body: `{"mode":"none"}`}),
		http.StatusForbidden, "AUTH-0003")
	// Replacing carries the If-Match version.
	rec = do(h, call{method: http.MethodPut, path: "/v1/production/ocm-profiles", duties: []string{DutySetupWrite}, body: `{"mode":"none"}`,
		headers: map[string]string{"If-Match": `"3"`}})
	if rec.Code != http.StatusOK || f.ocm.saved[len(f.ocm.saved)-1].IfMatch != 3 {
		t.Fatalf("replace: %d %+v", rec.Code, f.ocm.saved)
	}
}

func TestOCMProfiles_ListResolvesForASite(t *testing.T) {
	site := ids.New()
	f := &fakeStore{ocm: ocmFake{profiles: []store.OCMProfile{{LegalEntityID: entityID, OCMProfile: domain.OCMProfile{ItemGroup: domain.AllKey, SiteID: site, Mode: domain.OCMAutomatic}}}}}
	_, h := newServer(t, f)
	rec := do(h, call{method: http.MethodGet, path: "/v1/production/ocm-profiles?siteId=" + site + "&itemGroup=FG", duties: []string{DutySetupRead}})
	var body struct {
		Resolved domain.OCMProfile `json:"resolved"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 || body.Resolved.Mode != domain.OCMAutomatic {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/ocm-profiles?itemGroup=FG", duties: []string{DutySetupRead}}),
		http.StatusBadRequest, validationCode)
}

func TestDetect_ValidatesAndPassesTheNotice(t *testing.T) {
	f := &fakeStore{ocm: ocmFake{detected: store.ChangeDetectResult{EcoID: "ECO-1042", Created: []store.OrderChange{}}}}
	_, h := newServer(t, f)
	post := func(body string, duties ...string) *httptest.ResponseRecorder {
		return do(h, call{method: http.MethodPost, path: "/v1/production/order-changes:detect", duties: duties, body: body})
	}
	if rec := post(detectBody(nil), DutyWrite); rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	in := f.ocm.detects[0]
	if in.EcoID != "ECO-1042" || in.BOM == nil || in.Operations == nil || len(*in.BOM) != 1 || in.RequestHash == "" || in.Actor != "planner-1" {
		t.Fatalf("detect %+v", in)
	}
	// A BOM-only ECO leaves the route nil.
	post(detectBody(func(m map[string]any) { delete(m, "operations") }), DutyWrite)
	if f.ocm.detects[1].Operations != nil || f.ocm.detects[1].BOM == nil {
		t.Fatal("a BOM-only ECO must not carry a route")
	}
	for name, mut := range map[string]func(map[string]any){
		"ecoId":     func(m map[string]any) { m["ecoId"] = " " },
		"productId": func(m map[string]any) { m["productId"] = "x" },
		"siteId":    func(m map[string]any) { m["siteId"] = "x" },
		"bom":       func(m map[string]any) { delete(m, "bom"); delete(m, "operations") },
		"operations": func(m map[string]any) {
			m["operations"] = []any{}
		},
	} {
		rec := post(detectBody(mut), DutyWrite)
		if p := expectProblem(t, rec, http.StatusBadRequest, validationCode); p.Errors[name] == "" {
			t.Errorf("%s: no field error in %v", name, p.Errors)
		}
	}
	expectProblem(t, post(detectBody(nil), DutyRead), http.StatusForbidden, "AUTH-0003")
	if len(f.ocm.detects) != 2 {
		t.Fatalf("store reached %d times", len(f.ocm.detects))
	}
}

func TestChangeWorklist_FiltersAndRead(t *testing.T) {
	f := &fakeStore{ocm: ocmFake{change: store.OrderChange{ID: ids.New(), Version: 2}}}
	_, h := newServer(t, f)
	site, order := ids.New(), ids.New()
	rec := do(h, call{method: http.MethodGet, path: "/v1/production/order-changes?status=pending&siteId=" + site + "&orderId=" + order +
		"&ecoId=ECO-1&needsReview=true&limit=10", duties: []string{DutyRead}})
	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	got := f.ocm.filters[0]
	if got.Status != "pending" || got.SiteID != site || got.OrderID != order || got.EcoID != "ECO-1" || got.NeedsReview == nil || !*got.NeedsReview || got.Limit != 10 {
		t.Fatalf("filter %+v", got)
	}
	for _, q := range []string{"status=open", "siteId=x", "orderId=x", "needsReview=maybe", "limit=0"} {
		expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/order-changes?" + q, duties: []string{DutyRead}}), http.StatusBadRequest, validationCode)
	}
	rec = do(h, call{method: http.MethodGet, path: "/v1/production/order-changes/" + ids.New(), duties: []string{DutyRead}})
	if rec.Code != 200 || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("read: %d etag %q", rec.Code, rec.Header().Get("ETag"))
	}
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/order-changes/nope", duties: []string{DutyRead}}), http.StatusNotFound, store.CodeChangeNotFound)
}

func TestChangeCommands_DutiesHeadersAndStore(t *testing.T) {
	id := ids.New()
	f := &fakeStore{ocm: ocmFake{change: store.OrderChange{ID: id, Version: 3}, applied: store.ChangeApplyResult{Change: store.OrderChange{ID: id, Version: 4}}}}
	_, h := newServer(t, f)
	path := "/v1/production/order-changes/" + id
	// Preview needs the duty.
	expectProblem(t, do(h, call{method: http.MethodPost, path: path + ":preview", duties: []string{DutyRead}}), http.StatusForbidden, "AUTH-0003")
	if rec := do(h, call{method: http.MethodPost, path: path + ":preview", duties: []string{DutyApplyChange}}); rec.Code != 200 {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	// Apply needs If-Match and Idempotency-Key, and passes the release duty on.
	expectProblem(t, do(h, call{method: http.MethodPost, path: path + ":apply", duties: []string{DutyApplyChange}}), http.StatusPreconditionRequired, "precondition_required")
	expectProblem(t, do(h, call{method: http.MethodPost, path: path + ":apply", duties: []string{DutyApplyChange}, headers: map[string]string{"If-Match": `"3"`}}),
		http.StatusBadRequest, validationCode)
	rec := do(h, call{method: http.MethodPost, path: path + ":apply", duties: []string{DutyApplyChange, DutyBOMAfterRelease},
		headers: map[string]string{"If-Match": `"3"`, idempotencyName: "apply-1"}})
	if rec.Code != 200 || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
	}
	if in := f.ocm.applies[0]; in.ChangeID != id || in.IfMatch != 3 || in.IdempotencyKey != "apply-1" || !in.ReleaseDuty {
		t.Fatalf("apply %+v", in)
	}
	f.ocm.applied.Replayed = true
	rec = do(h, call{method: http.MethodPost, path: path + ":apply", duties: []string{DutyApplyChange},
		headers: map[string]string{"If-Match": `"3"`, idempotencyName: "apply-1"}})
	if rec.Header().Get("Idempotent-Replayed") != "true" || f.ocm.applies[1].ReleaseDuty {
		t.Fatalf("replay header %q, duty %v", rec.Header().Get("Idempotent-Replayed"), f.ocm.applies[1].ReleaseDuty)
	}
	// Dismiss carries the note.
	rec = do(h, call{method: http.MethodPost, path: path + ":dismiss", duties: []string{DutyApplyChange}, body: `{"note":" keep v2 "}`,
		headers: map[string]string{"If-Match": `"3"`}})
	if rec.Code != 200 || f.ocm.dismiss[0].Note != "keep v2" || f.ocm.dismiss[0].IfMatch != 3 {
		t.Fatalf("dismiss: %d %+v", rec.Code, f.ocm.dismiss)
	}
	expectProblem(t, do(h, call{method: http.MethodPost, path: path + ":dismiss", duties: []string{DutyApplyChange}, body: `{"note":"` + string(make([]byte, 0)) + `","x":1}`,
		headers: map[string]string{"If-Match": `"3"`}}), http.StatusBadRequest, validationCode)
	// Unknown command and malformed id.
	expectProblem(t, do(h, call{method: http.MethodPost, path: path + ":explode", duties: []string{DutyApplyChange}}), http.StatusNotFound, unknownActionCode)
	expectProblem(t, do(h, call{method: http.MethodPost, path: "/v1/production/order-changes/nope:apply", duties: []string{DutyApplyChange}}), http.StatusNotFound, unknownActionCode)
}

func TestChangeCommands_StoreRefusalsKeepTheirStatus(t *testing.T) {
	f := &fakeStore{ocm: ocmFake{err: httpx.NewProblem(http.StatusConflict, domain.CodeChangeResolved, "already applied")}}
	a, h := newServer(t, f)
	rec := do(h, call{method: http.MethodPost, path: "/v1/production/order-changes/" + ids.New() + ":apply", duties: []string{DutyApplyChange},
		headers: map[string]string{"If-Match": `"1"`, idempotencyName: "k"}})
	expectProblem(t, rec, http.StatusConflict, domain.CodeChangeResolved)
	if a.refused.Load() != 1 {
		t.Fatalf("refused counter %d", a.refused.Load())
	}
}
