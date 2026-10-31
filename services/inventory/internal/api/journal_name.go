package api

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// Journal name routes (PRD 3.16.13): the setup a keyed journal is created under.
const (
	dutyJournalNameWrite = "inventory.journal-name.write"
	journalNamesPath     = "/v1/inventory/journal-names"
)

func (a *API) registerJournalNames(r chi.Router) {
	r.With(authz.RequireDuty(dutyJournalNameWrite)).Post(journalNamesPath, a.createJournalName)
	r.With(authz.RequireDuty(dutyRead)).Get(journalNamesPath, a.listJournalNames)
	r.With(authz.RequireDuty(dutyRead)).Get(journalNamesPath+"/{id}", a.getJournalName)
	r.With(authz.RequireDuty(dutyJournalNameWrite)).Patch(journalNamesPath+"/{id}", a.updateJournalName)
}

type journalNameRequest struct {
	LegalEntityID    string `json:"legalEntityId"`
	Code             string `json:"code"`
	Description      string `json:"description"`
	JournalType      string `json:"journalType"`
	RequiresApproval bool   `json:"requiresApproval"`
	OffsetAccountID  string `json:"offsetAccountId"`
}

type journalNamePatchRequest struct {
	Description      *string `json:"description"`
	RequiresApproval *bool   `json:"requiresApproval"`
	OffsetAccountID  *string `json:"offsetAccountId"`
	Active           *bool   `json:"active"`
}

type journalNameResponse struct {
	ID               string    `json:"id"`
	LegalEntityID    string    `json:"legalEntityId"`
	Code             string    `json:"code"`
	Description      string    `json:"description"`
	JournalType      string    `json:"journalType"`
	RequiresApproval bool      `json:"requiresApproval"`
	OffsetAccountID  string    `json:"offsetAccountId,omitempty"`
	Active           bool      `json:"active"`
	Version          int       `json:"version"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

func toJournalNameResponse(n domain.JournalName) journalNameResponse {
	return journalNameResponse{ID: n.ID, LegalEntityID: n.LegalEntityID, Code: n.Code, Description: n.Description, JournalType: string(n.Type),
		RequiresApproval: n.RequiresApproval, OffsetAccountID: n.OffsetAccountID, Active: n.Active, Version: n.Version, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt}
}

func writeJournalName(w http.ResponseWriter, status int, n domain.JournalName) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(n.Version)))
	httpx.JSON(w, status, toJournalNameResponse(n))
}

func journalNameNotFound(w http.ResponseWriter, r *http.Request) {
	httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeJournalNameNotFound, "journal name not found"))
}

func (a *API) createJournalName(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req journalNameRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	n := domain.NormalizeJournalName(domain.JournalName{LegalEntityID: req.LegalEntityID, Code: req.Code, Description: req.Description,
		Type: domain.JournalType(req.JournalType), RequiresApproval: req.RequiresApproval, OffsetAccountID: req.OffsetAccountID})
	if !slices.Contains(domain.KeyedJournalTypes, n.Type) {
		fields["journalType"] = "must be MOVEMENT, ADJUSTMENT, OPENING_BALANCE, OWNERSHIP_CHANGE or TRANSFER"
	}
	if err := n.Validate(); err != nil && fields["journalType"] == "" {
		field := "code"
		switch {
		case strings.Contains(err.Error(), "description"):
			field = "description"
		case strings.Contains(err.Error(), "offsetAccountId"), strings.Contains(err.Error(), "offset account"):
			field = "offsetAccountId"
		}
		fields[field] = err.(*domain.Error).Detail
	}
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	out, replayed, err := a.store.CreateJournalName(r.Context(), key, n)
	if err != nil {
		a.failed(w, r, "create journal name", err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJournalName(w, status, out)
}

func (a *API) listJournalNames(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.JournalNameFilter{LegalEntityID: q.Get("legalEntityId"), JournalType: q.Get("journalType")}
	fields := map[string]string{}
	if f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if f.JournalType != "" && !slices.Contains(domain.KeyedJournalTypes, domain.JournalType(f.JournalType)) {
		fields["journalType"] = "must be MOVEMENT, ADJUSTMENT, OPENING_BALANCE, OWNERSHIP_CHANGE or TRANSFER"
	}
	if raw := q.Get("active"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			fields["active"] = "must be true or false"
		}
		f.Active = &b
	}
	f.Limit = pageLimit(r, 200, 200, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	found, err := a.store.ListJournalNames(r.Context(), f)
	if err != nil {
		a.failed(w, r, "list journal names", err)
		return
	}
	items := make([]journalNameResponse, 0, len(found))
	for _, n := range found {
		items = append(items, toJournalNameResponse(n))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getJournalName(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		journalNameNotFound(w, r)
		return
	}
	n, err := a.store.GetJournalName(r.Context(), id)
	if err != nil {
		a.failed(w, r, "get journal name", err)
		return
	}
	writeJournalName(w, http.StatusOK, n)
}

// updateJournalName changes the description, approval rule, default offset
// account or active flag; the version comes from If-Match.
func (a *API) updateJournalName(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		journalNameNotFound(w, r)
		return
	}
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req journalNamePatchRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Description == nil && req.RequiresApproval == nil && req.OffsetAccountID == nil && req.Active == nil {
		fields["body"] = "must change at least one of description, requiresApproval, offsetAccountId, active"
	}
	if req.Description != nil && utf8.RuneCountInString(strings.TrimSpace(*req.Description)) > domain.MaxJournalNameDescription {
		fields["description"] = "must be at most 200 characters"
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	n, err := a.store.UpdateJournalName(r.Context(), id, version, store.JournalNamePatch{Description: req.Description,
		RequiresApproval: req.RequiresApproval, OffsetAccountID: req.OffsetAccountID, Active: req.Active})
	if err != nil {
		a.failed(w, r, "update journal name", err)
		return
	}
	writeJournalName(w, http.StatusOK, n)
}
