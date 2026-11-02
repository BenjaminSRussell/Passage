package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
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

const activationSourceType = "StandardCostActivation"

// WithInventory attaches the on-hand reader. With one, activation quantities
// always come from the inventory projection (BR-39.8); without one, the
// request supplies them.
func (a *API) WithInventory(r inventory.Reader) *API {
	a.onHand = r
	return a
}

// WithLedger attaches the ledger the revaluation journal is posted to.
func (a *API) WithLedger(l ledgerclient.Ledger) *API {
	a.ledger = l
	return a
}

type activationLine struct {
	ItemID string           `json:"itemId"`
	SiteID string           `json:"siteId"`
	OldStd decimal.Decimal  `json:"oldStd"`
	NewStd decimal.Decimal  `json:"newStd"`
	Qty    *decimal.Decimal `json:"qty"`
}

type activationDryResponse struct {
	DryRun  bool                `json:"dryRun"`
	Version store.Version       `json:"version"`
	Preview domain.RevalPreview `json:"preview"`
}

func (r storedActivateRequest) revalues() bool {
	return r.DryRun || r.ReversesActivationID != "" || len(r.Lines) > 0
}

func (a *API) validateActivation(r *http.Request, req storedActivateRequest, v store.Version) map[string]string {
	fields := map[string]string{}
	if req.Version == nil || *req.Version < 1 {
		fields["version"] = "is required and must be at least 1"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "is required: an ISO 4217 code such as USD"
	}
	if _, err := time.Parse(time.DateOnly, req.PostingDate); err != nil {
		fields["postingDate"] = "must be a date YYYY-MM-DD"
	}
	if req.ReversesActivationID != "" {
		if !ids.Valid(req.ReversesActivationID) {
			fields["reversesActivationId"] = "must be a UUID"
		}
		if len(req.Lines) > 0 {
			fields["lines"] = "a reversal takes its lines from the activation it reverses"
		}
	} else if n := len(req.Lines); n == 0 || n > MaxRevalLines {
		fields["lines"] = fmt.Sprintf("must contain from 1 to %d lines", MaxRevalLines)
	}
	if !req.DryRun {
		if !ids.Valid(req.LedgerID) {
			fields["ledgerId"] = "must be a UUID"
		}
		if req.InventoryAccount == "" {
			fields["inventoryAccount"] = "is required"
		}
		if req.CostChangeAccount == "" {
			fields["costChangeAccount"] = "is required"
		}
		idempotencyKey(r, fields)
	}
	seen := map[[2]string]bool{}
	for i, ln := range req.Lines {
		at := func(key string) string { return fmt.Sprintf("lines[%d].%s", i, key) }
		if ln.ItemID == "" {
			fields[at("itemId")] = "is required"
		}
		if ln.SiteID == "" {
			fields[at("siteId")] = "is required"
		} else if len(v.SiteIDs) > 0 && !slices.Contains(v.SiteIDs, ln.SiteID) {
			fields[at("siteId")] = "is not a site of this version"
		}
		if key := [2]string{ln.ItemID, ln.SiteID}; seen[key] {
			fields[at("itemId")] = "item and site must be unique"
		} else {
			seen[key] = true
		}
		if ln.OldStd.IsNegative() {
			fields[at("oldStd")] = "must not be negative"
		}
		if ln.NewStd.IsNegative() {
			fields[at("newStd")] = "must not be negative"
		}
		switch {
		case a.onHand != nil && ln.Qty != nil:
			fields[at("qty")] = "is read from the inventory on-hand at activation and must be omitted"
		case a.onHand == nil && (ln.Qty == nil || ln.Qty.IsNegative()):
			fields[at("qty")] = "is required and must not be negative while no inventory service is configured"
		}
	}
	return fields
}

func activationProblem(status int, code, detail string) error {
	return httpx.NewProblem(status, code, detail)
}

// quantities reads the on-hand of the version's legal entity at the posting
// date: the live projection for today or later, the date replay before it.
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

func (a *API) activationLines(ctx context.Context, req storedActivateRequest, v store.Version) ([]domain.RevalLine, error) {
	var base []domain.RevalLine
	if req.ReversesActivationID != "" {
		orig, err := a.store.GetActivation(ctx, req.ReversesActivationID)
		if err != nil {
			return nil, err
		}
		if orig.VersionID != v.ID {
			return nil, &domain.CodedError{Code: domain.CodeReversalTarget, Detail: "activation " + orig.ID + " is not an activation of this version"}
		}
		base = domain.ReversalLines(orig.Lines)
	} else {
		for _, ln := range req.Lines {
			rl := domain.RevalLine{ItemID: ln.ItemID, SiteID: ln.SiteID, OldStd: ln.OldStd, NewStd: ln.NewStd}
			if ln.Qty != nil {
				rl.Qty = *ln.Qty
			}
			base = append(base, rl)
		}
	}
	if a.onHand == nil {
		if req.ReversesActivationID != "" {
			return nil, activationProblem(http.StatusServiceUnavailable, domain.CodeOnHandUnavailable, "reversing an activation reads the on-hand from inventory, which is not configured")
		}
		return base, nil
	}
	pos, err := a.quantities(ctx, v.LegalEntityID, req.PostingDate)
	if err != nil {
		return nil, err
	}
	for i, ln := range base {
		base[i].Qty = pos[inventory.Position{ItemID: ln.ItemID, SiteID: ln.SiteID}]
	}
	return base, nil
}

func ledgerEntry(req storedActivateRequest, v store.Version, preview domain.RevalPreview, activationID string) ledgerclient.Entry {
	accounts := map[string]string{domain.AccountInventory: req.InventoryAccount, domain.AccountCostChangeVar: req.CostChangeAccount}
	e := ledgerclient.Entry{LegalEntityID: v.LegalEntityID, LedgerID: req.LedgerID, DocumentID: activationID, PostingDate: req.PostingDate,
		Currency: req.Currency, IdempotencyKey: "productcost.activation:" + v.ID + ":" + strconv.Itoa(*req.Version), SourceType: activationSourceType}
	for _, jl := range preview.Journal {
		amount, credit := jl.Debit, false
		if !jl.Credit.IsZero() {
			amount, credit = jl.Credit, true
		}
		e.Lines = append(e.Lines, ledgerclient.Line{Account: accounts[jl.Account], Credit: credit, Amount: amount, Text: "Standard cost revaluation " + v.Code})
	}
	return e
}

// activateWithRevaluation revalues the on-hand of the lines (or, for a
// reversal, of the activation reversed), previews it, or activates the version
// and posts the journal in one transaction (FR-39.6).
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
	if req.ReversesActivationID == "" {
		if _, err := domain.Transition(v.Lifecycle(), domain.ActionActivate); err != nil {
			storeFailure(w, r, err)
			return
		}
	}
	lines, err := a.activationLines(r.Context(), req, v)
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	preview := domain.Revalue(lines, req.Currency)
	if req.DryRun {
		a.previews.Add(1)
		httpx.JSON(w, http.StatusOK, activationDryResponse{DryRun: true, Version: v, Preview: preview})
		return
	}
	if a.ledger == nil {
		httpx.Fail(w, r, activationProblem(http.StatusServiceUnavailable, domain.CodeActivationLedger, "the general ledger is not configured for this service"))
		return
	}
	res, replayed, err := a.store.ActivateVersion(r.Context(), store.ActivationInput{
		VersionID: id, Expected: *req.Version, Key: strings.TrimSpace(r.Header.Get(headerIdempotencyKey)), Fingerprint: fingerprint,
		ReversesID: req.ReversesActivationID, LedgerID: req.LedgerID, PostingDate: req.PostingDate, Currency: req.Currency,
		NewStandardForOpenOrders: req.NewStandardForOpenOrders, Preview: preview,
		Post: func(ctx context.Context, activationID string) (string, error) {
			journalID, err := a.ledger.Post(ctx, ledgerEntry(req, v, preview, activationID))
			if err != nil {
				a.activationPostFailures.Add(1)
				slog.ErrorContext(ctx, "activation journal not posted", "versionId", id, "ledgerCode", ledgerclient.CodeOf(err), "err", err, "correlationId", middleware.GetReqID(ctx))
				return "", activationProblem(http.StatusBadGateway, domain.CodeActivationLedger, "the general ledger did not accept the revaluation journal")
			}
			return journalID, nil
		},
	})
	if err != nil {
		storeFailure(w, r, err)
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
