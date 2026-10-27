package api

import (
	"encoding/base64"
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

type updateAddressRequest struct {
	VersionID string `json:"versionId"`
	ValidFrom string `json:"validFrom"`
	store.AddressPatch
}

type addressList struct {
	Items      []store.Address `json:"items"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

type addressVersionList struct {
	Items []store.AddressVersion `json:"items"`
}

func (a *API) SetGeocoder(g domain.Geocoder) { a.geocoder = g }

func (a *API) SetClock(now func() time.Time) { a.now = now }

func (a *API) registerAddresses(r chi.Router) {
	r.With(authz.RequireDuty(dutyAddress)).Post(addresses, a.createAddress)
	r.With(authz.RequireDuty(dutyAddress)).Patch(addresses+"/{id}", addressRoute(a.updateAddress))
	r.With(authz.RequireDuty(dutyRead)).Get(addresses, a.listAddresses)
	r.With(authz.RequireDuty(dutyRead)).Get(addresses+"/{id}", addressRoute(a.getAddress))
	r.With(authz.RequireDuty(dutyRead)).Get(addresses+"/{id}/versions", addressRoute(a.listAddressVersions))
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/org/address-versions/{id}", addressRoute(a.getAddressVersion))
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

func (req createAddressRequest) toAddress() store.Address {
	in := store.Address{OwnerType: req.OwnerType, OwnerID: req.OwnerID, CountryCode: req.CountryCode, RegionCode: optionalText(req.RegionCode), City: req.City,
		PostalCode: optionalText(req.PostalCode), Street1: req.Street1, Street2: optionalText(req.Street2), Street3: optionalText(req.Street3),
		Building: optionalText(req.Building), POBox: optionalText(req.POBox), GeoStatus: geoUnchecked, ValidFrom: req.ValidFrom}
	for _, purpose := range req.Purposes {
		in.Purposes = append(in.Purposes, store.AddressPurpose{Purpose: purpose.Purpose, Primary: purpose.Primary})
	}
	return in
}

func (a *API) geocodeAddress(r *http.Request, req createAddressRequest, in *store.Address) {
	geocode, err := a.geocoder.Geocode(r.Context(), domain.PostalAddress{CountryCode: req.CountryCode, RegionCode: req.RegionCode, City: req.City, PostalCode: req.PostalCode,
		Street1: req.Street1, Street2: req.Street2, Street3: req.Street3, Building: req.Building, POBox: req.POBox})
	if err != nil {
		slog.Warn("geocoding failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	} else if geocode.Found {
		in.Latitude, in.Longitude, in.GeoStatus = &geocode.Latitude, &geocode.Longitude, "geocoded"
	}
}

func (a *API) createAddress(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idemKey, keyOK := idempotencyKeyOf(w, r)
	if !keyOK {
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
	in := req.toAddress()
	a.geocodeAddress(r, req, &in)
	address, err := a.store.CreateAddress(r.Context(), in, idemKey)
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
	var address store.Address
	var err error
	if asOf, given := r.URL.Query()["asOf"]; given {
		if day, parseErr := time.Parse(dateLayout, asOf[0]); parseErr != nil || day.Format(dateLayout) != asOf[0] {
			badQuery(w, r, "asOf", "must be a calendar date as YYYY-MM-DD")
			return
		}
		address, err = a.store.GetAddressAsOf(r.Context(), id, asOf[0])
	} else {
		address, err = a.store.GetAddress(r.Context(), id)
	}
	if err != nil {
		slog.Error("get address failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, address)
}

func (a *API) listAddressVersions(w http.ResponseWriter, r *http.Request, id string) {
	versions, err := a.store.ListAddressVersions(r.Context(), id)
	if err != nil {
		slog.Error("list address versions failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, addressVersionList{Items: versions})
}

func (req *updateAddressRequest) normalize() {
	req.VersionID, req.ValidFrom = canonicalID(req.VersionID), strings.TrimSpace(req.ValidFrom)
	for _, field := range []*string{req.CountryCode, req.RegionCode, req.City, req.PostalCode, req.Street1, req.Street2, req.Street3, req.Building, req.POBox} {
		if field != nil {
			*field = strings.TrimSpace(*field)
		}
	}
	for _, field := range []*string{req.CountryCode, req.RegionCode} {
		if field != nil {
			*field = strings.ToUpper(*field)
		}
	}
}

func textOf(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (a *API) updateAddress(w http.ResponseWriter, r *http.Request, id string) {
	var req updateAddressRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.normalize()
	if !canonicalUUID(req.VersionID) {
		badQuery(w, r, "versionId", "is required and must be a UUID")
		return
	}
	current, err := a.store.GetAddress(r.Context(), id)
	if err != nil {
		slog.Error("get address failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if current.VersionID != req.VersionID {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, "current version is "+current.VersionID))
		return
	}
	merged, changed := req.AddressPatch.Apply(current)
	merge := createAddressRequest{OwnerType: merged.OwnerType, OwnerID: merged.OwnerID, CountryCode: merged.CountryCode, RegionCode: textOf(merged.RegionCode), City: merged.City,
		PostalCode: textOf(merged.PostalCode), Street1: merged.Street1, Street2: textOf(merged.Street2), Street3: textOf(merged.Street3), Building: textOf(merged.Building),
		POBox: textOf(merged.POBox), ValidFrom: req.ValidFrom}
	for _, purpose := range merged.Purposes {
		merge.Purposes = append(merge.Purposes, addressPurposeRequest{Purpose: purpose.Purpose, Primary: purpose.Primary})
	}
	merge.normalize()
	if fields := merge.validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !changed && req.ValidFrom == "" {
		httpx.JSON(w, http.StatusOK, current)
		return
	}
	next := merge.toAddress()
	if next.SamePostal(current) {
		next.Latitude, next.Longitude, next.GeoStatus = current.Latitude, current.Longitude, current.GeoStatus
	} else {
		a.geocodeAddress(r, merge, &next)
	}
	revised, err := a.store.ReviseAddress(r.Context(), id, req.VersionID, next)
	if err != nil {
		slog.Error("revise address failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.addressRevised.Add(1)
	httpx.JSON(w, http.StatusOK, revised)
}

func (a *API) getAddressVersion(w http.ResponseWriter, r *http.Request, id string) {
	address, err := a.store.GetAddressVersion(r.Context(), id)
	if err != nil {
		slog.Error("get address version failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, address)
}

func (a *API) listAddresses(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, cursor, err := pageParams(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.AddressFilter{OwnerType: q.Get("ownerType"), OwnerID: canonicalID(q.Get("ownerId")), Purpose: q.Get("purpose"),
		Query: strings.TrimSpace(q.Get("q")), CountryCode: q.Get("countryCode")}
	searching := q.Has("q")
	if searching {
		a.addressSearches.Add(1)
	}
	ownerGiven := q.Get("ownerType") != "" || q.Get("ownerId") != ""
	qLength := utf8.RuneCountInString(f.Query)
	for _, c := range []struct {
		field, rule string
		invalid     bool
	}{
		{"q", "must be 3 to 100 characters", searching && (qLength < 3 || qLength > 100)},
		{"ownerType", "must be legal_entity, operating_unit, site, warehouse, location or party", (!searching || ownerGiven) && !slices.Contains(addressOwnerTypes, f.OwnerType)},
		{"ownerId", "is required and must be a UUID", (!searching || ownerGiven) && !canonicalUUID(f.OwnerID)},
		{"countryCode", "must be an ISO 3166-1 alpha-2 code", f.CountryCode != "" && !countryCode.MatchString(f.CountryCode)},
		{"purpose", "must be one of " + strings.Join(domain.AddressPurposes, ", "), f.Purpose != "" && !domain.ValidPurpose(f.Purpose)},
		{"cursor", "is invalid", cursor != "" && !canonicalUUID(cursor)},
	} {
		if c.invalid {
			badQuery(w, r, c.field, c.rule)
			return
		}
	}
	page, err := a.store.ListAddresses(r.Context(), f, cursor, limit)
	if err != nil {
		slog.Error("list addresses failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	body := addressList{Items: page.Items}
	if page.NextID != "" {
		body.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.NextID))
	}
	httpx.JSON(w, http.StatusOK, body)
}
