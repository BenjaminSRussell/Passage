// Package api exposes the pricing HTTP surface: resolve, simulate and condition
// record lifecycle. Contract: api/openapi.yaml.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	dutyView         = "pricing.view"
	dutyMaintain     = "pricing.maintain"
	resolvePath      = "/v1/pricing/resolve"
	simulatePath     = "/v1/pricing/simulate"
	comparePath      = "/v1/pricing/compare"
	recordsPath      = "/v1/pricing/condition-records"
	idempotencyKey   = "Idempotency-Key"
	dateLayout       = "2006-01-02"
	validationDetail = "request is invalid"
	// DefaultMaxLines and DefaultExplainMaxBytes match PRICING_MAX_LINES and PRICING_EXPLAIN_MAX_BYTES.
	DefaultMaxLines        = 500
	DefaultExplainMaxBytes = 65536
	codeLinesExceeded      = "PRC-0009"
	readyTimeout           = 2 * time.Second
)

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// Config carries the request limits from the environment.
type Config struct {
	MaxLines        int
	ExplainMaxBytes int
}

// API holds the store and per-process counters exposed on /metrics.
type API struct {
	store            store.Store
	cfg              Config
	resolves         atomic.Int64
	linesPriced      atomic.Int64
	recordsCreated   atomic.Int64
	recordsActivated atomic.Int64
	recordsWithdrawn atomic.Int64
	recordsExpired   atomic.Int64
	journalsPosted   atomic.Int64
	accrualsPosted   atomic.Int64
	// commissionsPosted counts invoice lines that posted commission; retroRuns counts retroactive recomputes.
	commissionsPosted atomic.Int64
	retroRuns         atomic.Int64
	// fundUsages counts usage attributions recorded against trade funds.
	fundUsages atomic.Int64
	// contractAccruals counts invoice lines accrued against broker contracts and royalty agreements.
	contractAccruals atomic.Int64
}

// New builds the API; zero config values take the documented defaults.
func New(s store.Store, cfg Config) *API {
	if cfg.MaxLines <= 0 {
		cfg.MaxLines = DefaultMaxLines
	}
	if cfg.ExplainMaxBytes <= 0 {
		cfg.ExplainMaxBytes = DefaultExplainMaxBytes
	}
	return &API{store: s, cfg: cfg}
}

// Register mounts every route on r.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyView)).Post(resolvePath, a.resolve)
	r.With(authz.RequireDuty(dutyView)).Post(simulatePath, a.simulate)
	r.With(authz.RequireDuty(dutyView)).Post(comparePath, a.compare)
	r.With(authz.RequireDuty(dutyView)).Get(priceListPath, a.priceList)
	r.With(authz.RequireDuty(dutyView)).Get(netPriceListPath, a.netPriceList)
	r.With(authz.RequireDuty(dutyMaintain)).Post(recordsPath, a.createRecord)
	r.With(authz.RequireDuty(dutyView)).Get(recordsPath, a.listRecords)
	r.With(authz.RequireDuty(dutyView)).Get(recordsPath+"/{id}", a.getRecord)
	r.With(authz.RequireDuty(dutyMaintain)).Post(recordsPath+"/{id}:activate", a.activateRecord)
	r.With(authz.RequireDuty(dutyMaintain)).Post(recordsPath+"/{id}:withdraw", a.withdrawRecord)
	a.registerJournalRoutes(r)
	a.registerParameterRoutes(r)
	a.registerAssignmentRoutes(r)
	a.registerRebateRoutes(r)
	a.registerBrokerRoyaltyRoutes(r)
	a.registerRetroRoutes(r)
	a.registerCommissionRoutes(r)
	a.registerFundRoutes(r)
	a.registerCampaignRoutes(r)
	a.registerFreeGoodsRoutes(r)
	a.registerCommodityRoutes(r)
	a.registerProcedureRoutes(r)
	a.registerOpsRoutes(r)
}

// Metrics serves the Prometheus text exposition of the service counters.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		counters := []struct {
			name, help string
			v          *atomic.Int64
		}{
			{"pricing_resolves_total", "Resolve requests answered.", &a.resolves},
			{"pricing_lines_priced_total", "Document lines priced.", &a.linesPriced},
			{"pricing_records_created_total", "Condition records created as DRAFT.", &a.recordsCreated},
			{"pricing_records_activated_total", "Condition records activated.", &a.recordsActivated},
			{"pricing_records_withdrawn_total", "Condition records withdrawn.", &a.recordsWithdrawn},
			{"pricing_records_expired_total", "Condition records the expiry job moved to EXPIRED.", &a.recordsExpired},
			{"pricing_journals_posted_total", "Price journals posted or reversed.", &a.journalsPosted},
			{"pricing_rebate_accruals_total", "Invoice lines accrued against a rebate agreement.", &a.accrualsPosted},
			{"pricing_commission_postings_total", "Invoice lines that posted commission transactions.", &a.commissionsPosted},
			{"pricing_rebate_retro_recomputes_total", "Retroactive rebate recomputes run.", &a.retroRuns},
			{"pricing_contract_accruals_total", "Invoice lines accrued against broker contracts and royalty agreements.", &a.contractAccruals},
			{"pricing_fund_usages_total", "Usage attributions recorded against trade funds.", &a.fundUsages},
		}
		for _, c := range counters {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", c.name, c.help, c.name, c.name, c.v.Load())
		}
	})
}

// ready replaces the kernel's static /readyz: the service is ready only when Postgres answers.
func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

type lineDTO struct {
	ProductID      string `json:"productId"`
	ProductGroupID string `json:"productGroupId"`
	Variant        string `json:"variant"`
	Unit           string `json:"unit"`
	Quantity       string `json:"quantity"`
}

type resolveRequest struct {
	LegalEntityID   string `json:"legalEntityId"`
	CustomerID      string `json:"customerId"`
	CustomerGroupID string `json:"customerGroupId"`
	Currency        string `json:"currency"`
	PricingDate     string `json:"pricingDate"`
	// SalesChannel selects the channel's procedure assignment (PRD 5.26.13); Procedure names
	// a procedure to price with instead of the assigned one.
	SalesChannel string    `json:"salesChannel"`
	Procedure    string    `json:"procedure"`
	Lines        []lineDTO `json:"lines"`
}

type simulateRequest struct {
	resolveRequest
	Records []recordDTO `json:"records"`
}

// lineView is one priced line. Trace is set only when the caller asked for explain.
type lineView struct {
	domain.LineResult
	Trace []domain.TraceStep `json:"trace,omitempty"`
}

type resolveResponse struct {
	Currency      string           `json:"currency"`
	Lines         []lineView       `json:"lines"`
	Subtotal      decimal.Decimal  `json:"subtotal"`
	TotalDiscount decimal.Decimal  `json:"totalDiscount"`
	Surcharge     decimal.Decimal  `json:"surcharge"`
	OrderNet      decimal.Decimal  `json:"orderNet"`
	Applied       []domain.Applied `json:"applied"`
	// Procedure names the procedure version the prices came from; absent under the reference order.
	Procedure       *domain.ProcedureRef `json:"procedure,omitempty"`
	ExplainRef      string               `json:"explainRef"`
	ExplainTruncate bool                 `json:"explainTruncated,omitempty"`
	Trace           []domain.TraceStep   `json:"trace,omitempty"`
	// FreeGoods lists the zero-priced supplementary line proposals (FR-26.22) for w27.
	FreeGoods []domain.FreeGoodsProposal `json:"freeGoods"`
}

func (a *API) resolve(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req resolveRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	header, lines, problem := a.buildInput(req)
	if !ids.Valid(req.LegalEntityID) {
		if problem == nil {
			problem = validation(map[string]string{})
		}
		if problem.Errors != nil {
			problem.Errors["legalEntityId"] = "must be a UUID"
		}
	}
	if problem != nil {
		httpx.Fail(w, r, problem)
		return
	}
	in, err := a.resolveInput(r.Context(), store.ActiveQuery{LegalEntityID: req.LegalEntityID, Currency: req.Currency,
		CustomerGroupID: header.CustomerGroupID, AsOf: header.PricingDate})
	if err != nil {
		slog.Error("load active records failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	rules, err := a.freeGoodsRules(r, req.LegalEntityID, header, lines)
	if err != nil {
		slog.Error("load free goods rules failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	proc, err := a.store.DetermineProcedure(r.Context(), req.LegalEntityID, req.SalesChannel, req.Procedure)
	if err != nil {
		slog.Error("determine procedure failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.resolves.Add(1)
	a.linesPriced.Add(int64(len(lines)))
	in.Header, in.Lines, in.SmallOrderThreshold, in.Procedure = header, lines, domain.SmallOrderThreshold(header.Currency), proc
	res, free := domain.ResolveWithFreeGoods(in, rules)
	a.respond(w, r, res, free, r.URL.Query().Get("explain") == "true")
}

// recordSetSource is a store that serves the resolve snapshot pre-bucketed (store.Cache).
type recordSetSource interface {
	ActiveRecordSet(ctx context.Context, q store.ActiveQuery) (*domain.RecordSet, error)
}

// resolveInput loads the records for one resolve: the cache's shared RecordSet when the
// store has one (FR-26.14), the plain record list otherwise.
func (a *API) resolveInput(ctx context.Context, q store.ActiveQuery) (domain.Input, error) {
	if src, ok := a.store.(recordSetSource); ok {
		set, err := src.ActiveRecordSet(ctx, q)
		return domain.Input{Set: set}, err
	}
	records, err := a.store.ActiveRecords(ctx, q)
	return domain.Input{Records: records}, err
}

// simulate prices a tuple against records supplied in the body. Nothing is stored,
// so the endpoint is stateless and explains by default.
func (a *API) simulate(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req simulateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	header, lines, problem := a.buildInput(req.resolveRequest)
	if problem != nil {
		httpx.Fail(w, r, problem)
		return
	}
	records, problem := inlineRecords(req.Records)
	if problem != nil {
		httpx.Fail(w, r, problem)
		return
	}
	// A simulation names a legal entity only to choose the procedure it prices with.
	var proc *domain.Procedure
	if req.LegalEntityID != "" || req.Procedure != "" {
		if !ids.Valid(req.LegalEntityID) {
			validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
			return
		}
		var err error
		if proc, err = a.store.DetermineProcedure(r.Context(), req.LegalEntityID, req.SalesChannel, req.Procedure); err != nil {
			slog.Error("determine procedure failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
			httpx.Fail(w, r, err)
			return
		}
	}
	res := domain.Resolve(domain.Input{Header: header, Lines: lines, Records: records, Procedure: proc,
		SmallOrderThreshold: domain.SmallOrderThreshold(header.Currency)})
	a.respond(w, r, res, nil, r.URL.Query().Get("explain") != "false")
}

// MaxInlineRecords caps the condition records a simulate or compare body may carry. The 4 MiB body
// limit alone would let one request price 500 lines against tens of thousands of records (twice, for
// a comparison), so the cap keeps a stateless what-if as cheap as a resolve.
const MaxInlineRecords = 1000

// inlineRecords validates the records of a simulate or compare body and makes them ACTIVE, naming
// the ones without an id inline-1, inline-2, ...
func inlineRecords(dtos []recordDTO) ([]domain.Record, *httpx.Problem) {
	if len(dtos) > MaxInlineRecords {
		return nil, validation(map[string]string{"records": fmt.Sprintf("must hold at most %d records", MaxInlineRecords)})
	}
	records := make([]domain.Record, 0, len(dtos))
	for i, dto := range dtos {
		rec, p := toRecord(dto)
		if p != nil {
			return nil, p
		}
		rec.Status = domain.StatusActive
		if rec.ID == "" {
			rec.ID = fmt.Sprintf("inline-%d", i+1)
		}
		records = append(records, rec)
	}
	return records, nil
}

// buildInput validates a resolve body. Too many lines is PRC-0009; other faults are 422 field errors.
func (a *API) buildInput(req resolveRequest) (domain.Header, []domain.Line, *httpx.Problem) {
	if len(req.Lines) > a.cfg.MaxLines {
		return domain.Header{}, nil, httpx.NewProblem(http.StatusUnprocessableEntity, codeLinesExceeded, fmt.Sprintf("request exceeds %d lines", a.cfg.MaxLines))
	}
	fields := map[string]string{}
	date, err := time.Parse(dateLayout, req.PricingDate)
	if err != nil {
		fields["pricingDate"] = "must be a date in YYYY-MM-DD form"
	}
	if req.CustomerID == "" {
		fields["customerId"] = "is required"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be three upper-case letters"
	}
	if len(req.Lines) == 0 {
		fields["lines"] = "at least one line is required"
	}
	if msg := domain.ValidateSalesChannel(req.SalesChannel); msg != "" {
		fields["salesChannel"] = msg
	}
	if req.Procedure != "" {
		if msg := domain.ValidateProcedureName(req.Procedure); msg != "" {
			fields["procedure"] = msg
		}
	}
	lines := make([]domain.Line, 0, len(req.Lines))
	for i, l := range req.Lines {
		qty, qerr := decimal.NewFromString(l.Quantity)
		if l.ProductID == "" {
			fields[fmt.Sprintf("lines[%d].productId", i)] = "is required"
		}
		if l.Unit == "" {
			fields[fmt.Sprintf("lines[%d].unit", i)] = "is required"
		}
		if qerr != nil || !qty.IsPositive() {
			fields[fmt.Sprintf("lines[%d].quantity", i)] = "must be a decimal greater than 0"
		}
		lines = append(lines, domain.Line{No: i + 1, ProductID: l.ProductID, ProductGroupID: l.ProductGroupID, Variant: l.Variant, Unit: l.Unit, Quantity: qty})
	}
	if len(fields) > 0 {
		return domain.Header{}, nil, validation(fields)
	}
	return domain.Header{CustomerID: req.CustomerID, CustomerGroupID: req.CustomerGroupID, Currency: req.Currency, PricingDate: date}, lines, nil
}

func (a *API) respond(w http.ResponseWriter, r *http.Request, res domain.Result, free []domain.FreeGoodsProposal, explain bool) {
	out, err := a.responseOf(res, free, explain)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// responseOf builds the wire form of a priced document; the trace is left out when explain is false
// or the explanation is larger than the configured limit.
func (a *API) responseOf(res domain.Result, free []domain.FreeGoodsProposal, explain bool) (resolveResponse, error) {
	body, ref, err := domain.ExplainTrace(res)
	if err != nil {
		return resolveResponse{}, err
	}
	out := resolveResponse{Currency: res.Currency, Lines: make([]lineView, len(res.Lines)), Subtotal: res.Subtotal,
		TotalDiscount: res.TotalDiscount, Surcharge: res.Surcharge, OrderNet: res.OrderNet, Applied: res.Applied, Procedure: res.Procedure, ExplainRef: ref, FreeGoods: free}
	if out.FreeGoods == nil {
		out.FreeGoods = []domain.FreeGoodsProposal{}
	}
	for i, lr := range res.Lines {
		out.Lines[i] = lineView{LineResult: lr}
	}
	if explain {
		if len(body) > a.cfg.ExplainMaxBytes {
			out.ExplainTruncate = true
		} else {
			for i, lr := range res.Lines {
				out.Lines[i].Trace = lr.Trace
			}
			out.Trace = res.Trace
		}
	}
	return out, nil
}

type recordDTO struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	Type          string `json:"type"`
	CustomerKind  string `json:"customerKind"`
	CustomerKey   string `json:"customerKey"`
	ProductKind   string `json:"productKind"`
	ProductKey    string `json:"productKey"`
	Variant       string `json:"variant"`
	Currency      string `json:"currency"`
	Unit          string `json:"unit"`
	FromQuantity  string `json:"fromQuantity"`
	Amount        string `json:"amount"`
	DiscPct1      string `json:"discPct1"`
	DiscPct2      string `json:"discPct2"`
	ValidFrom     string `json:"validFrom"`
	ValidTo       string `json:"validTo"`
	SearchNext    bool   `json:"searchNext"`
	Exclusive     bool   `json:"exclusive"`
	// CampaignID optionally groups the record under a campaign (FR-26.2, FR-26.10).
	CampaignID string `json:"campaignId"`
}

// recordView is the wire form of a stored condition record.
type recordView struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	Type          string `json:"type"`
	CustomerKind  string `json:"customerKind"`
	CustomerKey   string `json:"customerKey"`
	ProductKind   string `json:"productKind"`
	ProductKey    string `json:"productKey"`
	Variant       string `json:"variant"`
	Currency      string `json:"currency"`
	Unit          string `json:"unit"`
	FromQuantity  string `json:"fromQuantity"`
	Amount        string `json:"amount"`
	DiscPct1      string `json:"discPct1"`
	DiscPct2      string `json:"discPct2"`
	ValidFrom     string `json:"validFrom"`
	ValidTo       string `json:"validTo"`
	SearchNext    bool   `json:"searchNext"`
	Exclusive     bool   `json:"exclusive"`
	Status        string `json:"status"`
	Version       int    `json:"version"`
	CampaignID    string `json:"campaignId,omitempty"`
}

func viewOf(e store.Entry) recordView {
	return recordView{ID: e.ID, LegalEntityID: e.LegalEntityID, Type: string(e.Type), CustomerKind: string(e.CustomerKind), CustomerKey: e.CustomerKey,
		ProductKind: string(e.ProductKind), ProductKey: e.ProductKey, Variant: e.Variant, Currency: e.Currency, Unit: e.Unit,
		FromQuantity: e.FromQuantity.String(), Amount: e.Amount.String(), DiscPct1: e.DiscPct1.String(), DiscPct2: e.DiscPct2.String(),
		ValidFrom: e.ValidFrom.Format(dateLayout), ValidTo: e.ValidTo.Format(dateLayout), SearchNext: e.SearchNext, Exclusive: e.Exclusive,
		Status: e.Status, Version: e.Version, CampaignID: e.CampaignID}
}

// toRecord converts a request record to the domain form and validates it (FR-26.2, BR-26.10).
func toRecord(dto recordDTO) (domain.Record, *httpx.Problem) {
	fields := map[string]string{}
	num := func(name, raw string) decimal.Decimal {
		if raw == "" {
			return decimal.Zero
		}
		v, err := decimal.NewFromString(raw)
		if err != nil {
			fields[name] = "must be a decimal"
		}
		return v
	}
	from := num("fromQuantity", dto.FromQuantity)
	amount := num("amount", dto.Amount)
	p1 := num("discPct1", dto.DiscPct1)
	p2 := num("discPct2", dto.DiscPct2)
	validFrom, err1 := time.Parse(dateLayout, dto.ValidFrom)
	validTo, err2 := time.Parse(dateLayout, dto.ValidTo)
	if err1 != nil || err2 != nil {
		fields["validity"] = "validFrom and validTo must be dates in YYYY-MM-DD form"
	}
	rec := domain.Record{ID: dto.ID, Type: domain.ConditionType(dto.Type), CustomerKind: domain.CustomerKind(dto.CustomerKind), CustomerKey: dto.CustomerKey,
		ProductKind: domain.ProductKind(dto.ProductKind), ProductKey: dto.ProductKey, Variant: dto.Variant, Currency: dto.Currency, Unit: dto.Unit,
		FromQuantity: from, Amount: amount, DiscPct1: p1, DiscPct2: p2, ValidFrom: validFrom, ValidTo: validTo,
		SearchNext: dto.SearchNext, Exclusive: dto.Exclusive, CampaignID: dto.CampaignID}
	if dto.CampaignID != "" && !ids.Valid(dto.CampaignID) {
		fields["campaignId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		return rec, validation(fields)
	}
	if verr := domain.ValidateRecord(rec); verr != nil {
		if verr.Code == domain.CodeNegative {
			return rec, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeNegative, "negative net price is not allowed")
		}
		return rec, validation(verr.Fields)
	}
	return rec, nil
}

func (a *API) createRecord(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var dto recordDTO
	if err := httpx.Decode(r, &dto); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	rec, problem := toRecord(dto)
	if !ids.Valid(dto.LegalEntityID) {
		if problem == nil {
			problem = validation(map[string]string{})
		}
		if problem.Errors != nil {
			problem.Errors["legalEntityId"] = "must be a UUID"
		}
	}
	if problem != nil {
		httpx.Fail(w, r, problem)
		return
	}
	e, err := a.store.CreateRecord(r.Context(), store.Entry{Record: rec, LegalEntityID: dto.LegalEntityID}, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("create condition record failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.recordsCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, viewOf(e))
}

func (a *API) listRecords(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.Filter{LegalEntityID: q.Get("legalEntityId"), Status: q.Get("status"), Type: q.Get("type"), Customer: q.Get("customer"),
		Product: q.Get("product"), CampaignID: q.Get("campaignId")}
	fields := map[string]string{}
	if f.CampaignID != "" && !ids.Valid(f.CampaignID) {
		fields["campaignId"] = "must be a UUID"
	}
	if f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if f.Status != "" && f.Status != domain.StatusDraft && f.Status != domain.StatusActive && f.Status != domain.StatusWithdrawn && f.Status != domain.StatusExpired {
		fields["status"] = "must be DRAFT, ACTIVE, EXPIRED or WITHDRAWN"
	}
	if f.Type != "" && !domain.ValidType(domain.ConditionType(f.Type)) {
		fields["type"] = "must be PRICE, LINE_DISC, MULTILINE_DISC, TOTAL_DISC or SURCHARGE"
	}
	if raw := q.Get("asOf"); raw != "" {
		day, err := time.Parse(dateLayout, raw)
		if err != nil {
			fields["asOf"] = "must be a date in YYYY-MM-DD form"
		} else {
			f.AsOf = &day
		}
	}
	for _, p := range []struct {
		name string
		into **time.Time
	}{{"validToFrom", &f.ValidToFrom}, {"validToTo", &f.ValidToTo}} {
		if raw := q.Get(p.name); raw != "" {
			day, err := time.Parse(dateLayout, raw)
			if err != nil {
				fields[p.name] = "must be a date in YYYY-MM-DD form"
				continue
			}
			*p.into = &day
		}
	}
	if f.ValidToFrom != nil && f.ValidToTo != nil && f.ValidToTo.Before(*f.ValidToFrom) {
		fields["validToTo"] = "must not be before validToFrom"
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxListLimit {
			fields["limit"] = fmt.Sprintf("must be an integer from 1 to %d", store.MaxListLimit)
		}
		f.Limit = n
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	entries, err := a.store.ListRecords(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]recordView, len(entries))
	for i, e := range entries {
		items[i] = viewOf(e)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getRecord(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := recordID(w, r)
	if !ok {
		return
	}
	e, err := a.store.GetRecord(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, viewOf(e))
}

type versionRequest struct {
	Version int    `json:"version"`
	Reason  string `json:"reason"`
}

func (a *API) activateRecord(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := recordID(w, r)
	if !ok {
		return
	}
	var req versionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Version < 1 {
		validationFailed(w, r, map[string]string{"version": "is required and must be at least 1"})
		return
	}
	e, err := a.store.ActivateRecord(r.Context(), id, req.Version, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("activate condition record failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.recordsActivated.Add(1)
	httpx.JSON(w, http.StatusOK, viewOf(e))
}

func (a *API) withdrawRecord(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := recordID(w, r)
	if !ok {
		return
	}
	var req versionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Version < 1 {
		validationFailed(w, r, map[string]string{"version": "is required and must be at least 1"})
		return
	}
	e, err := a.store.WithdrawRecord(r.Context(), id, req.Version, req.Reason, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("withdraw condition record failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.recordsWithdrawn.Add(1)
	httpx.JSON(w, http.StatusOK, viewOf(e))
}

func recordID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRecordNotFound, "condition record not found"))
		return "", false
	}
	return id, true
}

func validation(fields map[string]string) *httpx.Problem {
	p := httpx.NewProblem(http.StatusUnprocessableEntity, "COM-0001", validationDetail)
	p.Errors = fields
	return p
}

func validationFailed(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	httpx.Fail(w, r, validation(fields))
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}
