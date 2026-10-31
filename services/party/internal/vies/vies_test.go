package vies_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/vies"
)

func recorded(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 4 || parts[0] != "ms" || parts[2] != "vat" {
			http.NotFound(w, r)
			return
		}
		body, err := os.ReadFile(filepath.Join("testdata", strings.ToLower(parts[1])+"_"+parts[3]+".json"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClient_RecordedVIESResponses(t *testing.T) {
	srv := recorded(t)
	for _, tc := range []struct {
		name, country, number string
		want                  domain.Status
	}{
		{"DE registered", "DE", "811128135", domain.StatusValid},
		{"FR registered", "FR", "40303265045", domain.StatusValid},
		{"NL registered", "NL", "820646660B01", domain.StatusValid},
		{"DE unregistered", "DE", "123456789", domain.StatusInvalid},
		{"ES unregistered", "ES", "A00000000", domain.StatusInvalid},
		{"DE malformed number", "DE", "12", domain.StatusInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := vies.New(srv.URL, time.Second).Check(context.Background(), tc.country, tc.number)
			if err != nil || got != tc.want {
				t.Fatalf("Check = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestClient_NoVerdictIsAnError(t *testing.T) {
	srv := recorded(t)
	if _, err := vies.New(srv.URL, time.Second).Check(context.Background(), "XX", "123"); err == nil {
		t.Fatal("recorded INVALID_INPUT response must not become a verdict")
	}
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"member state unavailable": {200, `{"isValid":false,"userError":"MS_UNAVAILABLE"}`},
		"concurrency limit":        {200, `{"isValid":false,"userError":"MS_MAX_CONCURRENT_REQ"}`},
		"timeout":                  {200, `{"isValid":false,"userError":"TIMEOUT"}`},
		"valid contradicts flag":   {200, `{"isValid":false,"userError":"VALID"}`},
		"invalid contradicts flag": {200, `{"isValid":true,"userError":"INVALID"}`},
		"service error status":     {503, `{"actionSucceed":false,"errorWrappers":[{"error":"SERVICE_UNAVAILABLE"}]}`},
		"html error page":          {200, `<html>Sorry</html>`},
	} {
		t.Run(name, func(t *testing.T) {
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer bad.Close()
			if status, err := vies.New(bad.URL, time.Second).Check(context.Background(), "DE", "811128135"); err == nil {
				t.Fatalf("Check = %q, nil; want an error", status)
			}
		})
	}
}

func TestClient_ValidateReportsOutageAsUnverified(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"isValid":false,"userError":"MS_UNAVAILABLE"}`))
	}))
	defer down.Close()
	got, err := domain.Validate(context.Background(), vies.New(down.URL, time.Second), domain.TaxVAT, "DE", "DE811128135")
	if err != nil || got != domain.StatusUnverified {
		t.Fatalf("Validate = %q, %v; want unverified", got, err)
	}
}

func TestClient_UnreachableIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	if _, err := vies.New(srv.URL, time.Second).Check(context.Background(), "DE", "811128135"); err == nil {
		t.Fatal("want a transport error")
	}
}

func TestClient_RefusesCrossHostRedirect(t *testing.T) {
	var reached bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.Write([]byte(`{"isValid":true,"userError":"VALID"}`))
	}))
	defer elsewhere.Close()
	redirector := httptest.NewServer(http.RedirectHandler(elsewhere.URL, http.StatusFound))
	defer redirector.Close()
	if status, err := vies.New(redirector.URL, time.Second).Check(context.Background(), "DE", "811128135"); err == nil || reached {
		t.Fatalf("Check = %q, %v, reached=%t; want an error and no request to the other host", status, err, reached)
	}
}

func TestClient_FollowsSameHostRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			w.Write([]byte(`{"isValid":true,"userError":"VALID"}`))
			return
		}
		http.Redirect(w, r, "/moved", http.StatusFound)
	}))
	defer srv.Close()
	if status, err := vies.New(srv.URL, time.Second).Check(context.Background(), "DE", "811128135"); err != nil || status != domain.StatusValid {
		t.Fatalf("Check = %q, %v; want valid", status, err)
	}
}
