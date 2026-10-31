package identity

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

const scimGroupSelect = `select g.group_id::text, g.display_name, coalesce(g.external_id, ''), g.created_at,
  coalesce((select json_agg(json_build_object('value', m.user_id::text, 'display', u.display_name) order by m.user_id)
    from identity_scim_group_members m join identity_users u on u.tenant_id = m.tenant_id and u.user_id = m.user_id
    where m.tenant_id = g.tenant_id and m.group_id = g.group_id), '[]'::json)
from identity_scim_groups g where g.tenant_id = $1`

type scimGroup struct {
	ID          string
	DisplayName string
	ExternalID  string
	CreatedAt   time.Time
	Members     []scimRef
	memberIDs   map[string]struct{}
}

type scimGroupResource struct {
	Schemas     []string  `json:"schemas"`
	ID          string    `json:"id"`
	ExternalID  string    `json:"externalId,omitempty"`
	DisplayName string    `json:"displayName"`
	Members     []scimRef `json:"members,omitempty"`
	Meta        scimMeta  `json:"meta"`
}

func (g scimGroup) resource() scimGroupResource {
	out := scimGroupResource{Schemas: []string{schemaGroup}, ID: g.ID, ExternalID: g.ExternalID, DisplayName: g.DisplayName,
		Meta: scimMeta{ResourceType: "Group", Created: g.CreatedAt.UTC().Format(time.RFC3339), Location: scimLocation("Groups", g.ID)}}
	for _, m := range g.Members {
		out.Members = append(out.Members, scimRef{Value: m.Value, Display: m.Display, Ref: scimLocation("Users", m.Value)})
	}
	return out
}

func scanScimGroup(row pgx.CollectableRow) (scimGroup, error) {
	var g scimGroup
	if err := row.Scan(&g.ID, &g.DisplayName, &g.ExternalID, &g.CreatedAt, &g.Members); err != nil {
		return g, err
	}
	g.memberIDs = make(map[string]struct{}, len(g.Members))
	for _, m := range g.Members {
		g.memberIDs[m.Value] = struct{}{}
	}
	return g, nil
}

func (g scimGroup) clone() scimGroup {
	g.memberIDs = maps.Clone(g.memberIDs)
	if g.memberIDs == nil {
		g.memberIDs = map[string]struct{}{}
	}
	return g
}

func parseMembers(raw json.RawMessage) ([]string, error) {
	var entries []struct {
		Value string `json:"value"`
	}
	if string(raw) == "null" || len(raw) == 0 {
		return nil, nil
	}
	if json.Unmarshal(raw, &entries) != nil {
		return nil, badRequest("invalidValue", "members must be a list of {value} objects")
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		id, ok := canonicalUUID(e.Value)
		if !ok {
			return nil, badRequest("invalidValue", "a member value must be a user id")
		}
		out = append(out, id)
	}
	return out, nil
}

func (g *scimGroup) set(op, attr string, raw json.RawMessage) error {
	switch attr {
	case "displayname":
		name, err := rawString(raw)
		g.DisplayName = name
		return err
	case "externalid":
		external, err := rawString(raw)
		g.ExternalID = external
		return err
	case "members":
		members, err := parseMembers(raw)
		if err != nil {
			return err
		}
		switch {
		case op == "remove" && len(members) == 0:
			clear(g.memberIDs)
		case op == "remove":
			for _, id := range members {
				delete(g.memberIDs, id)
			}
		default:
			if op == "replace" {
				clear(g.memberIDs)
			}
			for _, id := range members {
				g.memberIDs[id] = struct{}{}
			}
		}
		return nil
	}
	return badRequest("invalidPath", "unsupported group attribute "+attr)
}

func (g *scimGroup) validate() error {
	if g.DisplayName == "" {
		return badRequest("invalidValue", "displayName is required")
	}
	if err := tooLong("displayName", g.DisplayName, scimMaxAttr); err != nil {
		return err
	}
	return tooLong("externalId", g.ExternalID, scimMaxAttr)
}

func groupFromBody(body map[string]json.RawMessage) (scimGroup, error) {
	g := scimGroup{memberIDs: map[string]struct{}{}}
	for key, raw := range body {
		attr := attrName(key)
		if attr == "schemas" || attr == "id" || attr == "meta" {
			continue
		}
		if err := g.set("replace", attr, raw); err != nil {
			return g, err
		}
	}
	return g, g.validate()
}

var memberFilter = regexp.MustCompile(`(?i)^members\[\s*value\s+eq\s+"([^"]+)"\s*\]$`)

func groupOperations(ops []patchOp) []patchOp {
	out := slices.Clone(ops)
	for i, op := range out {
		if m := memberFilter.FindStringSubmatch(op.Path); m != nil {
			raw, _ := json.Marshal([]map[string]string{{"value": m[1]}})
			out[i].Path, out[i].Value = "members", raw
		}
	}
	return out
}

func scimGroupID(r *http.Request) (string, error) {
	id, ok := canonicalUUID(chi.URLParam(r, "id"))
	if !ok {
		return "", scimNotFound("group")
	}
	return id, nil
}

func loadScimGroup(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (scimGroup, error) {
	query := scimGroupSelect + ` and g.group_id = $2`
	if lock {
		query += ` for update of g`
	}
	rows, err := tx.Query(ctx, query, tenantID, id)
	if err != nil {
		return scimGroup{}, err
	}
	g, err := pgx.CollectExactlyOneRow(rows, scanScimGroup)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, scimNotFound("group")
	}
	return g, err
}

func uniqueGroupName(err error) error {
	if pgCode(err) == pgUnique {
		return scimConflict("displayName is already in use")
	}
	return err
}

type groupEvent struct {
	GroupID     string   `json:"groupId"`
	DisplayName string   `json:"displayName"`
	ExternalID  string   `json:"externalId,omitempty"`
	Added       []string `json:"added,omitempty"`
	Removed     []string `json:"removed,omitempty"`
}

func recordGroup(ctx context.Context, tx pgx.Tx, tenantID, actor, verb string, g scimGroup, added, removed []string) error {
	return record(ctx, tx, tenantID, actor, "scimgroup", g.ID, verb, groupEvent{GroupID: g.ID, DisplayName: g.DisplayName, ExternalID: g.ExternalID, Added: added, Removed: removed})
}

func memberDiff(cur, next map[string]struct{}) (added, removed []string) {
	for id := range next {
		if _, ok := cur[id]; !ok {
			added = append(added, id)
		}
	}
	for id := range cur {
		if _, ok := next[id]; !ok {
			removed = append(removed, id)
		}
	}
	slices.Sort(added)
	slices.Sort(removed)
	return added, removed
}

func syncMembers(ctx context.Context, tx pgx.Tx, tenantID, groupID string, added, removed []string) error {
	if len(added) > 0 {
		var found int
		if err := tx.QueryRow(ctx, `select count(*) from identity_users where tenant_id = $1 and user_name is not null and user_id::text = any($2::text[])`, tenantID, added).Scan(&found); err != nil {
			return err
		}
		if found != len(added) {
			return badRequest("invalidValue", "every member must be a provisioned user of this tenant")
		}
		if _, err := tx.Exec(ctx, `insert into identity_scim_group_members (tenant_id, group_id, user_id)
select $1, $2, unnest($3::text[])::uuid on conflict do nothing`, tenantID, groupID, added); err != nil {
			return err
		}
	}
	if len(removed) > 0 {
		_, err := tx.Exec(ctx, `delete from identity_scim_group_members where tenant_id = $1 and group_id = $2 and user_id::text = any($3::text[])`, tenantID, groupID, removed)
		return err
	}
	return nil
}

func (a *SCIMAPI) listGroups(w http.ResponseWriter, r *http.Request) {
	p := scimPrincipal(r)
	start, count, err := scimPaging(r)
	var attr, value string
	if err == nil {
		attr, value, err = scimFilter(r, "displayName", "externalId")
	}
	if err != nil {
		scimFail(w, r, err)
		return
	}
	const where = ` and ($2::text = '' or case $2::text when 'displayname' then lower(g.display_name) = lower($3::text) else g.external_id = $3::text end)`
	var total int
	var groups []scimGroup
	err = a.inTx(r, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `select count(*) from identity_scim_groups g where g.tenant_id = $1`+where, p.TenantID, attr, value).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), scimGroupSelect+where+` order by g.group_id offset $4 limit $5`, p.TenantID, attr, value, start-1, count)
		if err != nil {
			return err
		}
		groups, err = pgx.CollectRows(rows, scanScimGroup)
		return err
	})
	if err != nil {
		scimFail(w, r, err)
		return
	}
	resources := make([]scimGroupResource, 0, len(groups))
	for _, g := range groups {
		resources = append(resources, g.resource())
	}
	writeSCIM(w, http.StatusOK, newList(total, start, len(resources), resources))
}

func (a *SCIMAPI) getGroup(w http.ResponseWriter, r *http.Request) {
	id, err := scimGroupID(r)
	var g scimGroup
	if err == nil {
		err = a.inTx(r, func(tx pgx.Tx) error {
			var loadErr error
			g, loadErr = loadScimGroup(r.Context(), tx, scimPrincipal(r).TenantID, id, false)
			return loadErr
		})
	}
	if err != nil {
		scimFail(w, r, err)
		return
	}
	writeSCIM(w, http.StatusOK, g.resource())
}

func (a *SCIMAPI) createGroup(w http.ResponseWriter, r *http.Request) {
	p := scimPrincipal(r)
	body, err := decodeSCIMObject(w, r)
	var g scimGroup
	if err == nil {
		g, err = groupFromBody(body)
	}
	if err != nil {
		scimFail(w, r, err)
		return
	}
	g.ID = ids.New()
	var created scimGroup
	err = a.inTx(r, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `insert into identity_scim_groups (tenant_id, group_id, display_name, external_id) values ($1, $2, $3, nullif($4, ''))`,
			p.TenantID, g.ID, g.DisplayName, g.ExternalID); err != nil {
			return uniqueGroupName(err)
		}
		added, _ := memberDiff(nil, g.memberIDs)
		if err := syncMembers(r.Context(), tx, p.TenantID, g.ID, added, nil); err != nil {
			return err
		}
		if err := recordGroup(r.Context(), tx, p.TenantID, p.UserID, "created", g, added, nil); err != nil {
			return err
		}
		var loadErr error
		created, loadErr = loadScimGroup(r.Context(), tx, p.TenantID, g.ID, false)
		return loadErr
	})
	if err != nil {
		scimFail(w, r, err)
		return
	}
	w.Header().Set("Location", scimLocation("Groups", created.ID))
	writeSCIM(w, http.StatusCreated, created.resource())
}

func (a *SCIMAPI) replaceGroup(w http.ResponseWriter, r *http.Request) {
	id, err := scimGroupID(r)
	var next scimGroup
	if err == nil {
		var body map[string]json.RawMessage
		if body, err = decodeSCIMObject(w, r); err == nil {
			next, err = groupFromBody(body)
		}
	}
	a.saveGroup(w, r, id, err, func(scimGroup) (scimGroup, error) { return next, nil })
}

func (a *SCIMAPI) patchGroup(w http.ResponseWriter, r *http.Request) {
	id, err := scimGroupID(r)
	var ops []patchOp
	if err == nil {
		ops, err = decodePatch(w, r)
	}
	a.saveGroup(w, r, id, err, func(cur scimGroup) (scimGroup, error) {
		next := cur.clone()
		if err := applyOperations(groupOperations(ops), next.set); err != nil {
			return cur, err
		}
		return next, next.validate()
	})
}

func (a *SCIMAPI) saveGroup(w http.ResponseWriter, r *http.Request, id string, prior error, change func(scimGroup) (scimGroup, error)) {
	if prior != nil {
		scimFail(w, r, prior)
		return
	}
	p := scimPrincipal(r)
	var result scimGroup
	err := a.inTx(r, func(tx pgx.Tx) error {
		cur, err := loadScimGroup(r.Context(), tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		next, err := change(cur)
		if err != nil {
			return err
		}
		next.ID = cur.ID
		added, removed := memberDiff(cur.memberIDs, next.memberIDs)
		if cur.DisplayName != next.DisplayName || cur.ExternalID != next.ExternalID {
			if _, err := tx.Exec(r.Context(), `update identity_scim_groups set display_name = $3, external_id = nullif($4, '') where tenant_id = $1 and group_id = $2`,
				p.TenantID, id, next.DisplayName, next.ExternalID); err != nil {
				return uniqueGroupName(err)
			}
		}
		if err := syncMembers(r.Context(), tx, p.TenantID, id, added, removed); err != nil {
			return err
		}
		if cur.DisplayName != next.DisplayName || cur.ExternalID != next.ExternalID || len(added)+len(removed) > 0 {
			if err := recordGroup(r.Context(), tx, p.TenantID, p.UserID, "updated", next, added, removed); err != nil {
				return err
			}
		}
		result, err = loadScimGroup(r.Context(), tx, p.TenantID, id, false)
		return err
	})
	if err != nil {
		scimFail(w, r, err)
		return
	}
	writeSCIM(w, http.StatusOK, result.resource())
}

func (a *SCIMAPI) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id, err := scimGroupID(r)
	if err != nil {
		scimFail(w, r, err)
		return
	}
	p := scimPrincipal(r)
	err = a.inTx(r, func(tx pgx.Tx) error {
		g, err := loadScimGroup(r.Context(), tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `delete from identity_scim_groups where tenant_id = $1 and group_id = $2`, p.TenantID, id); err != nil {
			return err
		}
		removed := slices.Sorted(maps.Keys(g.memberIDs))
		return recordGroup(r.Context(), tx, p.TenantID, p.UserID, "deleted", g, nil, removed)
	})
	if err != nil {
		scimFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
