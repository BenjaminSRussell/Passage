package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/batch/internal/domain"
	"github.com/BenjaminSRussell/passage/services/batch/internal/inbox"
)

func (a *API) inboxPage(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) || p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "a tenant and an identified user are required"))
		return
	}
	q := r.URL.Query()
	limit := inbox.DefaultLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > inbox.MaxLimit {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeInvalid, "limit must be a number from 1 to "+strconv.Itoa(inbox.MaxLimit)))
			return
		}
		limit = n
	}
	legalEntity := q.Get("legalEntityId")
	switch scopes := tenant.LegalEntityScopeList(p); {
	case legalEntity == "" && len(scopes) == 1:
		legalEntity = scopes[0]
	case legalEntity == "":
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeInvalid, "legalEntityId is required when you work in more than one legal entity"))
		return
	case !ids.Valid(legalEntity):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeInvalid, "legalEntityId must be a UUID"))
		return
	case !tenant.InLegalEntityScope(p, legalEntity):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity is outside your scope"))
		return
	}
	page, err := a.aggregator.Page(r.Context(), p, legalEntity, limit, q.Get("cursor"))
	if errors.Is(err, inbox.ErrSourcesChanged) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, domain.CodeInvalid, "Some of your work could not be loaded the same way as on the first page. Reload My work from the start."))
		return
	}
	if errors.Is(err, inbox.ErrBadCursor) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeInvalid, "cursor must be the nextCursor of the previous page"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}
