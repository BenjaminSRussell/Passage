package api

import (
	"fmt"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
)

// Slice-4 handlers: ad-hoc estimates from costing models (FR-39.13) and the
// sales order estimate lifecycle (FR-39.16, BR-39.14). They are stateless like
// the rest of the service: the caller sends the model, the prices and the
// versions it holds, and receives the estimate or the next versions. The routes
// and their duties are in routes() in api.go.

// MaxEstimateVersions bounds the version history sent with one sales order line.
const MaxEstimateVersions = 200

type adHocRequest struct {
	Currency        string                     `json:"currency"`
	Model           domain.CostingModel        `json:"model"`
	ObjectType      string                     `json:"objectType"`
	ObjectID        string                     `json:"objectId"`
	Characteristics map[string]decimal.Decimal `json:"characteristics"`
	Prices          map[string]domain.Price    `json:"prices"`
	Sheet           []domain.SheetNode         `json:"sheet,omitempty"`
	RoutingHours    decimal.Decimal            `json:"routingHours"`
}

// adHocEstimate prices a costing model for a quote, internal order or project
// task (FR-39.13, duty productcost.calculate). Request shape errors are 400; a model
// or characteristic the domain refuses is 422 with its CST code.
func (a *API) adHocEstimate(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req adHocRequest
	if !decode(w, r, &req) {
		return
	}
	fields := map[string]string{}
	scale := checkCurrency(req.Currency, fields)
	switch req.ObjectType {
	case domain.ObjectQuote, domain.ObjectInternalOrder, domain.ObjectProjectTask:
	default:
		fields["objectType"] = "must be Quote, InternalOrder or ProjectTask"
	}
	if req.ObjectID == "" {
		fields["objectId"] = "is required"
	}
	if req.RoutingHours.IsNegative() {
		fields["routingHours"] = "must not be negative"
	}
	for ref, p := range req.Prices {
		if p.Amount.IsNegative() {
			fields["prices."+ref+".amount"] = "must not be negative"
		}
	}
	checkSheet(req.Sheet, func(j int, key string) string { return fmt.Sprintf("sheet[%d].%s", j, key) }, fields)
	if !validation(w, r, fields) {
		return
	}
	est, err := domain.EstimateAdHoc(req.Model, domain.EstimateInput{
		ObjectType: req.ObjectType, ObjectID: req.ObjectID, Currency: req.Currency,
		Values: req.Characteristics, Prices: req.Prices, Sheet: req.Sheet,
		RoutingHours: req.RoutingHours, Scale: scale,
	})
	if err != nil {
		domainFailure(w, r, err, "model")
		return
	}
	a.estimates.Add(1)
	httpx.JSON(w, http.StatusOK, est)
}

// refreshRequest is a sales order line's order structure and configuration
// hash with the versions the caller already holds for the line.
type refreshRequest struct {
	salesRequest
	LineID     string                   `json:"lineId"`
	ConfigHash string                   `json:"configHash"`
	Versions   []domain.EstimateVersion `json:"versions"`
}

// checkVersions bounds the history; the domain checks its content.
func checkVersions(versions []domain.EstimateVersion, fields map[string]string) {
	if len(versions) > MaxEstimateVersions {
		fields["versions"] = fmt.Sprintf("must contain at most %d versions", MaxEstimateVersions)
	}
}

// refreshSalesEstimate recomputes a line's estimate for its configuration and
// versions it (FR-39.16, BR-39.14, duty productcost.calculate). The pair (lineId,
// configHash) is the PRD's idempotency key: the same pair returns the same
// current version unchanged.
func (a *API) refreshSalesEstimate(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req refreshRequest
	if !decode(w, r, &req) {
		return
	}
	fields := map[string]string{}
	scale := checkCurrency(req.Currency, fields)
	cat := catalog(req.Items, fields)
	opts := a.withDefaults(req.Options, fields)
	if req.ItemID == "" {
		fields["itemId"] = "is required"
	} else if _, ok := cat[req.ItemID]; cat != nil && !ok {
		fields["itemId"] = "must be one of the supplied items"
	}
	if !req.Quantity.IsPositive() {
		fields["quantity"] = "must be positive"
	}
	if !req.NetAmount.IsPositive() {
		fields["netAmount"] = "must be positive to compute margin"
	}
	if req.LineID == "" {
		fields["lineId"] = "is required"
	}
	if req.ConfigHash == "" {
		fields["configHash"] = "is required"
	}
	checkVersions(req.Versions, fields)
	if !validation(w, r, fields) {
		return
	}
	est, err := domain.SalesOrderEstimate(cat, req.ItemID, req.Quantity, req.NetAmount, scale, opts)
	if err != nil {
		domainFailure(w, r, err, "itemId")
		return
	}
	res, err := domain.RefreshEstimate(req.Versions, req.LineID, req.ConfigHash, est)
	if err != nil {
		domainFailure(w, r, err, "versions")
		return
	}
	if res.Changed {
		a.calculations.Add(1)
		if est.Status == domain.StatusError {
			a.rollErrors.Add(1)
		}
	}
	httpx.JSON(w, http.StatusOK, res)
}

type freezeRequest struct {
	LineID   string                   `json:"lineId"`
	Versions []domain.EstimateVersion `json:"versions"`
}

type freezeResponse struct {
	Versions  []domain.EstimateVersion `json:"versions"`
	Confirmed domain.EstimateVersion   `json:"confirmed"`
}

// freezeSalesEstimate freezes the current estimate at line confirmation
// (BR-39.14, duty productcost.calculate). Freezing a Frozen version returns it
// unchanged; a line without a usable estimate is CST-0763.
func (a *API) freezeSalesEstimate(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req freezeRequest
	if !decode(w, r, &req) {
		return
	}
	fields := map[string]string{}
	if req.LineID == "" {
		fields["lineId"] = "is required"
	}
	checkVersions(req.Versions, fields)
	if !validation(w, r, fields) {
		return
	}
	versions, confirmed, err := domain.FreezeEstimate(req.Versions, req.LineID)
	if err != nil {
		domainFailure(w, r, err, "versions")
		return
	}
	httpx.JSON(w, http.StatusOK, freezeResponse{Versions: versions, Confirmed: confirmed})
}
