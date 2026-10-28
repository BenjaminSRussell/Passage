package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
)

type postalAddressBody struct {
	CountryCode string `json:"countryCode"`
	RegionCode  string `json:"regionCode,omitempty"`
	City        string `json:"city,omitempty"`
	PostalCode  string `json:"postalCode,omitempty"`
	Street1     string `json:"street1,omitempty"`
	Street2     string `json:"street2,omitempty"`
	Street3     string `json:"street3,omitempty"`
	Building    string `json:"building,omitempty"`
	POBox       string `json:"poBox,omitempty"`
}

type addressSuggestionBody struct {
	Address postalAddressBody `json:"address"`
	Reason  string            `json:"reason"`
}

type normalizedAddress struct {
	Address     postalAddressBody       `json:"address"`
	Formatted   string                  `json:"formatted"`
	Suggestions []addressSuggestionBody `json:"suggestions"`
}

func (b postalAddressBody) postal() domain.PostalAddress {
	return domain.PostalAddress{CountryCode: b.CountryCode, RegionCode: b.RegionCode, City: b.City, PostalCode: b.PostalCode,
		Street1: b.Street1, Street2: b.Street2, Street3: b.Street3, Building: b.Building, POBox: b.POBox}
}

func bodyOf(p domain.PostalAddress) postalAddressBody {
	return postalAddressBody{CountryCode: p.CountryCode, RegionCode: p.RegionCode, City: p.City, PostalCode: p.PostalCode,
		Street1: p.Street1, Street2: p.Street2, Street3: p.Street3, Building: p.Building, POBox: p.POBox}
}

func (a *API) normalizeAddress(w http.ResponseWriter, r *http.Request) {
	var req postalAddressBody
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	normalized, err := a.validator.Normalize(r.Context(), req.postal())
	if err != nil {
		slog.Error("normalize address failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if !countryCode.MatchString(normalized.CountryCode) {
		validationFailed(w, r, map[string]string{"countryCode": "must be an ISO 3166-1 alpha-2 code"})
		return
	}
	suggestions, err := a.validator.Suggest(r.Context(), normalized)
	if err != nil {
		slog.Error("suggest address failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	body := normalizedAddress{Address: bodyOf(normalized), Formatted: domain.Format(normalized), Suggestions: []addressSuggestionBody{}}
	for _, s := range suggestions {
		body.Suggestions = append(body.Suggestions, addressSuggestionBody{Address: bodyOf(s.Address), Reason: s.Reason})
	}
	httpx.JSON(w, http.StatusOK, body)
}
