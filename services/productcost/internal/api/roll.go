package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/engineering"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/store"
)

// maxStoredRollItems bounds the items one stored roll that names its items reads
// from engineering (one or two requests each); a stored-BOM selection reads up to
// MaxItems, because the request carries no item list to outgrow the body cap.
// ponytail: one roll per request and sequential reads; upgrade to the durable mass roll (D5).
const (
	maxStoredRollItems = 200
	selectAllActive    = "allActive"
)

func (a *API) WithEngineering(r engineering.Reader) *API {
	a.engineering = r
	return a
}

// storedRollRequest names the version's site and the items to roll. The BOM and
// route structure of every item is read from engineering and is never accepted
// here. Prices and operation rates have no stored source yet, so they are the
// request's two price inputs; the user who rolls cannot activate (CST-0738).
type storedRollRequest struct {
	SiteID         string                     `json:"siteId"`
	ItemIDs        []string                   `json:"itemIds"`
	Selection      string                     `json:"selection"`
	AsOf           string                     `json:"asOf"`
	Prices         map[string]decimal.Decimal `json:"prices"`
	OperationRates map[string]decimal.Decimal `json:"operationRates"`
	Options        domain.RollOptions         `json:"options"`
}

func (r storedRollRequest) validate(fields map[string]string) {
	if !ids.Valid(r.SiteID) {
		fields["siteId"] = "must be a UUID"
	}
	switch {
	case r.Selection != "" && len(r.ItemIDs) > 0:
		fields["selection"] = "name the items with itemIds or select the stored BOMs, not both"
	case r.Selection != "" && r.Selection != selectAllActive:
		fields["selection"] = "must be " + selectAllActive + ": every item with an active BOM at the site on asOf"
	case r.Selection == "":
		r.validateItemIDs(fields)
	}
	if r.AsOf != "" {
		if _, err := time.Parse(time.DateOnly, r.AsOf); err != nil {
			fields["asOf"] = "must be a date YYYY-MM-DD"
		}
	}
	for key, v := range r.Prices {
		if v.IsNegative() {
			fields["prices."+key] = "must not be negative"
		}
	}
	for key, v := range r.OperationRates {
		if v.IsNegative() {
			fields["operationRates."+key] = "must not be negative"
		}
	}
}

func (r storedRollRequest) validateItemIDs(fields map[string]string) {
	if len(r.ItemIDs) == 0 || len(r.ItemIDs) > maxStoredRollItems {
		fields["itemIds"] = fmt.Sprintf("must contain 1 to %d item ids, or set selection", maxStoredRollItems)
	}
	seen := map[string]bool{}
	for i, id := range r.ItemIDs {
		canonical, ok := ids.Canonical(id)
		switch {
		case !ok:
			fields[fmt.Sprintf("itemIds[%d]", i)] = "must be a UUID"
		case seen[canonical]:
			fields[fmt.Sprintf("itemIds[%d]", i)] = "must be unique"
		}
		seen[canonical] = true
	}
}

type rollSource struct {
	catalog domain.Catalog
	sources map[string]store.RollSource
}

func rollInputError(detail string) error {
	return &domain.CodedError{Code: domain.CodeRollInput, Detail: detail}
}

func decimalOf(raw string) (decimal.Decimal, error) {
	if raw == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(raw)
}

func bomLines(bom *engineering.BOM, item string) ([]domain.BOMLine, error) {
	var out []domain.BOMLine
	for _, l := range bom.Lines {
		if l.LineType == "co_product" || l.LineType == "by_product" {
			continue
		}
		qty, errQty := decimalOf(l.Quantity)
		series, errSeries := decimalOf(l.Series)
		scrap, errScrap := decimalOf(l.ScrapVarPct)
		constScrap, errConst := decimalOf(l.ScrapConst)
		switch {
		case errQty != nil || errSeries != nil || errScrap != nil || errConst != nil:
			return nil, rollInputError("BOM " + bom.ID + " of item " + item + " has a line with a malformed number")
		case l.AltGroup != "":
			return nil, rollInputError("BOM " + bom.ID + " of item " + item + " has alternative item groups; the roll cannot choose a member")
		case !constScrap.IsZero():
			return nil, rollInputError("BOM " + bom.ID + " of item " + item + " has a constant scrap quantity, which the roll does not cost")
		case !ids.Valid(l.ComponentProductID):
			return nil, rollInputError("BOM " + bom.ID + " of item " + item + " has the component " + l.ComponentProductID + ", which is not a UUID")
		}
		if !series.IsPositive() {
			series = decimal.NewFromInt(1)
		}
		out = append(out, domain.BOMLine{ChildID: l.ComponentProductID, Qty: qty.Div(series), ScrapPct: scrap, Phantom: l.LineType == "phantom"})
	}
	return out, nil
}

func routeOperations(route *engineering.Route, item string, rates map[string]decimal.Decimal) ([]domain.Operation, error) {
	var out []domain.Operation
	for _, op := range route.Operations {
		if op.Priority != "" && op.Priority != "primary" {
			continue
		}
		setup, errSetup := decimalOf(op.SetupH)
		run, errRun := decimalOf(op.RunH)
		lot, errLot := decimalOf(op.ProcessQty)
		scrap, errScrap := decimalOf(op.ScrapPercent)
		if errSetup != nil || errRun != nil || errLot != nil || errScrap != nil {
			return nil, rollInputError("route " + route.ID + " of item " + item + " has an operation with a malformed number")
		}
		costed := setup.IsPositive() || run.IsPositive()
		if op.OperationID == "" && costed {
			return nil, rollInputError("route " + route.ID + " of item " + item + " has a costed operation without an operationId, so no operationRates entry can price it")
		}
		rate, ok := rates[op.OperationID]
		if !ok && costed {
			return nil, &domain.CodedError{Code: domain.CodeNoPrice, Detail: "no operationRates entry for operation " + op.OperationID + " of item " + item}
		}
		out = append(out, domain.Operation{SetupHours: setup, RunHours: run, Rate: rate, LotSize: lot, ScrapPct: scrap})
	}
	return out, nil
}

func (a *API) readCatalog(ctx context.Context, legalEntityID string, roots []string, limit int, req storedRollRequest, asOf string) (rollSource, error) {
	out := rollSource{catalog: domain.Catalog{}, sources: map[string]store.RollSource{}}
	queue := slices.Clone(roots)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if _, seen := out.catalog[id]; seen {
			continue
		}
		if len(out.catalog) >= limit {
			return out, rollInputError(fmt.Sprintf("the structures reach more than %d items including their components; roll fewer items or the subassemblies first", limit))
		}
		bom, err := a.engineering.ActiveBOM(ctx, legalEntityID, id, req.SiteID, asOf)
		if err != nil {
			return out, err
		}
		if bom == nil {
			price, ok := req.Prices[id]
			if !ok {
				return out, &domain.CodedError{Code: domain.CodeNoPrice, Detail: "item " + id + " has no active BOM at the site and no prices entry"}
			}
			out.catalog[id] = domain.Item{ID: id, Kind: domain.KindPurchased, Price: &price}
			continue
		}
		route, err := a.engineering.ActiveRoute(ctx, legalEntityID, id, req.SiteID, asOf)
		if err != nil {
			return out, err
		}
		item := domain.Item{ID: id, Kind: domain.KindManufactured}
		if item.BOM, err = bomLines(bom, id); err != nil {
			return out, err
		}
		src := store.RollSource{BOMVersionID: bom.ID}
		if route != nil {
			if item.Operations, err = routeOperations(route, id, req.OperationRates); err != nil {
				return out, err
			}
			src.RouteVersionID = route.ID
		}
		out.catalog[id], out.sources[id] = item, src
		for _, line := range item.BOM {
			queue = append(queue, line.ChildID)
		}
	}
	return out, nil
}

func (a *API) rollStoredVersion(w http.ResponseWriter, r *http.Request) {
	if !a.storeReady(w, r) {
		return
	}
	id, ok := versionID(w, r)
	if !ok {
		return
	}
	var req storedRollRequest
	if !decode(w, r, &req) {
		return
	}
	fields := map[string]string{}
	req.validate(fields)
	opts := a.withDefaults(req.Options, fields)
	key := idempotencyKey(r, fields)
	if !validation(w, r, fields) {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	if p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "the roll records the user who ran it; no user in the request"))
		return
	}
	if a.engineering == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, domain.CodeEngineeringUnavailable, "the engineering service is not configured; the BOM and route versions cannot be read"))
		return
	}
	v, err := a.store.GetVersion(r.Context(), id)
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	if !tenant.InLegalEntityScope(p, v.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, domain.CodeLegalEntityScope, "legal entity "+v.LegalEntityID+" is outside your scope"))
		return
	}
	if opts.Mode == "" {
		opts.Mode = domain.ModeMultilevel
	}
	if opts.View == "" {
		opts.View = domain.ViewLegal
	}
	asOf := req.AsOf
	if asOf == "" {
		asOf = a.now().UTC().Format(time.DateOnly)
		req.AsOf = asOf
	}
	fingerprint := struct {
		ID  string
		Req storedRollRequest
	}{id, req}
	// A retry of a committed roll replays its stored 201 before the Open check and
	// the engineering read: activating the version must not turn the retry into 422.
	if res, found, err := a.store.RollReplay(r.Context(), key, fingerprint); err != nil || found {
		if err != nil {
			storeFailure(w, r, err)
			return
		}
		created(w, res, true)
		return
	}
	if v.Status != domain.VersionOpen {
		storeFailure(w, r, &domain.CodedError{Code: domain.CodeVersionNotRollable, Detail: "version " + v.Code + " is " + v.Status + "; only an Open version can be rolled"})
		return
	}
	roots, limit := req.ItemIDs, maxStoredRollItems
	if req.Selection != "" {
		limit = 2 * MaxItems
		if roots, err = a.selectRoots(r.Context(), v.LegalEntityID, req.SiteID, asOf); err != nil {
			a.rollInputFailure(w, r, id, err)
			return
		}
	}
	src, err := a.readCatalog(r.Context(), v.LegalEntityID, roots, limit, req, asOf)
	if err != nil {
		a.rollInputFailure(w, r, id, err)
		return
	}
	run := domain.MassRoll(src.catalog, opts)
	res, replayed, err := a.store.StoreRoll(r.Context(), store.RollInput{
		VersionID: id, SiteID: req.SiteID, Key: key, Options: opts, Roots: roots, Sources: src.sources, Run: run,
		Fingerprint: fingerprint,
	})
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	if !replayed {
		a.massRolls.Add(1)
		a.itemsRolled.Add(int64(run.ItemCount))
		a.rollErrors.Add(int64(res.ErrorCount))
	}
	created(w, res, replayed)
}

func (a *API) selectRoots(ctx context.Context, legalEntityID, siteID, asOf string) ([]string, error) {
	products, err := a.engineering.ActiveBOMProducts(ctx, legalEntityID, siteID, asOf, MaxItems)
	if err != nil {
		return nil, err
	}
	switch {
	case len(products) == 0:
		return nil, rollInputError("no item has an active BOM at the site on " + asOf)
	case len(products) > MaxItems:
		return nil, rollInputError(fmt.Sprintf("more than %d items have an active BOM at the site; roll them in parts with itemIds", MaxItems))
	}
	roots := make([]string, len(products))
	for i, p := range products {
		canonical, ok := ids.Canonical(p)
		if !ok {
			return nil, rollInputError("engineering listed the product " + p + ", which is not a UUID")
		}
		roots[i] = canonical
	}
	return roots, nil
}

func (a *API) rollInputFailure(w http.ResponseWriter, r *http.Request, versionID string, err error) {
	var coded *domain.CodedError
	if errors.As(err, &coded) {
		storeFailure(w, r, err)
		return
	}
	slog.ErrorContext(r.Context(), "engineering read failed during a roll", "versionId", versionID, "err", err, "correlationId", middleware.GetReqID(r.Context()))
	httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, domain.CodeEngineeringUnavailable, "the BOM and route versions could not be read from engineering"))
}
