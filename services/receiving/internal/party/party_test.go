package party

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const (
	tenantID      = "00000000-0000-0000-0000-000000000001"
	legalEntityID = "00000000-0000-0000-0000-0000000000e1"
	vendorID      = "00000000-0000-4000-8000-0000000000a1"
	groupID       = "00000000-0000-4000-8000-0000000000c1"
)

func testCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: legalEntityID})
}

func serve(t *testing.T, status int, body string) (*Client, *http.Request) {
	t.Helper()
	var seen http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = *r.Clone(context.Background())
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, nil), &seen
}

func TestVendorGroupID_ReadsTheGroupFromTheVendorMaster(t *testing.T) {
	c, seen := serve(t, http.StatusOK, `{"id":"`+vendorID+`","legalEntityId":"`+legalEntityID+`","vendorGroupId":"`+groupID+`","blocked":"none"}`)
	got, err := c.VendorGroupID(testCtx(), legalEntityID, vendorID)
	if err != nil || got != groupID {
		t.Fatalf("group %q err %v, want %q", got, err, groupID)
	}
	if seen.Method != http.MethodGet || seen.URL.Path != "/v1/party/vendors/"+vendorID {
		t.Fatalf("request %s %s", seen.Method, seen.URL.Path)
	}
}

func TestVendorGroupID_MapsFailures(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		notFound bool
	}{
		{"unknown vendor", http.StatusNotFound, `{}`, true},
		{"forbidden", http.StatusForbidden, `{}`, false},
		{"server error", http.StatusInternalServerError, `{}`, false},
		{"not a vendor", http.StatusOK, `<html>`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := serve(t, tc.status, tc.body)
			got, err := c.VendorGroupID(testCtx(), legalEntityID, vendorID)
			if err == nil || got != "" || errors.Is(err, domain.ErrVendorNotFound) != tc.notFound {
				t.Fatalf("group %q err %v, want an error (not found: %v)", got, err, tc.notFound)
			}
		})
	}
}

func TestVendorGroupID_VendorOfAnotherLegalEntityReadsAsNotFound(t *testing.T) {
	c, _ := serve(t, http.StatusOK, `{"id":"`+vendorID+`","legalEntityId":"00000000-0000-0000-0000-0000000000e2","vendorGroupId":"`+groupID+`"}`)
	got, err := c.VendorGroupID(testCtx(), legalEntityID, vendorID)
	if got != "" || !errors.Is(err, domain.ErrVendorNotFound) {
		t.Fatalf("group %q err %v, want ErrVendorNotFound", got, err)
	}
}

func TestVendorGroupID_NeedsAPrincipal(t *testing.T) {
	c, _ := serve(t, http.StatusOK, `{}`)
	if _, err := c.VendorGroupID(context.Background(), legalEntityID, vendorID); err == nil {
		t.Fatal("lookup without a principal succeeded")
	}
}
