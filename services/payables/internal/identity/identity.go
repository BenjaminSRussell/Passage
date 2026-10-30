package identity

import (
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func Forward(req *http.Request, p tenant.Principal) {
	req.Header.Set(tenant.HeaderTenant, p.TenantID)
	if p.LegalEntityID != "" {
		req.Header.Set(tenant.HeaderLegalEntity, p.LegalEntityID)
	}
	if p.UserID != "" {
		req.Header.Set(tenant.HeaderUser, p.UserID)
	}
	if len(p.Duties) > 0 {
		req.Header.Set(tenant.HeaderDuties, strings.Join(slices.Sorted(maps.Keys(p.Duties)), ","))
	}
	if id := middleware.GetReqID(req.Context()); id != "" {
		req.Header.Set(middleware.RequestIDHeader, id)
	}
}
