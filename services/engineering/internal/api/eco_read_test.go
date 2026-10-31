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

func TestListEcos(t *testing.T) {
	f := &fakeStore{ecoList: []domain.Eco{
		{ID: ecoID, Number: "ECO-1", State: domain.EcoSubmitted, Replaces: []domain.Replacement{{OldVersionID: oldVersion, NewVersionID: newVersion}}},
		{ID: "00000000-0000-0000-0000-0000000000e2", Number: "ECO-2", State: domain.EcoReleased},
	}}
	r, _ := newRouter(f, approves)
	first := do(r, http.MethodGet, ecos+"?limit=1&state=submitted", "", allDuties, true)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"nextCursor"`) || !strings.Contains(first.Body.String(), `"newVersionId"`) || strings.Contains(first.Body.String(), "ECO-2") || f.ecoState != "submitted" {
		t.Fatalf("first page = %d %s (state %q)", first.Code, first.Body, f.ecoState)
	}
	empty, _ := newRouter(&fakeStore{}, approves)
	if rec := do(empty, http.MethodGet, ecos, "", allDuties, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty list = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, ecos+"?state=bogus", "", allDuties, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"state"`) {
		t.Fatalf("bad state = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, ecos+"?limit=0", "", allDuties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit = %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, ecos+"?cursor=not-a-cursor", "", allDuties, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
		t.Fatalf("bad cursor = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, ecos, "", "", true); rec.Code != http.StatusForbidden {
		t.Fatalf("no duty = %d; want 403", rec.Code)
	}
	if rec := do(r, http.MethodGet, ecos, "", allDuties, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no tenant = %d; want 401", rec.Code)
	}
	failing, _ := newRouter(&fakeStore{err: errors.New("boom")}, approves)
	if rec := do(failing, http.MethodGet, ecos, "", allDuties, true); rec.Code != http.StatusInternalServerError {
		t.Fatalf("store error = %d; want 500", rec.Code)
	}
}

func TestGetEco(t *testing.T) {
	f := &fakeStore{record: releasableRecord(domain.EcoScheduled, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))}
	r, _ := newRouter(f, approves)
	rec := do(r, http.MethodGet, ecos+"/"+ecoID, "", allDuties, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"scheduled"`) || !strings.Contains(rec.Body.String(), `"releaseDate":"2026-08-01"`) {
		t.Fatalf("get = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, ecos+"/not-a-uuid", "", allDuties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id = %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, ecos+"/"+ecoID, "", "", true); rec.Code != http.StatusForbidden {
		t.Fatalf("no duty = %d; want 403", rec.Code)
	}
	missing, _ := newRouter(&fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeEcoNotFound, "ECO not found")}, approves)
	if rec := do(missing, http.MethodGet, ecos+"/"+ecoID, "", allDuties, true); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeEcoNotFound) {
		t.Fatalf("missing eco = %d %s; want 404 %s", rec.Code, rec.Body, store.CodeEcoNotFound)
	}
	failing, _ := newRouter(&fakeStore{err: errors.New("boom")}, approves)
	if rec := do(failing, http.MethodGet, ecos+"/"+ecoID, "", allDuties, true); rec.Code != http.StatusInternalServerError {
		t.Fatalf("store error = %d; want 500", rec.Code)
	}
}
