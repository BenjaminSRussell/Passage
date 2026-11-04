package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables/internal/party"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

const (
	dutyEmployeeVendorMaintain = "employee-vendor.maintain"
	pathEmployeeVendors        = "/v1/payables/employee-vendors"
	CodeEmployeeVendorUnknown  = "AP-EMP-001"
	CodeEmployeeVendorTaken    = "AP-EMP-002"
)

// EmployeeVendorStore keeps the employee-vendor register.
type EmployeeVendorStore interface {
	RegisterEmployeeVendor(ctx context.Context, legalEntityID, workerID, vendorID, by string) (store.EmployeeVendor, bool, error)
}

type employeeVendorDeps struct {
	store      EmployeeVendorStore
	registered atomic.Int64
}

// WithEmployeeVendors mounts the employee-vendor register; the vendor is checked against the party
// vendor master given by WithVendorBanks.
func (a *API) WithEmployeeVendors(s EmployeeVendorStore) *API {
	a.employeeVendors = &employeeVendorDeps{store: s}
	return a
}

func (a *API) registerEmployeeVendors(r chi.Router) {
	if a.employeeVendors == nil {
		return
	}
	r.With(authz.RequireDuty(dutyEmployeeVendorMaintain)).Put(pathEmployeeVendors+"/{workerId}", a.putEmployeeVendor)
}

type employeeVendorRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	VendorID      string `json:"vendorId"`
}

// putEmployeeVendor maps a worker to the party vendor that pays their reimbursements. The vendor must
// be in the party vendor master; a mapping is set once (another vendor is a 409, since moving a payee's
// money to a new account is a change this register does not make yet).
func (a *API) putEmployeeVendor(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	caller, _ := tenant.FromContext(r.Context())
	if caller.UserID == "" {
		a.problem(w, r, http.StatusForbidden, "AUTH-0003", "a registration needs an identified user")
		return
	}
	var req employeeVendorRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	worker := chi.URLParam(r, "workerId")
	fields := map[string]string{}
	if !ids.Valid(worker) {
		fields["workerId"] = "must be a UUID"
	}
	if !ids.Valid(req.VendorID) {
		fields["vendorId"] = "must be a UUID"
	}
	le, ok := a.resolveEntity(w, r, req.LegalEntityID)
	if !ok {
		return
	}
	if len(fields) > 0 {
		a.invalid(w, r, fields)
		return
	}
	worker, _ = ids.Canonical(worker)
	vendor, _ := ids.Canonical(req.VendorID)
	if a.runs == nil || a.runs.banks == nil {
		a.problem(w, r, http.StatusServiceUnavailable, codeNotConfigured, "the party vendor master is not configured; a vendor cannot be checked")
		return
	}
	if _, err := a.runs.banks.BankAccounts(r.Context(), le, vendor); errors.Is(err, party.ErrVendorNotFound) {
		a.problem(w, r, http.StatusUnprocessableEntity, CodeEmployeeVendorUnknown, "vendor "+vendor+" is not in the party vendor master")
		return
	} else if err != nil {
		a.partyUnavailable(w, r, err)
		return
	}
	got, created, err := a.employeeVendors.store.RegisterEmployeeVendor(r.Context(), le, worker, vendor, caller.UserID)
	switch {
	case errors.Is(err, store.ErrEmployeeVendorTaken):
		a.problem(w, r, http.StatusConflict, CodeEmployeeVendorTaken, "worker "+worker+" is already mapped to another vendor")
	case err != nil:
		a.fault(w, r, "employee vendor registration failed", err)
	case created:
		a.employeeVendors.registered.Add(1)
		slog.Info("employee vendor registered", "legalEntityId", le, "workerId", worker, "vendorId", vendor, "repointedItems", got.Repointed, "userId", caller.UserID, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusCreated, got)
	default:
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, got)
	}
}
