// Package api exposes the intercompany HTTP surface. Slices 1 and 2 are
// stateless: the caller supplies the accounting matrix, balances, transfer
// prices or planning group, and the service returns the derived IC journal
// (BR-40.4), the reconciliation classification (FR-40.6, BR-40.5), the priced
// IC charge with its Journal-mode legs (FR-40.13), the centralized payment legs
// (FR-40.12) or the planning run sequence with IC demand (FR-40.15). Nothing is
// stored and no event is published, so no endpoint takes an Idempotency-Key.
package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
)

// Duties from PRD section 7.40.17.
const (
	DutyView      = "IntercompanyView"
	DutyConfigure = "IntercompanyConfigure"
	DutyPost      = "IntercompanyPost"
	DutyReconcile = "IntercompanyReconcile"
	DutyPlan      = "IntercompanyPlan"
)

const (
	validationDetail = "request failed validation"
	// MaxMatrixRows bounds the accounting matrix sent with one journal preview.
	MaxMatrixRows = 1000
	// MaxReconItems bounds one classification request.
	MaxReconItems = 10000
	// MaxPlanningMembers bounds the entities of one planning group.
	MaxPlanningMembers = 200
	// MaxPlanningRows bounds the edges and the planned orders of one sequencing request.
	MaxPlanningRows = 10000
	// MaxTransferPrices bounds the transfer price rows sent with one charge preview.
	MaxTransferPrices = 1000
)

var (
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	amountText   = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,6})?$`)
	signedText   = regexp.MustCompile(`^-?[0-9]+(\.[0-9]{1,6})?$`)
)

// API serves the intercompany routes and counts what it computes for /metrics.
type API struct {
	journalPreviews        atomic.Int64
	reconItems             atomic.Int64
	reconMismatches        atomic.Int64
	chargePreviews         atomic.Int64
	centralPaymentPreviews atomic.Int64
	planningSequences      atomic.Int64
}

// New returns the API.
func New() *API { return &API{} }

// route is one duty-guarded endpoint; api/openapi.yaml lists the same table.
type route struct {
	method, path, duty string
	handler            http.HandlerFunc
}

func (a *API) routes() []route {
	return []route{
		{http.MethodPost, "/v1/interco/journals:preview", DutyPost, a.previewJournal},
		{http.MethodPost, "/v1/interco/reconciliations:classify", DutyReconcile, a.classify},
		{http.MethodPost, "/v1/interco/charges:preview", DutyPost, a.previewCharge},
		{http.MethodPost, "/v1/interco/centralized-payments:preview", DutyConfigure, a.previewCentralPayment},
		{http.MethodPost, "/v1/interco/planning-groups:sequence", DutyPlan, a.sequencePlanning},
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
		counter("intercompany_journal_previews_total", "IC journals derived by the preview.", a.journalPreviews.Load())
		counter("intercompany_recon_items_classified_total", "Reconciliation items classified.", a.reconItems.Load())
		counter("intercompany_recon_mismatches_total", "Reconciliation items classified as Mismatch.", a.reconMismatches.Load())
		counter("intercompany_charge_previews_total", "IC charges priced by the preview.", a.chargePreviews.Load())
		counter("intercompany_central_payment_previews_total", "Centralized payment journals derived by the preview.", a.centralPaymentPreviews.Load())
		counter("intercompany_planning_sequences_total", "Planning group run sequences computed.", a.planningSequences.Load())
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

// ---- journal preview ----

type matrixRow struct {
	EntityA string `json:"entityA"`
	EntityB string `json:"entityB"`
	DueFrom string `json:"dueFrom"`
	DueTo   string `json:"dueTo"`
}

type previewRequest struct {
	Entity          string      `json:"entity"`
	Account         string      `json:"account"`
	Side            string      `json:"side"`
	Amount          string      `json:"amount"`
	Currency        string      `json:"currency"`
	OffsetEntity    string      `json:"offsetEntity"`
	OffsetAccount   string      `json:"offsetAccount"`
	CounterCurrency string      `json:"counterCurrency"`
	Rate            string      `json:"rate"`
	Matrix          []matrixRow `json:"matrix"`
}

type leg struct {
	Entity   string `json:"entity"`
	Account  string `json:"account"`
	Side     string `json:"side"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type previewResponse struct {
	PairID   string `json:"pairId"`
	Balanced bool   `json:"balanced"`
	Legs     []leg  `json:"legs"`
}

func (a *API) previewJournal(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req previewRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	line, matrix, counter, rate, fields := parsePreview(req)
	if !validation(w, r, fields) {
		return
	}
	pairID := ids.New()
	j, err := domain.DeriveJournal(matrix, pairID, line, req.OffsetEntity, req.OffsetAccount, counter, rate)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.journalPreviews.Add(1)
	httpx.JSON(w, http.StatusOK, previewResponse{PairID: pairID, Balanced: j.Balanced() == nil, Legs: legsOf(j)})
}

// parsePreview validates the request and converts it to domain values.
func parsePreview(req previewRequest) (domain.Line, domain.Matrix, string, decimal.Decimal, map[string]string) {
	fields := map[string]string{}
	need := func(name, v string) {
		if strings.TrimSpace(v) == "" {
			fields[name] = "is required"
		}
	}
	need("entity", req.Entity)
	need("account", req.Account)
	need("offsetEntity", req.OffsetEntity)
	need("offsetAccount", req.OffsetAccount)
	if req.Entity != "" && req.Entity == req.OffsetEntity {
		fields["offsetEntity"] = "must differ from entity"
	}
	side, err := domain.ParseSide(req.Side)
	if err != nil {
		fields["side"] = "must be debit or credit"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	var amount decimal.Decimal
	if amountText.MatchString(req.Amount) {
		amount = decimal.RequireFromString(req.Amount)
	}
	if !amount.IsPositive() {
		fields["amount"] = "must be a positive decimal with at most 6 decimals"
	}
	counter := req.CounterCurrency
	if counter == "" {
		counter = req.Currency
	}
	if !currencyCode.MatchString(counter) {
		fields["counterCurrency"] = "must be an ISO 4217 code"
	}
	rate := decimal.NewFromInt(1)
	if req.Rate != "" {
		if amountText.MatchString(req.Rate) {
			rate = decimal.RequireFromString(req.Rate)
		}
		if !amountText.MatchString(req.Rate) || !rate.IsPositive() {
			fields["rate"] = "must be a positive decimal with at most 6 decimals"
		}
	} else if counter != req.Currency {
		fields["rate"] = "is required when counterCurrency differs from currency"
	}
	matrix := parseMatrix(req.Matrix, fields)
	if len(fields) > 0 {
		return domain.Line{}, nil, "", decimal.Zero, fields
	}
	line := domain.Line{Entity: req.Entity, Account: req.Account, Side: side, Amount: money.Amount{Value: amount, Currency: req.Currency}}
	return line, matrix, counter, rate, nil
}

// parseMatrix converts the accounting matrix rows and records a field error for
// each missing value.
func parseMatrix(rows []matrixRow, fields map[string]string) domain.Matrix {
	if len(rows) > MaxMatrixRows {
		fields["matrix"] = fmt.Sprintf("must contain at most %d rows", MaxMatrixRows)
		return nil
	}
	matrix := make(domain.Matrix, 0, len(rows))
	for i, row := range rows {
		for name, v := range map[string]string{"entityA": row.EntityA, "entityB": row.EntityB, "dueFrom": row.DueFrom, "dueTo": row.DueTo} {
			if strings.TrimSpace(v) == "" {
				fields[fmt.Sprintf("matrix[%d].%s", i, name)] = "is required"
			}
		}
		matrix = append(matrix, domain.MatrixRow{EntityA: row.EntityA, EntityB: row.EntityB, DueFrom: row.DueFrom, DueTo: row.DueTo})
	}
	return matrix
}

// legsOf renders journal legs with each amount at its currency's minor unit.
func legsOf(j domain.Journal) []leg {
	out := make([]leg, 0, len(j.Legs))
	for _, l := range j.Legs {
		out = append(out, leg{Entity: l.Entity, Account: l.Account, Side: string(l.Side),
			Amount: l.Amount.Value.StringFixed(money.MinorUnits(l.Amount.Currency)), Currency: l.Amount.Currency})
	}
	return out
}

// ---- reconciliation classification ----

type reconItem struct {
	ID          string `json:"id"`
	IcPairID    string `json:"icPairId"`
	EntityA     string `json:"entityA"`
	EntityB     string `json:"entityB"`
	Period      string `json:"period"`
	Currency    string `json:"currency"`
	AmountA     string `json:"amountA"`
	AmountB     string `json:"amountB"`
	NextPeriodA string `json:"nextPeriodA"`
	NextPeriodB string `json:"nextPeriodB"`
	FXApplies   bool   `json:"fxApplies"`
}

type classifyRequest struct {
	FXTolerancePct string      `json:"fxTolerancePct"`
	Items          []reconItem `json:"items"`
}

type classifiedItem struct {
	ID               string `json:"id"`
	IcPairID         string `json:"icPairId,omitempty"`
	EntityA          string `json:"entityA"`
	EntityB          string `json:"entityB"`
	Period           string `json:"period"`
	Currency         string `json:"currency"`
	AmountA          string `json:"amountA"`
	AmountB          string `json:"amountB"`
	Difference       string `json:"difference"`
	Status           string `json:"status"`
	ToleranceApplied bool   `json:"toleranceApplied"`
}

type classifySummary struct {
	Matched  int `json:"matched"`
	Timing   int `json:"timing"`
	Mismatch int `json:"mismatch"`
}

type classifyResponse struct {
	FXTolerancePct string           `json:"fxTolerancePct"`
	Items          []classifiedItem `json:"items"`
	Summary        classifySummary  `json:"summary"`
}

// periodKey is the ledger's period key format (wave 11).
var periodKey = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}$`)

func (a *API) classify(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req classifyRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	inputs, tolerance, fields := parseClassify(req)
	if !validation(w, r, fields) {
		return
	}
	out := classifyResponse{FXTolerancePct: tolerance.String(), Items: make([]classifiedItem, 0, len(inputs))}
	for i, in := range inputs {
		item := req.Items[i]
		res := domain.Classify(in)
		id := item.ID
		if id == "" {
			id = fmt.Sprintf("item-%d", i+1)
		}
		out.Items = append(out.Items, classifiedItem{
			ID: id, IcPairID: item.IcPairID, EntityA: item.EntityA, EntityB: item.EntityB, Period: item.Period, Currency: item.Currency,
			AmountA:          in.AmountA.StringFixed(money.MinorUnits(item.Currency)),
			AmountB:          in.AmountB.StringFixed(money.MinorUnits(item.Currency)),
			Difference:       res.Difference.StringFixed(money.MinorUnits(item.Currency)),
			Status:           string(res.Status),
			ToleranceApplied: res.ToleranceApplied,
		})
		switch res.Status {
		case domain.ReconMatched:
			out.Summary.Matched++
		case domain.ReconTiming:
			out.Summary.Timing++
		default:
			out.Summary.Mismatch++
		}
	}
	a.reconItems.Add(int64(len(inputs)))
	a.reconMismatches.Add(int64(out.Summary.Mismatch))
	httpx.JSON(w, http.StatusOK, out)
}

func parseClassify(req classifyRequest) ([]domain.ReconInput, decimal.Decimal, map[string]string) {
	fields := map[string]string{}
	tolerance := domain.DefaultFXTolerancePct
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
	switch {
	case len(req.Items) == 0:
		fields["items"] = "must contain at least one item"
	case len(req.Items) > MaxReconItems:
		fields["items"] = fmt.Sprintf("must contain at most %d items", MaxReconItems)
	}
	if len(fields) > 0 {
		return nil, tolerance, fields
	}
	inputs := make([]domain.ReconInput, 0, len(req.Items))
	for i, it := range req.Items {
		at := func(name string) string { return fmt.Sprintf("items[%d].%s", i, name) }
		if strings.TrimSpace(it.EntityA) == "" {
			fields[at("entityA")] = "is required"
		}
		if strings.TrimSpace(it.EntityB) == "" {
			fields[at("entityB")] = "is required"
		} else if it.EntityA == it.EntityB {
			fields[at("entityB")] = "must differ from entityA"
		}
		if !periodKey.MatchString(it.Period) {
			fields[at("period")] = "must be a period key such as 2026-11"
		}
		if !currencyCode.MatchString(it.Currency) {
			fields[at("currency")] = "must be an ISO 4217 code"
		}
		amt := func(name, raw string, required bool) decimal.Decimal {
			if raw == "" && !required {
				return decimal.Zero
			}
			if !signedText.MatchString(raw) {
				fields[at(name)] = "must be a decimal with at most 6 decimals"
				return decimal.Zero
			}
			return decimal.RequireFromString(raw)
		}
		inputs = append(inputs, domain.ReconInput{
			AmountA:        amt("amountA", it.AmountA, true),
			AmountB:        amt("amountB", it.AmountB, true),
			NextPeriodA:    amt("nextPeriodA", it.NextPeriodA, false),
			NextPeriodB:    amt("nextPeriodB", it.NextPeriodB, false),
			FXApplies:      it.FXApplies,
			FXTolerancePct: tolerance,
		})
	}
	return inputs, tolerance, fields
}

// problemFrom maps a domain rule violation to a problem with its code.
func problemFrom(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		status := http.StatusUnprocessableEntity
		if de.Code == domain.CodeInvalid {
			status = http.StatusBadRequest
		}
		return httpx.NewProblem(status, de.Code, de.Detail)
	}
	return fmt.Errorf("intercompany: %w", err)
}
