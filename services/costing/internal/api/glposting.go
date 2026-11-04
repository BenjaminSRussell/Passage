package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costing/internal/store"
)

const (
	dutyPostingProfileWrite = "costing.posting-profile.write"
	dutyGLPost              = "costing.gl.post"
	dutyCloseRun            = "costing.close.run"
	CodeGLUnavailable       = "CST-GL-003"
	CodeGLRefused           = "CST-GL-007"
	CodePPVStandard         = "CST-PPV-001"
)

var accountPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)

// WithLedger books the PPV and close vouchers in the general ledger through ledger and reads its balances.
func (a *API) WithLedger(ledger ledgerclient.Ledger) *API {
	a.ledger = ledger
	return a
}

func (a *API) glReady(w http.ResponseWriter, r *http.Request) bool {
	if a.ledger == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeGLUnavailable, "the general ledger is not configured for this service"))
		return false
	}
	return true
}

func (a *API) failGL(w http.ResponseWriter, r *http.Request, op string, err error) {
	var le *ledgerclient.Error
	if errors.As(err, &le) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeGLRefused, "the general ledger refused the request ("+le.Code+")"))
		return
	}
	a.fail(w, r, op, err)
}

func entityInScope(w http.ResponseWriter, r *http.Request, raw string) (string, bool) {
	entity := strings.ToLower(raw)
	if !ids.Valid(entity) {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return "", false
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, entity) {
		httpx.Fail(w, r, outOfScope())
		return "", false
	}
	return entity, true
}

type postingProfileRequest struct {
	LegalEntityID     string `json:"legalEntityId"`
	LedgerID          string `json:"ledgerId"`
	InventoryAccount  string `json:"inventoryAccount"`
	GRIRAccount       string `json:"grirAccount"`
	APAccount         string `json:"apAccount"`
	PPVAccount        string `json:"ppvAccount"`
	AdjustmentAccount string `json:"adjustmentAccount"`
}

func (a *API) savePostingProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || !a.glReady(w, r) {
		return
	}
	var req postingProfileRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LedgerID) {
		fields["ledgerId"] = "must be a UUID"
	}
	for name, v := range map[string]string{"inventoryAccount": req.InventoryAccount, "grirAccount": req.GRIRAccount, "apAccount": req.APAccount,
		"ppvAccount": req.PPVAccount, "adjustmentAccount": req.AdjustmentAccount} {
		if !accountPattern.MatchString(strings.TrimSpace(v)) {
			fields[name] = "must be an account number of at most 40 characters"
		}
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	entity, ok := entityInScope(w, r, req.LegalEntityID)
	if !ok {
		return
	}
	if err := store.CheckLedgerOwner(r.Context(), a.ledger, entity, strings.ToLower(req.LedgerID)); err != nil {
		a.fail(w, r, "check ledger owner", err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	saved, err := a.store.SavePostingProfile(r.Context(), store.PostingProfile{LegalEntityID: entity, GLLedgerID: strings.ToLower(req.LedgerID),
		InventoryAccount: strings.TrimSpace(req.InventoryAccount), GRIRAccount: strings.TrimSpace(req.GRIRAccount), APAccount: strings.TrimSpace(req.APAccount),
		PPVAccount: strings.TrimSpace(req.PPVAccount), AdjustmentAccount: strings.TrimSpace(req.AdjustmentAccount)}, p.UserID)
	if err != nil {
		a.fail(w, r, "save posting profile", err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

func (a *API) getPostingProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	entity, ok := entityInScope(w, r, r.URL.Query().Get("legalEntityId"))
	if !ok {
		return
	}
	profile, err := a.store.GetPostingProfile(r.Context(), entity)
	if err != nil {
		a.fail(w, r, "get posting profile", err)
		return
	}
	httpx.JSON(w, http.StatusOK, profile)
}

type legalEntityRequest struct {
	LegalEntityID string `json:"legalEntityId"`
}

// postGL books the unposted PPV cost transactions of the legal entity. The request names only the entity: amounts come from the stored
// cost transactions and accounts and ledger from the stored posting profile.
func (a *API) postGL(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || !a.glReady(w, r) {
		return
	}
	var req legalEntityRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	entity, ok := entityInScope(w, r, req.LegalEntityID)
	if !ok {
		return
	}
	res, err := a.store.PostCostVouchers(r.Context(), a.ledger, store.GLPostRequest{LegalEntityID: entity})
	if err != nil {
		a.failGL(w, r, "post cost vouchers", err)
		return
	}
	a.vouchersPosted.Add(int64(len(res.Booked)))
	httpx.JSON(w, http.StatusOK, res)
}

type closeRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	PeriodEnd     string `json:"periodEnd"`
}

func (a *API) runClose(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || !a.glReady(w, r) {
		return
	}
	var req closeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	parseDate(fields, "periodEnd", req.PeriodEnd)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	entity, ok := entityInScope(w, r, req.LegalEntityID)
	if !ok {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	c, err := a.store.RunClose(r.Context(), a.ledger, store.CloseRequest{LegalEntityID: entity, PeriodEnd: req.PeriodEnd}, p.UserID)
	if err != nil {
		a.failGL(w, r, "inventory close", err)
		return
	}
	if !c.Replayed {
		a.closesCompleted.Add(1)
	}
	httpx.JSON(w, http.StatusOK, c)
}

func (a *API) listCloses(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	entity, ok := entityInScope(w, r, r.URL.Query().Get("legalEntityId"))
	if !ok {
		return
	}
	items, err := a.store.ListCloses(r.Context(), entity)
	if err != nil {
		a.fail(w, r, "list closes", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

type ledgerTieOutResponse struct {
	LegalEntityID   string         `json:"legalEntityId"`
	AsOf            string         `json:"asOf"`
	Currency        string         `json:"currency"`
	Account         string         `json:"account"`
	Inventory       string         `json:"inventory"`
	Ledger          string         `json:"ledger"`
	Difference      string         `json:"difference"`
	BookedByCosting string         `json:"bookedByCosting"`
	UnpostedPPV     int            `json:"unpostedPpvVouchers"`
	ParkedVouchers  int            `json:"parkedVouchers"`
	ParkedReasons   map[string]int `json:"parkedReasons"`
	Ties            bool           `json:"ties"`
}

// ledgerTieOut ties the inventory value report to the ledger balance of the profile's inventory account, and lists the vouchers
// that are not confirmed (unposted or parked), which keep the report from tying.
func (a *API) ledgerTieOut(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || !a.glReady(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	parseDate(fields, "asOf", q.Get("asOf"))
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	entity, ok := entityInScope(w, r, q.Get("legalEntityId"))
	if !ok {
		return
	}
	rec, err := a.store.ReconcileInventory(r.Context(), a.ledger, entity, q.Get("asOf"))
	if err != nil {
		a.failGL(w, r, "inventory ledger tie-out", err)
		return
	}
	scale := money.MinorUnits(rec.Currency)
	httpx.JSON(w, http.StatusOK, ledgerTieOutResponse{LegalEntityID: rec.LegalEntityID, AsOf: rec.AsOf, Currency: rec.Currency, Account: rec.Account,
		Inventory: formatAmount(rec.Inventory, scale), Ledger: formatAmount(rec.Ledger, scale), Difference: formatAmount(rec.Difference, scale),
		BookedByCosting: formatAmount(rec.BookedByCosting, scale), UnpostedPPV: rec.Unposted, ParkedVouchers: rec.Parked, ParkedReasons: rec.ParkedReasons, Ties: rec.Ties})
}
