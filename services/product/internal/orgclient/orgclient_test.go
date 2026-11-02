package orgclient_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/product/internal/orgclient"
	"github.com/BenjaminSRussell/passage/services/product/internal/upstream"
)

func TestCheckerMapsStatuses(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{"ok", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"id":"s1","legalEntityId":"le1","siteId":"s1"}`))
		}, nil},
		{"not found", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "{}", http.StatusNotFound) }, orgclient.ErrNotFound},
		{"server error", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "{}", http.StatusBadGateway) }, orgclient.ErrUnavailable},
		{"forbidden fails closed", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "{}", http.StatusForbidden) }, orgclient.ErrUnavailable},
		{"bad body", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`not json`)) }, orgclient.ErrUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			c := orgclient.New(srv.URL)
			if _, err := c.Site(context.Background(), "s1"); !errors.Is(err, tc.want) {
				t.Fatalf("Site err = %v, want %v", err, tc.want)
			}
			if _, err := c.Warehouse(context.Background(), "w1"); !errors.Is(err, tc.want) {
				t.Fatalf("Warehouse err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCheckerDecodesFieldsAndForwardsHeaders(t *testing.T) {
	var gotPath string
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotHeaders = r.URL.Path, r.Header
		_, _ = w.Write([]byte(`{"id":"w1","legalEntityId":"le1","siteId":"s1"}`))
	}))
	defer srv.Close()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: "t1", LegalEntityID: "le1", UserID: "u1", Duties: map[string]struct{}{"org.read": {}}})
	ctx = upstream.WithAuthorization(ctx, "Bearer abc")
	wh, err := orgclient.New(srv.URL+"/").Warehouse(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if wh != (orgclient.Warehouse{ID: "w1", LegalEntityID: "le1", SiteID: "s1"}) {
		t.Fatalf("warehouse = %+v", wh)
	}
	if gotPath != "/v1/org/warehouses/w1" {
		t.Fatalf("path = %s", gotPath)
	}
	for header, want := range map[string]string{
		tenant.HeaderTenant: "t1", tenant.HeaderLegalEntity: "le1", tenant.HeaderUser: "u1",
		tenant.HeaderDuties: "org.read", "Authorization": "Bearer abc",
	} {
		if got := gotHeaders.Get(header); got != want {
			t.Errorf("header %s = %q, want %q", header, got, want)
		}
	}
}

func TestCheckerTimeoutAndUnreachableAreUnavailable(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := orgclient.New(srv.URL).Site(ctx, "s1"); !errors.Is(err, orgclient.ErrUnavailable) {
		t.Fatalf("timeout err = %v", err)
	}
	if _, err := orgclient.New("http://127.0.0.1:1").Site(context.Background(), "s1"); !errors.Is(err, orgclient.ErrUnavailable) {
		t.Fatalf("unreachable err = %v", err)
	}
}

func TestCheckerEscapesIDs(t *testing.T) {
	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.EscapedPath()
		http.Error(w, "{}", http.StatusNotFound)
	}))
	defer srv.Close()
	_, _ = orgclient.New(srv.URL).Site(context.Background(), "a/b")
	if !strings.HasSuffix(raw, "/a%2Fb") {
		t.Fatalf("path = %s", raw)
	}
}

func TestServiceCallerIsSignedOrRefusedBeforeAnyRequest(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"id":"s1","legalEntityId":"le1"}`))
	}))
	defer srv.Close()
	loader, err := tenant.ServicePrincipal("migration-loader", "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000009")
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), loader)
	if _, err := orgclient.New(srv.URL).Site(ctx, "s1"); !errors.Is(err, orgclient.ErrServiceCaller) {
		t.Fatalf("unsigned service caller err = %v, want ErrServiceCaller", err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("product", priv)
	if err != nil {
		t.Fatal(err)
	}
	_, err = orgclient.NewSigned(srv.URL, signer).Warehouse(ctx, "w1")
	if len(tenant.ServiceGrantTable()["product"]["org"]) == 0 {
		if !errors.Is(err, orgclient.ErrServiceCaller) || !errors.Is(err, tenant.ErrNoServiceGrant) {
			t.Fatalf("service caller without a product -> org grant err = %v, want ErrServiceCaller wrapping ErrNoServiceGrant", err)
		}
		if hits != 0 {
			t.Fatalf("no request may reach org for an unsignable service caller, got %d", hits)
		}
	} else if err != nil || hits != 1 {
		t.Fatalf("granted service caller err = %v hits=%d", err, hits)
	}
}
