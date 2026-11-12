// Package api exposes the intercompany HTTP surface. Trading partners
// (FR-40.1) are stored: create with an Idempotency-Key, change with If-Match,
// and price IC lines (FR-40.4) or check a linked order change (BR-40.3)
// against a stored pairing. The other routes are stateless: the caller
// supplies the accounting matrix, balances, transfer prices, planning group,
// chain or priced lines, and the service returns the derived IC journal
// (BR-40.4), the reconciliation classification (FR-40.6, BR-40.5), the priced
// IC charge with its Journal-mode legs (FR-40.13), the centralized payment
// legs (FR-40.12) and their bank-return reversal (BR-40.11), the planning run
// sequence with IC demand (FR-40.15), the direct-delivery chain postings
// (FR-40.3) or the transfer-pricing evidence file (FR-40.4).
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
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/taxclient"
)

// Duties from PRD section 7.40.17.
const (
	DutyView      = "intercompany.view"
	DutyConfigure = "intercompany.configure"
	DutyPost      = "intercompany.post"
	DutyReconcile = "intercompany.reconcile"
	DutyPlan      = "intercompany.plan"
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
	partners PartnerStore
	poster   JournalPoster
	journals JournalStore
	charges  ChargeStore
	recon    ReconStore

	registry    RegistryStore
	ledgerRecon LedgerReconciler
	tax         taxclient.Tax

	journalsPosted   atomic.Int64
	chargesPosted    atomic.Int64
	chargesSuspended atomic.Int64
	chargesInvoiced  atomic.Int64
	reconRuns        atomic.Int64
	reconResolved    atomic.Int64

	journalPreviews        atomic.Int64
	reconItems             atomic.Int64
	reconMismatches        atomic.Int64
	chargePreviews         atomic.Int64
	centralPaymentPreviews atomic.Int64
	planningSequences      atomic.Int64
	chainPreviews          atomic.Int64
	evidenceExports        atomic.Int64
	reversalPreviews       atomic.Int64
	partnerChanges         atomic.Int64
	linesPriced            atomic.Int64
	changeEvaluations      atomic.Int64
}

// New returns the API over the partner store. The stateless routes do not
// touch it.
func New(partners PartnerStore) *API { return &API{partners: partners} }

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
		{http.MethodPost, "/v1/interco/chains:preview", DutyView, a.previewChain},
		{http.MethodPost, "/v1/interco/transfer-pricing/evidence:export", DutyView, a.exportEvidence},
		{http.MethodPost, "/v1/interco/centralized-payments/reversals:preview", DutyConfigure, a.previewReversal},
		{http.MethodPost, "/v1/interco/journals", DutyPost, a.postJournal},
		{http.MethodGet, "/v1/interco/journals", DutyView, a.listJournals},
		{http.MethodGet, "/v1/interco/journals/{id}", DutyView, a.getJournal},
		{http.MethodPost, "/v1/interco/charges", DutyPost, a.postCharge},
		{http.MethodGet, "/v1/interco/charges", DutyView, a.listCharges},
		{http.MethodGet, "/v1/interco/charges/{id}", DutyView, a.getCharge},
		{http.MethodPost, "/v1/interco/reconciliations/run", DutyReconcile, a.runReconciliation},
		{http.MethodGet, "/v1/interco/reconciliations", DutyView, a.listReconRuns},
		{http.MethodGet, "/v1/interco/reconciliations/{id}", DutyView, a.getReconRun},
		{http.MethodPost, "/v1/interco/reconciliations/items/{id}/resolve", DutyReconcile, a.resolveReconItem},
		{http.MethodPost, "/v1/interco/reconciliations/items/{id}/fx-adjust", DutyReconcile, a.adjustFX},
		{http.MethodGet, "/v1/interco/entities", DutyView, a.listEntities},
		{http.MethodPut, "/v1/interco/entities/{entity}", DutyConfigure, a.putEntity},
		{http.MethodGet, "/v1/interco/matrix", DutyView, a.getMatrix},
		{http.MethodPut, "/v1/interco/matrix", DutyConfigure, a.putMatrix},
		{http.MethodGet, "/v1/interco/transfer-prices", DutyView, a.listTransferPrices},
		{http.MethodPost, "/v1/interco/transfer-prices", DutyConfigure, a.addTransferPrice},
		{http.MethodGet, "/v1/interco/settings", DutyView, a.getSettings},
		{http.MethodPut, "/v1/interco/settings", DutyConfigure, a.putSettings},
		{http.MethodGet, "/v1/interco/partners", DutyView, a.listPartners},
		{http.MethodPost, "/v1/interco/partners", DutyConfigure, a.createPartner},
		{http.MethodGet, "/v1/interco/partners/{id}", DutyView, a.getPartner},
		{http.MethodPatch, "/v1/interco/partners/{id}", DutyConfigure, a.updatePartner},
		{http.MethodPost, "/v1/interco/partners/{id}/lines:price", DutyView, a.priceLine},
		{http.MethodPost, "/v1/interco/partners/{id}/changes:evaluate", DutyView, a.evaluateChange},
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
		counter("intercompany_chain_previews_total", "Direct delivery chain postings derived.", a.chainPreviews.Load())
		counter("intercompany_evidence_exports_total", "Transfer-pricing evidence files exported.", a.evidenceExports.Load())
		counter("intercompany_reversal_previews_total", "Centralized payment reversals derived.", a.reversalPreviews.Load())
		counter("intercompany_partner_changes_total", "Trading-partner pairings created or changed.", a.partnerChanges.Load())
		counter("intercompany_lines_priced_total", "IC lines priced from a stored pairing.", a.linesPriced.Load())
		counter("intercompany_journals_posted_total", "IC journals posted to both ledgers.", a.journalsPosted.Load())
		counter("intercompany_charges_posted_total", "IC charges posted to both ledgers.", a.chargesPosted.Load())
		counter("intercompany_charges_invoiced_total", "IC charges handed to the invoice documents in Invoice settlement mode.", a.chargesInvoiced.Load())
		counter("intercompany_charges_suspended_total", "IC charges kept on suspense for lack of a transfer price.", a.chargesSuspended.Load())
		counter("intercompany_recon_runs_total", "Reconciliation runs stored.", a.reconRuns.Load())
		counter("intercompany_recon_items_resolved_total", "Reconciliation items resolved.", a.reconResolved.Load())
		counter("intercompany_change_evaluations_total", "Linked order changes checked against a pairing's tolerance.", a.changeEvaluations.Load())
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

// classifySummary counts the outcomes. Blocking is true while any item is a
// Mismatch: the period-end summary must be free of Mismatch before
// consolidation, unless ConsolidationApprove overrides the gate (FR-40.6, CNS-0901).
type classifySummary struct {
	Matched  int  `json:"matched"`
	Timing   int  `json:"timing"`
	Mismatch int  `json:"mismatch"`
	Blocking bool `json:"blocking"`
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
	out.Summary.Blocking = out.Summary.Mismatch > 0
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
		switch de.Code {
		case domain.CodeInvalid:
			status = http.StatusBadRequest
		case domain.CodePartnerNotFound, domain.CodePostingNotFound, domain.CodeChargeNotFound, domain.CodeReconRunNotFound, domain.CodeReconItemNotFound:
			status = http.StatusNotFound
		case domain.CodeReconNotOpen, domain.CodeTransferPriceExist:
			status = http.StatusConflict
		case domain.CodeLedgerUnavailable, domain.CodeTaxUnavailable, domain.CodeNotConfigured:
			status = http.StatusServiceUnavailable
		case domain.CodePairingExists:
			status = http.StatusConflict
		case domain.CodeVersionConflict:
			status = http.StatusPreconditionFailed
		}
		return httpx.NewProblem(status, de.Code, de.Detail)
	}
	return fmt.Errorf("intercompany: %w", err)
}
