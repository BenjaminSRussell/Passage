package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/inventory"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/store"
)

func (a *API) WithInventory(r inventory.Reader) *API {
	a.onHand = r
	return a
}

func (a *API) WithLedger(l ledgerclient.Ledger) *API {
	a.ledger = l
	return a
}

type activationDryResponse struct {
	DryRun  bool                `json:"dryRun"`
	Version store.Version       `json:"version"`
	Preview domain.RevalPreview `json:"preview"`
}

// maxActivationItems bounds the distinct items of one activation: the
// inventory is read once per item.
const maxActivationItems = 1000

func (a *API) validateActivation(r *http.Request, req storedActivateRequest) map[string]string {
	fields := map[string]string{}
	if req.Version == nil || *req.Version < 1 {
		fields["version"] = "is required and must be at least 1"
	}
	if _, err := time.Parse(time.DateOnly, req.PostingDate); err != nil {
		fields["postingDate"] = "must be a date YYYY-MM-DD"
	}
	if len(req.Lines) > 0 {
		fields["lines"] = "the positions are the version's stored calculations; the request cannot list them"
	}
	if req.ReversesActivationID != "" {
		if !ids.Valid(req.ReversesActivationID) {
			fields["reversesActivationId"] = "must be a UUID"
		}
		for key, set := range map[string]bool{"currency": req.Currency != "", "ledgerId": req.LedgerID != ""} {
			if set {
				fields[key] = "a reversal takes this from the activation it reverses"
			}
		}
	} else {
		if !currencyCode.MatchString(req.Currency) {
			fields["currency"] = "is required: an ISO 4217 code such as USD"
		}
		if !req.DryRun && !ids.Valid(req.LedgerID) {
			fields["ledgerId"] = "must be a UUID"
		}
	}
	if !req.DryRun {
		idempotencyKey(r, fields)
	}
	return fields
}

func activationProblem(status int, code, detail string) error {
	return httpx.NewProblem(status, code, detail)
}

func (a *API) quantities(ctx context.Context, legalEntityID, postingDate string, positions []inventory.Position) (map[inventory.Position]decimal.Decimal, error) {
	asOf := ""
	if postingDate < a.now().UTC().Format(time.DateOnly) {
		asOf = postingDate
	}
	pos, err := a.onHand.OnHand(ctx, legalEntityID, asOf, positions)
	if err != nil {
		slog.ErrorContext(ctx, "activation on-hand read failed", "legalEntityId", legalEntityID, "asOf", asOf, "err", err, "correlationId", middleware.GetReqID(ctx))
		return nil, activationProblem(http.StatusBadGateway, domain.CodeOnHandUnavailable, "the inventory on-hand could not be read")
	}
	return pos, nil
}

func (a *API) activationFailure(w http.ResponseWriter, r *http.Request, versionID string, err error) {
	var refused *store.LedgerRefusal
	if errors.As(err, &refused) {
		a.activationPostFailures.Add(1)
		slog.ErrorContext(r.Context(), "activation journal not posted", "versionId", versionID, "ledgerCode", ledgerclient.CodeOf(err), "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, activationProblem(http.StatusBadGateway, domain.CodeActivationLedger, "the general ledger did not accept the revaluation journal"))
		return
	}
	storeFailure(w, r, err)
}

func (a *API) activateWithRevaluation(w http.ResponseWriter, r *http.Request, id string, req storedActivateRequest, positions []inventory.Position) {
	p, _ := tenant.FromContext(r.Context())
	v, err := a.store.GetVersion(r.Context(), id)
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	if canonical, ok := ids.Canonical(req.ReversesActivationID); ok {
		req.ReversesActivationID = canonical
	}
	fingerprint := struct {
		ID  string
		Req storedActivateRequest
	}{id, req}
	if req.PostingDate == "" {
		req.PostingDate = a.now().UTC().Format(time.DateOnly)
	}
	if !validation(w, r, a.validateActivation(r, req)) {
		return
	}
	if !req.DryRun && p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "the activation records the user who ran it; no user in the request"))
		return
	}
	in := store.ActivationInput{
		VersionID: id, Expected: *req.Version, Key: strings.TrimSpace(r.Header.Get(headerIdempotencyKey)), Fingerprint: fingerprint,
		ReversesID: req.ReversesActivationID, LedgerID: req.LedgerID, PostingDate: req.PostingDate, Currency: req.Currency,
		NewStandardForOpenOrders: req.NewStandardForOpenOrders,
	}
	if req.ReversesActivationID == "" {
		if _, err := domain.Transition(v.Lifecycle(), domain.ActionActivate); err != nil {
			storeFailure(w, r, err)
			return
		}
		if req.DryRun && v.Type != domain.VersionStandard {
			a.previews.Add(1)
			httpx.JSON(w, http.StatusOK, activationDryResponse{DryRun: true, Version: v, Preview: domain.Revalue(nil, req.Currency)})
			return
		}
		if a.onHand == nil {
			httpx.Fail(w, r, activationProblem(http.StatusServiceUnavailable, domain.CodeOnHandUnavailable, "the inventory service is not configured; the on-hand to revalue cannot be read"))
			return
		}
		items := map[string]bool{}
		for _, pos := range positions {
			items[pos.ItemID] = true
		}
		if len(items) > maxActivationItems || len(positions) > MaxRevalLines {
			httpx.Fail(w, r, activationProblem(http.StatusUnprocessableEntity, domain.CodeActivationTooBig,
				fmt.Sprintf("the version has %d items at %d positions; an activation revalues at most %d items and %d positions", len(items), len(positions), maxActivationItems, MaxRevalLines)))
			return
		}
		if len(positions) > 0 {
			if in.OnHand, err = a.quantities(r.Context(), v.LegalEntityID, req.PostingDate, positions); err != nil {
				storeFailure(w, r, err)
				return
			}
		}
	}
	if req.DryRun {
		preview, err := a.store.PreviewActivation(r.Context(), in)
		if err != nil {
			storeFailure(w, r, err)
			return
		}
		a.previews.Add(1)
		httpx.JSON(w, http.StatusOK, activationDryResponse{DryRun: true, Version: v, Preview: preview})
		return
	}
	if a.ledger == nil {
		httpx.Fail(w, r, activationProblem(http.StatusServiceUnavailable, domain.CodeActivationLedger, "the general ledger is not configured for this service"))
		return
	}
	in.Ledger = a.ledger
	res, replayed, err := a.store.ActivateVersion(r.Context(), in)
	if err != nil {
		a.activationFailure(w, r, id, err)
		return
	}
	if !replayed {
		a.activations.Add(1)
		if req.ReversesActivationID == "" {
			a.versionTransitions.Add(1)
		}
	} else {
		w.Header().Set(headerReplayed, "true")
	}
	httpx.JSON(w, http.StatusOK, res)
}
