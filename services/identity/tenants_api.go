package identity

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	codeTenantNotFound = "IDN-TENANT-NOT-FOUND"
	codeTenantExists   = "IDN-TENANT-EXISTS"
	maxDisplayName     = 200
)

var realmNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

type TenantsAPI struct {
	pool             *pgxpool.Pool
	operatorTenantID string
}

// NewTenantsAPI serves tenant provisioning for the platform operator on the ordinary application pool. Only
// callers of operatorTenantID holding Maintain tenants are served. Create scopes its transaction to the new
// tenant id so the tenant row passes its own RLS check; list and get read through the identity_operator policy,
// so identity_operator must hold operatorTenantID (seeded by the migrator).
func NewTenantsAPI(pool *pgxpool.Pool, operatorTenantID string) *TenantsAPI {
	return &TenantsAPI{pool: pool, operatorTenantID: operatorTenantID}
}

func (a *TenantsAPI) operatorOnly(next http.Handler) http.Handler {
	return requireAnyDuty(dutyMaintainTenants)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := tenant.FromContext(r.Context())
		if !ids.Valid(p.TenantID) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
			return
		}
		if a.operatorTenantID == "" || p.TenantID != a.operatorTenantID {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeForbidden, "tenant administration is restricted to the platform operator"))
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (a *TenantsAPI) Register(r chi.Router) {
	r.With(a.operatorOnly).Post("/v1/tenants", a.create)
	r.With(a.operatorOnly).Get("/v1/tenants", a.list)
	r.With(a.operatorOnly).Get("/v1/tenants/{id}", a.get)
}

type tenantResource struct {
	TenantID    string    `json:"tenantId"`
	RealmName   string    `json:"realmName"`
	DisplayName string    `json:"displayName"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
}

const tenantColumns = `tenant_id::text, realm_name, display_name, status, created_at`

func scanTenant(row pgx.CollectableRow) (tenantResource, error) {
	var t tenantResource
	err := row.Scan(&t.TenantID, &t.RealmName, &t.DisplayName, &t.Status, &t.CreatedAt)
	return t, err
}

type createTenantRequest struct {
	RealmName   string `json:"realmName"`
	DisplayName string `json:"displayName"`
}

func (a *TenantsAPI) create(w http.ResponseWriter, r *http.Request) {
	p, ok := actingPrincipal(w, r)
	if !ok {
		return
	}
	var req createTenantRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	fields := map[string]string{}
	if !realmNamePattern.MatchString(req.RealmName) {
		fields["realmName"] = "must be 2-63 lowercase letters, digits or hyphens, starting with a letter or digit"
	}
	if req.DisplayName == "" || len(req.DisplayName) > maxDisplayName {
		fields["displayName"] = "must be 1-200 characters"
	}
	if len(fields) > 0 {
		invalidFields(w, r, "invalid tenant", fields)
		return
	}

	created := tenantResource{}
	tenantID := ids.New()
	err := pgx.BeginFunc(r.Context(), a.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `select set_config('passage.tenant_id', $1, true)`, tenantID); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `insert into identity_tenants (tenant_id, realm_name, display_name) values ($1, $2, $3) returning `+tenantColumns,
			tenantID, req.RealmName, req.DisplayName)
		if err != nil {
			return err
		}
		if created, err = pgx.CollectExactlyOneRow(rows, scanTenant); err != nil {
			return err
		}
		if err := record(r.Context(), tx, tenantID, p.UserID, "tenant", tenantID, "created", created); err != nil {
			return err
		}
		detail, err := json.Marshal(created)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `select set_config('passage.tenant_id', $1, true)`, p.TenantID); err != nil {
			return err
		}
		return insertAudit(r.Context(), tx, p.TenantID, p.UserID, "tenant", tenantID, "created", detail)
	})
	if pgCode(err) == pgUnique {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeTenantExists, "a tenant with this realm name already exists"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/tenants/"+tenantID)
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *TenantsAPI) list(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	fields := map[string]string{}
	limit, after := pageParams(r, fields)
	if len(fields) > 0 {
		invalidFields(w, r, "invalid paging parameters", fields)
		return
	}
	var items []tenantResource
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select `+tenantColumns+` from identity_tenants
where ($1::text is null or tenant_id > $1::uuid) order by tenant_id limit $2`, after, limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, scanTenant)
		return err
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, paged(items, limit, func(t tenantResource) string { return t.TenantID }))
}

func (a *TenantsAPI) get(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		invalidFields(w, r, "tenant id must be a UUID", map[string]string{"id": "must be a UUID"})
		return
	}
	var found tenantResource
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select `+tenantColumns+` from identity_tenants where tenant_id = $1`, id)
		if err != nil {
			return err
		}
		found, err = pgx.CollectExactlyOneRow(rows, scanTenant)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeTenantNotFound, "tenant not found"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, found)
}
