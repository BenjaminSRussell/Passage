package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

type copyInclude struct {
	Sites         *bool `json:"sites"`
	Locations     *bool `json:"locations"`
	RoundingRules *bool `json:"roundingRules"`
}

type copyLegalEntityRequest struct {
	createLegalEntityRequest
	Include copyInclude `json:"include"`
}

func orDefault(v *bool, d bool) bool {
	if v == nil {
		return d
	}
	return *v
}

// copyLegalEntity creates a legal entity from an existing one (FR-6.15): POST /v1/org/legal-entities/{id}:copy.
func (a *API) copyLegalEntity(w http.ResponseWriter, r *http.Request, sourceID string) {
	idemKey, keyOK := idempotencyKeyOf(w, r)
	if !keyOK || rejectScopedCaller(w, r, "copy legal entities") {
		return
	}
	var req copyLegalEntityRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.LegalEntityCopyInput{
		SourceID: sourceID, Code: strings.ToUpper(req.Code), Name: strings.TrimSpace(req.Name), Country: req.Country, Currency: req.Currency,
		TimeZone: req.TimeZone, DefaultLanguage: req.DefaultLanguage, RegistrationNumber: req.RegistrationNumber,
		Sites: orDefault(req.Include.Sites, true), RoundingRules: orDefault(req.Include.RoundingRules, true),
	}
	in.Locations = orDefault(req.Include.Locations, in.Sites)
	if reservedCodes[in.Code] {
		p := httpx.NewProblem(http.StatusBadRequest, "ORG-LE-CODE-RESERVED", "legal entity code is reserved")
		p.Errors = map[string]string{"code": "must not be DAT, SYS or ALL"}
		httpx.Fail(w, r, p)
		return
	}
	// The time zone and language default from the source inside the store, so check only what was sent.
	check := store.LegalEntity{Code: in.Code, Name: in.Name, Country: in.Country, Currency: in.Currency, TimeZone: in.TimeZone, DefaultLanguage: in.DefaultLanguage, RegistrationNumber: in.RegistrationNumber}
	if check.TimeZone == "" {
		check.TimeZone = "UTC"
	}
	if check.DefaultLanguage == "" {
		check.DefaultLanguage = store.DefaultLanguage
	}
	fields := validateLegalEntity(check)
	if in.Locations && !in.Sites {
		fields["include.locations"] = "locations are copied with their warehouses, so sites must be included"
	}
	if len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
		p.Errors = fields
		httpx.Fail(w, r, p)
		return
	}
	result, err := a.store.CopyLegalEntity(r.Context(), in, idemKey)
	if err != nil {
		a.failed.Add(1)
		slog.Error("copy legal entity failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if !result.Replayed {
		a.copied.Add(1)
	}
	httpx.JSON(w, http.StatusCreated, result)
}
