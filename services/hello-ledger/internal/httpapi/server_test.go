package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthAndBalancedPost(t *testing.T) {
	ts := httptest.NewServer(NewServer().Handler())
	t.Cleanup(ts.Close)

	healthRes, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer healthRes.Body.Close()
	if healthRes.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", healthRes.StatusCode)
	}

	postRes, err := http.Post(ts.URL+"/v1/entries", "application/json", strings.NewReader(`{
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

	balanceRes, err := http.Get(ts.URL + "/v1/trial-balance")
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

func TestUnbalancedPostIsRejected(t *testing.T) {
	ts := httptest.NewServer(NewServer().Handler())
	t.Cleanup(ts.Close)
	res, err := http.Post(ts.URL+"/v1/entries", "application/json", strings.NewReader(`{
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
