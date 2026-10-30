package identity

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	dutyMaintainTenants       = "Maintain tenants"
	dutyMaintainLegalEntities = "Maintain legal entities"
	dutyMaintainSecurityRoles = "Maintain security roles"

	codeForbidden = "AUTH-0003"

	defaultPageSize = 50
	maxPageSize     = 200

	pgForeignKey = "23503"
	pgUnique     = "23505"
	pgExclusion  = "23P01"
	pgCheck      = "23514"
	pgDenied     = "42501"
)

type page[T any] struct {
	Items []T    `json:"items"`
	Next  string `json:"next,omitempty"`
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func failWrite(w http.ResponseWriter, r *http.Request, err error) {
	if pgCode(err) == pgDenied {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeForbidden, "outside the caller's legal-entity scope"))
		return
	}
	httpx.Fail(w, r, err)
}

func invalidFields(w http.ResponseWriter, r *http.Request, detail string, fields map[string]string) {
	problem := httpx.NewProblem(http.StatusBadRequest, codeInvalid, detail)
	problem.Errors = fields
	httpx.Fail(w, r, problem)
}

func allLegalEntities(p tenant.Principal) bool {
	return p.LegalEntityID == "" || p.LegalEntityID == "*"
}

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return p, false
	}
	return p, true
}

func actingPrincipal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := principal(w, r)
	if ok && p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no authenticated user to record as the actor"))
		return p, false
	}
	return p, ok
}

func pageParams(r *http.Request, fields map[string]string) (int, *string) {
	limit := defaultPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPageSize {
			fields["limit"] = "must be an integer from 1 to " + strconv.Itoa(maxPageSize)
		} else {
			limit = n
		}
	}
	var after *string
	if raw := r.URL.Query().Get("after"); raw != "" {
		if !ids.Valid(raw) {
			fields["after"] = "must be a UUID"
		} else {
			after = &raw
		}
	}
	return limit, after
}

func paged[T any](rows []T, limit int, id func(T) string) page[T] {
	if rows == nil {
		rows = []T{}
	}
	out := page[T]{Items: rows}
	if len(rows) > limit {
		out.Items = rows[:limit]
		out.Next = id(rows[limit-1])
	}
	return out
}

func record(ctx context.Context, tx pgx.Tx, tenantID, actor, entityType, entityID, verb string, data any) error {
	evt, err := events.New(events.Type("identity", entityType, verb, 1), "identity", entityID, tenantID, data)
	if err != nil {
		return err
	}
	evt.Actor = actor
	if _, err := tx.Exec(ctx, `insert into identity_audit (tenant_id, audit_id, actor, action, entity_type, entity_id, detail)
values ($1, $2, $3, $4, $5, $6, $7)`, tenantID, ids.New(), actor, entityType+"."+verb, entityType, entityID, evt.Data); err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, entityType, entityID, evt)
}
