package forecastclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mustNew(t *testing.T, base string) *Client {
	t.Helper()
	c, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewRefusesAnythingButAnAbsoluteHTTPURL(t *testing.T) {
	for _, u := range []string{"", "forecast:8080", "/v1", "ftp://forecast", "http://"} {
		if _, err := New(u); err == nil {
			t.Errorf("New(%q) accepted", u)
		}
	}
	if _, err := New(" https://forecast.passage.svc:8080/ "); err != nil {
		t.Errorf("https URL refused: %v", err)
	}
}

func TestGenerateRoundTrip(t *testing.T) {
	var got struct {
		Series []Series `json:"series"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/forecast/forecasts:generate" || r.Method != http.MethodPost {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"results":[{"itemId":"i","siteId":"s","model":"ets","shortHistory":false,"seasonLength":12,"forecast":[1.5,2.5],"mape":0.1,"wmape":null,"warnings":[]}]}`))
	}))
	defer srv.Close()
	res, err := mustNew(t, srv.URL+"/").Generate(context.Background(), []Series{{ItemID: "i", SiteID: "s", History: []float64{1, 2}, Model: "auto", HorizonPeriods: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Series) != 1 || got.Series[0].HorizonPeriods != 2 || len(res) != 1 || res[0].Forecast[1] != 2.5 || res[0].WMAPE != nil || *res[0].MAPE != 0.1 {
		t.Fatalf("sent %+v, got %+v", got, res)
	}
}

func TestGenerateRefusals(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"status":     func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"bad json":   func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{`)) },
		"short list": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"results":[]}`)) },
		"short path": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"results":[{"itemId":"i","siteId":"s","forecast":[1]}]}`))
		},
		"other series": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"results":[{"itemId":"j","siteId":"s","forecast":[1,2]}]}`))
		},
		"oversized": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"results":[{"itemId":"i","siteId":"s","forecast":[1,2],"warnings":["` + strings.Repeat("x", maxResponseBytes) + `"]}]}`))
		},
	}
	for name, h := range cases {
		srv := httptest.NewServer(h)
		_, err := mustNew(t, srv.URL).Generate(context.Background(), []Series{{ItemID: "i", SiteID: "s", History: []float64{1}, HorizonPeriods: 2}})
		srv.Close()
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := mustNew(t, "http://127.0.0.1:1").Generate(context.Background(), []Series{{HorizonPeriods: 1}}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("unreachable: %v", err)
	}
}
