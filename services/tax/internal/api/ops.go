package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
	"github.com/BenjaminSRussell/passage/services/tax/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/tax/internal/saft"
	"github.com/BenjaminSRussell/passage/services/tax/internal/store"
)

const (
	dutyRecordTransactions = "tax.transaction.record"
	dutySettle             = "tax.settlement.run"
	dutyRecommit           = "tax.recommit.run"
	dutySAFT               = "tax.saft.export"
	dutyLedgerConfig       = "tax.ledgerconfig.manage"

	codeNotConfigured     = "COM-0503"
	codeLedgerUnavailable = "COM-0502"

	maxTransactions      = 1000
	maxCodeLength        = 64
	defaultRecommitBatch = 20
	queueTimeout         = 5 * time.Second
	dateLayout           = "2006-01-02"
)

var accountPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,20}$`)

type Store interface {
	RecordTransactions(ctx context.Context, txns []domain.Txn) (int, error)
	SetLedgerConfig(ctx context.Context, cfg store.LedgerConfig) error
	SettleRun(ctx context.Context, period domain.Period, newGL func(store.LedgerConfig) domain.GLMovement) (store.Run, error)
	Totals(ctx context.Context, period domain.Period) ([]store.Total, error)
	EnqueueRecommit(ctx context.Context, provider, reason string, request json.RawMessage) (string, error)
	Recommit(ctx context.Context, limit int, process func(context.Context, store.Recommit) (json.RawMessage, error)) (store.RecommitSummary, error)
}

func invalid(field, format string, a ...any) *domain.Error {
	return &domain.Error{Code: domain.CodeValidation, Field: field, Detail: fmt.Sprintf(format, a...)}
}

func (a *API) storeReady(w http.ResponseWriter, r *http.Request) bool {
	if a.store == nil {
		fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, codeNotConfigured, "the tax store is not configured for this service"))
		return false
	}
	return true
}

type transactionInput struct {
	ID        string      `json:"id"`
	Code      string      `json:"code"`
	Direction string      `json:"direction"`
	TaxDate   domain.Date `json:"tax_date"`
	Amount    domain.Dec  `json:"amount"`
}

type transactionsRequest struct {
	Transactions []transactionInput `json:"transactions"`
}

func (req transactionsRequest) txns() ([]domain.Txn, error) {
	if len(req.Transactions) == 0 || len(req.Transactions) > maxTransactions {
		return nil, invalid("transactions", "transactions must hold between 1 and %d entries", maxTransactions)
	}
	out := make([]domain.Txn, len(req.Transactions))
	for i, t := range req.Transactions {
		field := fmt.Sprintf("transactions[%d]", i)
		switch {
		case !ids.Valid(t.ID):
			return nil, invalid(field+".id", "id must be a UUID")
		case t.Code == "" || len(t.Code) > maxCodeLength || strings.Contains(t.Code, "|"):
			return nil, invalid(field+".code", "code is required, at most %d characters and holds no |", maxCodeLength)
		case !validDirection(t.Direction):
			return nil, invalid(field+".direction", "direction must be %q, %q or %q", domain.DirOutput, domain.DirInput, domain.DirUse)
		case t.TaxDate.IsZero():
			return nil, invalid(field+".tax_date", "tax_date is required")
		}
		out[i] = domain.Txn{ID: t.ID, Code: t.Code, Direction: t.Direction, TaxDate: t.TaxDate, Amount: t.Amount}
	}
	return out, nil
}

func validDirection(d string) bool {
	return d == domain.DirOutput || d == domain.DirInput || d == domain.DirUse
}

func (a *API) recordTransactions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || !a.storeReady(w, r) {
		return
	}
	var req transactionsRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	txns, err := req.txns()
	if err != nil {
		a.reject(w, r, err)
		return
	}
	recorded, err := a.store.RecordTransactions(r.Context(), txns)
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]int{"recorded": recorded, "duplicates": len(txns) - recorded})
}

type settleRequest struct {
	PeriodFrom domain.Date `json:"period_from"`
	PeriodTo   domain.Date `json:"period_to"`
}

func (req settleRequest) validate() error {
	if req.PeriodFrom.IsZero() || req.PeriodTo.IsZero() || req.PeriodTo.Before(req.PeriodFrom.Time) {
		return invalid("period_from", "period_from and period_to are required and period_to may not precede period_from")
	}
	return nil
}

func validateLedgerConfig(cfg store.LedgerConfig) error {
	if !ids.Valid(cfg.LedgerID) {
		return invalid("ledger_id", "ledger_id must be a UUID")
	}
	if len(cfg.Accounts) == 0 {
		return invalid("accounts", "accounts must map every settled code|direction to its GL tax account")
	}
	for key, account := range cfg.Accounts {
		code, direction, found := strings.Cut(key, "|")
		if !found || code == "" || !validDirection(direction) {
			return invalid("accounts", "account key %q must be code|direction", key)
		}
		if !accountPattern.MatchString(account) {
			return invalid("accounts", "account for %q is not a valid GL account number", key)
		}
	}
	return nil
}

func (a *API) setLedgerConfig(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || !a.storeReady(w, r) {
		return
	}
	var cfg store.LedgerConfig
	if err := httpx.Decode(r, &cfg); err != nil {
		a.reject(w, r, err)
		return
	}
	if err := validateLedgerConfig(cfg); err != nil {
		a.reject(w, r, err)
		return
	}
	if err := a.store.SetLedgerConfig(r.Context(), cfg); err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, cfg)
}

func (a *API) settle(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || !a.storeReady(w, r) {
		return
	}
	if a.ledger == nil {
		fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, codeNotConfigured, "the general ledger is not configured for this service"))
		return
	}
	var req settleRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	if err := req.validate(); err != nil {
		a.reject(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	newGL := func(cfg store.LedgerConfig) domain.GLMovement {
		return ledger.GL{Client: a.ledger, LegalEntityID: p.LegalEntityID, LedgerID: cfg.LedgerID, Accounts: cfg.Accounts}
	}
	run, err := a.store.SettleRun(r.Context(), domain.Period{From: req.PeriodFrom, To: req.PeriodTo}, newGL)
	if err != nil {
		a.reject(w, r, err)
		return
	}
	a.settlements.Add(1)
	httpx.JSON(w, http.StatusCreated, run)
}

func (a *API) queueRecommit(ctx context.Context, req calculateRequest, notice *domain.FallbackNotice) {
	if a.store == nil {
		return
	}
	raw, err := json.Marshal(req)
	if err == nil {
		qctx, cancel := context.WithTimeout(ctx, queueTimeout)
		defer cancel()
		_, err = a.store.EnqueueRecommit(qctx, notice.Provider, notice.Reason, raw)
	}
	if err != nil {
		a.queueFailures.Add(1)
		slog.Error("queue recommit failed", "provider", notice.Provider, "err", err, "correlationId", middleware.GetReqID(ctx))
	}
}

func (a *API) recommitOne(ctx context.Context, item store.Recommit) (json.RawMessage, error) {
	provider, ok := a.providers[item.Provider]
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured", item.Provider)
	}
	var req calculateRequest
	if err := json.Unmarshal(item.Request, &req); err != nil {
		return nil, fmt.Errorf("queued request unreadable: %w", err)
	}
	res, notice, err := domain.CalculateWithFailover(ctx, provider, req.Document, req.Config, a.providerTimeout)
	if err != nil {
		return nil, err
	}
	if notice != nil {
		return nil, fmt.Errorf("provider still unavailable: %s", notice.Reason)
	}
	return json.Marshal(res)
}

func (a *API) recommit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || !a.storeReady(w, r) {
		return
	}
	limit := defaultRecommitBatch
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxRecommitBatch {
			a.reject(w, r, invalid("limit", "limit must be between 1 and %d", store.MaxRecommitBatch))
			return
		}
		limit = n
	}
	sum, err := a.store.Recommit(r.Context(), limit, a.recommitOne)
	if err != nil {
		a.reject(w, r, err)
		return
	}
	a.recommitted.Add(int64(sum.Completed))
	httpx.JSON(w, http.StatusOK, sum)
}

func queryDate(r *http.Request, field string) (domain.Date, error) {
	t, err := time.Parse(dateLayout, r.URL.Query().Get(field))
	if err != nil {
		return domain.Date{}, invalid(field, "%s is required as YYYY-MM-DD", field)
	}
	return domain.Date{Time: t}, nil
}

func (a *API) saft(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || !a.storeReady(w, r) {
		return
	}
	from, err := queryDate(r, "period_from")
	if err != nil {
		a.reject(w, r, err)
		return
	}
	to, err := queryDate(r, "period_to")
	if err != nil {
		a.reject(w, r, err)
		return
	}
	period := domain.Period{From: from, To: to}
	q := r.URL.Query()
	in := saft.Input{CompanyName: q.Get("company_name"), RegistrationNumber: q.Get("registration_number"), Country: q.Get("country"),
		Currency: q.Get("currency"), Period: period, Created: time.Now()}
	if err := saft.Validate(in); err != nil {
		a.reject(w, r, err)
		return
	}
	totals, err := a.store.Totals(r.Context(), period)
	if err != nil {
		a.reject(w, r, err)
		return
	}
	for _, t := range totals {
		in.Entries = append(in.Entries, saft.Entry{Code: t.Code, Direction: t.Direction, Amount: t.Amount, Count: t.Count})
	}
	if ledgerID := q.Get("ledger_id"); ledgerID != "" {
		if in.Journals, err = a.saftJournals(r.Context(), ledgerID, period); err != nil {
			a.reject(w, r, err)
			return
		}
	}
	doc, err := saft.Build(in)
	if err != nil {
		a.reject(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write(doc)
}

func (a *API) saftJournals(ctx context.Context, ledgerID string, period domain.Period) ([]saft.Journal, error) {
	reader, ok := a.ledger.(ledgerclient.LineReader)
	if !ok {
		return nil, ledger.ErrNotConfigured
	}
	if !ids.Valid(ledgerID) {
		return nil, invalid("ledger_id", "ledger_id must be a UUID")
	}
	p, _ := tenant.FromContext(ctx)
	owner, err := a.ledger.LegalEntity(ctx, p.LegalEntityID, ledgerID)
	if err != nil {
		if ledgerclient.Rejected(err) {
			return nil, invalid("ledger_id", "ledger_id is not a ledger of this legal entity")
		}
		return nil, fmt.Errorf("%w: ledger lookup: %w", ledger.ErrRead, err)
	}
	if owner != p.LegalEntityID {
		return nil, invalid("ledger_id", "ledger_id is not a ledger of this legal entity")
	}
	lines, err := reader.JournalLines(ctx, ledgerclient.LineQuery{LegalEntityID: p.LegalEntityID, LedgerID: ledgerID,
		DateFrom: period.From.Format(dateLayout), DateTo: period.To.Format(dateLayout)})
	if err != nil {
		return nil, fmt.Errorf("%w: journal lines: %w", ledger.ErrRead, err)
	}
	return ledger.Journals(lines)
}
