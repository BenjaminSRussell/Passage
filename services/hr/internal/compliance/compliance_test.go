package compliance

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	entity    = "00000000-0000-4000-8000-0000000e0001"
	candidate = "00000000-0000-4000-8000-0000000c0001"
)

func signer(t *testing.T) *tenant.ServiceSigner {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := tenant.NewServiceSigner("hr", key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ctx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{
		TenantID: "00000000-0000-4000-8000-000000000001", UserID: "system:hr-retention-purge",
		Duties: map[string]struct{}{tenant.DutyAllScope: {}},
	})
}

func serve(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1/privacy/holds:check" || q.Get("legalEntityId") != entity || q.Get("dataClass") != "CANDIDATE" || q.Get("recordRef") != candidate || r.Header.Get(tenant.HeaderServiceToken) == "" {
			t.Errorf("unexpected request %s with token %q", r.URL.String(), r.Header.Get(tenant.HeaderServiceToken))
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL).WithSigner(signer(t))
}

func TestCoveringCandidate(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    []string
		wantErr string
	}{
		{"no hold", 200, `{"held":false,"holdIds":[]}`, []string{}, ""},
		{"held", 200, `{"held":true,"holdIds":["h1","h2"]}`, []string{"h1", "h2"}, ""},
		{"held without ids still blocks", 200, `{"held":true}`, []string{"unnamed"}, ""},
		{"compliance down", 503, `{}`, nil, "unexpected status 503"},
		{"not a hold check", 200, `{"ok":true}`, nil, "not a hold check"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := serve(t, tc.status, tc.body).CoveringCandidate(ctx(), entity, candidate)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v err %v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestCoveringCandidateFailsClosedWithoutASigner(t *testing.T) {
	if _, err := New("http://compliance.invalid").CoveringCandidate(ctx(), entity, candidate); err == nil || !strings.Contains(err.Error(), "no service signing key") {
		t.Fatalf("err = %v, want a refusal without a signing key", err)
	}
}
