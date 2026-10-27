package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/costacct/internal/store"
)

func (f *fakeStore) CreateBehaviorRule(_ context.Context, _, _ string, in store.BehaviorRule) (int, []byte, error) {
	f.calls++
	f.behavior = in
	return http.StatusCreated, []byte(`{"id":"b"}`), nil
}

func (f *fakeStore) ListBehaviorRules(_ context.Context, le, element string) ([]store.BehaviorRule, error) {
	f.calls++
	f.filter = le + "|" + element
	return nil, nil
}

// TestBehaviorRule_Validation covers the FR-38.16 input rules: classes are
// Fixed or Variable only, the kind is a known cost object kind or empty, and
// the date is a calendar date. Invalid input never reaches the store.
func TestBehaviorRule_Validation(t *testing.T) {
	path := "/v1/controlling/cost-behavior-rules"
	good := `{"legalEntityId":"` + entityID + `","costElementId":"` + elemID + `","objectKind":"CostCenter","behavior":"Fixed","effectiveFrom":"2026-01-01"}`
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodPost, path, good, DutyMaintain, "k"); rec.Code != http.StatusCreated {
		t.Fatalf("good rule: status %d (%s)", rec.Code, rec.Body.String())
	}
	if st.behavior.ElementID != elemID || st.behavior.ObjectKind != "CostCenter" || st.behavior.Behavior != "Fixed" {
		t.Fatalf("store input %+v", st.behavior)
	}
	anyKind := strings.Replace(good, `"objectKind":"CostCenter",`, "", 1)
	if rec := serve(t, &fakeStore{}, http.MethodPost, path, anyKind, DutyMaintain, "k-any"); rec.Code != http.StatusCreated {
		t.Fatalf("rule for every kind: status %d (%s)", rec.Code, rec.Body.String())
	}
	cases := map[string]string{
		"unclassified": strings.Replace(good, `"Fixed"`, `"Unclassified"`, 1),
		"unknown kind": strings.Replace(good, `"CostCenter"`, `"Department"`, 1),
		"no date":      strings.Replace(good, `"2026-01-01"`, `""`, 1),
		"not a date":   strings.Replace(good, `"2026-01-01"`, `"2026-02-30"`, 1),
		"bad element":  strings.Replace(good, elemID, "not-a-uuid", 1),
		"id supplied":  strings.Replace(good, `{`, `{"id":"`+objA+`",`, 1),
		"unknown key":  strings.Replace(good, `{`, `{"regression":true,`, 1),
	}
	for name, body := range cases {
		st := &fakeStore{}
		rec := serve(t, st, http.MethodPost, path, body, DutyMaintain, "k")
		if rec.Code != http.StatusBadRequest || problemCode(t, rec) != "COM-0001" || st.calls != 0 {
			t.Errorf("%s: status %d calls %d (%s)", name, rec.Code, st.calls, rec.Body.String())
		}
	}
	if rec := serve(t, &fakeStore{}, http.MethodPost, path, good, DutyView, "k2"); rec.Code != http.StatusForbidden {
		t.Errorf("rule without controlling.maintain: status %d", rec.Code)
	}
	if rec := serve(t, &fakeStore{}, http.MethodPost, path, good, DutyMaintain, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("rule without Idempotency-Key: status %d", rec.Code)
	}
}

func TestBehaviorRule_ListFilters(t *testing.T) {
	st := &fakeStore{}
	rec := serve(t, st, http.MethodGet, "/v1/controlling/cost-behavior-rules?legalEntityId="+entityID+"&costElementId="+elemID, "", DutyView, "")
	if rec.Code != http.StatusOK || st.filter != entityID+"|"+elemID || strings.TrimSpace(rec.Body.String()) != `{"items":[]}` {
		t.Fatalf("list: status %d filter %q body %s", rec.Code, st.filter, rec.Body.String())
	}
	st = &fakeStore{}
	if rec := serve(t, st, http.MethodGet, "/v1/controlling/cost-behavior-rules?costElementId=nope", "", DutyView, ""); rec.Code != http.StatusBadRequest || st.calls != 0 {
		t.Errorf("bad element filter: status %d calls %d", rec.Code, st.calls)
	}
	if rec := serve(t, &fakeStore{}, http.MethodGet, "/v1/controlling/cost-behavior-rules", "", DutyRun, ""); rec.Code != http.StatusForbidden {
		t.Errorf("list without controlling.view: status %d", rec.Code)
	}
}

// A cost element's default behavior is Fixed, Variable or absent.
func TestCostElement_DefaultBehavior(t *testing.T) {
	base := `{"legalEntityId":"` + entityID + `","kind":"Primary","code":"PR-1","name":"x","mainAccountFrom":"1","mainAccountTo":"2"`
	if rec := serve(t, &fakeStore{}, http.MethodPost, "/v1/controlling/cost-elements", base+`,"defaultBehavior":"Variable"}`, DutyMaintain, "e1"); rec.Code != http.StatusCreated {
		t.Errorf("Variable default: status %d (%s)", rec.Code, rec.Body.String())
	}
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodPost, "/v1/controlling/cost-elements", base+`,"defaultBehavior":"Maybe"}`, DutyMaintain, "e2"); rec.Code != http.StatusBadRequest || st.calls != 0 {
		t.Errorf("bad default: status %d calls %d", rec.Code, st.calls)
	}
}
