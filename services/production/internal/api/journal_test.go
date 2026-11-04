package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

func journalCall(orderID, body string, duties ...string) call {
	return call{method: http.MethodPost, path: "/v1/production/orders/" + orderID + "/journals", body: body, duties: duties,
		headers: map[string]string{idempotencyName: "journal-1"}}
}

func TestJournal_CreateValidation(t *testing.T) {
	tests := []struct {
		name, body string
		field      string
	}{
		{"unknown type", `{"type":"time-card"}`, "type"},
		{"job card names an operation", `{"type":"job-card","hours":[{"operNumber":10,"hours":"1"}]}`, "hours[0].jobId"},
		{"job card job is a UUID", `{"type":"job-card","hours":[{"jobId":"x","hours":"1"}]}`, "hours[0].jobId"},
		{"route card names an operation, not a job", `{"type":"route-card","hours":[{"operNumber":10,"jobId":"` + ids.New() + `","hours":"1"}]}`, "hours[0].operNumber"},
		{"issues on a job card", `{"type":"job-card","issues":[{"lineNo":"10","quantity":"1"}]}`, "issues"},
		{"bad date", `{"type":"picking-list","postingDate":"26-10-2026"}`, "postingDate"},
		{"bad proposal", `{"type":"picking-list","proposal":"all"}`, "proposal"},
		{"RAF needs a quantity", `{"type":"report-as-finished"}`, "quantity"},
		{"RAF quantity positive", `{"type":"report-as-finished","quantity":"0"}`, "quantity"},
		{"quantity only on RAF", `{"type":"picking-list","quantity":"5"}`, "quantity"},
		{"issues on a route card", `{"type":"route-card","issues":[{"lineNo":"10","quantity":"1"}]}`, "issues"},
		{"hours on a picking list", `{"type":"picking-list","hours":[{"operNumber":10,"hours":"1"}]}`, "hours"},
		{"issue line number", `{"type":"picking-list","issues":[{"lineNo":"","quantity":"1"}]}`, "issues[0].lineNo"},
		{"issue quantity", `{"type":"picking-list","issues":[{"lineNo":"10","quantity":"-1"}]}`, "issues[0].quantity"},
		{"issue unit cost", `{"type":"picking-list","issues":[{"lineNo":"10","quantity":"1","unitCost":"-2"}]}`, "issues[0].unitCost"},
		{"hours precision", `{"type":"route-card","hours":[{"operNumber":10,"hours":"1.005"}]}`, "hours[0].hours"},
		{"empty feedback", `{"type":"route-card","hours":[{"operNumber":10,"hours":"0"}]}`, "hours[0].hours"},
		{"operation number", `{"type":"route-card","hours":[{"operNumber":0,"hours":"1"}]}`, "hours[0].operNumber"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			p := expectProblem(t, do(h, journalCall(ids.New(), tc.body, DutyJournalPost)), http.StatusBadRequest, validationCode)
			if _, ok := p.Errors[tc.field]; !ok {
				t.Fatalf("no error on %s: %v", tc.field, p.Errors)
			}
			if len(f.journals) != 0 {
				t.Fatal("an invalid journal reached the store")
			}
		})
	}
	f := &fakeStore{}
	_, h := newServer(t, f)
	c := journalCall(ids.New(), `{"type":"picking-list"}`, DutyJournalPost)
	delete(c.headers, idempotencyName)
	expectProblem(t, do(h, c), http.StatusBadRequest, validationCode)
}

func TestJournal_CreateDefaultsAndLines(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	order := ids.New()
	rec := do(h, journalCall(order, `{"type":"report-as-finished","quantity":"40","errorQuantity":"2"}`, DutyJournalPost))
	if rec.Code != http.StatusCreated || !strings.HasPrefix(rec.Header().Get("Location"), "/v1/production/journals/") {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	raf := f.journals[0]
	if raf.OrderID != order || raf.Proposal != domain.ProposeProportional || !raf.QtyGood.Equal(decimal.NewFromInt(40)) ||
		!raf.QtyError.Equal(decimal.NewFromInt(2)) || !raf.AutoStart || raf.Actor != "planner-1" || raf.RequestHash == "" {
		t.Fatalf("RAF input: %+v", raf)
	}

	c := journalCall(order, `{"type":"route-card","postingDate":"2026-10-26","hours":[{"operNumber":10,"hours":"46","qtyGood":"100","finished":true},{"operNumber":20,"hours":"0","qtyError":"3"}]}`, DutyJournalPost)
	c.headers[idempotencyName] = "journal-2"
	if rec := do(h, c); rec.Code != http.StatusCreated {
		t.Fatalf("route card: %d %s", rec.Code, rec.Body.String())
	}
	route := f.journals[1]
	if route.Proposal != "" || route.PostingDate != "2026-10-26" || len(route.Hours) != 2 ||
		!route.Hours[0].Hours.Equal(decimal.NewFromInt(46)) || !route.Hours[0].Finished || !route.Hours[1].QtyError.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("route-card input: %+v", route)
	}
	if route.RequestHash == raf.RequestHash {
		t.Fatal("different bodies share a request hash")
	}

	job := ids.New()
	c = journalCall(order, `{"type":"job-card","hours":[{"jobId":"`+job+`","hours":"2.5","qtyGood":"10","finished":true}]}`, DutyJournalPost)
	c.headers[idempotencyName] = "journal-job"
	if rec := do(h, c); rec.Code != http.StatusCreated {
		t.Fatalf("job card: %d %s", rec.Code, rec.Body.String())
	}
	if card := f.journals[2]; card.Type != domain.JournalJobCard || len(card.Hours) != 1 || card.Hours[0].JobID != job ||
		card.Hours[0].OperNumber != 0 || !card.Hours[0].Hours.Equal(decimal.RequireFromString("2.5")) || !card.Hours[0].Finished {
		t.Fatalf("job-card input: %+v", card)
	}

	c = journalCall(order, `{"type":"picking-list","proposal":"none","issues":[{"lineNo":"30.1","quantity":"102","unitCost":"10.20"}]}`, DutyJournalPost)
	c.headers[idempotencyName] = "journal-3"
	do(h, c)
	pick := f.journals[3]
	if pick.Proposal != domain.ProposeNone || len(pick.Issues) != 1 || pick.Issues[0].LineNo != "30.1" || pick.Issues[0].UnitCost == nil ||
		!pick.Issues[0].UnitCost.Equal(decimal.RequireFromString("10.2")) {
		t.Fatalf("picking input: %+v", pick)
	}
}

func TestJournal_Post(t *testing.T) {
	f := &fakeStore{}
	a, h := newServer(t, f)
	id := ids.New()
	post := call{method: http.MethodPost, path: "/v1/production/journals/" + id + ":post", duties: []string{DutyJournalPost, DutyOverConsume},
		headers: map[string]string{idempotencyName: "post-1"}}
	rec := do(h, post)
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("post: %d %s", rec.Code, rec.Body.String())
	}
	r := f.posts[0]
	if r.JournalID != id || !r.OverConsumptionDuty || !r.OverConsumptionPct.Equal(decimal.NewFromInt(10)) || !r.RAFTolerancePct.Equal(decimal.NewFromInt(5)) ||
		!r.AutoStart || r.IdempotencyKey != "post-1" || r.Actor != "planner-1" {
		t.Fatalf("post request: %+v", r)
	}
	if rec := do(h, post); rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatal("replay not flagged")
	}
	post.duties = []string{DutyJournalPost}
	do(h, post)
	if f.posts[2].OverConsumptionDuty {
		t.Fatal("over-consumption allowed without the duty")
	}
	if a.posted.Load() != 1 || a.replayed.Load() != 2 {
		t.Fatalf("counters: posted %d replayed %d", a.posted.Load(), a.replayed.Load())
	}

	expectProblem(t, do(h, call{method: http.MethodPost, path: "/v1/production/journals/" + id + ":approve", duties: []string{DutyJournalPost},
		headers: map[string]string{idempotencyName: "k"}}), http.StatusNotFound, unknownActionCode)
	expectProblem(t, do(h, call{method: http.MethodPost, path: "/v1/production/journals/" + id + ":post", duties: []string{DutyJournalPost}}),
		http.StatusBadRequest, validationCode)

	f.journalErr = httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeOverConsumption, "too much")
	post.headers[idempotencyName] = "post-2"
	expectProblem(t, do(h, post), http.StatusUnprocessableEntity, domain.CodeOverConsumption)
	if a.refused.Load() != 1 {
		t.Fatalf("refusal not counted: %d", a.refused.Load())
	}
}

func TestJournal_ReadAndDelete(t *testing.T) {
	id := ids.New()
	f := &fakeStore{journal: store.Journal{ID: id, Type: domain.JournalPickingList, JournalNumber: "PJ-000001"}}
	_, h := newServer(t, f)
	rec := do(h, call{method: http.MethodGet, path: "/v1/production/journals/" + id, duties: []string{DutyRead}})
	var j store.Journal
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil || j.JournalNumber != "PJ-000001" {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	order := ids.New()
	rec = do(h, call{method: http.MethodGet, path: "/v1/production/orders/" + order + "/journals", duties: []string{DutyRead}})
	var list struct {
		OrderID string          `json:"orderId"`
		Items   []store.Journal `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list.OrderID != order || len(list.Items) != 1 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, call{method: http.MethodDelete, path: "/v1/production/journals/" + id, duties: []string{DutyJournalPost}}); rec.Code != http.StatusNoContent || f.deleted[0] != id {
		t.Fatalf("delete: %d", rec.Code)
	}
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/journals/nope", duties: []string{DutyRead}}), http.StatusNotFound, store.CodeJournalNotFound)
	f.journalErr = httpx.NewProblem(http.StatusConflict, store.CodeJournalPosted, "posted")
	expectProblem(t, do(h, call{method: http.MethodDelete, path: "/v1/production/journals/" + id, duties: []string{DutyJournalPost}}), http.StatusConflict, store.CodeJournalPosted)
}

func profileBody(mut func(m map[string]any)) string {
	m := map[string]any{"wipAccount": "1450", "inventoryAccount": "1400", "finishedGoodsAccount": "1410", "laborAbsorptionAccount": "5510",
		"overheadAbsorptionAccount": "5520", "lotSizeVarianceAccount": "5610", "quantityVarianceAccount": "5620",
		"priceVarianceAccount": "5630", "substitutionVarianceAccount": "5640"}
	if mut != nil {
		mut(m)
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

func TestProfiles_Save(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	put := call{method: http.MethodPut, path: "/v1/production/posting-profiles", body: profileBody(nil), duties: []string{DutySetupWrite}}
	rec := do(h, put)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	in := f.saved[0]
	if in.LegalEntityID != entityID || in.Profile.ItemGroup != domain.AllKey || in.Profile.SiteID != domain.AllKey || in.IfMatch != 0 || in.Actor != "planner-1" {
		t.Fatalf("defaults: %+v", in)
	}
	site := ids.New()
	put.body = profileBody(func(m map[string]any) { m["itemGroup"], m["siteId"] = "FG", site })
	put.headers = map[string]string{"If-Match": `"3"`}
	if rec := do(h, put); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` || f.saved[1].IfMatch != 3 || f.saved[1].Profile.SiteID != site {
		t.Fatalf("replace: %d %+v", rec.Code, f.saved[1])
	}

	for name, mut := range map[string]func(m map[string]any){
		"wipAccount":    func(m map[string]any) { m["wipAccount"] = " " },
		"siteId":        func(m map[string]any) { m["siteId"] = "plant-1" },
		"legalEntityId": func(m map[string]any) { m["legalEntityId"] = ids.New() },
		"itemGroup":     func(m map[string]any) { m["itemGroup"] = strings.Repeat("g", 41) },
	} {
		p := expectProblem(t, do(h, call{method: http.MethodPut, path: "/v1/production/posting-profiles", body: profileBody(mut), duties: []string{DutySetupWrite}}),
			http.StatusBadRequest, validationCode)
		if _, ok := p.Errors[name]; !ok {
			t.Errorf("%s: no field error in %v", name, p.Errors)
		}
	}
	if len(f.saved) != 2 {
		t.Fatalf("invalid profiles reached the store: %d", len(f.saved))
	}
}

func TestProfiles_ListAndResolve(t *testing.T) {
	site := ids.New()
	f := &fakeStore{profiles: []store.PostingProfile{
		{LegalEntityID: entityID, Profile: domain.Profile{ItemGroup: domain.AllKey, SiteID: domain.AllKey, WIP: "1450"}},
		{LegalEntityID: entityID, Profile: domain.Profile{ItemGroup: "FG", SiteID: site, WIP: "1460"}},
	}}
	_, h := newServer(t, f)
	var out profileList
	rec := do(h, call{method: http.MethodGet, path: "/v1/production/posting-profiles?itemGroup=FG&siteId=" + site, duties: []string{DutySetupRead}})
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.LegalEntityID != entityID || len(out.Items) != 2 || out.Resolved == nil || out.Resolved.WIP != "1460" {
		t.Fatalf("resolve: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(h, call{method: http.MethodGet, path: "/v1/production/posting-profiles?itemGroup=RM&siteId=" + site, duties: []string{DutySetupRead}})
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Resolved == nil || out.Resolved.WIP != "1450" {
		t.Fatalf("fallback to All: %s", rec.Body.String())
	}
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/posting-profiles?itemGroup=FG", duties: []string{DutySetupRead}}),
		http.StatusBadRequest, validationCode)
	f.profiles = f.profiles[1:]
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/posting-profiles?siteId=" + ids.New(), duties: []string{DutySetupRead}}),
		http.StatusUnprocessableEntity, domain.CodeNoPostingProfile)
}

func TestCreate_ItemGroupAndFlushing(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	body := createBody(func(m map[string]any) {
		m["itemGroup"] = "FG"
		m["bom"].([]any)[0].(map[string]any)["flushing"] = "finish"
	})
	if rec := do(h, call{method: http.MethodPost, path: "/v1/production/orders", body: body, duties: []string{DutyWrite},
		headers: map[string]string{idempotencyName: "c-flush"}}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if in := f.created[0]; in.ItemGroup != "FG" || in.Lines[0].Flushing != domain.FlushFinish || in.Lines[1].Flushing != domain.FlushManual {
		t.Fatalf("create input: %s %+v", in.ItemGroup, in.Lines)
	}
	for field, mut := range map[string]func(m map[string]any){
		"itemGroup":            func(m map[string]any) { m["itemGroup"] = domain.AllKey },
		"bom[0].flushing":      func(m map[string]any) { m["bom"].([]any)[0].(map[string]any)["flushing"] = "always" },
		"operations[0].status": func(m map[string]any) { m["operations"].([]any)[0].(map[string]any)["hoursReported"] = "3" },
	} {
		p := expectProblem(t, do(h, call{method: http.MethodPost, path: "/v1/production/orders", body: createBody(mut), duties: []string{DutyWrite},
			headers: map[string]string{idempotencyName: "c-" + field}}), http.StatusBadRequest, validationCode)
		if _, ok := p.Errors[field]; !ok {
			t.Errorf("%s: no field error in %v", field, p.Errors)
		}
	}
}

func TestIdempotencyKey_RejectsSystemPrefix(t *testing.T) {
	for key, wantRejected := range map[string]bool{
		"sys:shopfloor-jobcard:" + ids.New(): true,
		"sys:shopfloor-jobcard-post:x":       true,
		"user-key-1":                         false,
	} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Header.Set(idempotencyName, key)
		got, fields := idempotencyKey(r)
		if rejected := len(fields) > 0; rejected != wantRejected || got != key {
			t.Errorf("%q: rejected=%v key=%q, want rejected=%v", key, rejected, got, wantRejected)
		}
	}
}
