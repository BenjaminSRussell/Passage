package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/taxclient"
)

// RegistryStore persists what the earlier slices took in every request: the entity registry, the accounting matrix, the transfer
// prices and the tenant's charge settings.
type RegistryStore interface {
	PutEntity(ctx context.Context, e domain.EntityRecord) error
	ListEntities(ctx context.Context) ([]domain.EntityRecord, error)
	ReplaceMatrix(ctx context.Context, m domain.Matrix) error
	GetMatrix(ctx context.Context) (domain.Matrix, error)
	AddTransferPrice(ctx context.Context, p domain.TransferPrice) (domain.TransferPriceRecord, error)
	ListTransferPrices(ctx context.Context, lender, borrower string) ([]domain.TransferPriceRecord, error)
	PricesFor(ctx context.Context, lender, borrower string) ([]domain.TransferPrice, error)
	GetSettings(ctx context.Context) (domain.Settings, error)
	PutSettings(ctx context.Context, s domain.Settings) error
}

// LedgerReconciler reads pair amounts from the ledgers and books FX differences (internal/ledgerrecon).
type LedgerReconciler interface {
	Run(ctx context.Context, period, currency string, tolerance decimal.Decimal, key string) (domain.ReconRun, bool, error)
	AdjustFX(ctx context.Context, itemID string) (domain.FXAdjustment, error)
}

// WithRegistry gives the API the stored registry, matrix, transfer prices and settings.
func (a *API) WithRegistry(s RegistryStore) *API {
	a.registry = s
	return a
}

// WithLedgerReconciliation gives the API the reconciler that reads the ledgers.
func (a *API) WithLedgerReconciliation(r LedgerReconciler) *API {
	a.ledgerRecon = r
	return a
}

// WithTax gives the API the tax service that prices cross-border charges.
func (a *API) WithTax(t taxclient.Tax) *API {
	a.tax = t
	return a
}

func (a *API) registryReady(w http.ResponseWriter, r *http.Request) bool {
	if !guard(w, r) {
		return false
	}
	if a.registry == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "the entity registry is not configured for this service"))
		return false
	}
	return true
}

type entityBody struct {
	Entity              string `json:"entity"`
	LegalEntityID       string `json:"legalEntityId"`
	LedgerID            string `json:"ledgerId"`
	Currency            string `json:"currency"`
	Country             string `json:"country"`
	FXDifferenceAccount string `json:"fxDifferenceAccount"`
	OutputTaxAccount    string `json:"outputTaxAccount"`
	InputTaxAccount     string `json:"inputTaxAccount"`
	TaxGroup            string `json:"taxGroup"`
}

func toEntityBody(e domain.EntityRecord) entityBody {
	return entityBody{Entity: e.Entity, LegalEntityID: e.LegalEntityID, LedgerID: e.LedgerID, Currency: e.Currency, Country: e.Country,
		FXDifferenceAccount: e.FXDifferenceAccount, OutputTaxAccount: e.OutputTaxAccount, InputTaxAccount: e.InputTaxAccount, TaxGroup: e.TaxGroup}
}

// putEntity stores the registry entry of an entity code: where its books are. The caller must be in the legal entity's scope.
func (a *API) putEntity(w http.ResponseWriter, r *http.Request) {
	if !a.registryReady(w, r) {
		return
	}
	var body entityBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	e := domain.EntityRecord{Entity: chi.URLParam(r, "entity"), LegalEntityID: strings.TrimSpace(body.LegalEntityID), LedgerID: strings.TrimSpace(body.LedgerID),
		Currency: body.Currency, Country: body.Country, FXDifferenceAccount: strings.TrimSpace(body.FXDifferenceAccount),
		OutputTaxAccount: strings.TrimSpace(body.OutputTaxAccount), InputTaxAccount: strings.TrimSpace(body.InputTaxAccount), TaxGroup: strings.TrimSpace(body.TaxGroup)}
	fields := map[string]string{}
	if !ids.Valid(e.LegalEntityID) {
		fields["legalEntityId"] = "must be a legal entity id"
	}
	if err := e.Validate(); err != nil && len(fields) == 0 {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if !validation(w, r, fields) {
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, e.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "that legal entity is outside the caller's scope"))
		return
	}
	if err := a.registry.PutEntity(r.Context(), e); err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, toEntityBody(e))
}

func (a *API) listEntities(w http.ResponseWriter, r *http.Request) {
	if !a.registryReady(w, r) {
		return
	}
	list, err := a.registry.ListEntities(r.Context())
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := struct {
		Items []entityBody `json:"items"`
	}{Items: make([]entityBody, 0, len(list))}
	p, _ := tenant.FromContext(r.Context())
	for _, e := range list {
		if tenant.InLegalEntityScope(p, e.LegalEntityID) {
			out.Items = append(out.Items, toEntityBody(e))
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

type matrixBody struct {
	Rows []matrixRow `json:"rows"`
}

func (a *API) putMatrix(w http.ResponseWriter, r *http.Request) {
	if !a.registryReady(w, r) {
		return
	}
	var body matrixBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	m := parseMatrix(body.Rows, fields)
	if !validation(w, r, fields) {
		return
	}
	// The matrix is shared by every pair of the tenant, so only a caller who sees every entity of it may replace it.
	if !a.callerSeesEveryEntity(w, r) {
		return
	}
	if err := a.registry.ReplaceMatrix(r.Context(), m); err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, matrixBody{Rows: matrixRows(m)})
}

func (a *API) callerSeesEveryEntity(w http.ResponseWriter, r *http.Request) bool {
	list, err := a.registry.ListEntities(r.Context())
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return false
	}
	p, _ := tenant.FromContext(r.Context())
	for _, e := range list {
		if !tenant.InLegalEntityScope(p, e.LegalEntityID) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the matrix covers a legal entity outside the caller's scope"))
			return false
		}
	}
	return true
}

func matrixRows(m domain.Matrix) []matrixRow {
	out := make([]matrixRow, 0, len(m))
	for _, row := range m {
		out = append(out, matrixRow{EntityA: row.EntityA, EntityB: row.EntityB, DueFrom: row.DueFrom, DueTo: row.DueTo})
	}
	return out
}

func (a *API) getMatrix(w http.ResponseWriter, r *http.Request) {
	if !a.registryReady(w, r) {
		return
	}
	m, err := a.registry.GetMatrix(r.Context())
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, matrixBody{Rows: matrixRows(m)})
}

type storedPriceBody struct {
	ID              string `json:"id,omitempty"`
	LendingEntity   string `json:"lendingEntity"`
	BorrowingEntity string `json:"borrowingEntity"`
	Category        string `json:"category"`
	ResourceRole    string `json:"resourceRole"`
	Method          string `json:"method"`
	MarkupPct       string `json:"markupPct,omitempty"`
	UnitPrice       string `json:"unitPrice,omitempty"`
	Currency        string `json:"currency"`
	EffectiveFrom   string `json:"effectiveFrom"`
	CreatedBy       string `json:"createdBy,omitempty"`
	CreatedAt       string `json:"createdAt,omitempty"`
}

func toStoredPrice(p domain.TransferPriceRecord) storedPriceBody {
	out := storedPriceBody{ID: p.ID, LendingEntity: p.Lender, BorrowingEntity: p.Borrower, Category: p.Category, ResourceRole: p.Role, Method: p.Method,
		Currency: p.Currency, EffectiveFrom: p.EffectiveFrom.Format(dateLayout), CreatedBy: p.CreatedBy, CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339)}
	switch p.Method {
	case domain.TPCostPlus:
		out.MarkupPct = p.MarkupPct.String()
	case domain.TPTable:
		out.UnitPrice = p.UnitPrice.String()
	}
	return out
}

// addTransferPrice stores an effective-dated transfer price (BR-40.12). Prices are never edited: the next one takes over from its date.
func (a *API) addTransferPrice(w http.ResponseWriter, r *http.Request) {
	if !a.registryReady(w, r) {
		return
	}
	var body storedPriceBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := fieldParser{}
	tp := domain.TransferPrice{Lender: f.text("lendingEntity", body.LendingEntity), Borrower: f.text("borrowingEntity", body.BorrowingEntity), Category: body.Category,
		Role: strings.TrimSpace(body.ResourceRole), Method: body.Method, Currency: f.currency("currency", body.Currency), EffectiveFrom: f.date("effectiveFrom", body.EffectiveFrom)}
	if !chargeCategories[body.Category] {
		f["category"] = "must be Expense, Hour, Item or Fee"
	}
	switch body.Method {
	case domain.TPCost:
	case domain.TPCostPlus:
		tp.MarkupPct = f.amount("markupPct", body.MarkupPct, true)
	case domain.TPTable:
		tp.UnitPrice = f.amount("unitPrice", body.UnitPrice, true)
	default:
		f["method"] = "must be Cost, CostPlus or Table"
	}
	if len(f) == 0 {
		if err := tp.Validate(); err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
	}
	if !validation(w, r, f) {
		return
	}
	rec, err := a.registry.AddTransferPrice(r.Context(), tp)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	w.Header().Set("Location", "/v1/interco/transfer-prices?lender="+tp.Lender+"&borrower="+tp.Borrower)
	httpx.JSON(w, http.StatusCreated, toStoredPrice(rec))
}

func (a *API) listTransferPrices(w http.ResponseWriter, r *http.Request) {
	if !a.registryReady(w, r) {
		return
	}
	q := r.URL.Query()
	list, err := a.registry.ListTransferPrices(r.Context(), strings.TrimSpace(q.Get("lender")), strings.TrimSpace(q.Get("borrower")))
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := struct {
		Items []storedPriceBody `json:"items"`
	}{Items: make([]storedPriceBody, 0, len(list))}
	for _, p := range list {
		out.Items = append(out.Items, toStoredPrice(p))
	}
	httpx.JSON(w, http.StatusOK, out)
}

type settingsBody struct {
	ChargeSettlementMode string `json:"chargeSettlementMode"`
	CrossBorderTax       bool   `json:"crossBorderTax"`
	ReconCurrency        string `json:"reconCurrency"`
	FXTolerancePct       string `json:"fxTolerancePct"`
	// FXAdjustMaxPct is the most, in percent of the larger side of a pair, that an FX adjustment may book (0 to 10, default 2).
	FXAdjustMaxPct string `json:"fxAdjustMaxPct"`
}

func toSettingsBody(s domain.Settings) settingsBody {
	return settingsBody{ChargeSettlementMode: s.ChargeSettlementMode, CrossBorderTax: s.CrossBorderTax, ReconCurrency: s.ReconCurrency, FXTolerancePct: s.FXTolerancePct.String(), FXAdjustMaxPct: s.FXAdjustMaxPct.String()}
}

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	if !a.registryReady(w, r) {
		return
	}
	s, err := a.registry.GetSettings(r.Context())
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, toSettingsBody(s))
}

func (a *API) putSettings(w http.ResponseWriter, r *http.Request) {
	if !a.registryReady(w, r) {
		return
	}
	var body settingsBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := fieldParser{}
	s := domain.Settings{ChargeSettlementMode: body.ChargeSettlementMode, CrossBorderTax: body.CrossBorderTax, ReconCurrency: body.ReconCurrency, FXTolerancePct: domain.DefaultFXTolerancePct, FXAdjustMaxPct: domain.DefaultFXAdjustMaxPct}
	if body.FXTolerancePct != "" {
		s.FXTolerancePct = f.amount("fxTolerancePct", body.FXTolerancePct, true)
	}
	if body.FXAdjustMaxPct != "" {
		s.FXAdjustMaxPct = f.amount("fxAdjustMaxPct", body.FXAdjustMaxPct, true)
	}
	if len(f) == 0 {
		if err := s.Validate(); err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
	}
	if !validation(w, r, f) {
		return
	}
	if err := a.registry.PutSettings(r.Context(), s); err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, toSettingsBody(s))
}

type fxAdjustmentResponse struct {
	ID            string `json:"id"`
	ItemID        string `json:"itemId"`
	Entity        string `json:"entity"`
	Currency      string `json:"currency"`
	Amount        string `json:"amount"`
	Direction     string `json:"direction"`
	Account       string `json:"account"`
	FXAccount     string `json:"fxAccount"`
	LedgerEntryID string `json:"ledgerEntryId"`
	PostedOn      string `json:"postedOn"`
	PeriodEnd     string `json:"periodEnd"`
}

// adjustFX books the FX difference of an open item to its entity's FX-difference account and resolves the item.
func (a *API) adjustFX(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	if a.ledgerRecon == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "posting is not configured: the ledger is not set for this service"))
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeReconItemNotFound, "no reconciliation item with id "+id))
		return
	}
	adj, err := a.ledgerRecon.AdjustFX(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	direction := "decrease"
	if adj.IncreasesBalance {
		direction = "increase"
	}
	a.reconResolved.Add(1)
	httpx.JSON(w, http.StatusOK, fxAdjustmentResponse{ID: adj.ID, ItemID: adj.ItemID, Entity: adj.Entity, Currency: adj.Currency,
		Amount: adj.Amount.StringFixed(int32(money.MinorUnits(adj.Currency))), Direction: direction, Account: adj.BalanceAccount, FXAccount: adj.FXAccount,
		LedgerEntryID: adj.LedgerEntryID, PostedOn: adj.PostedOn.Format(dateLayout), PeriodEnd: adj.PeriodEnd.Format(dateLayout)})
}

// registryEntities returns the stored registry by entity code, or nil when no registry is configured.
func (a *API) registryEntities(ctx context.Context) (map[string]domain.EntityRecord, error) {
	if a.registry == nil {
		return nil, nil
	}
	list, err := a.registry.ListEntities(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]domain.EntityRecord, len(list))
	for _, e := range list {
		out[e.Entity] = e
	}
	return out, nil
}

// targetsFor returns the ledger target of each named entity: the targets the request sent, or the registry's when it sent none.
func (a *API) targetsFor(ctx context.Context, sent map[string]domain.Target, names ...string) (map[string]domain.Target, error) {
	if len(sent) > 0 {
		return sent, nil
	}
	reg, err := a.registryEntities(ctx)
	if err != nil {
		return nil, err
	}
	if reg == nil {
		return nil, &domain.Error{Code: domain.CodeInvalid, Detail: "entities: list the entities with their legal entity and ledger, or store them in the registry"}
	}
	out := make(map[string]domain.Target, len(names))
	for _, n := range names {
		e, ok := reg[n]
		if !ok {
			return nil, &domain.Error{Code: domain.CodeEntityUnknown, Detail: "entity " + n + " is not in the registry; store it or send its legal entity and ledger with the request"}
		}
		out[n] = domain.Target{LegalEntityID: e.LegalEntityID, LedgerID: e.LedgerID}
	}
	return out, nil
}

// matrixFor returns the matrix the request sent, or the stored one when it sent none.
func (a *API) matrixFor(ctx context.Context, sent domain.Matrix) (domain.Matrix, error) {
	if len(sent) > 0 || a.registry == nil {
		return sent, nil
	}
	return a.registry.GetMatrix(ctx)
}
