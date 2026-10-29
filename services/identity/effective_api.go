package identity

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	dutyViewEffectivePermissions = "View effective permissions"
	dutyMaintainUsers            = "Maintain users"
	codeInvalid                  = "COM-0001"
	codeUserNotFound             = "IDN-USER-NOT-FOUND"
)

// EffectivePermissionsAPI serves the read-only effective-permission report.
// A role assignment's role holds a duty name until the role catalogue exists
// (issue #83); roles that are not seeded duties are reported as ignored.
type EffectivePermissionsAPI struct {
	pool      *pgxpool.Pool
	catalogue *Catalogue
}

func NewEffectivePermissionsAPI(pool *pgxpool.Pool, catalogue *Catalogue) *EffectivePermissionsAPI {
	return &EffectivePermissionsAPI{pool: pool, catalogue: catalogue}
}

func requireAnyDuty(duties ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := tenant.FromContext(r.Context())
			if !ok {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
				return
			}
			for _, duty := range duties {
				if p.HasDuty(duty) {
					next.ServeHTTP(w, r)
					return
				}
			}
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "missing duty "+duties[0]))
		})
	}
}

func (a *EffectivePermissionsAPI) Register(r chi.Router) {
	r.With(requireAnyDuty(dutyViewEffectivePermissions, dutyMaintainUsers)).Get("/v1/identity/users/{id}/effective-permissions", a.get)
}

type effectivePermission struct {
	Resource    string `json:"resource"`
	Method      string `json:"method"`
	Allow       bool   `json:"allow"`
	Duty        string `json:"duty,omitempty"`
	DutyMissing string `json:"dutyMissing,omitempty"`
}

type effectivePermissionsResponse struct {
	UserID        string                `json:"userId"`
	LegalEntityID string                `json:"legalEntityId,omitempty"`
	Duties        []string              `json:"duties"`
	IgnoredRoles  []string              `json:"ignoredRoles"`
	Permissions   []effectivePermission `json:"permissions"`
}

const activeRolesQuery = `select distinct role from identity_role_assignments
where tenant_id = $1 and user_id = $2
  and valid_from <= now() and (valid_to is null or valid_to > now())
  and (legal_entity_id is null or legal_entity_id = $3::uuid)
order by role`

func (a *EffectivePermissionsAPI) get(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return
	}
	if p.LegalEntityID != "" && p.LegalEntityID != "*" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "effective permissions require access to all legal entities"))
		return
	}
	userID := chi.URLParam(r, "id")
	legalEntityID := r.URL.Query().Get("legalEntityId")
	fields := map[string]string{}
	if !ids.Valid(userID) {
		fields["id"] = "must be a UUID"
	}
	if legalEntityID != "" && !ids.Valid(legalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		problem := httpx.NewProblem(http.StatusBadRequest, codeInvalid, "user id and legalEntityId must be UUIDs")
		problem.Errors = fields
		httpx.Fail(w, r, problem)
		return
	}
	var legalEntity *string
	if legalEntityID != "" {
		legalEntity = &legalEntityID
	}

	var roles []string
	var exists bool
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `select exists(select 1 from identity_users where tenant_id = $1 and user_id = $2)`, p.TenantID, userID).Scan(&exists); err != nil || !exists {
			return err
		}
		rows, err := tx.Query(r.Context(), activeRolesQuery, p.TenantID, userID, legalEntity)
		if err != nil {
			return err
		}
		roles, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !exists {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUserNotFound, "user not found"))
		return
	}

	duties, ignored := []string{}, []string{}
	for _, role := range roles {
		if a.catalogue.HasDuty(role) {
			duties = append(duties, role)
		} else {
			ignored = append(ignored, role)
		}
	}
	grants := a.catalogue.Effective(duties)
	permissions := make([]effectivePermission, len(grants))
	for i, g := range grants {
		permissions[i] = effectivePermission{Resource: g.Resource, Method: g.Method, Allow: g.Allow, Duty: g.Duty, DutyMissing: g.DutyMissing}
	}
	httpx.JSON(w, http.StatusOK, effectivePermissionsResponse{
		UserID: userID, LegalEntityID: legalEntityID,
		Duties: duties, IgnoredRoles: ignored, Permissions: permissions,
	})
}
