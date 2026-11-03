package api

import (
	"net/http"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

type leaseGLPostRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	LedgerID      string `json:"ledgerId"`
	LeaseID       string `json:"leaseId"`
	PostThrough   string `json:"postThrough"`
}

// postLeasesToGL books the IFRS 16 journals of the leases of a legal entity (or one lease) up to postThrough (D4d).
func (a *API) postLeasesToGL(w http.ResponseWriter, r *http.Request) {
	if !a.glReady(w, r) {
		return
	}
	var req leaseGLPostRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	for name, v := range map[string]string{"legalEntityId": req.LegalEntityID, "ledgerId": req.LedgerID} {
		if !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if req.LeaseID != "" && !ids.Valid(req.LeaseID) {
		fields["leaseId"] = "must be a UUID"
	}
	through, err := store.ParseDate(req.PostThrough)
	if err != nil {
		fields["postThrough"] = "must be a date such as 2026-12-31"
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !inScope(w, r, req.LegalEntityID) {
		return
	}
	res, err := a.gl.PostLeasesToGL(r.Context(), a.ledger, store.LeaseGLPostRequest{LegalEntityID: req.LegalEntityID, LedgerID: req.LedgerID, LeaseID: req.LeaseID, PostThrough: through})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.glBooked.Add(int64(len(res.Booked)))
	httpx.JSON(w, http.StatusOK, res)
}

// getLeasePostings tells which journals of a lease are in the ledger.
func (a *API) getLeasePostings(w http.ResponseWriter, r *http.Request) {
	if a.gl == nil {
		a.glReady(w, r)
		return
	}
	id, ok := leaseID(w, r)
	if !ok {
		return
	}
	out, err := a.gl.GetLeasePostings(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type leaseProfileRequest struct {
	LegalEntityID       string `json:"legalEntityId"`
	ROUAsset            string `json:"rouAssetAccount"`
	ROUAccumulated      string `json:"rouAccumulatedDepreciationAccount"`
	Liability           string `json:"liabilityAccount"`
	InterestExpense     string `json:"interestExpenseAccount"`
	DepreciationExpense string `json:"depreciationExpenseAccount"`
	PaymentClearing     string `json:"paymentClearingAccount"`
	RemeasurementGain   string `json:"remeasurementGainAccount"`
}

func (a *API) getLeasePostingProfile(w http.ResponseWriter, r *http.Request) {
	if a.gl == nil {
		a.glReady(w, r)
		return
	}
	entity := r.URL.Query().Get("legalEntityId")
	if !ids.Valid(entity) {
		writeValidation(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	if !inScope(w, r, entity) {
		return
	}
	out, err := a.gl.GetLeasePostingProfile(r.Context(), entity)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if out.Version > 0 {
		setETag(w, out.Version)
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) saveLeasePostingProfile(w http.ResponseWriter, r *http.Request) {
	if a.gl == nil {
		a.glReady(w, r)
		return
	}
	var req leaseProfileRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	for name, v := range map[string]string{"rouAssetAccount": req.ROUAsset, "rouAccumulatedDepreciationAccount": req.ROUAccumulated, "liabilityAccount": req.Liability,
		"interestExpenseAccount": req.InterestExpense, "depreciationExpenseAccount": req.DepreciationExpense, "paymentClearingAccount": req.PaymentClearing,
		"remeasurementGainAccount": req.RemeasurementGain} {
		if !accountPattern.MatchString(strings.TrimSpace(v)) {
			fields[name] = "must be an account number of at most 40 characters"
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !inScope(w, r, req.LegalEntityID) {
		return
	}
	version := 0
	if r.Header.Get("If-Match") != "" {
		var ok bool
		if version, ok = ifMatch(w, r); !ok {
			return
		}
	}
	p, _ := tenant.FromContext(r.Context())
	saved, created, err := a.gl.SaveLeasePostingProfile(r.Context(), store.LeasePostingProfile{LegalEntityID: req.LegalEntityID, ROUAsset: strings.TrimSpace(req.ROUAsset),
		ROUAccumulated: strings.TrimSpace(req.ROUAccumulated), Liability: strings.TrimSpace(req.Liability), InterestExpense: strings.TrimSpace(req.InterestExpense),
		DepreciationExpense: strings.TrimSpace(req.DepreciationExpense), PaymentClearing: strings.TrimSpace(req.PaymentClearing),
		RemeasurementGain: strings.TrimSpace(req.RemeasurementGain)}, version, p.UserID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, saved.Version)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, saved)
}
