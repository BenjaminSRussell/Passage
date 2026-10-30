package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/numbering/internal/api"
	"github.com/BenjaminSRussell/passage/services/numbering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/numbering/internal/store"
	"github.com/BenjaminSRussell/passage/services/numbering/migrations"
)

func TestAllocateOverHTTPUnderAppRole(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	entity := testkit.TenantID(9)
	seeder := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entity})
	if err := dbx.Migrate(seeder, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	if _, err := s.Create(seeder, store.Sequence{Code: "PARTY", Name: "Party", Format: "PTY-{######}", LegalEntityID: entity, Mode: domain.ModeGapFree, StartFrom: 1}, ""); err != nil {
		t.Fatal(err)
	}
	r := httpx.NewRouter()
	api.New(s).Register(r)

	allocate := func(headers map[string]string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/numbering/sequences/PARTY/next", nil)
		req.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
		req.Header.Set(tenant.HeaderDuties, "numbering.write")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	tests := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"scoped caller", map[string]string{tenant.HeaderLegalEntity: entity}, http.StatusOK},
		{"all-scope caller naming the entity", map[string]string{
			tenant.HeaderLegalEntity: entity, tenant.HeaderDuties: "numbering.write," + tenant.DutyAllScope,
		}, http.StatusOK},
		{"all-scope caller without an entity", map[string]string{
			tenant.HeaderDuties: "numbering.write," + tenant.DutyAllScope, tenant.HeaderLegalEntityScopeAll: tenant.LegalEntityScopeAll,
		}, http.StatusForbidden},
		{"caller without an entity", nil, http.StatusForbidden},
		{"caller scoped to another entity", map[string]string{tenant.HeaderLegalEntity: testkit.TenantID(8)}, http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := allocate(tc.headers); got != tc.want {
				t.Fatalf("status %d, want %d", got, tc.want)
			}
		})
	}
}
