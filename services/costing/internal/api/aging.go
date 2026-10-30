package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costing/internal/store"
)

const (
	// dutyAgingWrite saves aging bucket sets and stores month-end snapshots.
	dutyAgingWrite   = "costing.aging.write"
	maxBucketSetName = 64
)

// legalEntityInScope refuses a caller outside the legal-entity scope, as
// createVersion does; an empty legalEntityID is an unfiltered list and needs a
// scoped or all-scope caller. It writes the 403 and returns false.
func legalEntityInScope(w http.ResponseWriter, r *http.Request, legalEntityID string) bool {
	p, _ := tenant.FromContext(r.Context())
	allowed := tenant.InLegalEntityScope(p, legalEntityID)
	if legalEntityID == "" {
		allowed = p.LegalEntityID != "" || p.HasDuty(tenant.DutyAllScope)
	}
	if !allowed {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity is outside the caller's scope"))
		return false
	}
	return true
}

// legalEntityFilter reads the optional legalEntityId query parameter of a list.
func legalEntityFilter(w http.ResponseWriter, r *http.Request, fields map[string]string) (string, bool) {
	le := strings.ToLower(r.URL.Query().Get("legalEntityId"))
	if le != "" && !ids.Valid(le) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return "", false
	}
	return le, legalEntityInScope(w, r, le)
}

// checkBounds validates bucket bounds as the aging preview does: 1 to
// maxBucketBounds non-negative whole days in strictly ascending order.
func checkBounds(fields map[string]string, field string, bounds []int) {
	if len(bounds) == 0 || len(bounds) > maxBucketBounds {
		fields[field] = fmt.Sprintf("must hold 1 to %d bounds", maxBucketBounds)
		return
	}
	if _, err := domain.BucketsFromBounds(bounds); err != nil {
		fields[field] = "must be non-negative whole days in strictly ascending order"
	}
}

// ---- saved bucket sets (FR-20.17, AX "Inventory aging report setup") ----

type bucketSetRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	Name          string `json:"name"`
	Bounds        []int  `json:"bounds"`
}

type bucketSetList struct {
	Items []store.BucketSet `json:"items"`
}

func (a *API) createBucketSet(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req bucketSetRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.LegalEntityID = strings.ToLower(req.LegalEntityID)
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if n := len([]rune(req.Name)); n < 1 || n > maxBucketSetName {
		fields["name"] = fmt.Sprintf("must be 1 to %d characters", maxBucketSetName)
	}
	checkBounds(fields, "bounds", req.Bounds)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !legalEntityInScope(w, r, req.LegalEntityID) {
		return
	}
	set, err := a.store.CreateBucketSet(r.Context(), store.BucketSet{LegalEntityID: req.LegalEntityID, Name: req.Name, Bounds: req.Bounds})
	if err != nil {
		a.fail(w, r, "create aging bucket set", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, set)
}

func (a *API) listBucketSets(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le, ok := legalEntityFilter(w, r, map[string]string{})
	if !ok {
		return
	}
	sets, err := a.store.ListBucketSets(r.Context(), le)
	if err != nil {
		a.fail(w, r, "list aging bucket sets", err)
		return
	}
	httpx.JSON(w, http.StatusOK, bucketSetList{Items: sets})
}

// ---- month-end aging snapshots (FR-20.17, 3.20.15) ----

type snapshotLineRequest struct {
	ProductID            string           `json:"productId"`
	SiteID               string           `json:"siteId"`
	StandardUnitCost     *string          `json:"standardUnitCost"`
	DefaultValuationType string           `json:"defaultValuationType,omitempty"`
	Transactions         []transactionDTO `json:"transactions"`
}

type snapshotRequest struct {
	LegalEntityID     string                `json:"legalEntityId"`
	ValuationLedgerID string                `json:"valuationLedgerId"`
	AsOf              string                `json:"asOf"`
	Method            string                `json:"method"`
	Currency          string                `json:"currency"`
	Fallback          string                `json:"fallback"`
	BucketSetID       string                `json:"bucketSetId"`
	BucketBounds      []int                 `json:"bucketBounds"`
	Lines             []snapshotLineRequest `json:"lines"`
}

type snapshotListResponse struct {
	Items      []store.AgingSnapshot `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

// createAgingSnapshot ages every product x site line as of a date, the way the
// aging preview ages one product, and stores the result as month-end evidence
// with dev.passage.costing.agingsnapshot.created.v1. Buckets come from a saved
// bucket set, from explicit bounds, or default to 0-30, 31-90, 91-180,
// 181-365 and > 365 days. With a valuation ledger the method and currency are
// the ledger's. The lines carry the transactions: the CostTransaction
// projection that will feed the month-end job is a later slice.
func (a *API) createAgingSnapshot(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req snapshotRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key := strings.TrimSpace(r.Header.Get(idempotencyKey))
	req.LegalEntityID, req.ValuationLedgerID, req.BucketSetID = strings.ToLower(req.LegalEntityID), strings.ToLower(req.ValuationLedgerID), strings.ToLower(req.BucketSetID)
	fields := map[string]string{}
	if key == "" || len(key) > maxIdempotencyKey {
		fields[idempotencyKey] = fmt.Sprintf("is required, at most %d characters", maxIdempotencyKey)
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if req.ValuationLedgerID != "" && !ids.Valid(req.ValuationLedgerID) {
		fields["valuationLedgerId"] = "must be a UUID"
	}
	asOf := parseDate(fields, "asOf", req.AsOf)
	if !fallbacks[req.Fallback] {
		fields["fallback"] = "must be RUNNING_AVG, STANDARD or LAST_RECEIPT"
	}
	switch {
	case req.BucketSetID != "" && len(req.BucketBounds) > 0:
		fields["bucketSetId"] = "give a saved bucket set or bucket bounds, not both"
	case req.BucketSetID != "" && !ids.Valid(req.BucketSetID):
		fields["bucketSetId"] = "must be a UUID"
	case len(req.BucketBounds) > 0:
		checkBounds(fields, "bucketBounds", req.BucketBounds)
	}
	lines := parseSnapshotLines(fields, req.Lines, asOf)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !legalEntityInScope(w, r, req.LegalEntityID) {
		return
	}
	if !a.snapshotLedger(w, r, &req) {
		return
	}
	buckets, ok := a.snapshotBuckets(w, r, req)
	if !ok {
		return
	}
	opt := domain.Options{Method: domain.Method(req.Method), Scale: money.MinorUnits(req.Currency), Fallback: domain.Fallback(req.Fallback)}
	snap, err := domain.BuildSnapshot(lines, asOf, buckets, opt)
	if err != nil {
		a.fail(w, r, "aging snapshot", err)
		return
	}
	stored, created, err := a.store.CreateAgingSnapshot(r.Context(), snapshotToStore(snap, req, asOf, opt.Scale), key)
	if err != nil {
		a.fail(w, r, "create aging snapshot", err)
		return
	}
	if !created {
		httpx.JSON(w, http.StatusOK, stored)
		return
	}
	a.agingSnapshots.Add(1)
	httpx.JSON(w, http.StatusCreated, stored)
}

// parseSnapshotLines validates the lines into domain lines; field keys carry
// the line index so that errors on different lines do not collide.
func parseSnapshotLines(fields map[string]string, in []snapshotLineRequest, asOf time.Time) []domain.SnapshotLine {
	if len(in) == 0 || len(in) > domain.MaxSnapshotLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d product x site lines", domain.MaxSnapshotLines)
	}
	lines := make([]domain.SnapshotLine, 0, len(in))
	total := 0
	for i, ln := range in {
		lk := fmt.Sprintf("lines[%d]", i)
		line := domain.SnapshotLine{ProductID: strings.ToLower(ln.ProductID), SiteID: strings.ToLower(ln.SiteID), DefaultType: ln.DefaultValuationType}
		if !ids.Valid(line.ProductID) {
			fields[lk+".productId"] = "must be a UUID"
		}
		if !ids.Valid(line.SiteID) {
			fields[lk+".siteId"] = "must be a UUID"
		}
		if len(ln.DefaultValuationType) > maxValuationType {
			fields[lk+".defaultValuationType"] = fmt.Sprintf("must be at most %d characters", maxValuationType)
		}
		if ln.StandardUnitCost != nil {
			line.Standard = parseDecimal(fields, lk+".standardUnitCost", *ln.StandardUnitCost, true)
		}
		if len(ln.Transactions) == 0 {
			fields[lk+".transactions"] = "must hold at least one transaction"
		}
		total += len(ln.Transactions)
		lineFields := map[string]string{}
		line.Txns = parseTransactions(lineFields, ln.Transactions)
		for k, v := range lineFields {
			fields[lk+"."+k] = v
		}
		for j, t := range line.Txns {
			if !asOf.IsZero() && t.FinancialDate.After(asOf) {
				fields[fmt.Sprintf("%s.transactions[%d].financialDate", lk, j)] = "is after asOf"
			}
		}
		lines = append(lines, line)
	}
	if total > maxTxns {
		fields["lines"] = fmt.Sprintf("must hold at most %d transactions in total", maxTxns)
	}
	return lines
}

// snapshotLedger resolves the valuation ledger of a snapshot request: the
// ledger must belong to the legal entity, and the snapshot takes its method and
// currency (any given must match). Without a ledger both are required.
func (a *API) snapshotLedger(w http.ResponseWriter, r *http.Request, req *snapshotRequest) bool {
	fields := map[string]string{}
	if req.ValuationLedgerID != "" {
		l, err := a.store.GetValuationLedger(r.Context(), req.ValuationLedgerID)
		if err != nil {
			a.fail(w, r, "load valuation ledger", err)
			return false
		}
		if l.LegalEntityID != req.LegalEntityID {
			fields["valuationLedgerId"] = "belongs to another legal entity"
		}
		if req.Method != "" && req.Method != l.Method {
			fields["method"] = "must be the valuation ledger's method " + l.Method + " or be left out"
		}
		if req.Currency != "" && req.Currency != l.Currency {
			fields["currency"] = "must be the valuation ledger's currency " + l.Currency + " or be left out"
		}
		req.Method, req.Currency = l.Method, l.Currency
	}
	if !currencyPattern.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if !domain.Method(req.Method).Valid() {
		fields["method"] = "must be FIFO, LIFO, LIFO_DATE, WEIGHTED_AVG_RUNNING, WEIGHTED_AVG_PERIODIC, MOVING_AVG or STANDARD"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return false
	}
	return true
}

// snapshotBuckets resolves the buckets of a snapshot request: a saved set of
// the same legal entity, explicit bounds (already validated) or the default.
func (a *API) snapshotBuckets(w http.ResponseWriter, r *http.Request, req snapshotRequest) ([]domain.Bucket, bool) {
	bounds := req.BucketBounds
	if req.BucketSetID != "" {
		set, err := a.store.GetBucketSet(r.Context(), req.BucketSetID)
		if err != nil {
			a.fail(w, r, "load aging bucket set", err)
			return nil, false
		}
		if set.LegalEntityID != req.LegalEntityID {
			validationFailed(w, r, map[string]string{"bucketSetId": "belongs to another legal entity"})
			return nil, false
		}
		bounds = set.Bounds
	}
	if len(bounds) == 0 {
		return domain.DefaultBuckets(), true
	}
	buckets, err := domain.BucketsFromBounds(bounds)
	if err != nil {
		a.fail(w, r, "aging buckets", err)
		return nil, false
	}
	return buckets, true
}

func snapshotBuckets(in []domain.BucketTotal, scale int32) []store.SnapshotBucket {
	out := make([]store.SnapshotBucket, 0, len(in))
	for _, b := range in {
		out = append(out, store.SnapshotBucket{Label: b.Label, Qty: b.Qty.String(), Value: b.Value.StringFixed(scale)})
	}
	return out
}

// snapshotToStore converts the domain snapshot into its stored, wire form.
func snapshotToStore(snap domain.Snapshot, req snapshotRequest, asOf time.Time, scale int32) store.AgingSnapshot {
	out := store.AgingSnapshot{LegalEntityID: req.LegalEntityID, ValuationLedgerID: req.ValuationLedgerID,
		AsOf: asOf.Format(time.DateOnly), Method: req.Method, Currency: req.Currency, BucketSetID: req.BucketSetID,
		Buckets: snapshotBuckets(snap.Buckets, scale), TotalQty: snap.TotalQty.String(),
		TotalValue: snap.TotalValue.StringFixed(scale), RemainingValue: snap.RemainingValue.StringFixed(scale),
		TiesToRemaining: snap.Ties, Lines: make([]store.SnapshotLineOut, 0, len(snap.Lines))}
	for _, lr := range snap.Lines {
		line := store.SnapshotLineOut{ProductID: lr.ProductID, SiteID: lr.SiteID, Buckets: snapshotBuckets(lr.Report.Buckets, scale),
			TotalQty: lr.Report.TotalQty.String(), TotalValue: lr.Report.TotalValue.StringFixed(scale),
			RemainingValue: lr.RemainingValue.StringFixed(scale), TiesToRemaining: lr.Ties}
		for _, ta := range lr.PerType {
			line.ValuationTypes = append(line.ValuationTypes, store.SnapshotTypeOut{ValuationType: ta.ValuationType,
				Buckets: snapshotBuckets(ta.Report.Buckets, scale), TotalQty: ta.Report.TotalQty.String(),
				TotalValue: ta.Report.TotalValue.StringFixed(scale), RemainingValue: ta.RemainingValue.StringFixed(scale)})
		}
		out.Lines = append(out.Lines, line)
	}
	return out
}

func (a *API) listAgingSnapshots(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, fields := pageParams(r, false)
	if after != "" {
		asOf, _, _ := strings.Cut(after, "|")
		if _, err := time.Parse(time.DateOnly, asOf); err != nil {
			fields["cursor"] = "is not a cursor this endpoint returned"
		}
	}
	le, ok := legalEntityFilter(w, r, fields)
	if !ok {
		return
	}
	page, err := a.store.ListAgingSnapshots(r.Context(), le, after, limit)
	if err != nil {
		a.fail(w, r, "list aging snapshots", err)
		return
	}
	httpx.JSON(w, http.StatusOK, snapshotListResponse{Items: page.Items, NextCursor: cursor(page.Next)})
}

func (a *API) getAgingSnapshot(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeSnapshotNotFound, "aging snapshot not found"))
		return
	}
	snap, err := a.store.GetAgingSnapshot(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get aging snapshot", err)
		return
	}
	httpx.JSON(w, http.StatusOK, snap)
}
