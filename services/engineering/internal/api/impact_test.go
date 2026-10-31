package api_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

func TestEcoImpact(t *testing.T) {
	rec := releasableRecord(domain.EcoSubmitted, time.Time{})
	rec.Versions[1].Lines = []domain.BomLine{oneLine(1, "WHEEL", 2)}
	catalog := store.Snapshot{
		"BIKE":  {rec.Versions[0]},
		"TRIKE": {activeVersion("t1", "TRIKE", "2025-01-01", "", oneLine(1, "BIKE", 1))},
	}
	catalog["TRIKE"][0].SiteID = "plant-1"
	f := &fakeStore{record: rec, catalog: catalog}
	r, _ := newRouter(f, approves)
	res := do(r, http.MethodGet, ecos+"/"+ecoID+"/impact?asOf=2026-03-01", "", allDuties, true)
	body := res.Body.String()
	for _, want := range []string{`"asOf":"2026-03-01"`, `"productId":"TRIKE"`, `"reason":"parent"`, `"reason":"replaced"`, `"change":"changed"`, `"oldQuantity":"1"`, `"newQuantity":"2"`, `"siteIds":["plant-1"]`, `"topLevel":true`, `"openProductionOrders":0`} {
		if res.Code != http.StatusOK || !strings.Contains(body, want) {
			t.Fatalf("impact = %d %s; missing %s", res.Code, body, want)
		}
	}
	for _, tc := range []struct{ release, want string }{{"2026-09-15", "2026-09-15"}, {"2026-03-01", "2026-07-01"}, {"", "2026-07-01"}} {
		scheduled := rec
		if tc.release != "" {
			scheduled.Eco.ReleaseDate = day(tc.release)
		}
		sr, _ := newRouter(&fakeStore{record: scheduled, catalog: catalog}, approves)
		if res := do(sr, http.MethodGet, ecos+"/"+ecoID+"/impact", "", allDuties, true); !strings.Contains(res.Body.String(), `"asOf":"`+tc.want+`"`) {
			t.Fatalf("release %q: default asOf = %s, want %s (release date when later than today)", tc.release, res.Body, tc.want)
		}
	}
	if rec := do(r, http.MethodGet, ecos+"/"+ecoID+"/impact?asOf=nope", "", allDuties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad asOf = %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, ecos+"/not-a-uuid/impact", "", allDuties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id = %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, ecos+"/"+ecoID+"/impact", "", "", true); rec.Code != http.StatusForbidden {
		t.Fatalf("no duty = %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, ecos+"/"+ecoID+"/impact", "", allDuties, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no tenant = %d", rec.Code)
	}
	missing, _ := newRouter(&fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeEcoNotFound, "ECO not found")}, approves)
	if rec := do(missing, http.MethodGet, ecos+"/"+ecoID+"/impact", "", allDuties, true); rec.Code != http.StatusNotFound {
		t.Fatalf("missing = %d", rec.Code)
	}
	failing, _ := newRouter(&fakeStore{err: errors.New("boom")}, approves)
	if rec := do(failing, http.MethodGet, ecos+"/"+ecoID+"/impact", "", allDuties, true); rec.Code != http.StatusInternalServerError {
		t.Fatalf("store error = %d", rec.Code)
	}
	cyclic := store.Snapshot{"BIKE": {activeVersion("b2", "BIKE", "2025-01-01", "", oneLine(1, "BIKE", 1))}}
	cyc, _ := newRouter(&fakeStore{record: rec, catalog: cyclic}, approves)
	if res := do(cyc, http.MethodGet, ecos+"/"+ecoID+"/impact?asOf=2026-03-01", "", allDuties, true); res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cycle = %d %s", res.Code, res.Body)
	}
}
