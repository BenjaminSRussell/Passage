package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

const (
	maxCampaignOrders = 1000
	maxCampaignKey    = 64
)

type campaignOrderRequest struct {
	OrderID           string          `json:"orderId"`
	SetupGroup        string          `json:"setupGroup"`
	Quantity          decimal.Decimal `json:"quantity"`
	RunMinutes        int             `json:"runMinutes"`
	RouteSetupMinutes int             `json:"routeSetupMinutes"`
	DueAt             *time.Time      `json:"dueAt"`
}

type campaignRequest struct {
	ResourceID        string                 `json:"resourceId"`
	CampaignKey       string                 `json:"campaignKey"`
	PlannedStart      time.Time              `json:"plannedStart"`
	MaxOrders         *int                   `json:"maxOrders"`
	MaxQty            *decimal.Decimal       `json:"maxQty"`
	MaxMinutes        *int                   `json:"maxMinutes"`
	SequenceRule      string                 `json:"sequenceRule"`
	CurrentSetupGroup *string                `json:"currentSetupGroup"`
	Orders            []campaignOrderRequest `json:"orders"`
}

// checkCampaignOrder validates one order line and returns it for the store.
func checkCampaignOrder(o campaignOrderRequest, prefix string, fields map[string]string) store.CampaignOrder {
	o.OrderID = strings.ToLower(o.OrderID)
	switch {
	case !ids.Valid(o.OrderID):
		fields[prefix+"orderId"] = "must be a UUID"
	case !setupGroup.MatchString(o.SetupGroup):
		fields[prefix+"setupGroup"] = "must be 1 to 32 characters of A-Z, 0-9, _ or -"
	case checkDecimal(o.Quantity, maxQuantity, quantityScale, true) != "":
		fields[prefix+"quantity"] = checkDecimal(o.Quantity, maxQuantity, quantityScale, true)
	case o.RunMinutes < 0 || o.RunMinutes > 100000:
		fields[prefix+"runMinutes"] = "must be 0 to 100000"
	case o.RouteSetupMinutes < 0 || o.RouteSetupMinutes > maxWeekMinutes:
		fields[prefix+"routeSetupMinutes"] = fmt.Sprintf("must be 0 to %d", maxWeekMinutes)
	}
	var due *time.Time
	if o.DueAt != nil {
		d := o.DueAt.UTC()
		due = &d
	}
	return store.CampaignOrder{OrderID: o.OrderID, SetupGroup: o.SetupGroup, Quantity: o.Quantity, RunMinutes: o.RunMinutes,
		RouteSetupMinutes: o.RouteSetupMinutes, DueAt: due}
}

func (a *API) createCampaign(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req campaignRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	req.ResourceID = strings.ToLower(req.ResourceID)
	fields := map[string]string{}
	if !ids.Valid(req.ResourceID) {
		fields["resourceId"] = "must be a UUID"
	}
	if n := len(strings.TrimSpace(req.CampaignKey)); n < 1 || n > maxCampaignKey {
		fields["campaignKey"] = fmt.Sprintf("must be 1 to %d characters, such as a formula family or setup group", maxCampaignKey)
	}
	if req.PlannedStart.IsZero() {
		fields["plannedStart"] = "is required"
	}
	if req.MaxOrders != nil && (*req.MaxOrders < 1 || *req.MaxOrders > maxCampaignOrders) {
		fields["maxOrders"] = fmt.Sprintf("must be 1 to %d", maxCampaignOrders)
	}
	if req.MaxQty != nil {
		if msg := checkDecimal(*req.MaxQty, maxQuantity, quantityScale, true); msg != "" {
			fields["maxQty"] = msg
		}
	}
	if req.MaxMinutes != nil && (*req.MaxMinutes < 1 || *req.MaxMinutes > 1000000) {
		fields["maxMinutes"] = "must be 1 to 1000000"
	}
	rule := req.SequenceRule
	if rule == "" {
		rule = domain.SequenceNearestNeighbor
	}
	if rule != domain.SequenceNearestNeighbor && rule != domain.SequenceManual {
		fields["sequenceRule"] = "must be nearest-neighbor or manual (cp-sat sequencing is not available yet)"
	}
	if req.CurrentSetupGroup != nil && *req.CurrentSetupGroup == "" {
		req.CurrentSetupGroup = nil
	}
	if req.CurrentSetupGroup != nil && !setupGroup.MatchString(*req.CurrentSetupGroup) {
		fields["currentSetupGroup"] = "must be 1 to 32 characters of A-Z, 0-9, _ or -"
	}
	if len(req.Orders) > maxCampaignOrders {
		fields["orders"] = fmt.Sprintf("must list at most %d orders", maxCampaignOrders)
	}
	orders := make([]store.CampaignOrder, 0, len(req.Orders))
	for i, o := range req.Orders {
		orders = append(orders, checkCampaignOrder(o, fmt.Sprintf("orders[%d].", i), fields))
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	c, isNew, err := a.store.CreateCampaign(r.Context(), store.Campaign{ResourceID: req.ResourceID, CampaignKey: strings.TrimSpace(req.CampaignKey),
		PlannedStart: req.PlannedStart.UTC(), MaxOrders: req.MaxOrders, MaxQty: req.MaxQty, MaxMinutes: req.MaxMinutes, SequenceRule: rule,
		CurrentSetupGroup: req.CurrentSetupGroup, Orders: orders}, idem)
	if err != nil {
		slog.Error("create campaign failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(c.Version))
	created(w, c, isNew)
}

func (a *API) listCampaigns(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.CampaignFilter{Status: q.Get("status"), ResourceID: strings.ToLower(q.Get("resourceId"))}
	switch f.Status {
	case "", domain.CampaignProposed, domain.CampaignPlanned, domain.CampaignStarted, domain.CampaignCompleted, domain.CampaignCancelled:
	default:
		invalid(w, r, map[string]string{"status": "must be proposed, planned, started, completed or cancelled"})
		return
	}
	if f.ResourceID != "" && !ids.Valid(f.ResourceID) {
		invalid(w, r, map[string]string{"resourceId": "must be a UUID"})
		return
	}
	limit, ok := limitParam(w, r, 50, 200)
	if !ok {
		return
	}
	f.Limit = limit
	items, err := a.store.ListCampaigns(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func campaignID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeCampaignNotFound, "campaign not found"))
		return "", false
	}
	return id, true
}

func (a *API) getCampaign(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := campaignID(w, r)
	if !ok {
		return
	}
	c, err := a.store.GetCampaign(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(c.Version))
	httpx.JSON(w, http.StatusOK, c)
}

func etag(version int) string { return fmt.Sprintf(`"%d"`, version) }

// ifMatch reads the campaign version the caller last saw. Commands on a
// campaign need it so a stale screen cannot overwrite another planner's change.
func ifMatch(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.Header.Get("If-Match")
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, "precondition_required", "If-Match with the campaign version is required"))
		return 0, false
	}
	v, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(raw, "W/"), `"`))
	if err != nil || v < 1 {
		invalid(w, r, map[string]string{"If-Match": "must be the campaign version"})
		return 0, false
	}
	return v, true
}

// campaignCommand reads the target, If-Match and Idempotency-Key shared by
// every campaign command, then runs it.
func (a *API) campaignCommand(w http.ResponseWriter, r *http.Request, name string, body []byte,
	run func(cmd store.CampaignCommand, idem store.Idem) (store.Campaign, bool, error), cmd store.CampaignCommand) {
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	cmd.Version = version
	idem.Fingerprint = fingerprint(cmd.CampaignID+"|"+name+"|"+strconv.Itoa(version), body)
	c, replayed, err := run(cmd, idem)
	if err != nil {
		slog.Error("campaign command failed", "command", name, "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("ETag", etag(c.Version))
	httpx.JSON(w, http.StatusOK, c)
}

func (a *API) addCampaignOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := campaignID(w, r)
	if !ok {
		return
	}
	var req campaignOrderRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	order := checkCampaignOrder(req, "", fields)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	a.campaignCommand(w, r, "add-order", body, func(cmd store.CampaignCommand, idem store.Idem) (store.Campaign, bool, error) {
		return a.store.AddCampaignOrder(r.Context(), cmd, idem)
	}, store.CampaignCommand{CampaignID: id, Order: order})
}

func (a *API) removeCampaignOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := campaignID(w, r)
	if !ok {
		return
	}
	orderID := strings.ToLower(chi.URLParam(r, "orderId"))
	if !ids.Valid(orderID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeCampaignOrderNotFound, "the order is not in this campaign"))
		return
	}
	a.campaignCommand(w, r, "remove-order", []byte(orderID), func(cmd store.CampaignCommand, idem store.Idem) (store.Campaign, bool, error) {
		return a.store.RemoveCampaignOrder(r.Context(), cmd, idem)
	}, store.CampaignCommand{CampaignID: id, OrderID: orderID})
}

func (a *API) sequenceCampaign(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := campaignID(w, r)
	if !ok {
		return
	}
	body, err := readBody(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.campaignCommand(w, r, "sequence", body, func(cmd store.CampaignCommand, idem store.Idem) (store.Campaign, bool, error) {
		return a.store.SequenceCampaign(r.Context(), cmd, idem)
	}, store.CampaignCommand{CampaignID: id})
}

func (a *API) startCampaign(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := campaignID(w, r)
	if !ok {
		return
	}
	body, err := readBody(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.campaignCommand(w, r, "start", body, func(cmd store.CampaignCommand, idem store.Idem) (store.Campaign, bool, error) {
		return a.store.StartCampaign(r.Context(), cmd, idem)
	}, store.CampaignCommand{CampaignID: id})
}

type allocationActual struct {
	OrderID    string           `json:"orderId"`
	Quantity   *decimal.Decimal `json:"quantity"`
	RunMinutes *int             `json:"runMinutes"`
}

type allocateRequest struct {
	Amount   decimal.Decimal    `json:"amount"`
	Currency string             `json:"currency"`
	Basis    string             `json:"basis"`
	Actuals  []allocationActual `json:"actuals"`
}

type allocationLine struct {
	OrderID string          `json:"orderId"`
	Weight  decimal.Decimal `json:"weight"`
	Amount  string          `json:"amount"`
}

type allocationResponse struct {
	CampaignID string           `json:"campaignId"`
	Basis      string           `json:"basis"`
	Currency   string           `json:"currency"`
	Amount     string           `json:"amount"`
	Lines      []allocationLine `json:"lines"`
}

// allocateCampaignCost splits the actual cost of a campaign's setup and
// cleanout orders over its orders (FR-23.16, BR-23.14). It computes the split
// only: posting the campaign-changeover overhead lines needs the ledger
// client (see NOT-IMPLEMENTED.md), so nothing is written. Actual RAF
// quantities and run minutes override the planned ones per order.
func (a *API) allocateCampaignCost(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := campaignID(w, r)
	if !ok {
		return
	}
	var req allocateRequest
	if _, ok := decodeBody(w, r, &req); !ok {
		return
	}
	fields := map[string]string{}
	if msg := checkDecimal(req.Amount, maxAmount, moneyScale, false); msg != "" {
		fields["amount"] = msg
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	} else if !req.Amount.Equal(req.Amount.Round(money.MinorUnits(req.Currency))) {
		fields["amount"] = fmt.Sprintf("must have at most %d decimals in %s", money.MinorUnits(req.Currency), req.Currency)
	}
	basis := req.Basis
	if basis == "" {
		basis = domain.BasisQuantity
	}
	if basis != domain.BasisQuantity && basis != domain.BasisEqual && basis != domain.BasisRunMinutes {
		fields["basis"] = "must be quantity, equal or run-minutes"
	}
	actuals := map[string]allocationActual{}
	for _, act := range req.Actuals {
		act.OrderID = strings.ToLower(act.OrderID)
		switch {
		case !ids.Valid(act.OrderID):
			fields["actuals"] = "every orderId must be a UUID"
		case act.Quantity != nil && checkDecimal(*act.Quantity, maxQuantity, quantityScale, false) != "":
			fields["actuals"] = "quantity " + checkDecimal(*act.Quantity, maxQuantity, quantityScale, false)
		case act.RunMinutes != nil && *act.RunMinutes < 0:
			fields["actuals"] = "runMinutes must not be negative"
		}
		actuals[act.OrderID] = act
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	c, err := a.store.GetCampaign(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if c.Status == domain.CampaignCancelled {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeCampaignInvalid, "a cancelled campaign has no cost to allocate"))
		return
	}
	inCampaign := map[string]bool{}
	shares := make([]domain.CostShare, 0, len(c.Orders))
	for _, o := range c.Orders {
		inCampaign[o.OrderID] = true
		qty, minutes := o.Quantity, o.RunMinutes
		if act, ok := actuals[o.OrderID]; ok {
			if act.Quantity != nil {
				qty = *act.Quantity
			}
			if act.RunMinutes != nil {
				minutes = *act.RunMinutes
			}
		}
		weight, err := domain.AllocationWeight(basis, qty, minutes)
		if err != nil {
			invalid(w, r, map[string]string{"basis": err.Error()})
			return
		}
		shares = append(shares, domain.CostShare{OrderID: o.OrderID, Weight: weight})
	}
	for orderID := range actuals {
		if !inCampaign[orderID] {
			invalid(w, r, map[string]string{"actuals": "order " + orderID + " is not in this campaign"})
			return
		}
	}
	lines, err := domain.AllocateCost(money.Amount{Value: req.Amount, Currency: req.Currency}, shares)
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeCampaignInvalid, err.Error()))
		return
	}
	exp := money.MinorUnits(req.Currency)
	out := allocationResponse{CampaignID: c.ID, Basis: basis, Currency: req.Currency, Amount: req.Amount.StringFixed(exp), Lines: make([]allocationLine, 0, len(lines))}
	for i, l := range lines {
		out.Lines = append(out.Lines, allocationLine{OrderID: l.OrderID, Weight: shares[i].Weight, Amount: l.Amount.Value.StringFixed(exp)})
	}
	httpx.JSON(w, http.StatusOK, out)
}
