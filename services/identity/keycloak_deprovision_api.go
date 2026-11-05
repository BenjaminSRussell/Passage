package identity

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
)

const (
	codeDeprovisionNotFound = "IDN-KEYCLOAK-DEPROVISION-NOT-FOUND"
	codeDeprovisionDone     = "IDN-KEYCLOAK-DEPROVISION-DONE"
)

// KeycloakDeprovisionsAPI shows the Keycloak calls queued for a user by SCIM deprovisioning and lets a tenant
// administrator retry a parked one once its cause (a missing client secret, a client without manage-users, a
// duplicate username) is fixed. It sits under /v1/identity/users, so identity.user.maintain covers it.
type KeycloakDeprovisionsAPI struct {
	pool *pgxpool.Pool
}

func NewKeycloakDeprovisionsAPI(pool *pgxpool.Pool) *KeycloakDeprovisionsAPI {
	return &KeycloakDeprovisionsAPI{pool: pool}
}

func (a *KeycloakDeprovisionsAPI) Register(r chi.Router) {
	guard := requireAnyDuty(dutyMaintainUsers)
	r.With(guard).Get("/v1/identity/users/{id}/keycloak-deprovisions", a.list)
	// chi cannot match a literal ':' after a path parameter; action splits {ref} into id and verb.
	r.With(guard).Post("/v1/identity/users/{id}/keycloak-deprovisions/{ref}", a.action)
}

type keycloakDeprovision struct {
	DeprovisionID  string     `json:"deprovisionId"`
	UserID         string     `json:"userId"`
	Reason         string     `json:"reason"`
	State          string     `json:"state"`
	Outcome        string     `json:"outcome,omitempty"`
	Attempts       int        `json:"attempts"`
	LastError      string     `json:"lastError,omitempty"`
	KeycloakUserID string     `json:"keycloakUserId,omitempty"`
	RequestedBy    string     `json:"requestedBy"`
	RequestedAt    time.Time  `json:"requestedAt"`
	NextAttemptAt  *time.Time `json:"nextAttemptAt,omitempty"`
	CompletedAt    *time.Time `json:"completedAt,omitempty"`
	ParkedAt       *time.Time `json:"parkedAt,omitempty"`
}

const keycloakDeprovisionColumns = `deprovision_id::text, user_id::text, reason, coalesce(outcome, ''), attempts, coalesce(last_error, ''),
  coalesce(keycloak_user_id, ''), requested_by, requested_at, next_attempt_at, completed_at, parked_at`

func scanKeycloakDeprovision(row pgx.CollectableRow) (keycloakDeprovision, error) {
	var d keycloakDeprovision
	var next time.Time
	if err := row.Scan(&d.DeprovisionID, &d.UserID, &d.Reason, &d.Outcome, &d.Attempts, &d.LastError, &d.KeycloakUserID, &d.RequestedBy,
		&d.RequestedAt, &next, &d.CompletedAt, &d.ParkedAt); err != nil {
		return d, err
	}
	switch {
	case d.CompletedAt != nil:
		d.State = "done"
	case d.ParkedAt != nil:
		d.State = "parked"
	default:
		d.State = "pending"
		d.NextAttemptAt = &next
	}
	return d, nil
}

func (a *KeycloakDeprovisionsAPI) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	userID, ok := canonicalUUID(chi.URLParam(r, "id"))
	if !ok {
		invalidFields(w, r, "user id must be a UUID", map[string]string{"id": "must be a UUID"})
		return
	}
	var items []keycloakDeprovision
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(r.Context(), `select exists (select 1 from identity_users where tenant_id = $1 and user_id = $2)`, p.TenantID, userID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return pgx.ErrNoRows
		}
		rows, err := tx.Query(r.Context(), `select `+keycloakDeprovisionColumns+` from identity_keycloak_deprovisions
where tenant_id = $1 and user_id = $2 order by requested_at desc, deprovision_id`, p.TenantID, userID)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, scanKeycloakDeprovision)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUserNotFound, "user not found"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if items == nil {
		items = []keycloakDeprovision{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// action handles POST .../keycloak-deprovisions/{id}:retry. A parked or pending call becomes due now with its
// attempt count reset; the retry is audited. It needs a tenant administrator (tenant.administer, no legal-entity
// claim): it changes when the tenant's users are cut off from Keycloak.
func (a *KeycloakDeprovisionsAPI) action(w http.ResponseWriter, r *http.Request) {
	p, ok := actingPrincipal(w, r)
	if !ok {
		return
	}
	userID, userOK := canonicalUUID(chi.URLParam(r, "id"))
	ref, verb, _ := strings.Cut(chi.URLParam(r, "ref"), ":")
	id, idOK := canonicalUUID(ref)
	if !userOK || !idOK || verb != "retry" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeDeprovisionNotFound, "keycloak deprovisioning not found"))
		return
	}
	var result keycloakDeprovision
	err := dbx.WithTenantWideAdminTx(r.Context(), a.pool, p, dutyMaintainUsers, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select `+keycloakDeprovisionColumns+` from identity_keycloak_deprovisions
where tenant_id = $1 and user_id = $2 and deprovision_id = $3 for update`, p.TenantID, userID, id)
		if err != nil {
			return err
		}
		cur, err := pgx.CollectExactlyOneRow(rows, scanKeycloakDeprovision)
		if err != nil {
			return err
		}
		if cur.State == "done" {
			return httpx.NewProblem(http.StatusConflict, codeDeprovisionDone, "this keycloak deprovisioning already finished ("+cur.Outcome+")")
		}
		rows, err = tx.Query(r.Context(), `update identity_keycloak_deprovisions set parked_at = null, attempts = 0, next_attempt_at = now()
where tenant_id = $1 and deprovision_id = $2 returning `+keycloakDeprovisionColumns, p.TenantID, id)
		if err != nil {
			return err
		}
		if result, err = pgx.CollectExactlyOneRow(rows, scanKeycloakDeprovision); err != nil {
			return err
		}
		return record(r.Context(), tx, p.TenantID, p.UserID, "keycloakdeprovision", id, "retried", map[string]any{
			"deprovisionId": id, "userId": userID, "previousState": cur.State, "previousAttempts": cur.Attempts, "lastError": cur.LastError,
		})
	})
	switch {
	case errors.Is(err, dbx.ErrNotTenantWide):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeForbidden, "retrying a keycloak deprovisioning needs a tenant administrator"))
	case errors.Is(err, pgx.ErrNoRows):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeDeprovisionNotFound, "keycloak deprovisioning not found"))
	case err != nil:
		httpx.Fail(w, r, err)
	default:
		httpx.JSON(w, http.StatusOK, result)
	}
}
