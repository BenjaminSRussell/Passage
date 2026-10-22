package httpapi

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/Passage/services/hello-ledger/internal/ledger"
)

const testToken = "test-token"

func newTS(t *testing.T, book *ledger.Book) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(newServer(testToken, book).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func authedRequest(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("content-type", "application/json")
	return req
}

func TestHealthAndBalancedPost(t *testing.T) {
	ts := newTS(t, nil)

	healthRes, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer healthRes.Body.Close()
	if healthRes.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", healthRes.StatusCode)
	}

	postRes, err := http.DefaultClient.Do(authedRequest(t, http.MethodPost, ts.URL+"/v1/entries", `{
		"lines": [
			{"account": "Cash", "debit": 250, "credit": 0},
			{"account": "Revenue", "debit": 0, "credit": 250}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	defer postRes.Body.Close()
	if postRes.StatusCode != http.StatusCreated {
		t.Fatalf("post status = %d", postRes.StatusCode)
	}

	balanceRes, err := http.DefaultClient.Do(authedRequest(t, http.MethodGet, ts.URL+"/v1/trial-balance", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer balanceRes.Body.Close()
	var body struct {
		Total int64 `json:"total"`
	}
	if err := json.NewDecoder(balanceRes.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 0 {
		t.Fatalf("total = %d", body.Total)
	}
}

func TestJournalRequiresToken(t *testing.T) {
	ts := newTS(t, nil)
	res, err := http.Post(ts.URL+"/v1/entries", "application/json", strings.NewReader(`{"lines":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestUnbalancedPostIsRejected(t *testing.T) {
	ts := newTS(t, nil)
	res, err := http.DefaultClient.Do(authedRequest(t, http.MethodPost, ts.URL+"/v1/entries", `{
		"lines": [
			{"account": "Cash", "debit": 10, "credit": 0},
			{"account": "Revenue", "debit": 0, "credit": 4}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Error, "not balanced") {
		t.Fatalf("error = %q", body.Error)
	}
}

func TestTrialBalanceTotalDoesNotWrap(t *testing.T) {
	ts := newTS(t, nil)
	postMax := func(debitAccount, creditAccount string) {
		t.Helper()
		body := fmt.Sprintf(`{
			"lines": [
				{"account": %q, "debit": %d, "credit": 0},
				{"account": %q, "debit": 0, "credit": %d}
			]
		}`, debitAccount, math.MaxInt64, creditAccount, math.MaxInt64)
		posted, err := http.DefaultClient.Do(authedRequest(t, http.MethodPost, ts.URL+"/v1/entries", body))
		if err != nil {
			t.Fatal(err)
		}
		posted.Body.Close()
		if posted.StatusCode != http.StatusCreated {
			t.Fatalf("post %s/%s status = %d", debitAccount, creditAccount, posted.StatusCode)
		}
	}
	postMax("Cash", "Revenue")
	postMax("Bank", "Sales")
	balanceRes, err := http.DefaultClient.Do(authedRequest(t, http.MethodGet, ts.URL+"/v1/trial-balance", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer balanceRes.Body.Close()
	var decoded struct {
		Total int64 `json:"total"`
	}
	if err := json.NewDecoder(balanceRes.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Total != 0 {
		t.Fatalf("total = %d", decoded.Total)
	}
}

func TestInvalidJSONIsRejected(t *testing.T) {
	ts := newTS(t, nil)
	res, err := http.DefaultClient.Do(authedRequest(t, http.MethodPost, ts.URL+"/v1/entries", `{`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestListIsPagedAndFullJournalConflicts(t *testing.T) {
	book := ledger.NewBookMax(1)
	ts := newTS(t, book)
	post := func() *http.Response {
		t.Helper()
		res, err := http.DefaultClient.Do(authedRequest(t, http.MethodPost, ts.URL+"/v1/entries", `{
			"lines": [
				{"account": "Cash", "debit": 10, "credit": 0},
				{"account": "Revenue", "debit": 0, "credit": 10}
			]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	first := post()
	first.Body.Close()
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first status = %d", first.StatusCode)
	}
	second := post()
	second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("second status = %d", second.StatusCode)
	}
	listed, err := http.DefaultClient.Do(authedRequest(t, http.MethodGet, ts.URL+"/v1/entries?offset=0&limit=1", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer listed.Body.Close()
	var body struct {
		Entries []struct {
			ID int64 `json:"id"`
		} `json:"entries"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(listed.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 1 || len(body.Entries) != 1 {
		t.Fatalf("page = %+v", body)
	}
}
