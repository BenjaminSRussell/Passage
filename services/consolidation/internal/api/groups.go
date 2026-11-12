package api

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/domain"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/store"
)

// GroupStore persists the group structure (FR-40.7) and the collections
// (FR-40.8). internal/store implements it over Postgres.
type GroupStore interface {
	CreateGroup(ctx context.Context, g domain.Group, key string) (domain.Group, bool, error)
	ListGroups(ctx context.Context) ([]store.GroupSummary, error)
	GetGroup(ctx context.Context, id string) (store.GroupDetail, error)
	AddUnit(ctx context.Context, groupID string, u domain.Unit) (domain.Unit, error)
	AddOwnership(ctx context.Context, groupID, unitID string, h domain.Holding) (domain.Unit, error)
	UpdateUnit(ctx context.Context, groupID, unitID string, c store.UnitChange) (domain.Unit, error)
	RemoveUnit(ctx context.Context, groupID, unitID string) error
	SetUnitTerms(ctx context.Context, groupID, unitID string, t domain.UnitTerms) (store.UnitTermsRecord, error)
	GetUnitTerms(ctx context.Context, groupID string) (map[string]store.UnitTermsRecord, error)
	SetMapping(ctx context.Context, groupID string, rows []domain.AccountMapping) (int, error)
	GetMapping(ctx context.Context, groupID string, version int) ([]domain.AccountMapping, int, error)
	ListMappingVersions(ctx context.Context, groupID string) ([]store.MappingVersion, error)
	CreateCollection(ctx context.Context, groupID, period string, mappingVersion int, lines []domain.CollectLine, inputs any, key string) (store.Collection, bool, error)
	LatestCollection(ctx context.Context, groupID, period string) (store.CollectionDetail, bool, error)
	ListCollections(ctx context.Context, groupID string) ([]store.Collection, error)
	GetCollection(ctx context.Context, id string) (store.CollectionDetail, error)
	SetLayers(ctx context.Context, groupID, unitID, period string, layers []domain.ICLayer) error
	GetLayers(ctx context.Context, groupID, period string) (map[string][]domain.ICLayer, error)
	LatestInvestments(ctx context.Context, groupID, period string) (map[string]store.InvestmentRecord, bool, error)
}

// WithGroups gives the API the group and collection store.
func (a *API) WithGroups(g GroupStore) *API {
	a.groups = g
	return a
}

type groupBody struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Currency string `json:"currency"`
}

type groupResponse struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Currency  string    `json:"currency"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UnitCount *int      `json:"unitCount,omitempty"`
}

type holdingResponse struct {
	ID            string `json:"id"`
	HolderUnitID  string `json:"holderUnitId,omitempty"`
	EffectiveFrom string `json:"effectiveFrom"`
	EffectiveTo   string `json:"effectiveTo,omitempty"`
	Percent       string `json:"percent"`
}

type unitResponse struct {
	ID             string `json:"id"`
	LegalEntityID  string `json:"legalEntityId"`
	LedgerID       string `json:"ledgerId"`
	Currency       string `json:"currency"`
	CurrentPercent string `json:"currentPercent"`
	// FiscalYearStartMonth is the month (1 to 12) the unit's fiscal year starts in.
	FiscalYearStartMonth int               `json:"fiscalYearStartMonth"`
	Ownership            []holdingResponse `json:"ownership"`
}

type groupDetailResponse struct {
	groupResponse
	Units []unitResponse `json:"units"`
}

func toGroupResponse(g domain.Group) groupResponse {
	return groupResponse{ID: g.ID, Code: g.Code, Name: g.Name, Currency: g.Currency, CreatedBy: g.CreatedBy, CreatedAt: g.CreatedAt.UTC()}
}

func toUnitResponse(u domain.Unit, today time.Time) unitResponse {
	fy := u.FiscalYearStartMonth
	if fy == 0 {
		fy = 1
	}
	out := unitResponse{ID: u.ID, LegalEntityID: u.LegalEntityID, LedgerID: u.LedgerID, Currency: u.Currency, FiscalYearStartMonth: fy,
		CurrentPercent: u.PercentOn(today).StringFixed(4), Ownership: make([]holdingResponse, 0, len(u.Holdings))}
	for _, h := range u.Holdings {
		hr := holdingResponse{ID: h.ID, HolderUnitID: h.HolderUnitID, EffectiveFrom: h.From.Format(dateLayout), Percent: h.Percent.StringFixed(4)}
		if !h.To.IsZero() {
			hr.EffectiveTo = h.To.Format(dateLayout)
		}
		out.Ownership = append(out.Ownership, hr)
	}
	return out
}

func (a *API) groupsReady(w http.ResponseWriter, r *http.Request) bool {
	if !guard(w, r) || !allEntityScope(w, r) {
		return false
	}
	if a.groups == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "group storage is not configured"))
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request, name, code string) (string, bool) {
	id := chi.URLParam(r, name)
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, code, "no consolidation record with id "+id))
		return "", false
	}
	return id, true
}

func (a *API) createGroup(w http.ResponseWriter, r *http.Request) {
	if !a.groupsReady(w, r) {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > maxIdempotencyKey {
		validation(w, r, map[string]string{"Idempotency-Key": "header is required, at most 255 characters"})
		return
	}
	var body groupBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	g := domain.Group{Code: strings.TrimSpace(body.Code), Name: strings.TrimSpace(body.Name), Currency: body.Currency}
	if err := g.Validate(); err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out, replayed, err := a.groups.CreateGroup(r.Context(), g, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("Location", "/v1/consol/groups/"+out.ID)
	httpx.JSON(w, http.StatusCreated, toGroupResponse(out))
}

func (a *API) listGroups(w http.ResponseWriter, r *http.Request) {
	if !a.groupsReady(w, r) {
		return
	}
	list, err := a.groups.ListGroups(r.Context())
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := struct {
		Items []groupResponse `json:"items"`
	}{Items: make([]groupResponse, 0, len(list))}
	for _, g := range list {
		gr := toGroupResponse(g.Group)
		n := g.UnitCount
		gr.UnitCount = &n
		out.Items = append(out.Items, gr)
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getGroup(w http.ResponseWriter, r *http.Request) {
	if !a.groupsReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	d, err := a.groups.GetGroup(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	out := groupDetailResponse{groupResponse: toGroupResponse(d.Group), Units: make([]unitResponse, 0, len(d.Units))}
	for _, u := range d.Units {
		out.Units = append(out.Units, toUnitResponse(u, today))
	}
	httpx.JSON(w, http.StatusOK, out)
}

type unitBody struct {
	LegalEntityID string `json:"legalEntityId"`
	LedgerID      string `json:"ledgerId"`
	Currency      string `json:"currency"`
	// FiscalYearStartMonth is the month the unit's fiscal year starts in; 1 (the calendar year) when omitted.
	FiscalYearStartMonth int `json:"fiscalYearStartMonth"`
}

func (a *API) addUnit(w http.ResponseWriter, r *http.Request) {
	if !a.groupsReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	var body unitBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(body.LegalEntityID) {
		fields["legalEntityId"] = "must be a legal entity id"
	}
	if strings.TrimSpace(body.LedgerID) == "" {
		fields["ledgerId"] = "is required"
	}
	if !currencyCode.MatchString(body.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if body.FiscalYearStartMonth < 0 || body.FiscalYearStartMonth > 12 {
		fields["fiscalYearStartMonth"] = "must be a month from 1 to 12"
	}
	if !validation(w, r, fields) {
		return
	}
	u, err := a.groups.AddUnit(r.Context(), id, domain.Unit{LegalEntityID: body.LegalEntityID, LedgerID: strings.TrimSpace(body.LedgerID), Currency: body.Currency, FiscalYearStartMonth: body.FiscalYearStartMonth})
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusCreated, toUnitResponse(u, time.Now().UTC()))
}

type ownershipBody struct {
	HolderUnitID  string `json:"holderUnitId"`
	EffectiveFrom string `json:"effectiveFrom"`
	EffectiveTo   string `json:"effectiveTo"`
	Percent       string `json:"percent"`
}

func (a *API) addOwnership(w http.ResponseWriter, r *http.Request) {
	if !a.groupsReady(w, r) {
		return
	}
	groupID, ok := pathID(w, r, "id", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	unitID, ok := pathID(w, r, "unitId", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	var body ownershipBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	h := domain.Holding{HolderUnitID: body.HolderUnitID}
	if body.HolderUnitID != "" && !ids.Valid(body.HolderUnitID) {
		fields["holderUnitId"] = "must be a unit id of this group"
	}
	var err error
	if h.From, err = time.Parse(dateLayout, body.EffectiveFrom); err != nil {
		fields["effectiveFrom"] = "must be a date such as 2026-01-01"
	}
	if body.EffectiveTo != "" {
		if h.To, err = time.Parse(dateLayout, body.EffectiveTo); err != nil {
			fields["effectiveTo"] = "must be a date such as 2026-12-31"
		}
	}
	if !decimalText.MatchString(body.Percent) {
		fields["percent"] = "must be a decimal from 0 to 100"
	} else {
		h.Percent = decimal.RequireFromString(body.Percent)
	}
	if !validation(w, r, fields) {
		return
	}
	u, err := a.groups.AddOwnership(r.Context(), groupID, unitID, h)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusCreated, toUnitResponse(u, time.Now().UTC()))
}

type mappingRow struct {
	MainAccount          string `json:"mainAccount"`
	ConsolidationAccount string `json:"consolidationAccount"`
	AccountType          string `json:"accountType"`
	// Role names what a run reads the account for: asset, liability, equity, ic_sales, ic_cost, ic_receivable or ic_payable.
	Role string `json:"role,omitempty"`
}

type mappingBody struct {
	Accounts []mappingRow `json:"accounts"`
	// Version is the stored version of the mapping (set by the service).
	Version int `json:"version,omitempty"`
}

func (a *API) setMapping(w http.ResponseWriter, r *http.Request) {
	if !a.groupsReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	var body mappingBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	rows := make([]domain.AccountMapping, 0, len(body.Accounts))
	for _, m := range body.Accounts {
		rows = append(rows, domain.AccountMapping{MainAccount: m.MainAccount, ConsolidationAccount: m.ConsolidationAccount, Type: m.AccountType, Role: m.Role})
	}
	if err := domain.ValidateMapping(rows); err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	version, err := a.groups.SetMapping(r.Context(), id, rows)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, mappingBody{Accounts: body.Accounts, Version: version})
}

func (a *API) getMapping(w http.ResponseWriter, r *http.Request) {
	if !a.groupsReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	version := 0
	if raw := r.URL.Query().Get("version"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			validation(w, r, map[string]string{"version": "must be a mapping version number, 1 or more"})
			return
		}
		version = n
	}
	rows, got, err := a.groups.GetMapping(r.Context(), id, version)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := mappingBody{Accounts: make([]mappingRow, 0, len(rows)), Version: got}
	for _, m := range rows {
		out.Accounts = append(out.Accounts, mappingRow{MainAccount: m.MainAccount, ConsolidationAccount: m.ConsolidationAccount, AccountType: m.Type, Role: m.Role})
	}
	httpx.JSON(w, http.StatusOK, out)
}

type mappingVersionBody struct {
	Version   int       `json:"version"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	Accounts  int       `json:"accounts"`
}

// listMappingVersions handles GET /groups/{id}/account-map/versions: every stored version of the chart mapping, newest first.
func (a *API) listMappingVersions(w http.ResponseWriter, r *http.Request) {
	if !a.groupsReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	list, err := a.groups.ListMappingVersions(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := struct {
		Items []mappingVersionBody `json:"items"`
	}{Items: make([]mappingVersionBody, 0, len(list))}
	for _, v := range list {
		out.Items = append(out.Items, mappingVersionBody{Version: v.Version, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt.UTC(), Accounts: v.Rows})
	}
	httpx.JSON(w, http.StatusOK, out)
}

type unitPatch struct {
	LedgerID             *string `json:"ledgerId"`
	Currency             *string `json:"currency"`
	FiscalYearStartMonth *int    `json:"fiscalYearStartMonth"`
}

// updateUnit handles PATCH /groups/{id}/units/{unitId}: the ledger a unit is collected from, its currency (until a collection was
// made from it) and its fiscal year.
func (a *API) updateUnit(w http.ResponseWriter, r *http.Request) {
	if !a.groupsReady(w, r) {
		return
	}
	groupID, ok := pathID(w, r, "id", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	unitID, ok := pathID(w, r, "unitId", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	var body unitPatch
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if body.LedgerID == nil && body.Currency == nil && body.FiscalYearStartMonth == nil {
		fields["body"] = "name at least one of ledgerId, currency, fiscalYearStartMonth"
	}
	if body.LedgerID != nil {
		trimmed := strings.TrimSpace(*body.LedgerID)
		if trimmed == "" || len(trimmed) > 64 {
			fields["ledgerId"] = "must be 1 to 64 characters"
		}
		body.LedgerID = &trimmed
	}
	if body.Currency != nil && !currencyCode.MatchString(*body.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if body.FiscalYearStartMonth != nil && (*body.FiscalYearStartMonth < 1 || *body.FiscalYearStartMonth > 12) {
		fields["fiscalYearStartMonth"] = "must be a month from 1 to 12"
	}
	if !validation(w, r, fields) {
		return
	}
	u, err := a.groups.UpdateUnit(r.Context(), groupID, unitID, store.UnitChange{LedgerID: body.LedgerID, Currency: body.Currency, FiscalYearStartMonth: body.FiscalYearStartMonth})
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, toUnitResponse(u, time.Now().UTC()))
}

// removeUnit handles DELETE /groups/{id}/units/{unitId}.
func (a *API) removeUnit(w http.ResponseWriter, r *http.Request) {
	if !a.groupsReady(w, r) {
		return
	}
	groupID, ok := pathID(w, r, "id", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	unitID, ok := pathID(w, r, "unitId", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	if err := a.groups.RemoveUnit(r.Context(), groupID, unitID); err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type termsBody struct {
	InvestmentCost     string `json:"investmentCost"`
	ShareCapital       string `json:"shareCapital"`
	PreAcquisitionRE   string `json:"preAcquisitionRe"`
	HistoricalRate     string `json:"historicalRate"`
	SellerIsSubsidiary bool   `json:"sellerIsSubsidiary"`
	ICNetInvestment    bool   `json:"icNetInvestment"`
	FXGainLossAccount  string `json:"fxGainLossAccount"`
	// Method is full (the default: consolidated line by line, E1 to E6) or equity (an associate or joint venture, up to 50 percent,
	// taken with the equity method and shown in the investments register).
	Method string `json:"method"`
}

type termsResponse struct {
	UnitID string `json:"unitId"`
	termsBody
	UpdatedBy string    `json:"updatedBy"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func toTermsResponse(rec store.UnitTermsRecord) termsResponse {
	t := rec.UnitTerms
	return termsResponse{UnitID: rec.UnitID, UpdatedBy: rec.UpdatedBy, UpdatedAt: rec.UpdatedAt.UTC(), termsBody: termsBody{
		InvestmentCost: t.InvestmentCost.String(), ShareCapital: t.ShareCapital.String(), PreAcquisitionRE: t.PreAcquisitionRE.String(), HistoricalRate: t.HistoricalRate.String(),
		SellerIsSubsidiary: t.SellerIsSubsidiary, ICNetInvestment: t.ICNetInvestment, FXGainLossAccount: t.FXGainLossAccount, Method: methodOf(t.Method)}}
}

func methodOf(m string) string {
	if m == "" {
		return domain.MethodFull
	}
	return m
}

// setUnitTerms handles PUT /groups/{id}/units/{unitId}/terms: the stored investment of the group in the unit (its cost, the share
// capital and retained earnings at acquisition and the rate of that day), which a run cannot read from a trial balance.
func (a *API) setUnitTerms(w http.ResponseWriter, r *http.Request) {
	if !a.groupsReady(w, r) {
		return
	}
	groupID, ok := pathID(w, r, "id", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	unitID, ok := pathID(w, r, "unitId", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	var body termsBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := decimals{}
	t := domain.UnitTerms{
		InvestmentCost: f.get("investmentCost", body.InvestmentCost, false), ShareCapital: f.get("shareCapital", body.ShareCapital, false),
		PreAcquisitionRE: f.get("preAcquisitionRe", body.PreAcquisitionRE, false), HistoricalRate: f.get("historicalRate", body.HistoricalRate, true),
		SellerIsSubsidiary: body.SellerIsSubsidiary, ICNetInvestment: body.ICNetInvestment, FXGainLossAccount: body.FXGainLossAccount,
		Method: methodOf(body.Method),
	}
	if !domain.ValidMethod(body.Method) {
		f["method"] = "must be full or equity"
	}
	if _, bad := f["historicalRate"]; !bad && !t.HistoricalRate.IsPositive() {
		f["historicalRate"] = "must be above zero"
	}
	if t.InvestmentCost.IsNegative() {
		f["investmentCost"] = "must not be negative"
	}
	if t.ShareCapital.IsNegative() {
		f["shareCapital"] = "must not be negative"
	}
	if t.FXGainLossAccount != "" && !accountCode.MatchString(t.FXGainLossAccount) {
		f["fxGainLossAccount"] = "must be 1 to 64 letters, digits or . _ : -"
	}
	if !validation(w, r, f) {
		return
	}
	rec, err := a.groups.SetUnitTerms(r.Context(), groupID, unitID, t)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, toTermsResponse(rec))
}

// listUnitTerms handles GET /groups/{id}/unit-terms.
func (a *API) listUnitTerms(w http.ResponseWriter, r *http.Request) {
	if !a.groupsReady(w, r) {
		return
	}
	groupID, ok := pathID(w, r, "id", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	terms, err := a.groups.GetUnitTerms(r.Context(), groupID)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := struct {
		Items []termsResponse `json:"items"`
	}{Items: make([]termsResponse, 0, len(terms))}
	for _, rec := range terms {
		out.Items = append(out.Items, toTermsResponse(rec))
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].UnitID < out.Items[j].UnitID })
	httpx.JSON(w, http.StatusOK, out)
}

// codeStatus maps the group and collection codes to their HTTP status.
func codeStatus(code string) (int, bool) {
	switch code {
	case domain.CodeGroupNotFound, domain.CodeCollectionGone:
		return http.StatusNotFound, true
	case domain.CodeGroupExists, domain.CodeUnitExists, domain.CodeUnitInUse:
		return http.StatusConflict, true
	case domain.CodeUnitTerms, domain.CodeRunFromCollection:
		return http.StatusUnprocessableEntity, true
	case domain.CodeNoLedger:
		return http.StatusBadGateway, true
	}
	return 0, false
}
