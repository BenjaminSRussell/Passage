package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

type organizationBody struct {
	ID            string `json:"id,omitempty"`
	LegalEntityID string `json:"legalEntityId"`
	Code          string `json:"code"`
	Name          string `json:"name"`
}

func bodyFromOrg(o domain.EngineeringOrganization) organizationBody {
	return organizationBody{ID: o.ID, LegalEntityID: o.LegalEntityID, Code: o.Code, Name: o.Name}
}

func (a *API) createOrganization(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body organizationBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	body.LegalEntityID = legalEntityID("legalEntityId", body.LegalEntityID, fields)
	validID("code", body.Code, fields)
	if n := len(body.Name); n == 0 || n > 200 {
		fields["name"] = "must be 1 to 200 characters"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	created, err := a.store.CreateOrganization(r.Context(), domain.EngineeringOrganization{LegalEntityID: body.LegalEntityID, Code: body.Code, Name: body.Name}, idemOf(r, body))
	if err != nil {
		a.fail(w, r, "create organization", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusCreated, bodyFromOrg(created))
}

func (a *API) listOrganizations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	listPage(a, w, r, "list organizations", &a.bomFailed, a.store.ListOrganizations, bodyFromOrg)
}

func (a *API) getOrganization(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	o, err := a.store.GetOrganization(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get organization", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromOrg(o))
}

type nomenclatureBody struct {
	ID        string                `json:"id,omitempty"`
	Code      string                `json:"code"`
	Separator string                `json:"separator,omitempty"`
	Segments  []domain.NomenSegment `json:"segments"`
}

func bodyFromNomenclature(n domain.Nomenclature) nomenclatureBody {
	return nomenclatureBody{ID: n.ID, Code: n.Code, Separator: n.Separator, Segments: n.Segments}
}

func (a *API) createNomenclature(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body nomenclatureBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	n := domain.Nomenclature{Code: body.Code, Separator: body.Separator, Segments: body.Segments}
	if err := n.Validate(); err != nil {
		a.fail(w, r, "create nomenclature", err, &a.bomFailed)
		return
	}
	created, err := a.store.CreateNomenclature(r.Context(), n, idemOf(r, body))
	if err != nil {
		a.fail(w, r, "create nomenclature", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusCreated, bodyFromNomenclature(created))
}

func (a *API) listNomenclatures(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	listPage(a, w, r, "list nomenclatures", &a.bomFailed, a.store.ListNomenclatures, bodyFromNomenclature)
}

func (a *API) getNomenclature(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	n, err := a.store.GetNomenclature(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get nomenclature", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromNomenclature(n))
}

func (a *API) nextNomenclature(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	n, err := a.store.GetNomenclature(r.Context(), id)
	if err != nil {
		a.fail(w, r, "next nomenclature", err, &a.bomFailed)
		return
	}
	next, err := n.Next(r.URL.Query().Get("last"), a.today())
	if err != nil {
		a.fail(w, r, "next nomenclature", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"next": next})
}

type categoryBody struct {
	ID                 string `json:"id,omitempty"`
	Code               string `json:"code"`
	Name               string `json:"name"`
	ProductType        string `json:"productType,omitempty"`
	TrackVersions      bool   `json:"trackVersions"`
	VersionAsDimension bool   `json:"versionAsDimension"`
	NomenclatureID     string `json:"nomenclatureId,omitempty"`
	ReadinessPolicyID  string `json:"readinessPolicyId,omitempty"`
	ReleasePolicyID    string `json:"releasePolicyId,omitempty"`
	ChangeControlled   bool   `json:"changeControlled"`
}

func bodyFromCategory(c domain.EngineeringCategory) categoryBody {
	return categoryBody{ID: c.ID, Code: c.Code, Name: c.Name, ProductType: c.ProductType, TrackVersions: c.TrackVersions, VersionAsDimension: c.VersionAsDimension, NomenclatureID: c.NomenclatureID, ReadinessPolicyID: c.ReadinessPolicyID, ReleasePolicyID: c.ReleasePolicyID, ChangeControlled: c.ChangeControlled}
}

func (a *API) createCategory(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body categoryBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	validID("code", body.Code, fields)
	if n := len(body.Name); n == 0 || n > 200 {
		fields["name"] = "must be 1 to 200 characters"
	}
	checkUUID("nomenclatureId", body.NomenclatureID, fields)
	checkUUID("readinessPolicyId", body.ReadinessPolicyID, fields)
	checkUUID("releasePolicyId", body.ReleasePolicyID, fields)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	created, err := a.store.CreateCategory(r.Context(), domain.EngineeringCategory{
		Code: body.Code, Name: body.Name, ProductType: body.ProductType, TrackVersions: body.TrackVersions,
		VersionAsDimension: body.VersionAsDimension, NomenclatureID: body.NomenclatureID, ReadinessPolicyID: body.ReadinessPolicyID,
		ReleasePolicyID: body.ReleasePolicyID, ChangeControlled: body.ChangeControlled,
	}, idemOf(r, body))
	if err != nil {
		a.fail(w, r, "create category", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusCreated, bodyFromCategory(created))
}

func (a *API) listCategories(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	listPage(a, w, r, "list categories", &a.bomFailed, a.store.ListCategories, bodyFromCategory)
}

func (a *API) getCategory(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := a.store.GetCategory(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get category", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromCategory(c))
}

type engineeringVersionBody struct {
	ID              string `json:"id,omitempty"`
	ProductID       string `json:"productId"`
	VersionCode     string `json:"versionCode"`
	EngineeringLEID string `json:"engineeringLeId"`
	CategoryID      string `json:"categoryId,omitempty"`
	BOMVersionID    string `json:"bomVersionId,omitempty"`
	RouteVersionID  string `json:"routeVersionId,omitempty"`
	LifecycleState  string `json:"lifecycleState,omitempty"`
}

func bodyFromEngVersion(v domain.EngineeringVersion) engineeringVersionBody {
	return engineeringVersionBody{ID: v.ID, ProductID: v.ProductID, VersionCode: v.VersionCode, EngineeringLEID: v.EngineeringLEID, CategoryID: v.CategoryID, BOMVersionID: v.BOMVersionID, RouteVersionID: v.RouteVersionID, LifecycleState: v.LifecycleState}
}

func (a *API) createEngineeringVersion(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body engineeringVersionBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	validID("productId", body.ProductID, fields)
	validID("versionCode", body.VersionCode, fields)
	body.EngineeringLEID = legalEntityID("engineeringLeId", body.EngineeringLEID, fields)
	checkUUID("categoryId", body.CategoryID, fields)
	checkUUID("bomVersionId", body.BOMVersionID, fields)
	checkUUID("routeVersionId", body.RouteVersionID, fields)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	created, err := a.store.CreateEngineeringVersion(r.Context(), domain.EngineeringVersion{
		ProductID: body.ProductID, VersionCode: body.VersionCode, EngineeringLEID: body.EngineeringLEID,
		CategoryID: body.CategoryID, BOMVersionID: body.BOMVersionID, RouteVersionID: body.RouteVersionID, LifecycleState: body.LifecycleState,
	}, idemOf(r, body))
	if err != nil {
		a.fail(w, r, "create engineering version", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusCreated, bodyFromEngVersion(created))
}

func (a *API) listEngineeringVersions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	productID := r.URL.Query().Get("productId")
	if len(productID) > maxIDLength {
		invalid(w, r, map[string]string{"productId": fmt.Sprintf("must be at most %d characters", maxIDLength)})
		return
	}
	listPage(a, w, r, "list engineering versions", &a.bomFailed, func(ctx context.Context, after string, limit int) (store.Page[domain.EngineeringVersion], error) {
		return a.store.ListEngineeringVersions(ctx, productID, after, limit)
	}, bodyFromEngVersion)
}

func (a *API) getEngineeringVersion(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := a.store.GetEngineeringVersion(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get engineering version", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromEngVersion(v))
}

func (a *API) putIdentifier(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body struct {
		IDType    string `json:"idType"`
		Value     string `json:"value"`
		ProductID string `json:"productId"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	validID("idType", body.IDType, fields)
	validID("value", body.Value, fields)
	validID("productId", body.ProductID, fields)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	if err := a.store.PutIdentifier(r.Context(), body.IDType, body.Value, body.ProductID); err != nil {
		a.fail(w, r, "put identifier", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, body)
}
