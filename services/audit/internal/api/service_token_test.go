package api_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/phiaudit"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/audit/internal/api"
)

// TestReadsFromCallersWithoutALegalEntityAreAccepted: hr records reads for ALL-scope and tenant-administrator callers
// with a token that names no legal entity; the audit service's own middleware must accept it and take the reader from
// it, or those callers can never read PHI.
func TestReadsFromCallersWithoutALegalEntityAreAccepted(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("hr", priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier := tenant.NewServiceVerifierKids("audit", map[string]map[string]ed25519.PublicKey{"hr": {"": pub}})
	f := &fakeStore{}
	r := chi.NewRouter()
	r.Use(tenant.MiddlewareWith(verifier))
	api.New(f).Register(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	allScope := tenant.Principal{TenantID: testkit.TenantID(1), UserID: "hr-lead",
		Duties: map[string]struct{}{tenant.DutyAllScope: {}, "hr.worker.inquire": {}}}
	err = phiaudit.NewHTTP(srv.URL, signer).RecordRead(tenant.With(context.Background(), allScope), phiaudit.Read{
		Entity: "hr.worker.benefits", RecordID: "w-1", Fields: []string{"enrollments.optionCode"}, Purpose: "open enrollment review",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.rows) != 1 || f.rows[0].UserID != "hr-lead" || f.rows[0].SourceService != "hr" {
		t.Fatalf("rows %+v", f.rows)
	}
}
