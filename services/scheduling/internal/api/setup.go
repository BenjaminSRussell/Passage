package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

const (
	dutySetup    = "scheduling.setup.write"
	dutyCampaign = "scheduling.campaign.write"

	maxEdges        = 500
	maxEntries      = 2500
	maxPredicates   = 20
	maxWeekMinutes  = 10080
	maxRefLength    = 64
	moneyScale      = 6
	quantityScale   = 6
	maxAttributeLen = 64
)

var (
	setupGroup   = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,31}$`)
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	// maxAmount keeps money inside numeric(20,6) and quantities inside numeric(18,6).
	maxAmount   = decimal.New(1, 14)
	maxQuantity = decimal.New(1, 12)
)

// created writes 201 for a new row or 200 with Idempotent-Replayed for a replay.
func created(w http.ResponseWriter, v any, isNew bool) {
	status := http.StatusCreated
	if !isNew {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, status, v)
}

// validDate checks a YYYY-MM-DD date.
func validDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

// checkDecimal keeps a decimal non-negative, below limit and within scale decimals.
func checkDecimal(d decimal.Decimal, limit decimal.Decimal, scale int32, positive bool) string {
	switch {
	case positive && !d.IsPositive():
		return "must be greater than zero"
	case d.IsNegative():
		return "must not be negative"
	case d.GreaterThanOrEqual(limit):
		return fmt.Sprintf("must be less than %s", limit)
	case !d.Equal(d.Round(scale)):
		return fmt.Sprintf("must have at most %d decimals", scale)
	}
	return ""
}

type networkRequest struct {
	Code       string               `json:"code"`
	Name       string               `json:"name"`
	ActiveFrom string               `json:"activeFrom"`
	ActiveTo   *string              `json:"activeTo"`
	Edges      []domain.NetworkEdge `json:"edges"`
}

func (a *API) createNetwork(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req networkRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	fields := map[string]string{}
	if !resourceCode.MatchString(req.Code) {
		fields["code"] = "must be 1 to 32 characters of A-Z, 0-9, _ or -"
	}
	if n := len(strings.TrimSpace(req.Name)); n < 1 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if !validDate(req.ActiveFrom) {
		fields["activeFrom"] = "must be a date (YYYY-MM-DD)"
	}
	if req.ActiveTo != nil && (!validDate(*req.ActiveTo) || *req.ActiveTo < req.ActiveFrom) {
		fields["activeTo"] = "must be a date on or after activeFrom"
	}
	if len(req.Edges) < 1 || len(req.Edges) > maxEdges {
		fields["edges"] = fmt.Sprintf("must list 1 to %d allowed transitions", maxEdges)
	}
	for i := range req.Edges {
		e := &req.Edges[i]
		e.From, e.To, e.SecondaryResourceID = strings.ToLower(e.From), strings.ToLower(e.To), strings.ToLower(e.SecondaryResourceID)
		switch {
		case !ids.Valid(e.From) || !ids.Valid(e.To) || (e.SecondaryResourceID != "" && !ids.Valid(e.SecondaryResourceID)):
			fields["edges"] = "every resource id must be a UUID"
		case e.TransferMinutes < 0 || e.TransferMinutes > maxWeekMinutes:
			fields["edges"] = fmt.Sprintf("transferMinutes must be 0 to %d", maxWeekMinutes)
		case e.MaxWaitMinutes != nil && (*e.MaxWaitMinutes < 0 || *e.MaxWaitMinutes > maxWeekMinutes):
			fields["edges"] = fmt.Sprintf("maxWaitMinutes must be 0 to %d", maxWeekMinutes)
		}
	}
	if _, ok := fields["edges"]; !ok && len(fields) == 0 {
		if _, err := domain.NewResourceNetwork(req.Code, req.Edges); err != nil {
			fields["edges"] = err.Error()
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	n, isNew, err := a.store.CreateNetwork(r.Context(), store.Network{Code: req.Code, Name: strings.TrimSpace(req.Name),
		ActiveFrom: req.ActiveFrom, ActiveTo: req.ActiveTo, Edges: req.Edges}, idem)
	if err != nil {
		slog.Error("create network failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	created(w, n, isNew)
}

func (a *API) listNetworks(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	items, err := a.store.ListNetworks(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getNetwork(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNetworkNotFound, "resource network not found"))
		return
	}
	n, err := a.store.GetNetwork(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, n)
}

type setupMatrixRequest struct {
	ResourceID string              `json:"resourceId"`
	ActiveFrom string              `json:"activeFrom"`
	Currency   string              `json:"currency"`
	Entries    []domain.SetupEntry `json:"entries"`
}

func (a *API) createSetupMatrix(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req setupMatrixRequest
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
	if !validDate(req.ActiveFrom) {
		fields["activeFrom"] = "must be a date (YYYY-MM-DD)"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if len(req.Entries) < 1 || len(req.Entries) > maxEntries {
		fields["entries"] = fmt.Sprintf("must list 1 to %d changeovers", maxEntries)
	}
	for _, e := range req.Entries {
		switch {
		case !setupGroup.MatchString(e.FromGroup) || !setupGroup.MatchString(e.ToGroup):
			fields["entries"] = "setup groups must be 1 to 32 characters of A-Z, 0-9, _ or -"
		case e.Minutes < 0 || e.Minutes > maxWeekMinutes:
			fields["entries"] = fmt.Sprintf("minutes must be 0 to %d", maxWeekMinutes)
		case checkDecimal(e.Cost, maxAmount, moneyScale, false) != "":
			fields["entries"] = "cost " + checkDecimal(e.Cost, maxAmount, moneyScale, false)
		case len(e.CleaningSetID) > maxRefLength:
			fields["entries"] = fmt.Sprintf("cleaningSetId must be at most %d characters", maxRefLength)
		}
	}
	if _, bad := fields["entries"]; !bad && len(req.Entries) > 0 {
		if _, err := domain.NewSetupMatrix(req.Entries); err != nil {
			fields["entries"] = err.Error()
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	m, isNew, err := a.store.CreateSetupMatrix(r.Context(), store.SetupMatrix{ResourceID: req.ResourceID, ActiveFrom: req.ActiveFrom,
		Currency: req.Currency, Entries: req.Entries}, idem)
	if err != nil {
		slog.Error("create setup matrix failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	created(w, m, isNew)
}

func (a *API) listSetupMatrices(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	resourceID := strings.ToLower(r.URL.Query().Get("resourceId"))
	if resourceID != "" && !ids.Valid(resourceID) {
		invalid(w, r, map[string]string{"resourceId": "must be a UUID"})
		return
	}
	items, err := a.store.ListSetupMatrices(r.Context(), resourceID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getSetupMatrix(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeMatrixNotFound, "setup matrix not found"))
		return
	}
	m, err := a.store.GetSetupMatrix(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, m)
}

type cleanoutRuleRequest struct {
	SetupGroup      string `json:"setupGroup"`
	DurationMinutes int    `json:"durationMinutes"`
	OperationSetID  string `json:"operationSetId"`
	AfterBatches    *int   `json:"afterBatches"`
	AfterHours      *int   `json:"afterHours"`
	AtCampaignEnd   *bool  `json:"atCampaignEnd"`
}

func (a *API) createCleanoutRule(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req cleanoutRuleRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	atEnd := true
	if req.AtCampaignEnd != nil {
		atEnd = *req.AtCampaignEnd
	}
	fields := map[string]string{}
	if !setupGroup.MatchString(req.SetupGroup) {
		fields["setupGroup"] = "must be 1 to 32 characters of A-Z, 0-9, _ or -"
	}
	if req.DurationMinutes < 1 || req.DurationMinutes > maxWeekMinutes {
		fields["durationMinutes"] = fmt.Sprintf("must be 1 to %d", maxWeekMinutes)
	}
	if len(req.OperationSetID) > maxRefLength {
		fields["operationSetId"] = fmt.Sprintf("must be at most %d characters", maxRefLength)
	}
	if req.AfterBatches != nil && (*req.AfterBatches < 1 || *req.AfterBatches > 10000) {
		fields["afterBatches"] = "must be 1 to 10000"
	}
	if req.AfterHours != nil && (*req.AfterHours < 1 || *req.AfterHours > 8760) {
		fields["afterHours"] = "must be 1 to 8760"
	}
	if req.AfterBatches == nil && req.AfterHours == nil && !atEnd {
		fields["atCampaignEnd"] = "a rule needs afterBatches, afterHours or atCampaignEnd"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	rule, isNew, err := a.store.CreateCleanoutRule(r.Context(), store.CleanoutRule{SetupGroup: req.SetupGroup, DurationMinutes: req.DurationMinutes,
		OperationSetID: strings.TrimSpace(req.OperationSetID), AfterBatches: req.AfterBatches, AfterHours: req.AfterHours, AtCampaignEnd: atEnd}, idem)
	if err != nil {
		slog.Error("create cleanout rule failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	created(w, rule, isNew)
}

func (a *API) listCleanoutRules(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	items, err := a.store.ListCleanoutRules(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

type capacityBlockRequest struct {
	ResourceID string             `json:"resourceId"`
	Code       string             `json:"code"`
	Windows    []domain.Shift     `json:"windows"`
	StartAt    *time.Time         `json:"startAt"`
	EndAt      *time.Time         `json:"endAt"`
	Condition  []domain.Predicate `json:"condition"`
	Priority   *int               `json:"priority"`
	Active     *bool              `json:"active"`
}

func (a *API) createCapacityBlock(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req capacityBlockRequest
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
	if !resourceCode.MatchString(req.Code) {
		fields["code"] = "must be 1 to 32 characters of A-Z, 0-9, _ or -"
	}
	dated := req.StartAt != nil || req.EndAt != nil
	switch {
	case len(req.Windows) > 0 && dated:
		fields["windows"] = "give weekly windows or startAt and endAt, not both"
	case len(req.Windows) > 0:
		if msg := validShifts(req.Windows); msg != "" {
			fields["windows"] = msg
		}
	case req.StartAt == nil || req.EndAt == nil || !req.EndAt.After(*req.StartAt):
		fields["windows"] = "give weekly windows, or startAt and endAt with endAt after startAt"
	}
	if len(req.Condition) > maxPredicates {
		fields["condition"] = fmt.Sprintf("must hold at most %d predicates", maxPredicates)
	}
	for _, p := range req.Condition {
		if err := p.Validate(); err != nil {
			fields["condition"] = err.Error()
		} else if len(p.Attribute) > maxAttributeLen || len(p.Value) > maxAttributeLen {
			fields["condition"] = fmt.Sprintf("attributes and values are at most %d characters", maxAttributeLen)
		}
	}
	priority := 0
	if req.Priority != nil {
		priority = *req.Priority
		if priority < 0 || priority > 1000 {
			fields["priority"] = "must be 0 to 1000"
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	if req.Condition == nil {
		req.Condition = []domain.Predicate{}
	}
	in := store.CapacityBlock{ResourceID: req.ResourceID, Code: req.Code, Windows: req.Windows, Condition: req.Condition, Priority: priority, Active: active}
	if dated {
		start, end := req.StartAt.UTC(), req.EndAt.UTC()
		in.StartAt, in.EndAt = &start, &end
	}
	b, isNew, err := a.store.CreateCapacityBlock(r.Context(), in, idem)
	if err != nil {
		slog.Error("create capacity block failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	created(w, b, isNew)
}

func (a *API) listCapacityBlocks(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	resourceID := strings.ToLower(r.URL.Query().Get("resourceId"))
	if resourceID != "" && !ids.Valid(resourceID) {
		invalid(w, r, map[string]string{"resourceId": "must be a UUID"})
		return
	}
	items, err := a.store.ListCapacityBlocks(r.Context(), resourceID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
