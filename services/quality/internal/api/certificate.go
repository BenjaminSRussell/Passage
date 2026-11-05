package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
)

// certificateRequest records one incoming vendor certificate (FR-33.15). Values
// are keyed by test id: a value for a variable test, an outcome id for an
// attribute test. documentRef names the document (upload, EDI attachment or
// supplier-portal document).
type certificateRequest struct {
	Type        string                      `json:"type"`
	DocumentRef string                      `json:"documentRef"`
	Values      map[string]domain.CertValue `json:"values"`
}

// certificateView is a recorded certificate with the order's gate status after it.
type certificateView struct {
	domain.CertRecord
	CertificateStatus string `json:"certificateStatus"`
}

// certificatesView is an order's certificate records with its requirement and gate status.
type certificatesView struct {
	Required          string              `json:"required"`
	CertificateStatus string              `json:"certificateStatus"`
	Items             []domain.CertRecord `json:"items"`
}

// orderIDParam reads the {id} of an order route; an id that is not a UUID is a 404.
func orderIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeOrderMissing, "quality order not found"))
		return "", false
	}
	return id, true
}

// recordCertificate records a certificate receipt (FR-33.15). Its values are
// checked against the order's test group; out of specification, the receipt is
// RECEIVED_NOK and raises a VENDOR NCR in the same transaction (BR-33.13).
func (a *API) recordCertificate(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	id, ok := orderIDParam(w, r)
	if !ok {
		return
	}
	var req certificateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	order, err := a.store.GetOrder(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	group, err := a.store.GetTestGroup(r.Context(), order.TestGroupID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.DocumentRef = strings.TrimSpace(req.DocumentRef)
	if fields := domain.ValidateCertificate(req.Type, req.DocumentRef, req.Values, group); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	p, _ := tenant.FromContext(r.Context())
	now := time.Now().UTC()
	inSpec, deviations := domain.EvaluateCertificate(req.Type, req.Values, group)
	rec := domain.CertRecord{ID: ids.New(), OrderID: id, Type: req.Type, DocumentRef: req.DocumentRef, Values: req.Values,
		InSpec: inSpec, Deviations: deviations, RecordedBy: p.UserID, RecordedAt: now}
	var ncr *domain.Nonconformance
	if !inSpec {
		n, err := domain.CertificateNCR(order, rec, now)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		ncr = &n
	}
	a.writeCertificate(w, r, order, rec, ncr, key)
}

type waiverRequest struct {
	Reason string `json:"reason"`
}

// waiveCertificate waives the order's required certificate (FR-33.15 note). It
// needs quality.usage.decide, the duty of the decision it unblocks, and a reason.
func (a *API) waiveCertificate(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	id, ok := orderIDParam(w, r)
	if !ok {
		return
	}
	var req waiverRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := domain.ValidateCertWaiver(req.Reason); err != nil {
		httpx.Fail(w, r, validation(map[string]string{"reason": "is required and at most 500 characters"}))
		return
	}
	order, err := a.store.GetOrder(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	rec := domain.CertRecord{ID: ids.New(), OrderID: id, Type: order.CertificateRequired, Waiver: true,
		Reason: strings.TrimSpace(req.Reason), RecordedBy: p.UserID, RecordedAt: time.Now().UTC()}
	a.writeCertificate(w, r, order, rec, nil, key)
}

// writeCertificate stores the record and answers with it and the order's gate
// status: 201 when stored, 200 for a replayed key. The store checks, under the
// order's lock, that the order still takes the record (CheckCertificateOrder).
func (a *API) writeCertificate(w http.ResponseWriter, r *http.Request, order domain.Order, rec domain.CertRecord, ncr *domain.Nonconformance, key string) {
	saved, inserted, err := a.store.RecordCertificate(r.Context(), rec, ncr, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if inserted {
		a.certificatesRecorded.Add(1)
	}
	recs, err := a.store.ListCertificates(r.Context(), order.ID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, statusFor(inserted), certificateView{CertRecord: saved, CertificateStatus: domain.CertificateStatusOf(order.CertificateRequired, recs)})
}

func (a *API) listCertificates(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := orderIDParam(w, r)
	if !ok {
		return
	}
	order, err := a.store.GetOrder(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	recs, err := a.store.ListCertificates(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, certificatesView{Required: order.CertificateRequired,
		CertificateStatus: domain.CertificateStatusOf(order.CertificateRequired, recs), Items: recs})
}
