package api

import (
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

// maxReasonNameLength is the reason code name bound (FR-11.15).
const maxReasonNameLength = 60

type reasonCodeRequest struct {
	Name                          string   `json:"name"`
	AppliesTo                     []string `json:"appliesTo"`
	RequiresComment               bool     `json:"requiresComment"`
	NegativePostingAllowed        bool     `json:"negativePostingAllowed"`
	AlternativePostingDateAllowed *bool    `json:"alternativePostingDateAllowed"`
	Active                        *bool    `json:"active"`
}

func (a *API) listReasonCodes(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	items, err := a.store.ListReasonCodes(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getReasonCode(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	code := chi.URLParam(r, "code")
	if !domain.ValidReasonCode(code) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "LEDGER_REASON_NOT_FOUND", "reason code not found"))
		return
	}
	rc, err := a.store.GetReasonCode(r.Context(), code)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rc)
}

// putReasonCode creates or replaces a shared reason code. Codes are deactivated
// (active = false), never deleted, because journal entries reference them.
// Responds 201 when the code was created and 200 when it was replaced.
func (a *API) putReasonCode(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	code := chi.URLParam(r, "code")
	var req reasonCodeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !domain.ValidReasonCode(code) {
		fields["code"] = "must be 1 to 10 characters of A-Z, 0-9 or underscore"
	}
	if n := utf8.RuneCountInString(req.Name); n < 1 || n > maxReasonNameLength {
		fields["name"] = fmt.Sprintf("must be 1 to %d characters", maxReasonNameLength)
	}
	if len(req.AppliesTo) == 0 {
		fields["appliesTo"] = "at least one module is required"
	}
	for _, m := range req.AppliesTo {
		if !domain.ValidReasonModule(m) {
			fields["appliesTo"] = "modules must be ledger, bank, customer, vendor, fixed_asset, inventory_counting, tax or project"
		}
	}
	altDate, active := true, true
	if req.AlternativePostingDateAllowed != nil {
		altDate = *req.AlternativePostingDateAllowed
	}
	if req.Active != nil {
		active = *req.Active
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rc, created, err := a.store.PutReasonCode(r.Context(), code, store.ReasonCode{Name: req.Name, AppliesTo: req.AppliesTo,
		RequiresComment: req.RequiresComment, NegativePostingAllowed: req.NegativePostingAllowed,
		AlternativePostingDateAllowed: altDate, Active: active})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, rc)
}
