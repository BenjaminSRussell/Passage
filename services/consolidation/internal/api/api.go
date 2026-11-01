// Package api exposes the consolidation HTTP surface. Slices 1 to 3 are
// stateless: the caller supplies the inputs and the service returns the E1–E5
// elimination entries and the E6 dividend elimination with its rounding line
// (FR-40.9, BR-40.9), the IAS 21 translation with CTA (FR-40.8, BR-40.6),
// unrealized profit per IC receipt layer (FR-40.16), the equity method and
// divestiture gain (FR-40.18), the two-sided IC elimination with threshold
// (BR-40.15), the unit validation rules with the monitor gate (FR-40.20), the
// reclassification dry run (FR-40.17) or the top-side journal check (FR-40.19).
// The previews store nothing. Consolidation runs are stored: a run computes the
// translation and E1–E5 eliminations of its units and keeps the balances,
// entries and statements as an immutable version.
package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/domain"
)

// Duties from PRD section 7.40.5 and 7.40.17. The previews are the computing
// steps of a consolidation run, so they need ConsolidationRun; the top-side
// journal check needs ConsolidationJournal, the duty that posts group journals.
const (
	DutyView    = "ConsolidationView"
	DutyRun     = "ConsolidationRun"
	DutyJournal = "ConsolidationJournal"
)

const validationDetail = "request failed validation"

var (
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	// decimalText is the OpenAPI Decimal pattern: no exponent, no leading plus.
	decimalText = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
)

// API serves the consolidation routes and counts what it computes for /metrics.
type API struct {
	runs                 RunStore
	runsCompleted        atomic.Int64
	eliminationPreviews  atomic.Int64
	translationPreviews  atomic.Int64
	profitPreviews       atomic.Int64
	equityPreviews       atomic.Int64
	divestiturePreviews  atomic.Int64
	icDifferencePreviews atomic.Int64
	validationPreviews   atomic.Int64
	dividendPreviews     atomic.Int64
	reclassPreviews      atomic.Int64
	groupJournalPreviews atomic.Int64
}

// New returns the API over the run store. The preview routes do not touch it.
func New(runs RunStore) *API { return &API{runs: runs} }

// route is one duty-guarded endpoint; api/openapi.yaml lists the same table.
type route struct {
	method, path, duty string
	handler            http.HandlerFunc
}

func (a *API) routes() []route {
	return []route{
		{http.MethodPost, "/v1/consol/eliminations:preview", DutyRun, a.previewEliminations},
		{http.MethodPost, "/v1/consol/translations:preview", DutyRun, a.previewTranslation},
		{http.MethodPost, "/v1/consol/profit-in-inventory:preview", DutyRun, a.previewProfitInInventory},
		{http.MethodPost, "/v1/consol/equity-method:preview", DutyRun, a.previewEquityMethod},
		{http.MethodPost, "/v1/consol/divestitures:preview", DutyRun, a.previewDivestiture},
		{http.MethodPost, "/v1/consol/ic-differences:preview", DutyRun, a.previewICDifferences},
		{http.MethodPost, "/v1/consol/validations:preview", DutyRun, a.previewValidation},
		{http.MethodPost, "/v1/consol/dividends:preview", DutyRun, a.previewDividends},
		{http.MethodPost, "/v1/consol/reclassification-runs:preview", DutyRun, a.previewReclassification},
		{http.MethodPost, "/v1/consol/group-journals:preview", DutyJournal, a.previewGroupJournal},
		{http.MethodPost, "/v1/consol/runs", DutyRun, a.startRun},
		{http.MethodGet, "/v1/consol/runs", DutyView, a.listRuns},
		{http.MethodGet, "/v1/consol/runs/{id}", DutyView, a.getRun},
		{http.MethodGet, "/v1/consol/runs/{id}/statements", DutyView, a.getRunStatements},
	}
}

// Register mounts the routes on r.
func (a *API) Register(r chi.Router) {
	for _, rt := range a.routes() {
		r.With(authz.RequireDuty(rt.duty)).Method(rt.method, rt.path, rt.handler)
	}
}

// Metrics serves the Prometheus text exposition on the admin port.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		counter := func(name, help string, v int64) {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
		}
		counter("consolidation_runs_completed_total", "Consolidation runs stored.", a.runsCompleted.Load())
		counter("consolidation_elimination_previews_total", "Elimination previews computed.", a.eliminationPreviews.Load())
		counter("consolidation_translation_previews_total", "Unit translation previews computed.", a.translationPreviews.Load())
		counter("consolidation_profit_in_inventory_previews_total", "Unrealized profit layer previews computed.", a.profitPreviews.Load())
		counter("consolidation_equity_method_previews_total", "Equity-method previews computed.", a.equityPreviews.Load())
		counter("consolidation_divestiture_previews_total", "Divestiture gain previews computed.", a.divestiturePreviews.Load())
		counter("consolidation_ic_difference_previews_total", "Two-sided IC elimination previews computed.", a.icDifferencePreviews.Load())
		counter("consolidation_validation_previews_total", "Unit validation previews computed.", a.validationPreviews.Load())
		counter("consolidation_dividend_previews_total", "E6 dividend elimination previews computed.", a.dividendPreviews.Load())
		counter("consolidation_reclassification_previews_total", "Reclassification dry runs computed.", a.reclassPreviews.Load())
		counter("consolidation_group_journal_previews_total", "Top-side group journals checked.", a.groupJournalPreviews.Load())
	})
}

// guard checks the caller is a tenant principal. Duties are enforced by authz.RequireDuty.
func guard(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}

func validation(w http.ResponseWriter, r *http.Request, fields map[string]string) bool {
	if len(fields) == 0 {
		return true
	}
	p := httpx.NewProblem(http.StatusBadRequest, domain.CodeInvalid, validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
	return false
}

// decimals parses decimal fields and records a field error for each bad one.
type decimals map[string]string

func (f decimals) get(name, raw string, required bool) decimal.Decimal {
	if raw == "" {
		if required {
			f[name] = "is required"
		}
		return decimal.Zero
	}
	if !decimalText.MatchString(raw) {
		f[name] = "must be a decimal"
		return decimal.Zero
	}
	return decimal.RequireFromString(raw)
}

type ratesBody struct {
	Closing    string `json:"closing"`
	Average    string `json:"average"`
	Opening    string `json:"opening"`
	Historical string `json:"historical"`
}

// ---- eliminations ----

type eliminationRequest struct {
	Currency           string    `json:"currency"`
	OwnershipPct       string    `json:"ownershipPct"`
	Rates              ratesBody `json:"rates"`
	ICSales            string    `json:"icSales"`
	ICCost             string    `json:"icCost"`
	UnsoldShare        string    `json:"unsoldShare"`
	ICReceivable       string    `json:"icReceivable"`
	PayableGroup       string    `json:"payableGroup"`
	ShareCapital       string    `json:"shareCapital"`
	PreAcqRE           string    `json:"preAcqRe"`
	InvestmentCost     string    `json:"investmentCost"`
	NetIncome          string    `json:"netIncome"`
	SellerIsSubsidiary bool      `json:"sellerIsSubsidiary"`
}

type entryBody struct {
	Rule    string `json:"rule"`
	Account string `json:"account"`
	Debit   string `json:"debit"`
	Credit  string `json:"credit"`
}

type totalsBody struct {
	Debit  string `json:"debit"`
	Credit string `json:"credit"`
}

type eliminationResponse struct {
	Currency string                `json:"currency"`
	Entries  []entryBody           `json:"entries"`
	Rules    map[string]totalsBody `json:"rules"`
	Totals   totalsBody            `json:"totals"`
	Balanced bool                  `json:"balanced"`
}

func (a *API) previewEliminations(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req eliminationRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	pair, fields := toPair(req)
	if !validation(w, r, fields) {
		return
	}
	entries, err := domain.Eliminate(pair)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.eliminationPreviews.Add(1)
	places := money.MinorUnits(pair.Currency)
	out := eliminationResponse{Currency: pair.Currency, Entries: make([]entryBody, 0, len(entries)), Rules: map[string]totalsBody{},
		Balanced: domain.CheckBalanced(entries) == nil}
	byRule := map[string][]domain.Entry{}
	for _, e := range entries {
		out.Entries = append(out.Entries, entryBody{Rule: e.RuleCode, Account: e.Account, Debit: e.Debit.StringFixed(places), Credit: e.Credit.StringFixed(places)})
		byRule[e.RuleCode] = append(byRule[e.RuleCode], e)
	}
	for rule, es := range byRule {
		dr, cr := domain.Totals(es)
		out.Rules[rule] = totalsBody{Debit: dr.StringFixed(places), Credit: cr.StringFixed(places)}
	}
	dr, cr := domain.Totals(entries)
	out.Totals = totalsBody{Debit: dr.StringFixed(places), Credit: cr.StringFixed(places)}
	httpx.JSON(w, http.StatusOK, out)
}

// toPair parses the decimal fields. Missing amounts default to zero; currency,
// ownership and the closing, average and historical rates are required.
func toPair(req eliminationRequest) (domain.Pair, map[string]string) {
	f := decimals{}
	if !currencyCode.MatchString(req.Currency) {
		f["currency"] = "must be an ISO 4217 code"
	}
	p := domain.Pair{
		Currency:     req.Currency,
		OwnershipPct: f.get("ownershipPct", req.OwnershipPct, true),
		Rates: domain.Rates{
			Closing:    f.get("rates.closing", req.Rates.Closing, true),
			Average:    f.get("rates.average", req.Rates.Average, true),
			Opening:    f.get("rates.opening", req.Rates.Opening, false),
			Historical: f.get("rates.historical", req.Rates.Historical, true),
		},
		ICSales:            f.get("icSales", req.ICSales, false),
		ICCost:             f.get("icCost", req.ICCost, false),
		UnsoldShare:        f.get("unsoldShare", req.UnsoldShare, false),
		ICReceivable:       f.get("icReceivable", req.ICReceivable, false),
		PayableGroup:       f.get("payableGroup", req.PayableGroup, false),
		ShareCapital:       f.get("shareCapital", req.ShareCapital, false),
		PreAcqRE:           f.get("preAcqRe", req.PreAcqRE, false),
		InvestmentCost:     f.get("investmentCost", req.InvestmentCost, false),
		NetIncome:          f.get("netIncome", req.NetIncome, false),
		SellerIsSubsidiary: req.SellerIsSubsidiary,
	}
	return p, f
}

// ---- translation ----

type translationRequest struct {
	Currency         string    `json:"currency"`
	OwnershipPct     string    `json:"ownershipPct"`
	Rates            ratesBody `json:"rates"`
	OpeningNetAssets string    `json:"openingNetAssets"`
	NetIncome        string    `json:"netIncome"`
	ClosingNetAssets string    `json:"closingNetAssets"`
}

type translationResponse struct {
	Currency         string `json:"currency"`
	OpeningNetAssets string `json:"openingNetAssets"`
	NetIncome        string `json:"netIncome"`
	ClosingNetAssets string `json:"closingNetAssets"`
	CTA              string `json:"cta"`
	NCIShareOfCTA    string `json:"nciShareOfCta"`
	GroupShareOfCTA  string `json:"groupShareOfCta"`
}

func (a *API) previewTranslation(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req translationRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := decimals{}
	if !currencyCode.MatchString(req.Currency) {
		f["currency"] = "must be an ISO 4217 code"
	}
	own := f.get("ownershipPct", req.OwnershipPct, true)
	rates := domain.Rates{
		Closing: f.get("rates.closing", req.Rates.Closing, true),
		Average: f.get("rates.average", req.Rates.Average, true),
		Opening: f.get("rates.opening", req.Rates.Opening, true),
	}
	stmt := domain.Statement{
		OpeningNetAssets: f.get("openingNetAssets", req.OpeningNetAssets, true),
		NetIncome:        f.get("netIncome", req.NetIncome, true),
		ClosingNetAssets: f.get("closingNetAssets", req.ClosingNetAssets, true),
	}
	if !validation(w, r, f) {
		return
	}
	t, err := domain.TranslateUnit(stmt, rates, own, req.Currency)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.translationPreviews.Add(1)
	places := money.MinorUnits(req.Currency)
	httpx.JSON(w, http.StatusOK, translationResponse{
		Currency:         t.Currency,
		OpeningNetAssets: t.OpeningNetAssets.StringFixed(places),
		NetIncome:        t.NetIncome.StringFixed(places),
		ClosingNetAssets: t.ClosingNetAssets.StringFixed(places),
		CTA:              t.CTA.StringFixed(places),
		NCIShareOfCTA:    t.NCIShareOfCTA.StringFixed(places),
		GroupShareOfCTA:  t.GroupShareOfCTA.StringFixed(places),
	})
}

// problemFrom maps a domain rule violation to a problem with its code.
func problemFrom(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		status := http.StatusUnprocessableEntity
		switch de.Code {
		case domain.CodeInvalid:
			status = http.StatusBadRequest
		case domain.CodeRunNotFound:
			status = http.StatusNotFound
		case domain.CodeIdempotencyReuse:
			status = http.StatusConflict
		}
		return httpx.NewProblem(status, de.Code, de.Detail)
	}
	return fmt.Errorf("consolidation: %w", err)
}
