package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	addresses    = "/v1/org/addresses"
	dutyAddress  = "org.address.write"
	dateLayout   = "2006-01-02"
	maxPurposes  = 7
	geoUnchecked = "unverified"
)

var (
	addressOwnerTypes = []string{"legal_entity", "operating_unit", "site", "warehouse", "location", "party"}
	regionSuffix      = regexp.MustCompile(`^[A-Z0-9]{1,3}$`)
	addressFieldMax   = map[string]int{"city": 60, "postalCode": 12, "street1": 100, "street2": 100, "street3": 100, "building": 20, "poBox": 20}
)

type addressPurposeRequest struct {
	Purpose string `json:"purpose"`
	Primary bool   `json:"primary"`
}

type createAddressRequest struct {
	OwnerType   string                  `json:"ownerType"`
	OwnerID     string                  `json:"ownerId"`
	CountryCode string                  `json:"countryCode"`
	RegionCode  string                  `json:"regionCode"`
	City        string                  `json:"city"`
	PostalCode  string                  `json:"postalCode"`
	Street1     string                  `json:"street1"`
	Street2     string                  `json:"street2"`
	Street3     string                  `json:"street3"`
	Building    string                  `json:"building"`
	POBox       string                  `json:"poBox"`
	ValidFrom   string                  `json:"validFrom"`
	Purposes    []addressPurposeRequest `json:"purposes"`
}

func (a *API) SetGeocoder(g domain.Geocoder) { a.geocoder = g }

func (a *API) registerAddresses(r chi.Router) {
	r.With(authz.RequireDuty(dutyAddress)).Post(addresses, a.createAddress)
	r.With(authz.RequireDuty(dutyRead)).Get(addresses+"/{id}", addressRoute(a.getAddress))
}

func optionalText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (req *createAddressRequest) normalize() {
	req.OwnerType, req.OwnerID = strings.TrimSpace(req.OwnerType), canonicalID(req.OwnerID)
	req.CountryCode, req.RegionCode = strings.ToUpper(strings.TrimSpace(req.CountryCode)), strings.ToUpper(strings.TrimSpace(req.RegionCode))
	for _, field := range []*string{&req.City, &req.PostalCode, &req.Street1, &req.Street2, &req.Street3, &req.Building, &req.POBox, &req.ValidFrom} {
		*field = strings.TrimSpace(*field)
	}
}

func (req createAddressRequest) validate() map[string]string {
	fields := map[string]string{}
	if !slices.Contains(addressOwnerTypes, req.OwnerType) {
		fields["ownerType"] = "must be legal_entity, operating_unit, site, warehouse, location or party"
	}
	if !canonicalUUID(req.OwnerID) {
		fields["ownerId"] = "is required and must be a UUID"
	}
	if !countryCode.MatchString(req.CountryCode) {
		fields["countryCode"] = "must be an ISO 3166-1 alpha-2 code"
	}
	if req.RegionCode != "" && !regionSuffix.MatchString(req.RegionCode) {
		fields["regionCode"] = "must be 1 to 3 characters of A-Z or 0-9"
	}
	for field, value := range map[string]string{"city": req.City, "street1": req.Street1} {
		if value == "" {
			fields[field] = "is required"
		}
	}
	for field, value := range map[string]string{"city": req.City, "postalCode": req.PostalCode, "street1": req.Street1, "street2": req.Street2, "street3": req.Street3, "building": req.Building, "poBox": req.POBox} {
		if _, failed := fields[field]; !failed && utf8.RuneCountInString(value) > addressFieldMax[field] {
			fields[field] = fmt.Sprintf("must be at most %d characters", addressFieldMax[field])
		}
	}
	if _, err := time.Parse(dateLayout, req.ValidFrom); req.ValidFrom != "" && err != nil {
		fields["validFrom"] = "must be a date as YYYY-MM-DD"
	}
	seen := map[string]bool{}
	if len(req.Purposes) < 1 || len(req.Purposes) > maxPurposes {
		fields["purposes"] = "must list 1 to 7 purposes"
	}
	for _, purpose := range req.Purposes {
		if !domain.ValidPurpose(purpose.Purpose) || seen[purpose.Purpose] {
			fields["purposes"] = "must be distinct values of " + strings.Join(domain.AddressPurposes, ", ")
		}
		seen[purpose.Purpose] = true
	}
	return fields
}

func (a *API) createAddress(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req createAddressRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.normalize()
	if fields := req.validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	in := store.Address{OwnerType: req.OwnerType, OwnerID: req.OwnerID, CountryCode: req.CountryCode, RegionCode: optionalText(req.RegionCode), City: req.City,
		PostalCode: optionalText(req.PostalCode), Street1: req.Street1, Street2: optionalText(req.Street2), Street3: optionalText(req.Street3),
		Building: optionalText(req.Building), POBox: optionalText(req.POBox), GeoStatus: geoUnchecked, ValidFrom: req.ValidFrom}
	for _, purpose := range req.Purposes {
		in.Purposes = append(in.Purposes, store.AddressPurpose{Purpose: purpose.Purpose, Primary: purpose.Primary})
	}
	geocode, err := a.geocoder.Geocode(r.Context(), domain.PostalAddress{CountryCode: req.CountryCode, RegionCode: req.RegionCode, City: req.City, PostalCode: req.PostalCode,
		Street1: req.Street1, Street2: req.Street2, Street3: req.Street3, Building: req.Building, POBox: req.POBox})
	if err != nil {
		slog.Warn("geocoding failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	} else if geocode.Found {
		in.Latitude, in.Longitude, in.GeoStatus = &geocode.Latitude, &geocode.Longitude, "geocoded"
	}
	address, err := a.store.CreateAddress(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("create address failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.addressCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, address)
}

func addressRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAddressNotFound, "address not found"))
			return
		}
		next(w, r, id)
	}
}

func (a *API) getAddress(w http.ResponseWriter, r *http.Request, id string) {
	address, err := a.store.GetAddress(r.Context(), id)
	if err != nil {
		slog.Error("get address failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, address)
}
