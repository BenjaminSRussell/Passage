package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

const (
	holdUUID   = "44444444-4444-4444-4444-444444444444"
	entityUUID = "55555555-5555-5555-5555-555555555555"
)

type holdStore struct {
	Store
	spec    store.HoldSpec
	placed  int
	err     error
	replay  bool
	relID   string
	relIf   *int
	relNote string
	relBy   string
	active  bool
	sweepN  int
}

func (h *holdStore) hold() store.LegalHold {
	return store.LegalHold{ID: holdUUID, LegalEntityID: entityUUID, Reason: "Audit", PlacedBy: "u1", PlacedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), Active: true, Revision: 1}
}

func (h *holdStore) PlaceHold(_ context.Context, spec store.HoldSpec) (store.LegalHold, error) {
	h.spec, h.placed = spec, h.placed+1
	out := h.hold()
	out.Replayed = h.replay
	return out, h.err
}

func (h *holdStore) ReleaseHold(_ context.Context, id string, ifMatch *int, note, actor string) (store.LegalHold, error) {
	h.relID, h.relIf, h.relNote, h.relBy = id, ifMatch, note, actor
	out := h.hold()
	out.Active, out.Revision = false, 2
	return out, h.err
}

func (h *holdStore) ListHolds(_ context.Context, active bool) ([]store.LegalHold, error) {
	h.active = active
	return []store.LegalHold{h.hold()}, h.err
}

func (h *holdStore) GetHold(context.Context, string) (store.LegalHold, error) { return h.hold(), h.err }

func (h *holdStore) ListSweeps(_ context.Context, limit int) ([]store.Sweep, error) {
	h.sweepN = limit
	return []store.Sweep{{ID: holdUUID, LegalEntityID: entityUUID, DeletedCount: 3, HeldCount: 1}}, h.err
}

const holdBody = `{"legalEntityId":"` + entityUUID + `","reason":"Audit"}`

func TestPlaceHold_RequiresTheDutyAndAKeyAndPassesTheRequest(t *testing.T) {
	st := &holdStore{}
	r := scheduleRouter(st)
	if w := call(r, http.MethodPost, "/v1/reporting/legal-holds", holdBody, map[string]string{"Idempotency-Key": "k"}, DutyView); w.Code != http.StatusForbidden {
		t.Fatalf("without the duty: %d", w.Code)
	}
	w := call(r, http.MethodPost, "/v1/reporting/legal-holds", holdBody, nil, DutyHold)
	if w.Code != http.StatusBadRequest || problemCode(t, w) != "COM-0003" {
		t.Fatalf("without a key: %d %s", w.Code, w.Body.String())
	}
	w = call(r, http.MethodPost, "/v1/reporting/legal-holds", `{"legalEntityId":"`+entityUUID+`","reason":"a","extra":1}`, map[string]string{"Idempotency-Key": "k"}, DutyHold)
	if w.Code != http.StatusBadRequest || st.placed != 0 {
		t.Fatalf("unknown field: %d placed %d", w.Code, st.placed)
	}
	w = call(r, http.MethodPost, "/v1/reporting/legal-holds", holdBody, map[string]string{"Idempotency-Key": "k"}, DutyHold)
	if w.Code != http.StatusCreated || w.Header().Get("ETag") != `"1"` || w.Header().Get("Location") != "/v1/reporting/legal-holds/"+holdUUID {
		t.Fatalf("place: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	if st.spec.LegalEntityID != entityUUID || st.spec.Reason != "Audit" || st.spec.Actor != "u1" || st.spec.IdempotencyKey != "k" || len(st.spec.RequestHash) != 64 {
		t.Fatalf("spec %+v", st.spec)
	}
	st.replay = true
	if w := call(r, http.MethodPost, "/v1/reporting/legal-holds", holdBody, map[string]string{"Idempotency-Key": "k"}, DutyHold); w.Code != http.StatusOK {
		t.Fatalf("replay: %d", w.Code)
	}
	st.err = httpx.NewProblem(409, store.CodeHoldActive, "held")
	w = call(r, http.MethodPost, "/v1/reporting/legal-holds", holdBody, map[string]string{"Idempotency-Key": "k2"}, DutyHold)
	if w.Code != http.StatusConflict || problemCode(t, w) != store.CodeHoldActive {
		t.Fatalf("conflict: %d %s", w.Code, w.Body.String())
	}
}

func TestReleaseHold_ReadsTheRevisionAndTheNote(t *testing.T) {
	st := &holdStore{}
	r := scheduleRouter(st)
	path := "/v1/reporting/legal-holds/" + holdUUID + ":release"
	if w := call(r, http.MethodPost, path, `{}`, map[string]string{"If-Match": `"1"`}, DutyView); w.Code != http.StatusForbidden {
		t.Fatalf("without the duty: %d", w.Code)
	}
	w := call(r, http.MethodPost, path, `{"note":"matter closed"}`, map[string]string{"If-Match": `"1"`}, DutyHold)
	if w.Code != http.StatusOK || w.Header().Get("ETag") != `"2"` {
		t.Fatalf("release: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	if st.relID != holdUUID || st.relIf == nil || *st.relIf != 1 || st.relNote != "matter closed" || st.relBy != "u1" {
		t.Fatalf("store got %q %v %q %q", st.relID, st.relIf, st.relNote, st.relBy)
	}
	// No body and no If-Match reach the store as such (the store answers 428).
	if w := call(r, http.MethodPost, path, "", nil, DutyHold); w.Code != http.StatusOK || st.relIf != nil || st.relNote != "" {
		t.Fatalf("empty release: %d if %v note %q", w.Code, st.relIf, st.relNote)
	}
	if w := call(r, http.MethodPost, path, `{}`, map[string]string{"If-Match": "abc"}, DutyHold); w.Code != http.StatusBadRequest {
		t.Fatalf("bad If-Match: %d", w.Code)
	}
	if w := call(r, http.MethodPost, "/v1/reporting/legal-holds/"+holdUUID+":delete", `{}`, nil, DutyHold); w.Code != http.StatusNotFound {
		t.Fatalf("unknown action: %d", w.Code)
	}
	if w := call(r, http.MethodPost, "/v1/reporting/legal-holds/nope:release", `{}`, nil, DutyHold); w.Code != http.StatusNotFound || problemCode(t, w) != store.CodeHoldNotFound {
		t.Fatalf("bad id: %d %s", w.Code, w.Body.String())
	}
	st.err = httpx.NewProblem(409, store.CodeHoldReleased, "done")
	if w := call(r, http.MethodPost, path, `{}`, map[string]string{"If-Match": `"1"`}, DutyHold); w.Code != http.StatusConflict {
		t.Fatalf("already released: %d", w.Code)
	}
}

func TestListHoldsGetHoldAndSweeps(t *testing.T) {
	st := &holdStore{}
	r := scheduleRouter(st)
	w := call(r, http.MethodGet, "/v1/reporting/legal-holds?active=true", "", nil, DutyView)
	if w.Code != http.StatusOK || !st.active || !strings.Contains(w.Body.String(), `"legalEntityId":"`+entityUUID+`"`) {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if w := call(r, http.MethodGet, "/v1/reporting/legal-holds?active=maybe", "", nil, DutyView); w.Code != http.StatusBadRequest {
		t.Fatalf("bad active: %d", w.Code)
	}
	if w := call(r, http.MethodGet, "/v1/reporting/legal-holds", "", nil, DutyHold); w.Code != http.StatusForbidden {
		t.Fatalf("list needs the view duty: %d", w.Code)
	}
	w = call(r, http.MethodGet, "/v1/reporting/legal-holds/"+holdUUID, "", nil, DutyView)
	if w.Code != http.StatusOK || w.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d %v", w.Code, w.Header())
	}
	if w := call(r, http.MethodGet, "/v1/reporting/legal-holds/nope", "", nil, DutyView); w.Code != http.StatusNotFound {
		t.Fatalf("get bad id: %d", w.Code)
	}
	w = call(r, http.MethodGet, "/v1/reporting/library-sweeps?limit=5", "", nil, DutyView)
	if w.Code != http.StatusOK || st.sweepN != 5 || !strings.Contains(w.Body.String(), `"deletedCount":3`) {
		t.Fatalf("sweeps: %d %s limit %d", w.Code, w.Body.String(), st.sweepN)
	}
	if w := call(r, http.MethodGet, "/v1/reporting/library-sweeps?limit=0", "", nil, DutyView); w.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", w.Code)
	}
}
