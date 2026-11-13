package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPromSourceReadsTheThreeQueries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if !strings.Contains(q, "[6h]") {
			t.Errorf("window not in query: %s", q)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(q, "histogram_quantile"):
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"metric":{"job":"ledger"},"value":[1,"0.25"]},{"metric":{"job":"quiet"},"value":[1,"NaN"]}]}}`))
		case strings.Contains(q, `status=~"5.."`):
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"metric":{"job":"ledger"},"value":[1,"3"]}]}}`))
		default:
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"metric":{"job":"ledger"},"value":[1,"1000"]},{"metric":{"job":"quiet"},"value":[1,"0"]}]}}`))
		}
	}))
	defer srv.Close()
	samples, err := PromSource{URL: srv.URL}.Samples(context.Background(), 6*time.Hour)
	if err != nil || len(samples) != 2 {
		t.Fatalf("samples = %+v err=%v", samples, err)
	}
	for _, s := range samples {
		switch s.Service {
		case "ledger":
			if s.Requests != 1000 || s.Failed != 3 || s.P95 == nil || *s.P95 != 0.25 {
				t.Fatalf("ledger = %+v", s)
			}
		case "quiet":
			if s.P95 != nil {
				t.Fatalf("NaN p95 kept: %+v", s)
			}
		}
	}
	if promRange(30*24*time.Hour) != "30d" || promRange(time.Hour) != "1h" {
		t.Fatal("range selectors")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"error","error":"parse error"}`))
	}))
	defer bad.Close()
	if _, err := (PromSource{URL: bad.URL}).Samples(context.Background(), time.Hour); err == nil || !strings.Contains(err.Error(), "parse error") {
		t.Fatalf("error answer = %v", err)
	}
}
