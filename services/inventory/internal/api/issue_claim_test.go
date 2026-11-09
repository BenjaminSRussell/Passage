package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

type issueClaimFake struct {
	in       store.IssueClaimInput
	replay   bool
	err      error
	released string
}

func (f *fakeStore) ClaimIssue(_ context.Context, in store.IssueClaimInput) (bool, error) {
	f.claim.in = in
	return f.claim.replay, f.claim.err
}

func (f *fakeStore) ReleaseIssueClaim(_ context.Context, key string) error {
	f.claim.released = key
	return nil
}

func claimBody(kind string) string {
	return `{"legalEntityId":"` + ids.New() + `","productId":"` + ids.New() + `","warehouse":"W1","batch":"B1","kind":"` + kind + `","qty":"2"}`
}

func TestIssueClaim_OnlyTheServiceDutyReachesIt(t *testing.T) {
	for duty, want := range map[string]int{"inventory.issue.claim": http.StatusCreated, "inventory.read": http.StatusForbidden, "inventory.hold.apply": http.StatusForbidden} {
		rec := do(t, newServer(t, &fakeStore{}), http.MethodPost, "/v1/inventory/issue-claims", claimBody("PICK"), []string{duty}, map[string]string{"Idempotency-Key": "k1"})
		if rec.Code != want {
			t.Errorf("%s: %d, want %d (%s)", duty, rec.Code, want, rec.Body)
		}
	}
}

func TestIssueClaim_PassesTheRequestAndMarksAReplay(t *testing.T) {
	fs := &fakeStore{claim: issueClaimFake{replay: true}}
	body := strings.Replace(claimBody("CONSUME"), `"qty":"2"`, `"qty":"2","issueRef":"SALES_LINE:l1"`, 1)
	rec := do(t, newServer(t, fs), http.MethodPost, "/v1/inventory/issue-claims", body, []string{"inventory.issue.claim"}, map[string]string{"Idempotency-Key": "k1"})
	in := fs.claim.in
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" || in.IdempotencyKey != "k1" || in.Kind != "CONSUME" ||
		in.Warehouse != "W1" || in.Batch != "B1" || in.Qty.String() != "2" || in.IssueRef != "SALES_LINE:l1" {
		t.Fatalf("replay: %d %v %+v", rec.Code, rec.Header(), in)
	}
}

func TestIssueClaim_HoldRefusalIsAnUnprocessableQualityHold(t *testing.T) {
	fs := &fakeStore{claim: issueClaimFake{err: httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeQualityHold, "held")}}
	rec := do(t, newServer(t, fs), http.MethodPost, "/v1/inventory/issue-claims", claimBody("SHIP"), []string{"inventory.issue.claim"}, map[string]string{"Idempotency-Key": "k"})
	if p := decodeProblem(t, rec); rec.Code != http.StatusUnprocessableEntity || p.Code != domain.CodeQualityHold {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestIssueClaim_RejectsMalformedRequests(t *testing.T) {
	cases := map[string]struct{ body, field string }{
		"unknown kind":   {claimBody("TRANSFER"), "kind"},
		"zero quantity":  {strings.Replace(claimBody("PICK"), `"qty":"2"`, `"qty":"0"`, 1), "qty"},
		"no warehouse":   {strings.Replace(claimBody("PICK"), `"warehouse":"W1"`, `"warehouse":""`, 1), "warehouse"},
		"bad product id": {strings.Replace(claimBody("PICK"), `"productId":"`, `"productId":"x`, 1), "productId"},
		"long issue ref": {strings.Replace(claimBody("PICK"), `"qty":"2"`, `"qty":"2","issueRef":"`+strings.Repeat("r", 256)+`"`, 1), "issueRef"},
	}
	for name, c := range cases {
		rec := do(t, newServer(t, &fakeStore{}), http.MethodPost, "/v1/inventory/issue-claims", c.body, []string{"inventory.issue.claim"}, map[string]string{"Idempotency-Key": "k"})
		if p := decodeProblem(t, rec); rec.Code != http.StatusBadRequest || p.Errors[c.field] == "" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	rec := do(t, newServer(t, &fakeStore{}), http.MethodPost, "/v1/inventory/issue-claims", claimBody("PICK"), []string{"inventory.issue.claim"}, nil)
	if p := decodeProblem(t, rec); rec.Code != http.StatusBadRequest || p.Errors["Idempotency-Key"] == "" {
		t.Errorf("missing key: %d %s", rec.Code, rec.Body)
	}
}

func TestIssueClaim_ReleaseNamesTheKey(t *testing.T) {
	fs := &fakeStore{}
	rec := do(t, newServer(t, fs), http.MethodPost, "/v1/inventory/issue-claims:release", "", []string{"inventory.issue.claim"}, map[string]string{"Idempotency-Key": "k9"})
	if rec.Code != http.StatusNoContent || fs.claim.released != "k9" {
		t.Fatalf("release: %d %q", rec.Code, fs.claim.released)
	}
}
