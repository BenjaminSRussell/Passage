package api

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

// Supplier collaboration routes (#511, items 5 and 6). The /vendor-portal routes are for the supplier portal service: the duties are
// service-only and the vendor comes from the verified service token, never from the request. The other routes are the buyer's.
const (
	dutyPortalRead  = "party.vendor-portal.read"
	dutyPortalWrite = "party.vendor-portal.write"
	// dutyVendorRequestDecide approves or rejects vendor requests and onboardings; it is separate from party.approve.
	dutyVendorRequestDecide = "party.vendor-request.decide"
	codeSupplierScope       = "PTY-SUP-SCOPE"
	// uploadBodyLimit leaves room for the base64 of a 5 MiB file.
	uploadBodyLimit = 8 << 20
)

// WithSuppliers enables the supplier collaboration routes.
func (a *API) WithSuppliers(s store.SupplierStore) *API {
	a.suppliers = s
	return a
}

func (a *API) registerSuppliers(r chi.Router) {
	read, write := authz.RequireDuty(dutyPortalRead), authz.RequireDuty(dutyPortalWrite)
	r.With(read).Get("/v1/party/vendor-portal/requests", a.vendorListRequests)
	r.With(read).Get("/v1/party/vendor-portal/requests/{id}", a.vendorGetRequest)
	r.With(write).Post("/v1/party/vendor-portal/requests", a.vendorCreateRequest)
	r.With(write).Post("/v1/party/vendor-portal/requests/{id}:submit", a.vendorSubmitRequest)
	r.With(write).Post("/v1/party/vendor-portal/requests/{id}:withdraw", a.vendorWithdrawRequest)
	r.With(read).Get("/v1/party/vendor-portal/documents", a.vendorListDocuments)
	r.With(read).Get("/v1/party/vendor-portal/documents/{id}/content", a.vendorDocumentContent)
	r.With(write).Post("/v1/party/vendor-portal/documents", a.vendorUploadDocument)
	r.With(read).Get("/v1/party/vendor-portal/distributions", a.vendorListDistributions)
	r.With(read).Get("/v1/party/vendor-portal/distributions/{id}/content", a.vendorDistributionContent)
	r.With(write).Post("/v1/party/vendor-portal/distributions/{id}:acknowledge", a.vendorAcknowledge)

	buyerRead, buyerWrite, approve := authz.RequireDuty(dutyRead), authz.RequireDuty(dutyWrite), authz.RequireDuty(dutyVendorRequestDecide)
	r.With(buyerWrite).Post("/v1/party/vendor-questionnaires", a.createQuestionnaire)
	r.With(buyerRead).Get("/v1/party/vendor-questionnaires", a.listQuestionnaires)
	r.With(buyerWrite).Post("/v1/party/vendors/{id}/onboarding", a.inviteOnboarding)
	r.With(buyerRead).Get("/v1/party/vendor-requests", a.buyerListRequests)
	r.With(buyerRead).Get("/v1/party/vendor-requests/{id}", a.buyerGetRequest)
	r.With(approve).Post("/v1/party/vendor-requests/{id}:approve", a.decideRequest(true))
	r.With(approve).Post("/v1/party/vendor-requests/{id}:reject", a.decideRequest(false))
	r.With(buyerRead).Get("/v1/party/vendor-documents", a.buyerListDocuments)
	r.With(buyerRead).Get("/v1/party/vendor-documents/{id}/content", a.buyerDocumentContent)
	r.With(buyerWrite).Post("/v1/party/vendors/{id}/documents", a.buyerSendDocument)
	r.With(buyerWrite).Post("/v1/party/controlled-documents", a.publishControlled)
	r.With(buyerRead).Get("/v1/party/controlled-documents", a.listControlled)
	r.With(buyerWrite).Post("/v1/party/controlled-documents/{id}:distribute", a.distributeControlled)
	r.With(buyerRead).Get("/v1/party/controlled-documents/{id}/distributions", a.listDocumentDistributions)
	r.With(buyerRead).Get("/v1/party/vendor-audit", a.listSupplierAudit)
}

// portalVendor is the vendor the verified caller acts for. A caller that is not a supplier portal service token is refused, so
// the vendor-portal duties alone never open another vendor's rows.
func portalVendor(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || p.Service != tenant.VendorPortalIssuer || !ids.Valid(p.VendorID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeSupplierScope, "this account is not a supplier account"))
		return "", false
	}
	return p.VendorID, true
}

func pageLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 || n > 200 {
		return 50
	}
	return n
}

func invalid(w http.ResponseWriter, r *http.Request, errs map[string]string) {
	p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeSupplierInvalid, "request failed validation")
	p.Errors = errs
	httpx.Fail(w, r, p)
}

func (a *API) vendorListRequests(w http.ResponseWriter, r *http.Request) {
	vendor, ok := portalVendor(w, r)
	if !ok {
		return
	}
	items, err := a.suppliers.ListRequests(r.Context(), vendor, r.URL.Query().Get("status"), pageLimit(r))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) vendorGetRequest(w http.ResponseWriter, r *http.Request) {
	vendor, ok := portalVendor(w, r)
	if !ok {
		return
	}
	req, err := a.suppliers.GetRequest(r.Context(), vendor, chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, req)
}

type requestBody struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Details string `json:"details"`
}

func (a *API) vendorCreateRequest(w http.ResponseWriter, r *http.Request) {
	vendor, ok := portalVendor(w, r)
	if !ok {
		return
	}
	var req requestBody
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Subject, req.Details = strings.TrimSpace(req.Subject), strings.TrimSpace(req.Details)
	errs := map[string]string{}
	if req.Kind != domain.RequestProfileChange && req.Kind != domain.RequestOther {
		errs["kind"] = "must be PROFILE_CHANGE or OTHER; an onboarding is opened by the buyer"
	}
	if n := len([]rune(req.Subject)); n < 1 || n > 200 {
		errs["subject"] = "must be 1 to 200 characters"
	}
	if n := len([]rune(req.Details)); n < 1 || n > 4000 {
		errs["details"] = "must be 1 to 4000 characters"
	}
	if len(errs) > 0 {
		invalid(w, r, errs)
		return
	}
	out, err := a.suppliers.CreateRequest(r.Context(), vendor, req.Kind, req.Subject, req.Details)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) vendorSubmitRequest(w http.ResponseWriter, r *http.Request) {
	vendor, ok := portalVendor(w, r)
	if !ok {
		return
	}
	var req struct {
		Answers map[string]string `json:"answers"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := a.suppliers.SubmitRequest(r.Context(), vendor, chi.URLParam(r, "id"), req.Answers)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) vendorWithdrawRequest(w http.ResponseWriter, r *http.Request) {
	vendor, ok := portalVendor(w, r)
	if !ok {
		return
	}
	out, err := a.suppliers.WithdrawRequest(r.Context(), vendor, chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type uploadBody struct {
	Title         string `json:"title"`
	FileName      string `json:"fileName"`
	ContentType   string `json:"contentType"`
	ContentBase64 string `json:"contentBase64"`
}

func (b uploadBody) document() (domain.Document, map[string]string) {
	content, err := base64.StdEncoding.DecodeString(b.ContentBase64)
	doc, errs := domain.CheckDocument(b.Title, b.FileName, b.ContentType, content)
	if err != nil {
		errs["content"] = "must be base64"
	}
	return doc, errs
}

func (a *API) upload(w http.ResponseWriter, r *http.Request, vendorID, direction string) {
	var body uploadBody
	if err := httpx.DecodeLimit(r, &body, uploadBodyLimit); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	doc, errs := body.document()
	if len(errs) > 0 {
		invalid(w, r, errs)
		return
	}
	meta, err := a.suppliers.AddDocument(r.Context(), vendorID, direction, doc)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, meta)
}

func (a *API) vendorUploadDocument(w http.ResponseWriter, r *http.Request) {
	if vendor, ok := portalVendor(w, r); ok {
		a.upload(w, r, vendor, domain.DirectionFromVendor)
	}
}

func (a *API) buyerSendDocument(w http.ResponseWriter, r *http.Request) {
	a.upload(w, r, chi.URLParam(r, "id"), domain.DirectionToVendor)
}

func writeContent(w http.ResponseWriter, d store.DocumentContent) {
	w.Header().Set("Content-Type", d.ContentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.NewReplacer(`"`, "", "\r", "", "\n", "").Replace(d.FileName)+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-SHA256", d.SHA256)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(d.Content)
}

func (a *API) vendorListDocuments(w http.ResponseWriter, r *http.Request) {
	vendor, ok := portalVendor(w, r)
	if !ok {
		return
	}
	items, err := a.suppliers.ListDocuments(r.Context(), vendor, pageLimit(r))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) vendorDocumentContent(w http.ResponseWriter, r *http.Request) {
	vendor, ok := portalVendor(w, r)
	if !ok {
		return
	}
	d, err := a.suppliers.GetDocument(r.Context(), vendor, chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	writeContent(w, d)
}

func (a *API) vendorListDistributions(w http.ResponseWriter, r *http.Request) {
	vendor, ok := portalVendor(w, r)
	if !ok {
		return
	}
	items, err := a.suppliers.ListDistributions(r.Context(), "", vendor)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) vendorDistributionContent(w http.ResponseWriter, r *http.Request) {
	vendor, ok := portalVendor(w, r)
	if !ok {
		return
	}
	d, err := a.suppliers.GetDistributedContent(r.Context(), vendor, chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	writeContent(w, d)
}

func (a *API) vendorAcknowledge(w http.ResponseWriter, r *http.Request) {
	vendor, ok := portalVendor(w, r)
	if !ok {
		return
	}
	out, err := a.suppliers.Acknowledge(r.Context(), vendor, chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) createQuestionnaire(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string            `json:"name"`
		Questions []domain.Question `json:"questions"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	errs := domain.ValidateQuestions(req.Questions)
	req.Name = strings.TrimSpace(req.Name)
	if n := len([]rune(req.Name)); n < 1 || n > 140 {
		errs["name"] = "must be 1 to 140 characters"
	}
	if len(errs) > 0 {
		invalid(w, r, errs)
		return
	}
	out, err := a.suppliers.CreateQuestionnaire(r.Context(), req.Name, req.Questions)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listQuestionnaires(w http.ResponseWriter, r *http.Request) {
	items, err := a.suppliers.ListQuestionnaires(r.Context())
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) inviteOnboarding(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionnaireID string `json:"questionnaireId"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := a.suppliers.InviteOnboarding(r.Context(), chi.URLParam(r, "id"), req.QuestionnaireID)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) buyerListRequests(w http.ResponseWriter, r *http.Request) {
	items, err := a.suppliers.ListRequests(r.Context(), r.URL.Query().Get("vendorId"), r.URL.Query().Get("status"), pageLimit(r))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) buyerGetRequest(w http.ResponseWriter, r *http.Request) {
	out, err := a.suppliers.GetRequest(r.Context(), "", chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) decideRequest(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Note string `json:"note"`
		}
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &req); err != nil {
				httpx.Fail(w, r, err)
				return
			}
		}
		if !approve && strings.TrimSpace(req.Note) == "" {
			invalid(w, r, map[string]string{"note": "is required when rejecting"})
			return
		}
		if len([]rune(req.Note)) > 1000 {
			invalid(w, r, map[string]string{"note": "must be at most 1000 characters"})
			return
		}
		out, err := a.suppliers.DecideRequest(r.Context(), chi.URLParam(r, "id"), approve, strings.TrimSpace(req.Note))
		if err != nil {
			failParty(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

func (a *API) buyerListDocuments(w http.ResponseWriter, r *http.Request) {
	items, err := a.suppliers.ListDocuments(r.Context(), r.URL.Query().Get("vendorId"), pageLimit(r))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) buyerDocumentContent(w http.ResponseWriter, r *http.Request) {
	d, err := a.suppliers.GetDocument(r.Context(), "", chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	writeContent(w, d)
}

func (a *API) publishControlled(w http.ResponseWriter, r *http.Request) {
	var body struct {
		uploadBody
		DocNo       string `json:"docNo"`
		RequiresAck *bool  `json:"requiresAck"`
	}
	if err := httpx.DecodeLimit(r, &body, uploadBodyLimit); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	doc, errs := body.document()
	if !domain.CheckDocNo(body.DocNo) {
		errs["docNo"] = "must be 1 to 40 letters, digits, dot, dash, slash or underscore"
	}
	if len(errs) > 0 {
		invalid(w, r, errs)
		return
	}
	out, err := a.suppliers.PublishControlled(r.Context(), body.DocNo, body.RequiresAck == nil || *body.RequiresAck, doc)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listControlled(w http.ResponseWriter, r *http.Request) {
	items, err := a.suppliers.ListControlled(r.Context(), pageLimit(r))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) distributeControlled(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VendorIDs []string `json:"vendorIds"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(req.VendorIDs) < 1 || len(req.VendorIDs) > 200 {
		invalid(w, r, map[string]string{"vendorIds": "must hold 1 to 200 vendors"})
		return
	}
	out, err := a.suppliers.Distribute(r.Context(), chi.URLParam(r, "id"), req.VendorIDs)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) listDocumentDistributions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		failParty(w, r, store.ErrNotFound)
		return
	}
	out, err := a.suppliers.ListDistributions(r.Context(), id, "")
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) listSupplierAudit(w http.ResponseWriter, r *http.Request) {
	items, err := a.suppliers.ListAudit(r.Context(), r.URL.Query().Get("vendorId"), pageLimit(r))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
