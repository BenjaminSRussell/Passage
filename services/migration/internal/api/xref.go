package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const xrefPath = "/v1/integration/xref"

type xrefView struct {
	SourceSystem       string    `json:"sourceSystem"`
	Entity             string    `json:"entity"`
	SourceKey          string    `json:"sourceKey"`
	TargetID           string    `json:"targetId"`
	ContentHash        string    `json:"contentHash,omitempty"`
	MergedIntoTargetID string    `json:"mergedIntoTargetId,omitempty"`
	LegalEntityID      string    `json:"legalEntityId,omitempty"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

func (a *API) registerXref(r chi.Router) {
	r.With(authz.RequireDuty(dutyRun)).Get(xrefPath, a.listXref)
	r.With(authz.RequireDuty(dutyRun)).Get(xrefPath+"/summary", a.xrefSummary)
	r.With(authz.RequireDuty(store.DutyXrefRead)).Get(xrefPath+":resolve", a.resolveXref)
}

// maxResolveKeys bounds one resolve request.
const maxResolveKeys = 500

// resolveXref answers GET /v1/integration/xref:resolve?sourceSystem=&entity=&keys=k1,k2: the Passage id of each legacy key this
// service loaded, for a loading service that must not be sent the mapping by hand (#49, H-S8-6: the migration factory resolves a
// legacy customer of an open item to the customer the party migration loaded). Service-only; the token's legal-entity scope
// applies, so a customer of another legal entity is not found.
func (a *API) resolveXref(w http.ResponseWriter, r *http.Request) {
	if p, ok := tenant.FromContext(r.Context()); !ok || !ids.Valid(p.TenantID) {
		fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return
	}
	q := r.URL.Query()
	bad := map[string]string{}
	system, entity := q.Get("sourceSystem"), q.Get("entity")
	if system == "" || len(system) > 64 {
		bad["sourceSystem"] = "is required (1 to 64 characters)"
	}
	if entity == "" || len(entity) > 64 {
		bad["entity"] = "is required (1 to 64 characters)"
	}
	var keys []string
	for _, k := range strings.Split(q.Get("keys"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			if len(k) > 255 {
				bad["keys"] = "each key is at most 255 characters"
			}
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 || len(keys) > maxResolveKeys {
		bad["keys"] = "must name 1 to 500 keys, separated by commas"
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	found, err := a.store.ResolveXref(r.Context(), system, entity, keys)
	if err != nil {
		a.logFailure(r, "resolve xref failed", err)
		fail(w, r, err)
		return
	}
	type resolved struct {
		SourceKey string `json:"sourceKey"`
		TargetID  string `json:"targetId"`
	}
	out := struct {
		Items []resolved `json:"items"`
	}{Items: make([]resolved, 0, len(found))}
	for _, k := range keys {
		if id, ok := found[k]; ok {
			out.Items = append(out.Items, resolved{SourceKey: k, TargetID: id})
			delete(found, k)
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

func encodeCursor(c store.XrefCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s string) (*store.XrefCursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, false
	}
	var c store.XrefCursor
	if json.Unmarshal(raw, &c) != nil {
		return nil, false
	}
	return &c, true
}

// listXref answers "where did legacy record X go": by exact legacy key, key prefix or Passage id, in key order
// with a keyset cursor.
func (a *API) listXref(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	q := r.URL.Query()
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	if limit == 0 {
		limit = defaultPage
	}
	bad := map[string]string{}
	if v := q.Get("sourceSystem"); v != "" && !domain.ValidSourceSystem(v) {
		bad["sourceSystem"] = domain.SourceSystemHint
	}
	if v := q.Get("targetId"); v != "" && !ids.Valid(v) {
		bad["targetId"] = "must be a Passage id (UUID)"
	}
	var after *store.XrefCursor
	if v := q.Get("cursor"); v != "" {
		c, ok := decodeCursor(v)
		if !ok {
			bad["cursor"] = "is not a cursor returned by this endpoint"
		}
		after = c
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	rows, err := a.store.ListXref(r.Context(), store.XrefFilter{
		SourceSystem: q.Get("sourceSystem"), Entity: q.Get("entity"), SourceKey: q.Get("sourceKey"),
		KeyPrefix: q.Get("keyPrefix"), TargetID: q.Get("targetId"), After: after, Limit: limit + 1,
	})
	if err != nil {
		a.logFailure(r, "list xref failed", err)
		fail(w, r, err)
		return
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		next = encodeCursor(store.XrefCursor{SourceSystem: last.SourceSystem, Entity: last.Entity, SourceKey: last.SourceKey})
	}
	items := make([]xrefView, 0, len(rows))
	for _, x := range rows {
		items = append(items, xrefView(x))
	}
	body := map[string]any{"items": items}
	if next != "" {
		body["nextCursor"] = next
	}
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) xrefSummary(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	counts, err := a.store.XrefSummary(r.Context())
	if err != nil {
		a.logFailure(r, "summarize xref failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": counts})
}
