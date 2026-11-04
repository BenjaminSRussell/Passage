package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

// FR-45.3 over HTTP: create a DRAFT agreement, activate it with If-Match, record
// visits until the allowance is used, suspend it, and expire an ended one.
func TestAPI_AgreementLifecycleAndVisits(t *testing.T) {
	h, ctx := dbRouter(t)
	prof := call(t, h, ctx, http.MethodPost, "/v1/service/sla-profiles", `{"code":"STD","name":"Standard","timeZone":"America/Chicago","openMinute":480,"closeMinute":1080,"targets":[{"priority":2,"responseMinutes":60,"resolutionMinutes":480}]}`, map[string]string{"Idempotency-Key": "p1"})
	var p store.SLAProfile
	if err := json.Unmarshal(prof.Body.Bytes(), &p); err != nil || prof.Code != http.StatusCreated {
		t.Fatalf("profile: %d %s", prof.Code, prof.Body.String())
	}
	body := `{"customerPartyId":"` + ids.New() + `","legalEntityId":"` + ids.New() + `","validFrom":"2026-01-01","validTo":"VALIDTO","slaProfileId":"` + p.ID +
		`","billingModel":"PER_VISIT","priorityRank":5,"lines":[{"includedVisits":1,"includedHours":"2","laborCovered":true}]}`
	created := call(t, h, ctx, http.MethodPost, "/v1/service/agreements", strings.Replace(body, "VALIDTO", "2099-12-31", 1), map[string]string{"Idempotency-Key": "a1"})
	var agr store.Agreement
	if err := json.Unmarshal(created.Body.Bytes(), &agr); err != nil || created.Code != http.StatusCreated || created.Header().Get("ETag") != `"1"` || agr.Status != "DRAFT" {
		t.Fatalf("create: %d ETag %q %s", created.Code, created.Header().Get("ETag"), created.Body.String())
	}
	path := "/v1/service/agreements/" + agr.ID

	noMatch := call(t, h, ctx, http.MethodPost, path+":transition", `{"to":"ACTIVE"}`, nil)
	if noMatch.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", noMatch.Code)
	}
	bad := call(t, h, ctx, http.MethodPost, path+":transition", `{"to":"EXPIRED"}`, map[string]string{"If-Match": `"1"`})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("EXPIRED requested: %d", bad.Code)
	}
	for _, body := range []string{`{"to":"SUSPENDED"}`, `{"to":"CANCELED","reason":"   "}`} {
		if noReason := call(t, h, ctx, http.MethodPost, path+":transition", body, map[string]string{"If-Match": `"1"`}); noReason.Code != http.StatusBadRequest {
			t.Fatalf("%s without a reason: %d", body, noReason.Code)
		}
	}
	active := call(t, h, ctx, http.MethodPost, path+":transition", `{"to":"ACTIVE"}`, map[string]string{"If-Match": `"1"`})
	if active.Code != http.StatusOK || active.Header().Get("ETag") != `"2"` {
		t.Fatalf("activate: %d %s", active.Code, active.Body.String())
	}
	stale := call(t, h, ctx, http.MethodPost, path+":transition", `{"to":"SUSPENDED","reason":"hold"}`, map[string]string{"If-Match": `"1"`})
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale: %d", stale.Code)
	}

	visit := `{"lineIndex":0,"kind":"PREVENTIVE","visitDate":"2026-03-01"}`
	first := call(t, h, ctx, http.MethodPost, path+"/visits", visit, map[string]string{"Idempotency-Key": "v1"})
	var v store.AgreementVisit
	if err := json.Unmarshal(first.Body.Bytes(), &v); err != nil || first.Code != http.StatusCreated || v.VisitsLeft != 0 {
		t.Fatalf("visit: %d %s", first.Code, first.Body.String())
	}
	replay := call(t, h, ctx, http.MethodPost, path+"/visits", visit, map[string]string{"Idempotency-Key": "v1"})
	if replay.Code != http.StatusOK || replay.Header().Get(replayedHeader) != "true" {
		t.Fatalf("visit replay: %d", replay.Code)
	}
	if call(t, h, ctx, http.MethodPost, path+"/visits", visit, nil).Code != http.StatusBadRequest {
		t.Fatal("a visit without Idempotency-Key must be refused")
	}
	over := call(t, h, ctx, http.MethodPost, path+"/visits", visit, map[string]string{"Idempotency-Key": "v2"})
	if code, _ := problemOf(t, over); over.Code != http.StatusUnprocessableEntity || code != store.CodeVisitsExhausted {
		t.Fatalf("over the allowance: %d %s", over.Code, over.Body.String())
	}
	if rec := call(t, h, ctx, http.MethodPost, path+"/visits", `{"lineIndex":0,"kind":"X","visitDate":"nope"}`, map[string]string{"Idempotency-Key": "v3"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid visit: %d", rec.Code)
	}
	list := call(t, h, ctx, http.MethodGet, path+"/visits", "", nil)
	var visits struct{ Items []store.AgreementVisit }
	if err := json.Unmarshal(list.Body.Bytes(), &visits); err != nil || len(visits.Items) != 1 {
		t.Fatalf("visits: %d %s", list.Code, list.Body.String())
	}

	susp := call(t, h, ctx, http.MethodPost, path+":transition", `{"to":"SUSPENDED","reason":"hold"}`, map[string]string{"If-Match": `"2"`})
	if susp.Code != http.StatusOK {
		t.Fatalf("suspend: %d %s", susp.Code, susp.Body.String())
	}
	hist := call(t, h, ctx, http.MethodGet, path+"/history", "", nil)
	var changes struct{ Items []store.AgreementChange }
	if err := json.Unmarshal(hist.Body.Bytes(), &changes); err != nil || len(changes.Items) != 2 || changes.Items[1].To != "SUSPENDED" {
		t.Fatalf("history: %d %s", hist.Code, hist.Body.String())
	}
	got := call(t, h, ctx, http.MethodGet, path, "", nil)
	if got.Header().Get("ETag") != `"3"` {
		t.Fatalf("get ETag %q", got.Header().Get("ETag"))
	}

	// An ended agreement expires in the pass; the pass is repeatable.
	ended := call(t, h, ctx, http.MethodPost, "/v1/service/agreements", strings.Replace(body, "VALIDTO", "2026-01-31", 1), map[string]string{"Idempotency-Key": "a2"})
	var old store.Agreement
	_ = json.Unmarshal(ended.Body.Bytes(), &old)
	if r := call(t, h, ctx, http.MethodPost, "/v1/service/agreements/"+old.ID+":transition", `{"to":"ACTIVE"}`, map[string]string{"If-Match": `"1"`}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("activate an ended agreement: %d %s", r.Code, r.Body.String())
	}
	if call(t, h, ctx, http.MethodPost, "/v1/service/agreements:expire?limit=0", "", nil).Code != http.StatusBadRequest {
		t.Fatal("limit 0 must be refused")
	}
	pass := call(t, h, ctx, http.MethodPost, "/v1/service/agreements:expire", "", nil)
	if pass.Code != http.StatusOK || !strings.Contains(pass.Body.String(), `"expired":0`) {
		t.Fatalf("pass: %d %s", pass.Code, pass.Body.String())
	}
	if call(t, h, ctx, http.MethodGet, "/v1/service/agreements/"+ids.New()+"/visits", "", nil).Code != http.StatusNotFound {
		t.Fatal("visits of an unknown agreement must be 404")
	}
}
