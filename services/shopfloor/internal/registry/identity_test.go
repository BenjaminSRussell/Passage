package registry

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	operator = "0195f0a0-0000-7000-8000-000000000001"
	t1       = "0195f0a0-0000-7000-8000-0000000000a1"
	t2       = "0195f0a0-0000-7000-8000-0000000000a2"
	t3       = "0195f0a0-0000-7000-8000-0000000000a3"
)

func signer(t *testing.T) (*tenant.ServiceSigner, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := tenant.NewServiceSigner("shopfloor", priv)
	if err != nil {
		t.Fatal(err)
	}
	return s, pub
}

// The registry follows the cursor, asks for active tenants only, and signs the read for the operator tenant
// with the one registry duty.
func TestTenantsFollowsThePagesAndSignsTheRead(t *testing.T) {
	s, pub := signer(t)
	verifier := tenant.NewServiceVerifierKids("identity", map[string]map[string]ed25519.PublicKey{"shopfloor": {"": pub}})
	calls := 0
	srv := httptest.NewServer(tenant.MiddlewareWith(verifier)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		p, ok := tenant.FromContext(r.Context())
		if !ok || p.TenantID != operator || !p.HasDuty(Duty) || r.URL.Query().Get("status") != "active" || r.URL.Path != "/v1/tenants" {
			t.Errorf("request %s principal %+v ok=%v", r.URL, p, ok)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("after") == "" {
			_, _ = w.Write([]byte(`{"items":[{"tenantId":"` + t1 + `"},{"tenantId":"` + strings.ToUpper(t2) + `"}],"next":"` + t2 + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"tenantId":"` + t3 + `"}]}`))
	})))
	defer srv.Close()
	reg, err := New(srv.URL+"/", operator, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reg.Tenants(context.Background())
	if err != nil || len(got) != 3 || got[0] != t1 || got[1] != t2 || got[2] != t3 || calls != 2 {
		t.Fatalf("tenants %v err %v calls %d", got, err, calls)
	}
}

func TestTenantsReportsRefusalsAndBadAnswers(t *testing.T) {
	s, _ := signer(t)
	for name, handler := range map[string]http.HandlerFunc{
		"refused":  func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) },
		"not json": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("nope")) },
		"bad id":   func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"items":[{"tenantId":"x"}]}`)) },
		"endless": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"items":[],"next":"` + t1 + `"}`))
		},
	} {
		srv := httptest.NewServer(handler)
		reg, _ := New(srv.URL, operator, s, nil)
		if _, err := reg.Tenants(context.Background()); err == nil {
			t.Errorf("%s: accepted", name)
		}
		srv.Close()
	}
}

func TestNewNeedsItsSettings(t *testing.T) {
	s, _ := signer(t)
	for name, f := range map[string]func() error{
		"url":      func() error { _, err := New("", operator, s, nil); return err },
		"operator": func() error { _, err := New("http://i", "nope", s, nil); return err },
		"signer":   func() error { _, err := New("http://i", operator, nil, nil); return err },
	} {
		if f() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
