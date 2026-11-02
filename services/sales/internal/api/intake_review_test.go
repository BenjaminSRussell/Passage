package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

func TestParameters_IntakeDedupWindow(t *testing.T) {
	path := parameters + "?legalEntityId=" + testLE
	withWindow := func(v string) string {
		return validParameters[:len(validParameters)-1] + `,"intakeDedupWindowDays":` + v + "}"
	}
	s := &parametersStore{}
	for name, body := range map[string]string{"zero": withWindow("0"), "over 90 days": withWindow("91"), "negative": withWindow("-3")} {
		rec := serveBody(t, s, http.MethodPut, path, body, principal(dutyParameter), map[string]string{"If-Match": `"0"`})
		if rec.Code != http.StatusBadRequest || s.puts != 0 || !strings.Contains(rec.Body.String(), "intakeDedupWindowDays") {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := serveBody(t, s, http.MethodPut, path, validParameters, principal(dutyParameter), map[string]string{"If-Match": `"0"`}); rec.Code != http.StatusOK || s.saved.IntakeDedupWindowDays != 30 {
		t.Fatalf("absent window defaults to 30: %d %+v", rec.Code, s.saved)
	}
	if rec := serveBody(t, s, http.MethodPut, path, withWindow("90"), principal(dutyParameter), map[string]string{"If-Match": `"0"`}); rec.Code != http.StatusOK || s.saved.IntakeDedupWindowDays != 90 ||
		!strings.Contains(rec.Body.String(), `"intakeDedupWindowDays":90`) {
		t.Fatalf("put 90: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(t, &parametersStore{}, http.MethodGet, path, principal(dutyRead), nil); !strings.Contains(rec.Body.String(), `"intakeDedupWindowDays":30`) {
		t.Fatalf("defaults carry the window: %s", rec.Body.String())
	}
}

type reviewDueStore struct {
	store.Store
	dueBy, plainLE string
	dueCalls       int
	plainCalls     int
}

func (s *reviewDueStore) ListCreditProfiles(_ context.Context, le string, _ int) ([]store.CreditProfile, error) {
	s.plainCalls++
	s.plainLE = le
	return []store.CreditProfile{}, nil
}

func (s *reviewDueStore) ListCreditProfilesReviewDue(_ context.Context, _, dueBy string, _ int) ([]store.CreditProfile, error) {
	s.dueCalls++
	s.dueBy = dueBy
	return []store.CreditProfile{}, nil
}

func TestCreditProfiles_ReviewDueBy(t *testing.T) {
	s := &reviewDueStore{}
	if rec := serve(t, s, http.MethodGet, creditProfiles+"?reviewDueBy=2027-03-04", allScopePrincipal(dutyRead), nil); rec.Code != http.StatusOK || s.dueCalls != 1 || s.dueBy != "2027-03-04" || s.plainCalls != 0 {
		t.Fatalf("review due list: %d due %d %q plain %d", rec.Code, s.dueCalls, s.dueBy, s.plainCalls)
	}
	for _, bad := range []string{"soon", "2027-13-01", "2027-3-4"} {
		if rec := serve(t, s, http.MethodGet, creditProfiles+"?reviewDueBy="+bad, allScopePrincipal(dutyRead), nil); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "reviewDueBy") {
			t.Fatalf("%q: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	if rec := serve(t, s, http.MethodGet, creditProfiles, allScopePrincipal(dutyRead), nil); rec.Code != http.StatusOK || s.plainCalls != 1 || s.dueCalls != 1 {
		t.Fatalf("without the filter: %d", rec.Code)
	}
}
