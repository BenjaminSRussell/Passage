package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

const scimUserSelect = `select u.user_id::text, u.user_name, coalesce(u.external_id, ''), u.display_name, coalesce(u.email, ''), u.status = 'active', u.created_at,
  coalesce((select json_agg(json_build_object('value', g.group_id::text, 'display', g.display_name) order by g.display_name)
    from identity_scim_group_members m join identity_scim_groups g on g.tenant_id = m.tenant_id and g.group_id = m.group_id
    where m.tenant_id = u.tenant_id and m.user_id = u.user_id), '[]'::json)
from identity_users u where u.tenant_id = $1 and u.user_name is not null`

type scimUser struct {
	ID           string
	UserName     string
	ExternalID   string
	DisplayName  string
	Email        string
	Active       bool
	CreatedAt    time.Time
	Groups       []scimRef
	nameFallback string
}

type scimEmail struct {
	Value   string `json:"value"`
	Type    string `json:"type,omitempty"`
	Primary bool   `json:"primary"`
}

type scimUserResource struct {
	Schemas     []string    `json:"schemas"`
	ID          string      `json:"id"`
	ExternalID  string      `json:"externalId,omitempty"`
	UserName    string      `json:"userName"`
	DisplayName string      `json:"displayName"`
	Emails      []scimEmail `json:"emails,omitempty"`
	Active      bool        `json:"active"`
	Groups      []scimRef   `json:"groups,omitempty"`
	Meta        scimMeta    `json:"meta"`
}

func (u scimUser) resource() scimUserResource {
	out := scimUserResource{Schemas: []string{schemaUser}, ID: u.ID, ExternalID: u.ExternalID, UserName: u.UserName, DisplayName: u.DisplayName,
		Active: u.Active, Meta: scimMeta{ResourceType: "User", Created: u.CreatedAt.UTC().Format(time.RFC3339), Location: scimLocation("Users", u.ID)}}
	if u.Email != "" {
		out.Emails = []scimEmail{{Value: u.Email, Type: "work", Primary: true}}
	}
	for _, g := range u.Groups {
		out.Groups = append(out.Groups, scimRef{Value: g.Value, Display: g.Display, Ref: scimLocation("Groups", g.Value)})
	}
	return out
}

func scanScimUser(row pgx.CollectableRow) (scimUser, error) {
	var u scimUser
	err := row.Scan(&u.ID, &u.UserName, &u.ExternalID, &u.DisplayName, &u.Email, &u.Active, &u.CreatedAt, &u.Groups)
	return u, err
}

func parseActive(raw json.RawMessage) (bool, error) {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
	}
	return false, badRequest("invalidValue", "active must be true or false")
}

func parseEmail(raw json.RawMessage) (string, error) {
	if s, err := rawString(raw); err == nil {
		return s, nil
	}
	type entry struct {
		Value   string `json:"value"`
		Primary bool   `json:"primary"`
	}
	var entries []entry
	if json.Unmarshal(raw, &entries) != nil {
		var one entry
		if json.Unmarshal(raw, &one) != nil {
			return "", badRequest("invalidValue", "emails must be a list of {value} objects")
		}
		entries = []entry{one}
	}
	chosen := ""
	for i, e := range entries {
		if i == 0 || e.Primary {
			chosen = strings.TrimSpace(e.Value)
		}
		if e.Primary {
			break
		}
	}
	return chosen, nil
}

func (u *scimUser) set(op, attr string, raw json.RawMessage) error {
	var err error
	if op == "remove" {
		raw = jsonNull
	}
	switch attr {
	case "username":
		u.UserName, err = rawString(raw)
	case "displayname":
		u.DisplayName, err = rawString(raw)
	case "externalid":
		u.ExternalID, err = rawString(raw)
	case "active":
		u.Active, err = parseActive(raw)
	case "emails", "emails.value":
		u.Email, err = parseEmail(raw)
	case "name":
		var n struct{ Formatted, GivenName, FamilyName string }
		if json.Unmarshal(raw, &n) == nil {
			u.nameFallback = strings.TrimSpace(firstNonEmpty(n.Formatted, n.GivenName+" "+n.FamilyName))
		}
	}
	return err
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (u *scimUser) validate() error {
	if u.DisplayName == "" {
		u.DisplayName = firstNonEmpty(u.nameFallback, u.UserName)
	}
	if u.UserName == "" {
		return badRequest("invalidValue", "userName is required")
	}
	for _, check := range []error{tooLong("userName", u.UserName, scimMaxAttr), tooLong("displayName", u.DisplayName, scimMaxAttr),
		tooLong("externalId", u.ExternalID, scimMaxAttr), tooLong("emails", u.Email, scimMaxEmail)} {
		if check != nil {
			return check
		}
	}
	return nil
}

func userFromBody(body map[string]json.RawMessage) (scimUser, error) {
	u := scimUser{Active: true}
	for key, raw := range body {
		if err := u.set("replace", attrName(key), raw); err != nil {
			return u, err
		}
	}
	return u, u.validate()
}

func loadScimUser(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (scimUser, error) {
	query := scimUserSelect + ` and u.user_id = $2`
	if lock {
		query += ` for update of u`
	}
	rows, err := tx.Query(ctx, query, tenantID, id)
	if err != nil {
		return scimUser{}, err
	}
	u, err := pgx.CollectExactlyOneRow(rows, scanScimUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, scimNotFound("user")
	}
	return u, err
}

func scimUserID(r *http.Request) (string, error) {
	id, ok := canonicalUUID(chi.URLParam(r, "id"))
	if !ok {
		return "", scimNotFound("user")
	}
	return id, nil
}

type userEvent struct {
	UserID     string `json:"userId"`
	UserName   string `json:"userName"`
	ExternalID string `json:"externalId,omitempty"`
	Status     string `json:"status"`
}

func recordUser(ctx context.Context, tx pgx.Tx, tenantID, actor, verb string, u scimUser) error {
	status := "active"
	if !u.Active {
		status = "deprovisioned"
	}
	return record(ctx, tx, tenantID, actor, "user", u.ID, verb, userEvent{UserID: u.ID, UserName: u.UserName, ExternalID: u.ExternalID, Status: status})
}

func uniqueUserName(err error) error {
	if pgCode(err) == pgUnique {
		return scimConflict("userName is already in use")
	}
	return err
}

func (a *SCIMAPI) listUsers(w http.ResponseWriter, r *http.Request) {
	p := scimPrincipal(r)
	start, count, err := scimPaging(r)
	var attr, value string
	if err == nil {
		attr, value, err = scimFilter(r, "userName", "externalId")
	}
	if err != nil {
		scimFail(w, r, err)
		return
	}
	const where = ` and ($2::text = '' or case $2::text when 'username' then lower(u.user_name) = lower($3::text) else u.external_id = $3::text end)`
	var total int
	var users []scimUser
	err = a.inTx(r, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `select count(*) from identity_users u where u.tenant_id = $1 and u.user_name is not null`+where, p.TenantID, attr, value).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), scimUserSelect+where+` order by u.user_id offset $4 limit $5`, p.TenantID, attr, value, start-1, count)
		if err != nil {
			return err
		}
		users, err = pgx.CollectRows(rows, scanScimUser)
		return err
	})
	if err != nil {
		scimFail(w, r, err)
		return
	}
	resources := make([]scimUserResource, 0, len(users))
	for _, u := range users {
		resources = append(resources, u.resource())
	}
	writeSCIM(w, http.StatusOK, newList(total, start, len(resources), resources))
}

func (a *SCIMAPI) getUser(w http.ResponseWriter, r *http.Request) {
	id, err := scimUserID(r)
	var u scimUser
	if err == nil {
		err = a.inTx(r, func(tx pgx.Tx) error {
			var loadErr error
			u, loadErr = loadScimUser(r.Context(), tx, scimPrincipal(r).TenantID, id, false)
			return loadErr
		})
	}
	if err != nil {
		scimFail(w, r, err)
		return
	}
	writeSCIM(w, http.StatusOK, u.resource())
}

func (a *SCIMAPI) createUser(w http.ResponseWriter, r *http.Request) {
	p := scimPrincipal(r)
	body, err := decodeSCIMObject(w, r)
	var u scimUser
	if err == nil {
		u, err = userFromBody(body)
	}
	if err != nil {
		scimFail(w, r, err)
		return
	}
	u.ID = ids.New()
	status := "deprovisioned"
	if u.Active {
		status = "active"
	}
	err = a.inTx(r, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `insert into identity_users (tenant_id, user_id, idp_subject, user_name, external_id, display_name, email, status)
values ($1, $2, $3, $4, nullif($5, ''), $6, nullif($7, ''), $8) returning created_at`,
			p.TenantID, u.ID, "scim:"+u.ID, u.UserName, u.ExternalID, u.DisplayName, u.Email, status).Scan(&u.CreatedAt); err != nil {
			return uniqueUserName(err)
		}
		return recordUser(r.Context(), tx, p.TenantID, p.UserID, "provisioned", u)
	})
	if err != nil {
		scimFail(w, r, err)
		return
	}
	w.Header().Set("Location", scimLocation("Users", u.ID))
	writeSCIM(w, http.StatusCreated, u.resource())
}

func (a *SCIMAPI) replaceUser(w http.ResponseWriter, r *http.Request) {
	id, err := scimUserID(r)
	var next scimUser
	if err == nil {
		var body map[string]json.RawMessage
		if body, err = decodeSCIMObject(w, r); err == nil {
			next, err = userFromBody(body)
		}
	}
	a.saveUser(w, r, id, err, func(scimUser) (scimUser, error) { return next, nil })
}

func (a *SCIMAPI) patchUser(w http.ResponseWriter, r *http.Request) {
	id, err := scimUserID(r)
	var ops []patchOp
	if err == nil {
		ops, err = decodePatch(w, r)
	}
	a.saveUser(w, r, id, err, func(cur scimUser) (scimUser, error) {
		next := cur
		next.nameFallback = ""
		if err := applyOperations(ops, next.set); err != nil {
			return cur, err
		}
		return next, next.validate()
	})
}

func (a *SCIMAPI) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := scimUserID(r)
	a.saveUser(w, r, id, err, func(cur scimUser) (scimUser, error) {
		cur.Active = false
		return cur, nil
	})
}

func (a *SCIMAPI) saveUser(w http.ResponseWriter, r *http.Request, id string, prior error, change func(scimUser) (scimUser, error)) {
	if prior != nil {
		scimFail(w, r, prior)
		return
	}
	p := scimPrincipal(r)
	var result scimUser
	err := a.inTx(r, func(tx pgx.Tx) error {
		cur, err := loadScimUser(r.Context(), tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		next, err := change(cur)
		if err != nil {
			return err
		}
		next.ID, next.CreatedAt, next.Groups = cur.ID, cur.CreatedAt, cur.Groups
		result = next
		return writeUserChange(r.Context(), tx, p.TenantID, p.UserID, cur, next)
	})
	if err != nil {
		scimFail(w, r, err)
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeSCIM(w, http.StatusOK, result.resource())
}

func writeUserChange(ctx context.Context, tx pgx.Tx, tenantID, actor string, cur, next scimUser) error {
	if cur.UserName == next.UserName && cur.ExternalID == next.ExternalID && cur.DisplayName == next.DisplayName && cur.Email == next.Email && cur.Active == next.Active {
		return nil
	}
	status := "deprovisioned"
	if next.Active {
		status = "active"
	}
	if _, err := tx.Exec(ctx, `update identity_users set user_name = $3, external_id = nullif($4, ''), display_name = $5, email = nullif($6, ''), status = $7
where tenant_id = $1 and user_id = $2`, tenantID, next.ID, next.UserName, next.ExternalID, next.DisplayName, next.Email, status); err != nil {
		return uniqueUserName(err)
	}
	switch {
	case cur.Active && !next.Active:
		if err := endAssignments(ctx, tx, tenantID, actor, next.ID); err != nil {
			return err
		}
		return recordUser(ctx, tx, tenantID, actor, "deprovisioned", next)
	case !cur.Active && next.Active:
		return recordUser(ctx, tx, tenantID, actor, "reactivated", next)
	}
	return recordUser(ctx, tx, tenantID, actor, "updated", next)
}

func endAssignments(ctx context.Context, tx pgx.Tx, tenantID, actor, userID string) error {
	rows, err := tx.Query(ctx, `update identity_role_assignments set valid_to = greatest(now(), valid_from)
where tenant_id = $1 and user_id = $2 and (valid_to is null or valid_to > greatest(now(), valid_from)) returning `+assignmentColumns, tenantID, userID)
	if err != nil {
		return err
	}
	ended, err := pgx.CollectRows(rows, scanAssignment)
	if err != nil {
		return err
	}
	for _, a := range ended {
		if err := record(ctx, tx, tenantID, actor, "roleassignment", a.AssignmentID, "ended", a); err != nil {
			return err
		}
	}
	return nil
}
