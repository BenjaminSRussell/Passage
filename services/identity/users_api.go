package identity

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
)

const maxQuery = 100

// UsersAPI serves the read-only user directory of the caller's tenant. Users belong to the tenant, not to a
// legal entity, so every caller holding identity.user.maintain sees the whole tenant; the optional legalEntityId filter
// narrows to users with an active assignment for that entity, and is further bounded by the caller's own scope.
type UsersAPI struct {
	pool *pgxpool.Pool
}

func NewUsersAPI(pool *pgxpool.Pool) *UsersAPI {
	return &UsersAPI{pool: pool}
}

func (a *UsersAPI) Register(r chi.Router) {
	guard := requireAnyDuty(dutyMaintainUsers)
	r.With(guard).Get("/v1/identity/users", a.list)
	r.With(guard).Get("/v1/identity/users/{id}", a.get)
	r.With(requireAnyDuty(dutyReadUserNames, dutyMaintainUsers)).Get("/v1/identity/user-names", a.names)
}

// dutyReadUserNames lets an ordinary user see who did something: it returns display names only, never e-mail addresses,
// subjects or status, so it is safe to grant to every role (#548/#552).
const dutyReadUserNames = "identity.user-name.read"

// maxUserNameIDs bounds one batch lookup.
const maxUserNameIDs = 50

type userNameResource struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
}

// names resolves up to 50 user ids (`ids`, comma separated) of the caller's tenant to display names. Unknown ids are
// left out; deprovisioned users still resolve, because the page that names them is history.
func (a *UsersAPI) names(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var wanted []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(r.URL.Query().Get("ids"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		id, ok := canonicalUUID(raw)
		if !ok {
			invalidFields(w, r, "invalid user ids", map[string]string{"ids": "must be a comma-separated list of UUIDs"})
			return
		}
		if !seen[id] {
			seen[id] = true
			wanted = append(wanted, id)
		}
	}
	if len(wanted) == 0 || len(wanted) > maxUserNameIDs {
		invalidFields(w, r, "invalid user ids", map[string]string{"ids": "must list between 1 and 50 user ids"})
		return
	}
	items := []userNameResource{}
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select u.user_id::text, u.display_name from identity_users u
where u.tenant_id = $1 and u.user_id = any($2::uuid[]) order by u.user_id`, p.TenantID, wanted)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (userNameResource, error) {
			var n userNameResource
			return n, row.Scan(&n.UserID, &n.DisplayName)
		})
		return err
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

type userResource struct {
	UserID      string    `json:"userId"`
	IdpSubject  string    `json:"idpSubject"`
	Email       string    `json:"email,omitempty"`
	DisplayName string    `json:"displayName"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
}

const userColumns = `u.user_id::text, u.idp_subject, coalesce(u.email, ''), u.display_name, u.status, u.created_at`

func scanUser(row pgx.CollectableRow) (userResource, error) {
	var u userResource
	err := row.Scan(&u.UserID, &u.IdpSubject, &u.Email, &u.DisplayName, &u.Status, &u.CreatedAt)
	return u, err
}

func (a *UsersAPI) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	status, legalEntityID := query.Get("status"), query.Get("legalEntityId")
	q := strings.TrimSpace(query.Get("q"))
	fields := map[string]string{}
	limit, after := pageParams(r, fields)
	if status != "" && status != "active" && status != "deprovisioned" {
		fields["status"] = "must be active or deprovisioned"
	}
	if legalEntityID != "" {
		if canonical, ok := canonicalUUID(legalEntityID); ok {
			legalEntityID = canonical
		} else {
			fields["legalEntityId"] = "must be a UUID"
		}
	}
	if utf8.RuneCountInString(q) > maxQuery {
		fields["q"] = "must be at most 100 characters"
	}
	if len(fields) > 0 {
		invalidFields(w, r, "invalid user filter", fields)
		return
	}
	var items []userResource
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select `+userColumns+` from identity_users u
where u.tenant_id = $1
  and ($2::text = '' or u.status = $2::text)
  and ($3::text = '' or strpos(lower(u.display_name || ' ' || coalesce(u.email, '') || ' ' || u.idp_subject), lower($3::text)) > 0)
  and ($4::text = '' or exists (select 1 from identity_role_assignments a
       where a.tenant_id = u.tenant_id and a.user_id = u.user_id and a.legal_entity_id = nullif($4::text, '')::uuid
	       and a.valid_from <= now() and (a.valid_to is null or a.valid_to > now())))
  and ($5::text is null or u.user_id > $5::uuid)
order by u.user_id limit $6`, p.TenantID, status, q, legalEntityID, after, limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, scanUser)
		return err
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, paged(items, limit, func(u userResource) string { return u.UserID }))
}

func (a *UsersAPI) get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !validUUID(id) {
		invalidFields(w, r, "user id must be a UUID", map[string]string{"id": "must be a UUID"})
		return
	}
	var found userResource
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select `+userColumns+` from identity_users u where u.tenant_id = $1 and u.user_id = $2`, p.TenantID, id)
		if err != nil {
			return err
		}
		found, err = pgx.CollectExactlyOneRow(rows, scanUser)
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
	httpx.JSON(w, http.StatusOK, found)
}
