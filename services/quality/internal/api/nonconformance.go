package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/internal/store"
)

// Duties of the nonconformance screens (section 6.33.17).
const (
	dutyNCR  = "QM_NCR_MAINTAIN"
	dutyCAPA = "QM_CAPA_MAINTAIN"

	dutyCAPAExecute = "quality.capa.execute"
)

// ncrRequest is the create body. Links are optional UUIDs; an NCR that names a
// quality order takes its item, dims and unit when they are left out.
type ncrRequest struct {
	Source            string            `json:"source"`
	ItemID            string            `json:"itemId"`
	Dims              map[string]string `json:"dims"`
	Qty               decimal.Decimal   `json:"qty"`
	UOM               string            `json:"uom"`
	ProblemType       string            `json:"problemType"`
	Severity          string            `json:"severity"`
	Description       string            `json:"description"`
	QualityOrderID    string            `json:"qualityOrderId"`
	VendorID          string            `json:"vendorId"`
	CustomerID        string            `json:"customerId"`
	ProductionOrderID string            `json:"productionOrderId"`
	QuarantineOrderID string            `json:"quarantineOrderId"`
}

// linkFields reports every id in the request that is not a UUID.
func linkFields(req ncrRequest) map[string]string {
	fields := map[string]string{}
	for name, v := range map[string]string{"itemId": req.ItemID, "qualityOrderId": req.QualityOrderID, "vendorId": req.VendorID,
		"customerId": req.CustomerID, "productionOrderId": req.ProductionOrderID, "quarantineOrderId": req.QuarantineOrderID} {
		if v != "" && !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	return fields
}

// createNonconformance raises an NCR (FR-33.7). Against a quality order the item
// must be the order's and the quantity at most its lot quantity.
func (a *API) createNonconformance(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req ncrRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := linkFields(req)
	for k, v := range dimsFields(req.Dims) {
		fields[k] = v
	}
	n := domain.Nonconformance{Source: req.Source, ItemID: req.ItemID, Dims: req.Dims, Qty: req.Qty, UOM: strings.TrimSpace(req.UOM),
		ProblemType: req.ProblemType, Severity: req.Severity, Description: strings.TrimSpace(req.Description),
		QualityOrderID: req.QualityOrderID, VendorID: req.VendorID, CustomerID: req.CustomerID,
		ProductionOrderID: req.ProductionOrderID, QuarantineOrderID: req.QuarantineOrderID}
	if n.QualityOrderID != "" && fields["qualityOrderId"] == "" {
		order, err := a.store.GetOrder(r.Context(), n.QualityOrderID)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if n.ItemID == "" {
			n.ItemID = order.ItemID
		}
		if n.Dims == nil {
			n.Dims = order.Dims
		}
		if n.UOM == "" {
			n.UOM = order.UOM
		}
		if n.ItemID != order.ItemID {
			fields["itemId"] = "must be the quality order's item"
		}
		if n.Qty.GreaterThan(order.LotQty) {
			fields["qty"] = "must be at most the quality order's lot quantity " + order.LotQty.String()
		}
	}
	if n.ItemID == "" {
		fields["itemId"] = "is required"
	}
	for name, msg := range domain.ValidateNCR(n) {
		if _, taken := fields[name]; !taken {
			fields[name] = msg
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	p, _ := tenant.FromContext(r.Context())
	n, err := domain.NewNCR(n, p.UserID, time.Now())
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	n.ID = ids.New()
	saved, inserted, err := a.store.CreateNonconformance(r.Context(), n, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if inserted {
		a.ncrsCreated.Add(1)
	}
	writeNCR(w, saved, statusFor(inserted))
}

// writeNCR answers with the NCR, its ETag and the read-time correction fields.
func writeNCR(w http.ResponseWriter, n domain.Nonconformance, status int) {
	if n.Correction != nil {
		c := n.Correction.WithComputed(time.Now())
		n.Correction = &c
	}
	setETag(w, n.Version)
	httpx.JSON(w, status, n)
}

// listNonconformances answers ?status=&source=&severity=&qualityOrderId=&limit=, newest first.
func (a *API) listNonconformances(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.NCRFilter{Status: q.Get("status"), Source: q.Get("source"), Severity: q.Get("severity"), QualityOrderID: q.Get("qualityOrderId")}
	fields := map[string]string{}
	if f.Status != "" && !domain.ValidNCRStatus(f.Status) {
		fields["status"] = "is not a nonconformance status"
	}
	if f.Source != "" && !domain.ValidNCRSource(f.Source) {
		fields["source"] = "is not a nonconformance source"
	}
	if f.Severity != "" && !domain.ValidNCRSeverity(f.Severity) {
		fields["severity"] = "must be MINOR, MAJOR or CRITICAL"
	}
	if f.QualityOrderID != "" && !ids.Valid(f.QualityOrderID) {
		fields["qualityOrderId"] = "must be a UUID"
	}
	limit, err := limitParam(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	f.Limit = limit
	items, err := a.store.ListNonconformances(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, list[domain.Nonconformance]{Items: items})
}

func ncrID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeNCRMissing, "nonconformance not found"))
		return "", false
	}
	return id, true
}

func (a *API) getNonconformance(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := ncrID(w, r)
	if !ok {
		return
	}
	n, err := a.store.GetNonconformance(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeNCR(w, n, http.StatusOK)
}

// change runs one guarded change of an NCR: If-Match carries its version.
func (a *API) change(w http.ResponseWriter, r *http.Request, key string, body any,
	apply func(n domain.Nonconformance, user string, now time.Time) (domain.Nonconformance, error)) {
	if !validTenant(w, r) {
		return
	}
	id, ok := ncrID(w, r)
	if !ok {
		return
	}
	if body != nil {
		if err := httpx.Decode(r, body); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	version, err := ifMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	now := time.Now()
	n, applied, err := a.store.UpdateNonconformance(r.Context(), id, version, key, func(cur domain.Nonconformance) (domain.Nonconformance, error) {
		next, err := apply(cur, p.UserID, now)
		return next, problemFrom(err)
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if key != "" && applied {
		status = http.StatusCreated
	}
	writeNCR(w, n, status)
}

func (a *API) approveNonconformance(w http.ResponseWriter, r *http.Request) {
	a.change(w, r, "", nil, func(n domain.Nonconformance, _ string, now time.Time) (domain.Nonconformance, error) {
		return n.Approve(now)
	})
}

type reasonRequest struct {
	Reason string `json:"reason"`
}

func (a *API) refuseNonconformance(w http.ResponseWriter, r *http.Request) {
	var req reasonRequest
	a.change(w, r, "", &req, func(n domain.Nonconformance, _ string, now time.Time) (domain.Nonconformance, error) {
		return n.Refuse(req.Reason, now)
	})
}

type correctionRequest struct {
	EffectivenessCheckDays int `json:"effectivenessCheckDays"`
}

// startCorrection opens the 8D of an approved NCR (FR-33.8). It takes an
// Idempotency-Key as well as If-Match: a replayed key answers 200 with the NCR.
func (a *API) startCorrection(w http.ResponseWriter, r *http.Request) {
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req correctionRequest
	a.change(w, r, key, &req, func(n domain.Nonconformance, user string, now time.Time) (domain.Nonconformance, error) {
		return n.StartCorrection(user, req.EffectivenessCheckDays, now)
	})
}

type stepRequest struct {
	Step  int    `json:"step"`
	Notes string `json:"notes"`
}

// completeStep completes the current 8D step (D1 to D7).
func (a *API) completeStep(w http.ResponseWriter, r *http.Request) {
	var req stepRequest
	a.change(w, r, "", &req, func(n domain.Nonconformance, user string, now time.Time) (domain.Nonconformance, error) {
		return n.CompleteStep(req.Step, req.Notes, user, now)
	})
}

type effectivenessRequest struct {
	Result string `json:"result"`
	Note   string `json:"note"`
}

// checkEffectiveness records the effectiveness check; EFFECTIVE closes the 8D and
// the NCR and writes capa.closed.
func (a *API) checkEffectiveness(w http.ResponseWriter, r *http.Request) {
	var req effectivenessRequest
	a.change(w, r, "", &req, func(n domain.Nonconformance, user string, now time.Time) (domain.Nonconformance, error) {
		return n.CheckEffectiveness(req.Result, req.Note, user, now)
	})
}
