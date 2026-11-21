package api

import (
	"net/http"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/services/ledger/internal/store"
)

func (a *API) registerPostingRequests(r chi.Router) {
	r.With(authz.RequireAnyDuty(dutyRead, dutyEntryPost)).Get("/posting-requests/{key}", a.getPostingRequest)
}

// getPostingRequest answers what the ledger recorded for a posting key: its status and, once posted, the journal entry.
func (a *API) getPostingRequest(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	key := chi.URLParam(r, "key")
	if utf8.RuneCountInString(key) > maxIdempotencyKey {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodePostingRequestNotFound, "no posting request is recorded for this key"))
		return
	}
	pr, err := a.store.GetPostingRequest(r.Context(), key)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, pr)
}
