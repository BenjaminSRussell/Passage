package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/store"
)

type fakeHolds struct {
	covering   []string
	lastKey    string
	lastTarget domain.HoldScope
	lastErase  store.ErasureInput
	placed     store.HoldInput
}

func (f *fakeStore) PlaceHold(_ context.Context, in store.HoldInput, key string, at time.Time) (store.Hold, error) {
	f.holds.lastKey, f.holds.placed = key, in
	return store.Hold{HoldID: ids.New(), LegalEntityID: in.LegalEntityID, SubjectRef: in.SubjectRef, State: "ACTIVE", Reason: in.Reason, PlacedAt: at, Version: 1}, nil
}

func (f *fakeStore) ReleaseHold(_ context.Context, id string, expected int, _ string, _ time.Time) (store.Hold, error) {
	if expected != 1 {
		return store.Hold{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeHoldVersion, "stale")
	}
	return store.Hold{HoldID: id, State: "RELEASED", Version: 2}, nil
}

func (f *fakeStore) GetHold(_ context.Context, id string) (store.Hold, error) {
	return store.Hold{HoldID: id, State: "ACTIVE", Version: 1}, nil
}

func (f *fakeStore) ListHolds(context.Context, store.HoldFilter) (store.HoldPage, error) {
	return store.HoldPage{Items: []store.Hold{}}, nil
}

func (f *fakeStore) CheckHolds(_ context.Context, target domain.HoldScope) (store.HoldCheck, error) {
	f.holds.lastTarget = target
	return store.HoldCheck{Held: len(f.holds.covering) > 0, HoldIDs: append([]string{}, f.holds.covering...)}, nil
}

func (f *fakeStore) RequestErasure(_ context.Context, in store.ErasureInput, key string, at time.Time) (store.ErasureCommand, error) {
	f.holds.lastErase, f.holds.lastKey = in, key
	if len(f.holds.covering) > 0 {
		return store.ErasureCommand{}, domain.ErasureBlocked(f.holds.covering)
	}
	return store.ErasureCommand{ErasureID: ids.New(), LegalEntityID: in.LegalEntityID, SubjectRef: in.SubjectRef, DataClasses: []string{}, RequestedAt: at}, nil
}

func TestHoldDutiesAreEnforced(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	id := ids.New()
	for _, tc := range []struct{ method, path, duty string }{
		{http.MethodPost, "/v1/privacy/holds", DutyLegalHoldPlace},
		{http.MethodGet, "/v1/privacy/holds", DutyLegalHoldRead},
		{http.MethodGet, "/v1/privacy/holds/" + id, DutyLegalHoldRead},
		{http.MethodGet, "/v1/privacy/holds:check", DutyLegalHoldCheck},
		{http.MethodPost, "/v1/privacy/holds/" + id + "/release", DutyLegalHoldRelease},
		{http.MethodPost, "/v1/privacy/erasures", DutyErasureRequest},
		{http.MethodGet, "/v1/privacy/retention-policies", DutyRetentionRead},
	} {
		others := []string{DutyLegalHoldPlace, DutyLegalHoldRead, DutyLegalHoldRelease, DutyLegalHoldCheck, DutyErasureRequest, DutyRetentionRead, DutyPrivacyOfficer}
		rest := others[:0:0]
		for _, d := range others {
			if d != tc.duty {
				rest = append(rest, d)
			}
		}
		rec := serve(t, h, call{method: tc.method, path: tc.path, duties: rest})
		if rec.Code != http.StatusForbidden || problemCode(t, rec) != "AUTH-0003" {
			t.Errorf("%s %s without %s: %d", tc.method, tc.path, tc.duty, rec.Code)
		}
	}
}

func TestHoldReadAndReleaseRefuseAMalformedTenant(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	id := ids.New()
	for _, tc := range []struct{ method, path, duty string }{
		{http.MethodGet, "/v1/privacy/holds/" + id, DutyLegalHoldRead},
		{http.MethodPost, "/v1/privacy/holds/" + id + "/release", DutyLegalHoldRelease},
	} {
		p := tenant.Principal{TenantID: "not-a-uuid", UserID: ids.New(), Duties: map[string]struct{}{tc.duty: {}}}
		req := httptest.NewRequest(tc.method, tc.path, nil).WithContext(tenant.With(context.Background(), p))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || problemCode(t, rec) != "AUTH-0001" {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestPlaceHold_ValidatesScopeAndRequiresKeyAndLegalEntityScope(t *testing.T) {
	le := ids.New()
	f := &fakeStore{}
	h := router(newAPI(f, true))
	place := func(le, key string, body map[string]any) *httptest.ResponseRecorder {
		c := call{method: http.MethodPost, path: "/v1/privacy/holds", duties: []string{DutyLegalHoldPlace}, user: ids.New(), legalEntity: le, body: body}
		if key != "" {
			c.headers = map[string]string{"Idempotency-Key": key}
		}
		return serve(t, h, c)
	}
	good := map[string]any{"legalEntityId": le, "subjectRef": "party:42", "reason": "litigation hold, matter 2026-114"}
	if rec := place(le, "", good); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key: %d", rec.Code)
	}
	for name, body := range map[string]map[string]any{
		"no scope":     {"legalEntityId": le, "reason": "litigation hold, matter 2026-114"},
		"short reason": {"legalEntityId": le, "subjectRef": "party:42", "reason": "legal"},
		"bad class":    {"legalEntityId": le, "dataClass": "EVERYTHING", "reason": "litigation hold, matter 2026-114"},
		"bad entity":   {"legalEntityId": "x", "subjectRef": "party:42", "reason": "litigation hold, matter 2026-114"},
	} {
		if rec := place(le, "k", body); rec.Code != http.StatusBadRequest || problemCode(t, rec) != "COM-0002" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := place(ids.New(), "k", good); rec.Code != http.StatusForbidden || problemCode(t, rec) != "AUTH-0003" {
		t.Fatalf("legal entity outside the caller's scope must be refused: %d", rec.Code)
	}
	if rec := place("", "k", good); rec.Code != http.StatusForbidden {
		t.Fatalf("a caller with no legal entity scope must be refused: %d", rec.Code)
	}
	rec := place(le, "k-1", good)
	if rec.Code != http.StatusCreated || f.holds.lastKey != "k-1" || f.holds.placed.SubjectRef != "party:42" || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("place: %d %s key=%q", rec.Code, rec.Body.String(), f.holds.lastKey)
	}
}

func TestReleaseHold_NeedsIfMatchAndReason(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	id := ids.New()
	rel := func(ifMatch string, body any) int {
		c := call{method: http.MethodPost, path: "/v1/privacy/holds/" + id + "/release", duties: []string{DutyLegalHoldRelease}, body: body}
		if ifMatch != "" {
			c.headers = map[string]string{"If-Match": ifMatch}
		}
		return serve(t, h, c).Code
	}
	reason := map[string]string{"reason": "matter settled, counsel confirmed"}
	if got := rel("", reason); got != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", got)
	}
	if got := rel(`"1"`, map[string]string{"reason": "done"}); got != http.StatusBadRequest {
		t.Fatalf("short reason: %d", got)
	}
	if got := rel(`"2"`, reason); got != http.StatusPreconditionFailed {
		t.Fatalf("stale: %d", got)
	}
	if got := rel(`"1"`, reason); got != http.StatusOK {
		t.Fatalf("release: %d", got)
	}
}

func TestCheckHolds_AnswersCoveringHoldsInTheCallersScope(t *testing.T) {
	le := ids.New()
	f := &fakeStore{holds: fakeHolds{covering: []string{"h-1"}}}
	h := router(newAPI(f, true))
	path := "/v1/privacy/holds:check?legalEntityId=" + le + "&subjectRef=party:42&dataClass=AP_PAYEE&recordRef=r-1"
	rec := serve(t, h, call{method: http.MethodGet, path: path, duties: []string{DutyLegalHoldCheck}, legalEntity: le})
	var got store.HoldCheck
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || !got.Held || len(got.HoldIDs) != 1 {
		t.Fatalf("check: %d %s", rec.Code, rec.Body.String())
	}
	if want := (domain.HoldScope{LegalEntityID: le, SubjectRef: "party:42", DataClass: "AP_PAYEE", RecordRef: "r-1"}); f.holds.lastTarget != want {
		t.Fatalf("target = %+v", f.holds.lastTarget)
	}
	if rec := serve(t, h, call{method: http.MethodGet, path: path, duties: []string{DutyLegalHoldCheck}, legalEntity: ids.New()}); rec.Code != http.StatusForbidden {
		t.Fatalf("another legal entity's scope: %d", rec.Code)
	}
	if rec := serve(t, h, call{method: http.MethodGet, path: "/v1/privacy/holds:check?legalEntityId=" + le + "&dataClass=NOPE", duties: []string{DutyLegalHoldCheck}, legalEntity: le}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown class: %d", rec.Code)
	}
}

func TestRequestErasure_RefusedUnderHoldAndEmittedOtherwise(t *testing.T) {
	le := ids.New()
	f := &fakeStore{}
	h := router(newAPI(f, true))
	erase := func(body map[string]any) *httptest.ResponseRecorder {
		return serve(t, h, call{method: http.MethodPost, path: "/v1/privacy/erasures", duties: []string{DutyErasureRequest}, user: ids.New(), legalEntity: le,
			headers: map[string]string{"Idempotency-Key": "e-1"}, body: body})
	}
	body := map[string]any{"legalEntityId": le, "subjectRef": "party:42"}
	if rec := erase(body); rec.Code != http.StatusAccepted || f.holds.lastKey != "e-1" {
		t.Fatalf("erasure: %d %s", rec.Code, rec.Body.String())
	}
	f.holds.covering = []string{"h-9"}
	rec := erase(body)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodePrivacyLegalHold {
		t.Fatalf("under hold: %d %s", rec.Code, rec.Body.String())
	}
	for name, bad := range map[string]map[string]any{
		"no subject":  {"legalEntityId": le},
		"bad class":   {"legalEntityId": le, "subjectRef": "party:42", "dataClasses": []string{"NOPE"}},
		"bad request": {"legalEntityId": le, "subjectRef": "party:42", "requestId": "x"},
	} {
		if rec := erase(bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
}

func TestRetentionPolicies_DocumentedDefaults(t *testing.T) {
	rec := serve(t, router(newAPI(&fakeStore{}, true)), call{method: http.MethodGet, path: "/v1/privacy/retention-policies", duties: []string{DutyRetentionRead}})
	var got struct{ Items []domain.RetentionPolicy }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("policies: %d", rec.Code)
	}
	months := map[domain.DataClass]int{}
	for _, p := range got.Items {
		months[p.DataClass] = p.Months
	}
	want := map[domain.DataClass]int{domain.ClassAPPayee: 84, domain.ClassFinancialRecord: 84, domain.ClassEmployment: 72, domain.ClassCandidate: 6, domain.ClassSystemLog: 12}
	for c, m := range want {
		if months[c] != m {
			t.Errorf("%s = %d months, want %d", c, months[c], m)
		}
	}
}
