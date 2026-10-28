package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

// collectionsFake extends fakeStore with the dispute, promise, dunning and interest
// port methods. It records the transitions the handlers forward.
type collectionsFake struct {
	fakeStore
	disputeTo string
	runTo     string
	runGrace  int
}

func (f *collectionsFake) OpenDispute(_ context.Context, in store.Dispute, _ string) (store.Dispute, error) {
	in.ID, in.Version, in.Status = ids.New(), 1, "OPEN"
	return in, nil
}
func (f *collectionsFake) TransitionDispute(_ context.Context, id string, version int, to string, _ *string) (store.Dispute, error) {
	f.disputeTo = to
	return store.Dispute{ID: id, Version: version + 1, Status: to}, nil
}
func (f *collectionsFake) CreatePromise(_ context.Context, in store.Promise, _ string) (store.Promise, error) {
	in.ID = ids.New()
	return in, nil
}
func (f *collectionsFake) CreateDunningRun(_ context.Context, in store.DunningRun, _ string) (store.DunningRun, error) {
	f.runGrace = in.GraceDays
	in.ID, in.Version, in.Status = ids.New(), 1, "DRAFT"
	in.Exclusions = []domain.Exclusion{}
	in.Letters = []store.DunningLetter{}
	return in, nil
}
func (f *collectionsFake) GetDunningRun(_ context.Context, id string) (store.DunningRun, error) {
	return store.DunningRun{ID: id, Version: 1, Status: "DRAFT"}, nil
}
func (f *collectionsFake) TransitionDunningRun(_ context.Context, id string, version int, to string) (store.DunningRun, error) {
	f.runTo = to
	return store.DunningRun{ID: id, Version: version + 1, Status: to}, nil
}
func (f *collectionsFake) CalculateInterest(context.Context, store.InterestRequest) (store.InterestResult, error) {
	return store.InterestResult{Candidates: []domain.InterestCandidate{}, Exclusions: []domain.Exclusion{}, Posted: []store.InterestNote{}}, nil
}
func (f *collectionsFake) PostInterest(context.Context, store.InterestRequest, string) (store.InterestResult, error) {
	return store.InterestResult{Posted: []store.InterestNote{{ID: ids.New(), Period: "2027-03"}}}, nil
}

const (
	testRun     = "00000000-0000-0000-0000-000000000005"
	testDispute = "00000000-0000-0000-0000-000000000006"
)

func TestOpenDispute_Validation(t *testing.T) {
	h := router(&collectionsFake{})
	key := map[string]string{"Idempotency-Key": "d-1"}
	body := func(reason, amount string) string {
		return `{"legalEntityId":"` + testLE + `","openItemId":"` + testItem + `","reasonCode":"` + reason +
			`","disputedAmount":"` + amount + `","owner":"ana.collector"}`
	}
	tests := []struct {
		name    string
		body    string
		headers map[string]string
		duties  string
		status  int
		field   string
	}{
		{"missing key", body("PRICE", "500.00"), nil, "CollectionsMaintain", http.StatusBadRequest, ""},
		{"without duty", body("PRICE", "500.00"), key, "ARView", http.StatusForbidden, ""},
		{"lower-case reason", body("price", "500.00"), key, "CollectionsMaintain", http.StatusBadRequest, "reasonCode"},
		{"zero amount", body("PRICE", "0"), key, "CollectionsMaintain", http.StatusBadRequest, "disputedAmount"},
		{"valid", body("PRICE", "500.00"), key, "CollectionsMaintain", http.StatusCreated, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(h, http.MethodPost, "/v1/receivables/disputes", tc.body, tc.duties, tc.headers)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.field != "" && !strings.Contains(rec.Body.String(), tc.field) {
				t.Fatalf("want field %q in %s", tc.field, rec.Body.String())
			}
		})
	}
}

func TestDisputeActions_RequireIfMatchAndValidResolution(t *testing.T) {
	cf := &collectionsFake{}
	h := router(cf)
	base := "/v1/receivables/disputes/" + testDispute
	if rec := do(h, http.MethodPost, base+":review", "", "CollectionsMaintain", nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("review without If-Match: %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, base+":review", "", "CollectionsMaintain", map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusOK || cf.disputeTo != "UNDER_REVIEW" {
		t.Fatalf("review: %d to %q", rec.Code, cf.disputeTo)
	}
	bad := `{"resolution":"MAYBE"}`
	if rec := do(h, http.MethodPost, base+":resolve", bad, "CollectionsMaintain", map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad resolution: %d", rec.Code)
	}
	rec := do(h, http.MethodPost, base+":resolve", `{"resolution":"CREDIT","note":"credit memo CM-7"}`, "CollectionsMaintain", map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusOK || cf.disputeTo != "RESOLVED_CREDIT" || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("resolve: %d to %q etag %q", rec.Code, cf.disputeTo, rec.Header().Get("ETag"))
	}
	if rec := do(h, http.MethodPost, base+":escalate", "", "CollectionsMaintain", map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown action: %d", rec.Code)
	}
}

func TestCreatePromise_Validation(t *testing.T) {
	h := router(&collectionsFake{})
	key := map[string]string{"Idempotency-Key": "p-1"}
	body := `{"legalEntityId":"` + testLE + `","openItemId":"` + testItem + `","amount":"200.00","promiseDate":"2099-04-10"}`
	if rec := do(h, http.MethodPost, "/v1/receivables/promises", body, "CollectionsMaintain", key); rec.Code != http.StatusCreated {
		t.Fatalf("valid promise: %d %s", rec.Code, rec.Body.String())
	}
	for _, date := range []string{"10/04/2027", "2020-01-01"} {
		bad := strings.Replace(body, "2099-04-10", date, 1)
		if rec := do(h, http.MethodPost, "/v1/receivables/promises", bad, "CollectionsMaintain", key); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "promiseDate") {
			t.Fatalf("promise date %s: %d %s", date, rec.Code, rec.Body.String())
		}
	}
}

func TestDunningRun_CreateReviewIssue(t *testing.T) {
	cf := &collectionsFake{}
	h := router(cf)
	key := map[string]string{"Idempotency-Key": "r-1"}
	bad := `{"legalEntityId":"` + testLE + `","runDate":"2027-03-31","graceDays":99}`
	if rec := do(h, http.MethodPost, "/v1/receivables/dunning-runs", bad, "DunningRun", key); rec.Code != http.StatusBadRequest {
		t.Fatalf("grace 99: %d", rec.Code)
	}
	good := `{"legalEntityId":"` + testLE + `","runDate":"2027-03-31"}`
	rec := do(h, http.MethodPost, "/v1/receivables/dunning-runs", good, "DunningRun", key)
	if rec.Code != http.StatusCreated || cf.runGrace != 5 || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d grace %d etag %q", rec.Code, cf.runGrace, rec.Header().Get("ETag"))
	}
	run := "/v1/receivables/dunning-runs/" + testRun
	if rec := do(h, http.MethodPost, run+":issue", "", "DunningRun", nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("issue without If-Match: %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, run+":issue", "", "DunningRun", map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusOK || cf.runTo != "ISSUED" {
		t.Fatalf("issue: %d to %q", rec.Code, cf.runTo)
	}
	if rec := do(h, http.MethodGet, run, "", "ARView", nil); rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, run, "", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("get without duty: %d", rec.Code)
	}
}

func TestInterest_PostNeedsKeyAndValidRate(t *testing.T) {
	h := router(&collectionsFake{})
	body := `{"legalEntityId":"` + testLE + `","calculationDate":"2027-03-31","annualRate":"0.0900","basis":"ACT_365"}`
	if rec := do(h, http.MethodPost, "/v1/receivables/interest-notes:post", body, "InterestPost", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("post without key: %d", rec.Code)
	}
	key := map[string]string{"Idempotency-Key": "i-1"}
	if rec := do(h, http.MethodPost, "/v1/receivables/interest-notes:post", body, "InterestPost", key); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "2027-03") {
		t.Fatalf("post: %d %s", rec.Code, rec.Body.String())
	}
	over := strings.Replace(body, `"0.0900"`, `"9"`, 1)
	if rec := do(h, http.MethodPost, "/v1/receivables/interest-notes:calculate", over, "InterestPost", nil); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "annualRate") {
		t.Fatalf("rate 9: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, http.MethodPost, "/v1/receivables/interest-notes:calculate", body, "ARView", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("calculate without duty: %d", rec.Code)
	}
}

func TestCollectionsLists_ValidateFiltersAndNeedView(t *testing.T) {
	h := router(&collectionsFake{})
	tests := []struct {
		path   string
		duties string
		status int
	}{
		{"/v1/receivables/disputes?status=active", "ARView", http.StatusOK},
		{"/v1/receivables/disputes?status=OPEN&customer=" + testItem, "ARView", http.StatusOK},
		{"/v1/receivables/disputes?status=CLOSED", "ARView", http.StatusBadRequest},
		{"/v1/receivables/disputes?openItem=nope", "ARView", http.StatusBadRequest},
		{"/v1/receivables/disputes", "CollectionsMaintain", http.StatusForbidden},
		{"/v1/receivables/disputes/" + testDispute, "ARView", http.StatusOK},
		{"/v1/receivables/disputes/nope", "ARView", http.StatusNotFound},
		{"/v1/receivables/promises?openItem=" + testItem, "ARView", http.StatusOK},
		{"/v1/receivables/promises?customer=x", "ARView", http.StatusBadRequest},
		{"/v1/receivables/dunning-runs?status=ISSUED", "ARView", http.StatusOK},
		{"/v1/receivables/dunning-runs?status=SENT", "ARView", http.StatusBadRequest},
		{"/v1/receivables/dunning-runs?limit=0", "ARView", http.StatusBadRequest},
		{"/v1/receivables/interest-notes", "ARView", http.StatusOK},
		{"/v1/receivables/interest-notes?customer=x", "ARView", http.StatusBadRequest},
		{"/v1/receivables/lockbox/imports", "ARView", http.StatusOK},
		{"/v1/receivables/lockbox/imports", "", http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := do(h, http.MethodGet, tc.path, "", tc.duties, nil)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == http.StatusOK && !strings.HasSuffix(tc.path, testDispute) && !strings.Contains(rec.Body.String(), `"items":[]`) {
				t.Errorf("list body = %s, want an items array", rec.Body.String())
			}
		})
	}
}

func TestDunningRunAction_UnknownActionAndDuty(t *testing.T) {
	h := router(&collectionsFake{})
	run := "/v1/receivables/dunning-runs/" + testRun
	if rec := do(h, http.MethodPost, run+":print", "", "DunningRun", map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown action: %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, run+":review", "", "ARView", map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("review without DunningRun: %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/v1/receivables/dunning-runs", `{}`, "ARView", map[string]string{"Idempotency-Key": "k"}); rec.Code != http.StatusForbidden {
		t.Fatalf("create without DunningRun: %d", rec.Code)
	}
}
