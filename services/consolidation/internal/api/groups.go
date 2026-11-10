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
	SetMapping(ctx context.Context, groupID string, rows []domain.AccountMapping) error
	GetMapping(ctx context.Context, groupID string) ([]domain.AccountMapping, error)
	CreateCollection(ctx context.Context, groupID, period string, lines []domain.CollectLine, inputs any, key string) (store.Collection, bool, error)
	ListCollections(ctx context.Context, groupID string) ([]store.Collection, error)
	GetCollection(ctx context.Context, id string) (store.CollectionDetail, error)
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
	ID             string            `json:"id"`
	LegalEntityID  string            `json:"legalEntityId"`
	LedgerID       string            `json:"ledgerId"`
	Currency       string            `json:"currency"`
	CurrentPercent string            `json:"currentPercent"`
	Ownership      []holdingResponse `json:"ownership"`
}

type groupDetailResponse struct {
	groupResponse
	Units []unitResponse `json:"units"`
}

func toGroupResponse(g domain.Group) groupResponse {
	return groupResponse{ID: g.ID, Code: g.Code, Name: g.Name, Currency: g.Currency, CreatedBy: g.CreatedBy, CreatedAt: g.CreatedAt.UTC()}
}

func toUnitResponse(u domain.Unit, today time.Time) unitResponse {
	out := unitResponse{ID: u.ID, LegalEntityID: u.LegalEntityID, LedgerID: u.LedgerID, Currency: u.Currency,
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
	if !validation(w, r, fields) {
		return
	}
	u, err := a.groups.AddUnit(r.Context(), id, domain.Unit{LegalEntityID: body.LegalEntityID, LedgerID: strings.TrimSpace(body.LedgerID), Currency: body.Currency})
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
}

type mappingBody struct {
	Accounts []mappingRow `json:"accounts"`
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
		rows = append(rows, domain.AccountMapping{MainAccount: m.MainAccount, ConsolidationAccount: m.ConsolidationAccount, Type: m.AccountType})
	}
	if err := domain.ValidateMapping(rows); err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if err := a.groups.SetMapping(r.Context(), id, rows); err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, mappingBody{Accounts: body.Accounts})
}

func (a *API) getMapping(w http.ResponseWriter, r *http.Request) {
	if !a.groupsReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	rows, err := a.groups.GetMapping(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := mappingBody{Accounts: make([]mappingRow, 0, len(rows))}
	for _, m := range rows {
		out.Accounts = append(out.Accounts, mappingRow{MainAccount: m.MainAccount, ConsolidationAccount: m.ConsolidationAccount, AccountType: m.Type})
	}
	httpx.JSON(w, http.StatusOK, out)
}

// codeStatus maps the group and collection codes to their HTTP status.
func codeStatus(code string) (int, bool) {
	switch code {
	case domain.CodeGroupNotFound, domain.CodeCollectionGone:
		return http.StatusNotFound, true
	case domain.CodeGroupExists, domain.CodeUnitExists:
		return http.StatusConflict, true
	case domain.CodeNoLedger:
		return http.StatusBadGateway, true
	}
	return 0, false
}
