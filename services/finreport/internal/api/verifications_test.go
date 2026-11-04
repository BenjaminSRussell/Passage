package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/verify"
)

type fakeVerifier struct {
	req      verify.Request
	replayed bool
	err      error
	listed   [2]any
}

func (f *fakeVerifier) Run(_ context.Context, req verify.Request) (store.Verification, error) {
	f.req = req
	return differing(req.LedgerID, f.replayed), f.err
}

func differing(ledger string, replayed bool) store.Verification {
	return store.Verification{VerificationSummary: store.VerificationSummary{ID: "5e0d8d34-8d8f-4d49-8b1c-0a3a2e8a9d11", LedgerID: ledger,
		Status: "differs", DiffCount: 1}, Diffs: []store.VerificationDiff{{Account: "1000", ReadModel: "1.000000", Ledger: "2.000000", Difference: "-1.000000"}},
		Replayed: replayed}
}

func (f *fakeVerifier) List(_ context.Context, ledger string, limit int) ([]store.VerificationSummary, error) {
	f.listed = [2]any{ledger, limit}
	return []store.VerificationSummary{{ID: "5e0d8d34-8d8f-4d49-8b1c-0a3a2e8a9d11", Status: "differs", DiffCount: 1}}, nil
}

func (f *fakeVerifier) Get(_ context.Context, id string) (store.Verification, error) {
	if id != "5e0d8d34-8d8f-4d49-8b1c-0a3a2e8a9d11" {
		return store.Verification{}, httpx.NewProblem(http.StatusNotFound, store.CodeVerificationNotFound, "gone")
	}
	return differing(ledgerUUID, false), nil
}

func verifyRouter(v Verifier) chi.Router {
	r := chi.NewRouter()
	a := New(&fakeStore{})
	if v != nil {
		a.WithVerifier(v)
	}
	a.Register(r)
	return r
}

const vBody = `{"ledgerId":"` + ledgerUUID + `"}`

func TestCreateVerification(t *testing.T) {
	f := &fakeVerifier{}
	r := verifyRouter(f)
	key := map[string]string{"Idempotency-Key": "k1"}

	w := call(r, "POST", "/v1/reporting/balance-verifications", vBody, key, DutyVerify, DutyLedgerBalanceRead)
	if w.Code != http.StatusCreated || w.Header().Get("Location") != "/v1/reporting/balance-verifications/5e0d8d34-8d8f-4d49-8b1c-0a3a2e8a9d11" {
		t.Fatalf("create = %d %s %v", w.Code, w.Body.String(), w.Header())
	}
	if f.req.LedgerID != ledgerUUID || f.req.IdempotencyKey != "k1" || f.req.RequestedBy != "u1" || len(f.req.RequestHash) != 64 {
		t.Errorf("request = %+v", f.req)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got["status"] != "differs" || got["diffCount"] != float64(1) {
		t.Errorf("body = %s", w.Body.String())
	}
	if _, leaked := got["Replayed"]; leaked {
		t.Error("Replayed is serialized")
	}

	f.replayed = true
	if w := call(r, "POST", "/v1/reporting/balance-verifications", vBody, key, DutyVerify, DutyLedgerBalanceRead); w.Code != http.StatusOK {
		t.Errorf("replay = %d", w.Code)
	}
}

// The check reads the ledger as the finreport service, so the ledger never sees
// the user's duties: a user who may not read the ledger's trial balance must
// not get it through this route (no confused deputy).
func TestCreateVerificationNeedsTheLedgerBalanceDuty(t *testing.T) {
	f := &fakeVerifier{}
	w := call(verifyRouter(f), "POST", "/v1/reporting/balance-verifications", vBody, map[string]string{"Idempotency-Key": "k1"}, DutyVerify)
	if w.Code != http.StatusForbidden || f.req.LedgerID != "" {
		t.Fatalf("without ledger.balance.read = %d %s (verifier called: %v)", w.Code, w.Body.String(), f.req.LedgerID != "")
	}
}

func TestGetVerificationWithholdsLedgerAmountsWithoutTheLedgerDuty(t *testing.T) {
	r := verifyRouter(&fakeVerifier{})
	path := "/v1/reporting/balance-verifications/5e0d8d34-8d8f-4d49-8b1c-0a3a2e8a9d11"
	var got struct {
		Diffs         []map[string]string `json:"diffs"`
		DiffsWithheld bool                `json:"diffsWithheld"`
		DiffCount     int                 `json:"diffCount"`
	}
	w := call(r, "GET", path, "", nil, DutyAudit)
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 || !got.DiffsWithheld || got.Diffs == nil || len(got.Diffs) != 0 || got.DiffCount != 1 {
		t.Fatalf("audit only = %d %s", w.Code, w.Body.String())
	}
	got.DiffsWithheld = false
	w = call(r, "GET", path, "", nil, DutyAudit, DutyLedgerBalanceRead)
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.DiffsWithheld || len(got.Diffs) != 1 || got.Diffs[0]["ledger"] != "2.000000" {
		t.Fatalf("with ledger duty = %d %s", w.Code, w.Body.String())
	}
}

func TestListVerificationsServesNoLedgerAmounts(t *testing.T) {
	w := call(verifyRouter(&fakeVerifier{}), "GET", "/v1/reporting/balance-verifications", "", nil, DutyAudit)
	var got struct{ Items []map[string]any }
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 || len(got.Items) != 1 {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	if _, ok := got.Items[0]["diffs"]; ok {
		t.Errorf("list item carries diffs: %v", got.Items[0])
	}
}

func TestCreateVerificationRefusals(t *testing.T) {
	r := verifyRouter(&fakeVerifier{})
	key := map[string]string{"Idempotency-Key": "k1"}
	for name, tc := range map[string]struct {
		body    string
		headers map[string]string
		duty    string
		want    int
	}{
		"no duty":          {vBody, key, DutyAudit, http.StatusForbidden},
		"no key":           {vBody, nil, DutyVerify, http.StatusBadRequest},
		"bad json":         {`{`, key, DutyVerify, http.StatusBadRequest},
		"unknown field":    {`{"ledgerId":"` + ledgerUUID + `","x":1}`, key, DutyVerify, http.StatusBadRequest},
		"ledger not uuid":  {`{"ledgerId":"abc"}`, key, DutyVerify, http.StatusUnprocessableEntity},
		"entity not uuid":  {`{"ledgerId":"` + ledgerUUID + `","legalEntityId":"x"}`, key, DutyVerify, http.StatusUnprocessableEntity},
		"missing ledgerId": {`{}`, key, DutyVerify, http.StatusUnprocessableEntity},
	} {
		if w := call(r, "POST", "/v1/reporting/balance-verifications", tc.body, tc.headers, tc.duty, DutyLedgerBalanceRead); w.Code != tc.want {
			t.Errorf("%s = %d, want %d: %s", name, w.Code, tc.want, w.Body.String())
		}
	}
	bad := &fakeVerifier{err: httpx.NewProblem(http.StatusBadGateway, verify.CodeLedgerUnavailable, "down")}
	w := call(verifyRouter(bad), "POST", "/v1/reporting/balance-verifications", vBody, key, DutyVerify, DutyLedgerBalanceRead)
	if w.Code != http.StatusBadGateway || problemCode(t, w) != verify.CodeLedgerUnavailable {
		t.Errorf("ledger down = %d %s", w.Code, w.Body.String())
	}
}

func TestVerificationRoutesAnswer503WithoutAVerifier(t *testing.T) {
	r := verifyRouter(nil)
	for _, c := range []struct{ method, path, duty string }{
		{"POST", "/v1/reporting/balance-verifications", DutyVerify},
		{"GET", "/v1/reporting/balance-verifications", DutyAudit},
		{"GET", "/v1/reporting/balance-verifications/5e0d8d34-8d8f-4d49-8b1c-0a3a2e8a9d11", DutyAudit},
	} {
		w := call(r, c.method, c.path, vBody, map[string]string{"Idempotency-Key": "k"}, c.duty, DutyLedgerBalanceRead)
		if w.Code != http.StatusServiceUnavailable || problemCode(t, w) != verify.CodeLedgerUnavailable {
			t.Errorf("%s %s = %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
}

func TestListAndGetVerification(t *testing.T) {
	f := &fakeVerifier{}
	r := verifyRouter(f)
	if w := call(r, "GET", "/v1/reporting/balance-verifications?ledgerId="+ledgerUUID+"&limit=5", "", nil, DutyAudit); w.Code != 200 ||
		f.listed != [2]any{ledgerUUID, 5} {
		t.Errorf("list = %d %v", w.Code, f.listed)
	}
	if w := call(r, "GET", "/v1/reporting/balance-verifications", "", nil, DutyAudit); w.Code != 200 || f.listed != [2]any{"", store.MaxVerificationPage} {
		t.Errorf("default list = %d %v", w.Code, f.listed)
	}
	for _, q := range []string{"?ledgerId=x", "?limit=0", "?limit=201", "?limit=a"} {
		if w := call(r, "GET", "/v1/reporting/balance-verifications"+q, "", nil, DutyAudit); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d", q, w.Code)
		}
	}
	if w := call(r, "GET", "/v1/reporting/balance-verifications", "", nil, DutyVerify); w.Code != http.StatusForbidden {
		t.Errorf("list without audit duty = %d", w.Code)
	}
	if w := call(r, "GET", "/v1/reporting/balance-verifications/5e0d8d34-8d8f-4d49-8b1c-0a3a2e8a9d11", "", nil, DutyAudit); w.Code != 200 {
		t.Errorf("get = %d", w.Code)
	}
	for _, id := range []string{"7e0d8d34-8d8f-4d49-8b1c-0a3a2e8a9d11", "nope"} {
		if w := call(r, "GET", "/v1/reporting/balance-verifications/"+id, "", nil, DutyAudit); w.Code != 404 || problemCode(t, w) != store.CodeVerificationNotFound {
			t.Errorf("get %s = %d %s", id, w.Code, w.Body.String())
		}
	}
}
