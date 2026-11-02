package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
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
