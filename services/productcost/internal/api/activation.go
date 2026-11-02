package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
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

type activationLine struct {
	ItemID string `json:"itemId"`
	SiteID string `json:"siteId"`
}

type activationDryResponse struct {
	DryRun  bool                `json:"dryRun"`
	Version store.Version       `json:"version"`
	Preview domain.RevalPreview `json:"preview"`
}

func (r storedActivateRequest) revalues() bool {
	return r.DryRun || r.ReversesActivationID != "" || len(r.Lines) > 0
}

func canonicalPosition(ln activationLine) (inventory.Position, bool) {
	item, okItem := ids.Canonical(ln.ItemID)
	site, okSite := ids.Canonical(ln.SiteID)
	return inventory.Position{ItemID: item, SiteID: site}, okItem && okSite
}

func (a *API) validateActivation(r *http.Request, req storedActivateRequest, v store.Version) map[string]string {
	fields := map[string]string{}
	if req.Version == nil || *req.Version < 1 {
		fields["version"] = "is required and must be at least 1"
	}
	if _, err := time.Parse(time.DateOnly, req.PostingDate); err != nil {
		fields["postingDate"] = "must be a date YYYY-MM-DD"
	}
	if req.ReversesActivationID != "" {
		if !ids.Valid(req.ReversesActivationID) {
			fields["reversesActivationId"] = "must be a UUID"
		}
		for key, set := range map[string]bool{"lines": len(req.Lines) > 0, "currency": req.Currency != "", "ledgerId": req.LedgerID != ""} {
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
		if n := len(req.Lines); n == 0 || n > MaxRevalLines {
			fields["lines"] = fmt.Sprintf("must contain from 1 to %d lines", MaxRevalLines)
		}
	}
	if !req.DryRun {
		idempotencyKey(r, fields)
	}
	seen := map[inventory.Position]bool{}
	for i, ln := range req.Lines {
		at := func(key string) string { return fmt.Sprintf("lines[%d].%s", i, key) }
		pos, ok := canonicalPosition(ln)
		switch {
		case !ids.Valid(ln.ItemID):
			fields[at("itemId")] = "must be a UUID"
		case !ids.Valid(ln.SiteID):
			fields[at("siteId")] = "must be a UUID"
		case len(v.SiteIDs) > 0 && !slices.Contains(v.SiteIDs, pos.SiteID):
			fields[at("siteId")] = "is not a site of this version"
		case ok && seen[pos]:
			fields[at("itemId")] = "item and site must be unique"
		}
		seen[pos] = true
	}
	return fields
}

func activationProblem(status int, code, detail string) error {
	return httpx.NewProblem(status, code, detail)
}

func (a *API) quantities(ctx context.Context, legalEntityID, postingDate string) (map[inventory.Position]decimal.Decimal, error) {
	asOf := ""
	if postingDate < a.now().UTC().Format(time.DateOnly) {
		asOf = postingDate
	}
	pos, err := a.onHand.OnHand(ctx, legalEntityID, asOf)
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

func (a *API) activateWithRevaluation(w http.ResponseWriter, r *http.Request, id string, req storedActivateRequest) {
	p, _ := tenant.FromContext(r.Context())
	v, err := a.store.GetVersion(r.Context(), id)
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	fingerprint := struct {
		ID  string
		Req storedActivateRequest
	}{id, req}
	if req.PostingDate == "" {
		req.PostingDate = a.now().UTC().Format(time.DateOnly)
	}
	if !validation(w, r, a.validateActivation(r, req, v)) {
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
		if a.onHand == nil {
			httpx.Fail(w, r, activationProblem(http.StatusServiceUnavailable, domain.CodeOnHandUnavailable, "the inventory service is not configured; the on-hand to revalue cannot be read"))
			return
		}
		for _, ln := range req.Lines {
			pos, _ := canonicalPosition(ln)
			in.Positions = append(in.Positions, pos)
		}
		if in.OnHand, err = a.quantities(r.Context(), v.LegalEntityID, req.PostingDate); err != nil {
			storeFailure(w, r, err)
			return
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
