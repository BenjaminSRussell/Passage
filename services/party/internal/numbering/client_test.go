package numbering_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/numbering"
)

const entityID = "00000000-0000-0000-0000-0000000000e1"

func principalContext() context.Context {
	return tenant.With(context.Background(), tenant.Principal{
		TenantID: "00000000-0000-0000-0000-000000000001", LegalEntityID: entityID, UserID: "user-1",
		Duties: map[string]struct{}{"party.write": {}, "party.read": {}},
	})
}

func wantUnavailable(t *testing.T, err error) {
	t.Helper()
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != http.StatusServiceUnavailable || p.Code != numbering.CodeUnavailable {
		t.Fatalf("got %v, want 503 %s", err, numbering.CodeUnavailable)
	}
}

func TestNext_PostsWithPrincipalHeadersAndReturnsFormatted(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		_, _ = w.Write([]byte(`{"value":7,"formatted":"PTY-000007","sequence":"PARTY","legalEntityId":"x"}`))
	}))
	defer srv.Close()
	formatted, err := numbering.New(srv.URL+"/").Next(principalContext(), "PARTY", "key-1")
	if err != nil || formatted != "PTY-000007" {
		t.Fatalf("got %q, %v", formatted, err)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/v1/numbering/sequences/PARTY/next" {
		t.Fatalf("got %s %s", got.Method, got.URL.Path)
	}
	for header, want := range map[string]string{
		"Idempotency-Key": "key-1", "X-Passage-Tenant": "00000000-0000-0000-0000-000000000001",
		"X-Passage-Legal-Entity": entityID, "X-Passage-User": "user-1", "X-Passage-Duties": "party.read,party.write",
	} {
		if got.Header.Get(header) != want {
			t.Errorf("%s = %q, want %q", header, got.Header.Get(header), want)
		}
	}
}

func TestNext_FailuresAreUnavailable(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"server error", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }},
		{"not found", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }},
		{"malformed body", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) }},
		{"missing formatted", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"value":1}`)) }},
		{"timeout", func(w http.ResponseWriter, _ *http.Request) { time.Sleep(300 * time.Millisecond) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			_, err := numbering.NewWithTimeout(srv.URL, 100*time.Millisecond).Next(principalContext(), "PARTY", "k")
			wantUnavailable(t, err)
		})
	}
}

func TestNext_TransportErrorIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := numbering.New(url).Next(principalContext(), "PARTY", "k")
	wantUnavailable(t, err)
}

func TestUnavailable_AlwaysFails(t *testing.T) {
	_, err := numbering.Unavailable{}.Next(principalContext(), "PARTY", "k")
	wantUnavailable(t, err)
}

func TestNext_RequiresASingleLegalEntity(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++ }))
	defer srv.Close()
	for name, entity := range map[string]string{"none": "", "list": entityID + ",00000000-0000-0000-0000-0000000000e2"} {
		t.Run(name, func(t *testing.T) {
			ctx := tenant.With(context.Background(), tenant.Principal{TenantID: "00000000-0000-0000-0000-000000000001", LegalEntityID: entity})
			_, err := numbering.New(srv.URL).Next(ctx, "PARTY", "k")
			var p *httpx.Problem
			if !errors.As(err, &p) || p.Status != http.StatusUnprocessableEntity || p.Code != numbering.CodeEntityRequired {
				t.Fatalf("got %v, want 422 %s", err, numbering.CodeEntityRequired)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("numbering called %d times without a legal entity", calls)
	}
}
