package api

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

const maxSearchText = 100

func (a *API) WithSearch(search store.SearchStore) *API {
	a.search = search
	return a
}

func (a *API) registerSearch(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/parties:search", a.searchRoute(store.SearchParties))
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/vendors:search", a.searchRoute(store.SearchVendors))
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/customers:search", a.searchRoute(store.SearchCustomers))
}

type searchResponse struct {
	Items      []store.SearchHit `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func badSearchParam(w http.ResponseWriter, r *http.Request, field, rule string) {
	p := httpx.NewProblem(http.StatusBadRequest, codeInvalid, "search parameters failed validation")
	p.Errors = map[string]string{field: rule}
	httpx.Fail(w, r, p)
}

func (a *API) searchRoute(kind store.SearchKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		query := r.URL.Query()
		text := strings.TrimSpace(query.Get("q"))
		if len([]rune(text)) > maxSearchText {
			badSearchParam(w, r, "q", "must be at most 100 characters")
			return
		}
		limit := store.MaxSearchPage
		if raw := query.Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > store.MaxSearchPage {
				badSearchParam(w, r, "limit", "must be a whole number from 1 to 50")
				return
			}
			limit = n
		}
		var after store.SearchKey
		if cursor := query.Get("cursor"); cursor != "" {
			raw, err := base64.RawURLEncoding.DecodeString(cursor)
			name, id, ok := strings.Cut(string(raw), "\x00")
			if err != nil || !ok || !ids.Valid(id) {
				badSearchParam(w, r, "cursor", "is invalid")
				return
			}
			after = store.SearchKey{Name: name, ID: id}
		}
		page, err := a.search.Search(r.Context(), kind, text, after, limit)
		if err != nil {
			failParty(w, r, err)
			return
		}
		body := searchResponse{Items: page.Items}
		if page.Next != nil {
			body.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.Next.Name + "\x00" + page.Next.ID))
		}
		httpx.JSON(w, http.StatusOK, body)
	}
}
