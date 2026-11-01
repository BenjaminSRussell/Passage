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
	"github.com/BenjaminSRussell/passage/services/eam/internal/store"
)

const CodeGLUnavailable = "EAM-GL-003"

var (
	accountCode = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)
	periodCode  = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)
	profileType = map[string]bool{"ALL": true, "PREVENTIVE": true, "CORRECTIVE": true, "INSPECTION": true, "EMERGENCY": true, "CAPITAL": true, "ROUND": true, "REFURBISHMENT": true}
)

// WithLedger books work order costs in the general ledger through ledger and reconciles them to its balances.
func (h *Handler) WithLedger(ledger ledgerclient.Ledger) *Handler {
	h.ledger = ledger
	return h
}

func (h *Handler) glReady(w http.ResponseWriter, r *http.Request) bool {
	if h.ledger == nil {
		fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeGLUnavailable, "the general ledger is not configured for this service"))
		return false
	}
	return true
}

func glScope(r *http.Request, entity string) (string, error) {
	p, _ := tenant.FromContext(r.Context())
	if entity == "" {
		entity = p.LegalEntityID
	}
	if !ids.Valid(entity) || !tenant.InLegalEntityScope(p, entity) {
		return "", invalid("legalEntityId must be a legal entity of the caller")
	}
	return entity, nil
}

type profileIn struct {
	LegalEntityID string `json:"legalEntityId"`
	WorkOrderType string `json:"workOrderType"`
	PartsExpense  string `json:"partsExpenseAccount"`
	LaborExpense  string `json:"laborExpenseAccount"`
	PartsOffset   string `json:"partsOffsetAccount"`
	LaborOffset   string `json:"laborOffsetAccount"`
}

func (h *Handler) listPostingProfiles(w http.ResponseWriter, r *http.Request) {
	entity, err := glScope(r, r.URL.Query().Get("legalEntityId"))
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.ListPostingProfiles(r.Context(), entity)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out, "placeholder": store.PlaceholderProfile})
}

func (h *Handler) savePostingProfile(w http.ResponseWriter, r *http.Request) {
	var in profileIn
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	entity, err := glScope(r, in.LegalEntityID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if in.WorkOrderType == "" {
		in.WorkOrderType = "ALL"
	}
	if !profileType[in.WorkOrderType] {
		fail(w, r, invalid("workOrderType must be ALL or a work order type"))
		return
	}
	for name, v := range map[string]string{"partsExpenseAccount": in.PartsExpense, "laborExpenseAccount": in.LaborExpense, "partsOffsetAccount": in.PartsOffset, "laborOffsetAccount": in.LaborOffset} {
		if !accountCode.MatchString(strings.TrimSpace(v)) {
			fail(w, r, invalid(name+" must be an account number of at most 40 characters"))
			return
		}
	}
	version := 0
	if r.Header.Get("If-Match") != "" {
		if version, err = ifMatch(r); err != nil {
			fail(w, r, err)
			return
		}
	}
	p, _ := tenant.FromContext(r.Context())
	saved, created, err := h.store.SavePostingProfile(r.Context(), store.PostingProfile{LegalEntityID: entity, WorkOrderType: in.WorkOrderType, PartsExpense: in.PartsExpense,
		LaborExpense: in.LaborExpense, PartsOffset: in.PartsOffset, LaborOffset: in.LaborOffset}, version, p.UserID)
	if err != nil {
		fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, saved)
}

type glPostIn struct {
	LegalEntityID string `json:"legalEntityId"`
	LedgerID      string `json:"ledgerId"`
	Currency      string `json:"currency"`
	SiteID        string `json:"siteId"`
	WorkOrderID   string `json:"workOrderId"`
}

func (h *Handler) postGL(w http.ResponseWriter, r *http.Request) {
	if !h.glReady(w, r) {
		return
	}
	var in glPostIn
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	entity, err := glScope(r, in.LegalEntityID)
	switch {
	case err != nil:
		fail(w, r, err)
		return
	case !ids.Valid(in.LedgerID):
		fail(w, r, invalid("ledgerId must be a UUID"))
		return
	case !currencyCode.MatchString(in.Currency):
		fail(w, r, invalid("currency must be a 3-letter currency code"))
		return
	case in.SiteID != "" && !ids.Valid(in.SiteID), in.WorkOrderID != "" && !ids.Valid(in.WorkOrderID):
		fail(w, r, invalid("siteId and workOrderId must be UUIDs"))
		return
	}
	res, err := h.store.PostWorkOrderCosts(r.Context(), h.ledger, store.GLPostRequest{LegalEntityID: entity, LedgerID: in.LedgerID, Currency: in.Currency, SiteID: in.SiteID, WorkOrderID: in.WorkOrderID})
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

type glAccountOut struct {
	Account    string `json:"account"`
	Subledger  string `json:"subledger"`
	GL         string `json:"gl"`
	Difference string `json:"difference"`
}

type glReconciliationOut struct {
	Currency   string         `json:"currency"`
	Reconciled bool           `json:"reconciled"`
	Accounts   []glAccountOut `json:"accounts"`
}

func (h *Handler) glReconciliation(w http.ResponseWriter, r *http.Request) {
	if !h.glReady(w, r) {
		return
	}
	q := r.URL.Query()
	entity, err := glScope(r, q.Get("legalEntityId"))
	switch {
	case err != nil:
		fail(w, r, err)
		return
	case !ids.Valid(q.Get("ledgerId")):
		fail(w, r, invalid("ledgerId must be a UUID"))
		return
	case !currencyCode.MatchString(q.Get("currency")):
		fail(w, r, invalid("currency must be a 3-letter currency code"))
		return
	case q.Get("periodTo") != "" && !periodCode.MatchString(q.Get("periodTo")):
		fail(w, r, invalid("periodTo must be a period as YYYY-MM"))
		return
	}
	rec, err := h.store.ReconcileGL(r.Context(), h.ledger, store.GLReconcileRequest{LegalEntityID: entity, LedgerID: q.Get("ledgerId"), Currency: q.Get("currency"), PeriodTo: q.Get("periodTo")})
	var le *ledgerclient.Error
	if errors.As(err, &le) {
		fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeGLUnavailable, "the general ledger refused the read ("+le.Code+")"))
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	out := glReconciliationOut{Currency: rec.Currency, Reconciled: rec.Reconciled, Accounts: make([]glAccountOut, 0, len(rec.Accounts))}
	for _, a := range rec.Accounts {
		mu := money.MinorUnits(rec.Currency)
		out.Accounts = append(out.Accounts, glAccountOut{Account: a.Account, Subledger: a.Subledger.StringFixed(mu), GL: a.GL.StringFixed(mu), Difference: a.Difference.StringFixed(mu)})
	}
	httpx.JSON(w, http.StatusOK, out)
}
