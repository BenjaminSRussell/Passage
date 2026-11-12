package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/store"
)

// ReconStore persists reconciliation runs and their resolutions (FR-40.6).
type ReconStore interface {
	CreateReconRun(ctx context.Context, run domain.ReconRun, items []domain.ReconItem, key, hash string) (domain.ReconRun, bool, error)
	ListReconRuns(ctx context.Context, after string, limit int) ([]domain.ReconRun, string, error)
	GetReconRun(ctx context.Context, id, status string) (domain.ReconRun, []domain.ReconItem, error)
	ResolveItem(ctx context.Context, itemID, note string) (domain.ReconItem, string, error)
}

// WithReconciliation gives the API the reconciliation store.
func (a *API) WithReconciliation(s ReconStore) *API {
	a.recon = s
	return a
}

// MaxResolutionNote bounds the note that resolves an item.
const MaxResolutionNote = 1000

type reconRunRequest struct {
	Period         string `json:"period"`
	Currency       string `json:"currency"`
	FXTolerancePct string `json:"fxTolerancePct"`
	// Source is "request" (the default: the pair amounts are in items) or "ledger": the pair amounts are read from the ledgers of
	// the stored accounting matrix's entities, and items must be empty.
	Source string      `json:"source"`
	Items  []reconItem `json:"items"`
}

type reconRunResponse struct {
	ID             string    `json:"id"`
	Period         string    `json:"period"`
	Currency       string    `json:"currency"`
	FXTolerancePct string    `json:"fxTolerancePct"`
	Matched        int       `json:"matched"`
	Timing         int       `json:"timing"`
	Mismatch       int       `json:"mismatch"`
	Open           int       `json:"open"`
	Blocking       bool      `json:"blocking"`
	Source         string    `json:"source,omitempty"`
	CreatedBy      string    `json:"createdBy"`
	CreatedAt      time.Time `json:"createdAt"`
}

type reconItemResponse struct {
	ID               string     `json:"id"`
	RunID            string     `json:"runId"`
	Ref              string     `json:"ref,omitempty"`
	EntityA          string     `json:"entityA"`
	EntityB          string     `json:"entityB"`
	AmountA          string     `json:"amountA"`
	AmountB          string     `json:"amountB"`
	NextPeriodA      string     `json:"nextPeriodA"`
	NextPeriodB      string     `json:"nextPeriodB"`
	FXApplies        bool       `json:"fxApplies"`
	Difference       string     `json:"difference"`
	Status           string     `json:"status"`
	ToleranceApplied bool       `json:"toleranceApplied"`
	Resolution       string     `json:"resolution"`
	Note             string     `json:"note,omitempty"`
	ResolvedBy       string     `json:"resolvedBy,omitempty"`
	ResolvedAt       *time.Time `json:"resolvedAt,omitempty"`
}

type reconRunDetailResponse struct {
	reconRunResponse
	Items []reconItemResponse `json:"items"`
}

type reconRunListResponse struct {
	Items      []reconRunResponse `json:"items"`
	NextCursor string             `json:"nextCursor,omitempty"`
}

func toReconRunResponse(r domain.ReconRun) reconRunResponse {
	return reconRunResponse{ID: r.ID, Period: r.Period, Currency: r.Currency, FXTolerancePct: r.FXTolerancePct.String(), Matched: r.Matched, Timing: r.Timing,
		Mismatch: r.Mismatch, Open: r.Open, Blocking: r.Mismatch > 0, Source: r.Source, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt.UTC()}
}

func toReconItemResponse(it domain.ReconItem, currency string) reconItemResponse {
	places := money.MinorUnits(currency)
	out := reconItemResponse{ID: it.ID, RunID: it.RunID, Ref: it.Ref, EntityA: it.Entity, EntityB: it.Counterparty,
		AmountA: it.AmountA.StringFixed(places), AmountB: it.AmountB.StringFixed(places), NextPeriodA: it.NextPeriodA.StringFixed(places), NextPeriodB: it.NextPeriodB.StringFixed(places),
		FXApplies: it.FXApplies, Difference: it.Difference.StringFixed(places), Status: string(it.Status), ToleranceApplied: it.ToleranceApplied,
		Resolution: string(it.Resolution), Note: it.Note, ResolvedBy: it.ResolvedBy}
	if !it.ResolvedAt.IsZero() {
		at := it.ResolvedAt.UTC()
		out.ResolvedAt = &at
	}
	return out
}

func (a *API) reconReady(w http.ResponseWriter, r *http.Request) bool {
	if !guard(w, r) {
		return false
	}
	if a.recon == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "reconciliation storage is not configured"))
		return false
	}
	return true
}

// runReconciliation classifies every pair of a period with the same rules as
// the stateless classify route and stores the result (FR-40.6, AC-40.5). Items
// that are not Matched start open; the run publishes
// reconciliation.flagged.v1 when any are left.
func (a *API) runReconciliation(w http.ResponseWriter, r *http.Request) {
	if !a.reconReady(w, r) {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var req reconRunRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !periodKey.MatchString(req.Period) {
		fields["period"] = "must be a period key such as 2026-11"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if req.Source == "ledger" {
		a.runLedgerReconciliation(w, r, req, key, fields)
		return
	}
	if req.Source != "" && req.Source != "request" {
		fields["source"] = "must be request or ledger"
	}
	// The run fixes the period and currency of its items.
	cr := classifyRequest{FXTolerancePct: req.FXTolerancePct, Items: make([]reconItem, len(req.Items))}
	for i, it := range req.Items {
		it.Period, it.Currency = req.Period, req.Currency
		cr.Items[i] = it
	}
	inputs, tolerance, itemFields := parseClassify(cr)
	for k, v := range itemFields {
		fields[k] = v
	}
	if !validation(w, r, fields) {
		return
	}
	run := domain.ReconRun{Period: req.Period, Currency: req.Currency, FXTolerancePct: tolerance}
	items := make([]domain.ReconItem, 0, len(inputs))
	for i, in := range inputs {
		src := req.Items[i]
		ref := src.IcPairID
		if ref == "" {
			ref = src.ID
		}
		it := domain.ReconItem{Entity: strings.TrimSpace(src.EntityA), Counterparty: strings.TrimSpace(src.EntityB), Ref: ref,
			AmountA: in.AmountA, AmountB: in.AmountB, NextPeriodA: in.NextPeriodA, NextPeriodB: in.NextPeriodB, FXApplies: in.FXApplies}
		res := domain.ClassifyItem(it, tolerance)
		it.Status, it.Difference, it.ToleranceApplied = res.Status, res.Difference, res.ToleranceApplied
		it.Resolution = domain.ResolutionOpen
		switch res.Status {
		case domain.ReconMatched:
			it.Resolution = domain.ResolutionNone
			run.Matched++
		case domain.ReconTiming:
			run.Timing++
		default:
			run.Mismatch++
		}
		items = append(items, it)
	}
	hash, err := store.HashOf("recon", req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	created, replayed, err := a.recon.CreateReconRun(r.Context(), run, items, key, hash)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	} else {
		a.reconRuns.Add(1)
		a.reconItems.Add(int64(len(items)))
		a.reconMismatches.Add(int64(run.Mismatch))
	}
	w.Header().Set("Location", "/v1/interco/reconciliations/"+created.ID)
	httpx.JSON(w, http.StatusCreated, toReconRunResponse(created))
}

func (a *API) listReconRuns(w http.ResponseWriter, r *http.Request) {
	if !a.reconReady(w, r) {
		return
	}
	fields := map[string]string{}
	after, limit := pageParams(w, r, fields)
	if !validation(w, r, fields) {
		return
	}
	runs, next, err := a.recon.ListReconRuns(r.Context(), after, limit)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := reconRunListResponse{Items: make([]reconRunResponse, 0, len(runs)), NextCursor: next}
	for _, run := range runs {
		out.Items = append(out.Items, toReconRunResponse(run))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getReconRun(w http.ResponseWriter, r *http.Request) {
	if !a.reconReady(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeReconRunNotFound, "no reconciliation run with id "+id))
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", string(domain.ReconMatched), string(domain.ReconTiming), string(domain.ReconMismatch):
	default:
		validation(w, r, map[string]string{"status": "must be Matched, Timing or Mismatch"})
		return
	}
	run, items, err := a.recon.GetReconRun(r.Context(), id, status)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := reconRunDetailResponse{reconRunResponse: toReconRunResponse(run), Items: make([]reconItemResponse, 0, len(items))}
	for _, it := range items {
		out.Items = append(out.Items, toReconItemResponse(it, run.Currency))
	}
	httpx.JSON(w, http.StatusOK, out)
}

type resolveRequest struct {
	Note string `json:"note"`
}

// resolveReconItem marks a flagged item resolved with a note (FR-40.6).
func (a *API) resolveReconItem(w http.ResponseWriter, r *http.Request) {
	if !a.reconReady(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeReconItemNotFound, "no reconciliation item with id "+id))
		return
	}
	var req resolveRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	note := strings.TrimSpace(req.Note)
	if note == "" || len(note) > MaxResolutionNote {
		validation(w, r, map[string]string{"note": "is required, at most 1000 characters"})
		return
	}
	it, currency, err := a.recon.ResolveItem(r.Context(), id, note)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.reconResolved.Add(1)
	httpx.JSON(w, http.StatusOK, toReconItemResponse(it, currency))
}

// runLedgerReconciliation reads the pair amounts of every pair of the stored matrix from the ledgers (FR-40.6) and stores the run.
func (a *API) runLedgerReconciliation(w http.ResponseWriter, r *http.Request, req reconRunRequest, key string, fields map[string]string) {
	if len(req.Items) > 0 {
		fields["items"] = "must be empty when the pair amounts are read from the ledgers"
	}
	tolerance := domain.DefaultFXTolerancePct
	if a.registry != nil {
		if st, err := a.registry.GetSettings(r.Context()); err == nil {
			tolerance = st.FXTolerancePct
		}
	}
	if req.FXTolerancePct != "" {
		v := decimal.Zero
		if amountText.MatchString(req.FXTolerancePct) {
			v = decimal.RequireFromString(req.FXTolerancePct)
		}
		if !amountText.MatchString(req.FXTolerancePct) || domain.ValidateFXTolerance(v) != nil {
			fields["fxTolerancePct"] = "must be a decimal from 0 to 5"
		} else {
			tolerance = v
		}
	}
	if !validation(w, r, fields) {
		return
	}
	if a.ledgerRecon == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "reading the ledgers is not configured: the ledger is not set for this service"))
		return
	}
	run, replayed, err := a.ledgerRecon.Run(r.Context(), req.Period, req.Currency, tolerance, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	} else {
		a.reconRuns.Add(1)
		a.reconMismatches.Add(int64(run.Mismatch))
	}
	w.Header().Set("Location", "/v1/interco/reconciliations/"+run.ID)
	httpx.JSON(w, http.StatusCreated, toReconRunResponse(run))
}
