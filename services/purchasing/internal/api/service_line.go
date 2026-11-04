package api

import (
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// outlineNodeBody is one node of a service outline in the request: a LEVEL with children, or an
// ITEM with a product or description, quantity, unit and unit price. total is what the caller
// expects a LEVEL to add up to (PRC-SRV-004 when it does not).
type outlineNodeBody struct {
	Kind        string            `json:"kind"`
	Description string            `json:"description,omitempty"`
	ProductID   string            `json:"productId,omitempty"`
	Qty         string            `json:"qty,omitempty"`
	Unit        string            `json:"unit,omitempty"`
	UnitPrice   string            `json:"unitPrice,omitempty"`
	Total       string            `json:"total,omitempty"`
	Children    []outlineNodeBody `json:"children,omitempty"`
}

// serviceSpecBody is the PUT body of a line's service specification (FR-17.23): the limits and
// either an outline or a modelId whose outline is copied. The outline may be empty (a limit item
// with no planned services).
type serviceSpecBody struct {
	ValueLimit              string            `json:"valueLimit"`
	ExpectedValue           string            `json:"expectedValue,omitempty"`
	ContractAgreementLineID string            `json:"contractAgreementLineId,omitempty"`
	ModelID                 string            `json:"modelId,omitempty"`
	Outline                 []outlineNodeBody `json:"outline,omitempty"`
}

// modelServiceBody is the POST body of a model service specification. PUT takes the same fields;
// code and legalEntityId are ignored there because they do not change.
type modelServiceBody struct {
	LegalEntityID string            `json:"legalEntityId"`
	Code          string            `json:"code"`
	Name          string            `json:"name"`
	Currency      string            `json:"currency"`
	Status        string            `json:"status,omitempty"`
	Outline       []outlineNodeBody `json:"outline,omitempty"`
}

// outlineNodes converts the request tree; the outline rules (sums, depth, counts) are the
// domain's, the shape (decimals, uuids) is checked here. depth bounds the recursion.
func outlineNodes(field string, in []outlineNodeBody, depth int) ([]domain.OutlineNode, error) {
	if depth > domain.MaxOutlineDepth {
		return nil, invalid(field, "nests deeper than "+strconv.Itoa(domain.MaxOutlineDepth)+" levels")
	}
	out := make([]domain.OutlineNode, 0, len(in))
	for i, n := range in {
		f := field + "[" + strconv.Itoa(i) + "]"
		if err := uuidField(f+".productId", n.ProductID, false); err != nil {
			return nil, err
		}
		qty, err := decimalField(f+".qty", n.Qty, "0")
		if err != nil {
			return nil, err
		}
		price, err := decimalField(f+".unitPrice", n.UnitPrice, "0")
		if err != nil {
			return nil, err
		}
		node := domain.OutlineNode{Kind: n.Kind, Description: n.Description, ProductID: n.ProductID, Qty: qty, Unit: n.Unit, UnitPrice: price}
		if n.Total != "" {
			total, err := decimalField(f+".total", n.Total, "")
			if err != nil {
				return nil, err
			}
			node.Total = &total
		}
		if node.Children, err = outlineNodes(f+".children", n.Children, depth+1); err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, nil
}

func serviceInputFrom(body serviceSpecBody) (store.ServiceInput, error) {
	limit, err := decimalField("valueLimit", body.ValueLimit, "")
	if err != nil {
		return store.ServiceInput{}, err
	}
	if strings.TrimSpace(body.ValueLimit) == "" {
		return store.ServiceInput{}, invalid("valueLimit", "is required; 0 allows no unplanned services")
	}
	expected, err := decimalField("expectedValue", body.ExpectedValue, "0")
	if err != nil {
		return store.ServiceInput{}, err
	}
	for _, f := range []struct{ name, v string }{{"contractAgreementLineId", body.ContractAgreementLineID}, {"modelId", body.ModelID}} {
		if err := uuidField(f.name, f.v, false); err != nil {
			return store.ServiceInput{}, err
		}
	}
	if body.ModelID != "" && len(body.Outline) > 0 {
		return store.ServiceInput{}, invalid("outline", "is copied from the model; give a modelId or an outline, not both")
	}
	nodes, err := outlineNodes("outline", body.Outline, 1)
	if err != nil {
		return store.ServiceInput{}, err
	}
	return store.ServiceInput{Terms: domain.ServiceTerms{ValueLimit: limit, ExpectedValue: expected,
		ContractAgreementLineID: body.ContractAgreementLineID}, Outline: nodes, ModelID: body.ModelID}, nil
}

// listServiceSpecs handles GET /purchase-orders/{id}/service-lines: the service lines of a PO
// with their outlines and limits.
func (a *API) listServiceSpecs(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	items, err := a.store.ListServiceSpecs(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, pageBody[domain.ServiceSpec]{Items: items})
}

// getServiceSpec handles GET /purchase-orders/{id}/lines/{lineNo}/service. The ETag is the PO row
// version, which PUT and DELETE take as If-Match.
func (a *API) getServiceSpec(w http.ResponseWriter, r *http.Request) {
	id, lineNo, err := planLine(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetServiceSpec(r.Context(), id, lineNo)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.PORowVersion))
	httpx.JSON(w, http.StatusOK, out)
}

// putServiceSpec handles PUT /purchase-orders/{id}/lines/{lineNo}/service: saves the line's
// service specification (FR-17.23). If-Match is the PO row version (BR-17.8). PUT is idempotent,
// so no Idempotency-Key is taken.
func (a *API) putServiceSpec(w http.ResponseWriter, r *http.Request) {
	id, lineNo, err := planLine(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body serviceSpecBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	in, err := serviceInputFrom(body)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.PutServiceSpec(r.Context(), id, lineNo, want, in)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.serviceSpecsSaved.Add(1)
	w.Header().Set("ETag", etag(out.PORowVersion))
	httpx.JSON(w, http.StatusOK, out)
}

// deleteServiceSpec handles DELETE /purchase-orders/{id}/lines/{lineNo}/service.
func (a *API) deleteServiceSpec(w http.ResponseWriter, r *http.Request) {
	id, lineNo, err := planLine(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	v, err := a.store.DeleteServiceSpec(r.Context(), id, lineNo, want)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(v))
	w.WriteHeader(http.StatusNoContent)
}

// checkServiceLimit handles GET /purchase-orders/{id}/lines/{lineNo}/service/limit-check?unplanned=&entry=:
// whether an entry of unplanned services fits the value limit and the contract limit (BR-17.19,
// AC-17.17). Wave 18 asks for a service entry sheet; a refused entry answers 200 with allowed
// false and code PRC-SRV-003, so the caller keeps the figures. It changes nothing.
func (a *API) checkServiceLimit(w http.ResponseWriter, r *http.Request) {
	id, lineNo, err := planLine(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	unplanned, err := decimalField("unplanned", q.Get("unplanned"), "0")
	if err != nil {
		fail(w, r, err)
		return
	}
	if q.Get("entry") == "" {
		fail(w, r, invalid("entry", "is required"))
		return
	}
	entry, err := decimalField("entry", q.Get("entry"), "")
	if err != nil {
		fail(w, r, err)
		return
	}
	if unplanned.IsNegative() || entry.IsNegative() {
		fail(w, r, invalid("entry", "unplanned and entry must not be negative"))
		return
	}
	out, err := a.store.CheckServiceLimit(r.Context(), id, lineNo, unplanned, entry)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.serviceLimitChecks.Add(1)
	httpx.JSON(w, http.StatusOK, out)
}

// modelServiceFrom validates a body into a model and its requested outline.
func modelServiceFrom(body modelServiceBody) (domain.ModelService, []domain.OutlineNode, error) {
	cur, err := currencyField(body.Currency)
	if err != nil {
		return domain.ModelService{}, nil, err
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || utf8.RuneCountInString(name) > 200 {
		return domain.ModelService{}, nil, invalid("name", "is required (at most 200 characters)")
	}
	if !domain.ValidModelCode(body.Code) {
		return domain.ModelService{}, nil, invalid("code", "must be 1 to 20 of A-Z, 0-9, underscore and hyphen")
	}
	status := orDefault(body.Status, domain.ModelActive)
	if status != domain.ModelActive && status != domain.ModelInactive {
		return domain.ModelService{}, nil, invalid("status", "must be ACTIVE or INACTIVE")
	}
	nodes, err := outlineNodes("outline", body.Outline, 1)
	if err != nil {
		return domain.ModelService{}, nil, err
	}
	return domain.ModelService{LegalEntityID: body.LegalEntityID, Code: body.Code, Name: name, Currency: cur, Status: status}, nodes, nil
}

func (a *API) createModelServiceSpec(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body modelServiceBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if err := uuidField("legalEntityId", body.LegalEntityID, true); err != nil {
		fail(w, r, err)
		return
	}
	m, nodes, err := modelServiceFrom(body)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, m.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.CreateModelServiceSpec(r.Context(), m, nodes, store.Idem{Key: key, Hash: hashBody("model-service-spec.create", body)})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.modelServiceSpecsSaved.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) getModelServiceSpec(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetModelServiceSpec(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listModelServiceSpecs(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.ModelServiceFilter{LegalEntityID: q.Get("legalEntityId"), Status: q.Get("status"), Text: strings.TrimSpace(q.Get("q"))}
	if err := uuidField("legalEntityId", f.LegalEntityID, false); err != nil {
		fail(w, r, err)
		return
	}
	if f.Status != "" && f.Status != domain.ModelActive && f.Status != domain.ModelInactive {
		fail(w, r, invalid("status", "must be ACTIVE or INACTIVE"))
		return
	}
	page, err := a.store.ListModelServiceSpecs(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// changeModelServiceSpec handles PUT /model-service-specs/{id}: it replaces everything but the
// code and the legal entity under If-Match.
func (a *API) changeModelServiceSpec(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body modelServiceBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	cur, err := a.store.GetModelServiceSpec(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	body.LegalEntityID, body.Code = cur.LegalEntityID, cur.Code
	m, nodes, err := modelServiceFrom(body)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.UpdateModelServiceSpec(r.Context(), id, want, m, nodes)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.modelServiceSpecsSaved.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// registerServiceRoutes mounts the FR-17.23 routes under /v1/procurement. The specification of a
// line is written with PurchaseOrderMaintain and read with PurchaseOrderInquire or the lookup
// duty (receiving's), models are set up with ProcurementPolicyMaintain like charge codes.
func (a *API) registerServiceRoutes(r chi.Router) {
	maintain := r.With(authz.RequireDuty(dutyPurchaseOrderMaintain))
	lookup := r.With(authz.RequireAnyDuty(dutyPurchaseOrderInquire, dutyPurchaseOrderLookup))
	lookup.Get("/purchase-orders/{id}/service-lines", a.listServiceSpecs)
	lookup.Get("/purchase-orders/{id}/lines/{lineNo}/service", a.getServiceSpec)
	lookup.Get("/purchase-orders/{id}/lines/{lineNo}/service/limit-check", a.checkServiceLimit)
	maintain.Put("/purchase-orders/{id}/lines/{lineNo}/service", a.putServiceSpec)
	maintain.Delete("/purchase-orders/{id}/lines/{lineNo}/service", a.deleteServiceSpec)
	write := r.With(authz.RequireDuty(dutyPolicyMaintain))
	read := r.With(authz.Require(anyDuty{dutyPolicyMaintain, dutyPurchaseOrderInquire},
		dutyPolicyMaintain+" or "+dutyPurchaseOrderInquire, ""))
	write.Post("/model-service-specs", a.createModelServiceSpec)
	read.Get("/model-service-specs", a.listModelServiceSpecs)
	read.Get("/model-service-specs/{id}", a.getModelServiceSpec)
	write.Put("/model-service-specs/{id}", a.changeModelServiceSpec)
}
