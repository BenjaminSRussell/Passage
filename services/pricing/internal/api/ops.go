package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

const (
	expirePath           = recordsPath + ":expire"
	workspacePath        = "/v1/pricing/workspace"
	settlementExportPath = "/v1/pricing/commission/settlement-export"
	// expiryLeadDays is how far past today (UTC) the expiry job may run: entities east of
	// UTC start their day earlier. A later asOf would end validity that is still running.
	expiryLeadDays = 1
	// maxSettlementDays bounds one settlement export to a year of invoices.
	maxSettlementDays = 366
)

type expireRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	AsOf          string `json:"asOf"`
}

type expireView struct {
	LegalEntityID string   `json:"legalEntityId"`
	AsOf          string   `json:"asOf"`
	Expired       int      `json:"expired"`
	RecordIDs     []string `json:"recordIds"`
}

// expireRecords is the record expiry job (BR-26.9, §5.26.16, 00:10 entity-local) exposed for
// the scheduler: ACTIVE records of the legal entity whose validity ended before asOf become
// EXPIRED. Running it again for the same day expires nothing more.
func (a *API) expireRecords(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req expireRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	asOf, err := time.Parse(dateLayout, req.AsOf)
	switch {
	case err != nil:
		fields["asOf"] = "must be a date in YYYY-MM-DD form"
	case asOf.After(time.Now().UTC().AddDate(0, 0, expiryLeadDays)):
		fields["asOf"] = "must not be later than tomorrow; expiry never ends validity that is still running"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	expired, err := a.store.ExpireRecords(r.Context(), req.LegalEntityID, asOf)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := expireView{LegalEntityID: req.LegalEntityID, AsOf: req.AsOf, Expired: len(expired), RecordIDs: make([]string, len(expired))}
	for i, e := range expired {
		out.RecordIDs[i] = e.ID
	}
	a.recordsExpired.Add(int64(len(expired)))
	slog.Info("condition records expired", "legalEntityId", req.LegalEntityID, "asOf", req.AsOf, "expired", len(expired),
		"correlationId", middleware.GetReqID(r.Context()))
	httpx.JSON(w, http.StatusOK, out)
}

type accrualTotalView struct {
	PartyRole string `json:"partyRole"`
	Currency  string `json:"currency"`
	Amount    string `json:"amount"`
}

type workspaceView struct {
	LegalEntityID          string             `json:"legalEntityId,omitempty"`
	AsOf                   string             `json:"asOf"`
	Month                  string             `json:"month"`
	ActiveRecords          int                `json:"activeRecords"`
	DraftRecords           int                `json:"draftRecords"`
	ExpiringIn30Days       int                `json:"expiringIn30Days"`
	JournalsToPost         int                `json:"journalsToPost"`
	LiveCampaigns          int                `json:"liveCampaigns"`
	ClaimsAwaitingApproval int                `json:"claimsAwaitingApproval"`
	AccrualsThisMonth      []accrualTotalView `json:"accrualsThisMonth"`
}

// workspace feeds the Pricing workspace tiles (§5.26.6): expiring in 30 days, draft
// journals, campaigns and accrual this period, plus the record and claim counts. Without
// legalEntityId it counts every legal entity in the caller's scope. asOf defaults to today (UTC).
func (a *API) workspace(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	le := q.Get("legalEntityId")
	fields := map[string]string{}
	if le != "" && !ids.Valid(le) {
		fields["legalEntityId"] = "must be a UUID"
	}
	asOf := time.Now().UTC().Truncate(24 * time.Hour)
	if v := q.Get("asOf"); v != "" {
		d, err := time.Parse(dateLayout, v)
		if err != nil {
			fields["asOf"] = "must be a date in YYYY-MM-DD form"
		}
		asOf = d
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	s, err := a.store.WorkspaceSummary(r.Context(), le, asOf)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := workspaceView{LegalEntityID: le, AsOf: asOf.Format(dateLayout), Month: s.Month, ActiveRecords: s.ActiveRecords,
		DraftRecords: s.DraftRecords, ExpiringIn30Days: s.ExpiringIn30Days, JournalsToPost: s.JournalsToPost,
		LiveCampaigns: s.LiveCampaigns, ClaimsAwaitingApproval: s.ClaimsAwaitingApproval,
		AccrualsThisMonth: make([]accrualTotalView, len(s.AccrualsThisMonth))}
	for i, t := range s.AccrualsThisMonth {
		out.AccrualsThisMonth[i] = accrualTotalView{PartyRole: t.PartyRole, Currency: t.Currency, Amount: t.Amount.String()}
	}
	httpx.JSON(w, http.StatusOK, out)
}

// settlementExport writes the commission settlement CSV per rep and currency for invoices
// dated from..to (FR-26.18, §5.26.15 "CSV export to payroll/AP"). target is AP (default, the
// commission_settlement_target default) or PAYROLL.
func (a *API) settlementExport(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	le := q.Get("legalEntityId")
	fields := map[string]string{}
	if !ids.Valid(le) {
		fields["legalEntityId"] = "must be a UUID"
	}
	from, errFrom := time.Parse(dateLayout, q.Get("from"))
	if errFrom != nil {
		fields["from"] = "must be a date in YYYY-MM-DD form"
	}
	to, errTo := time.Parse(dateLayout, q.Get("to"))
	if errTo != nil {
		fields["to"] = "must be a date in YYYY-MM-DD form"
	}
	if errFrom == nil && errTo == nil {
		switch {
		case to.Before(from):
			fields["to"] = "must not be before from"
		case to.Sub(from) >= maxSettlementDays*24*time.Hour:
			fields["to"] = "an export covers at most 366 days"
		}
	}
	target := q.Get("target")
	if target == "" {
		target = domain.SettlementTargetAP
	}
	if !domain.ValidSettlementTarget(target) {
		fields["target"] = "must be AP or PAYROLL"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rows, err := a.store.SettlementTotals(r.Context(), le, from, to)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	raw, err := domain.SettlementCSV(target, q.Get("from"), q.Get("to"), rows)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="commission-settlement-`+target+`-`+q.Get("from")+`-`+q.Get("to")+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// registerOpsRoutes mounts the record expiry job, the workspace tiles, the commission
// settlement export and the CSV journal import.
func (a *API) registerOpsRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyMaintain)).Post(expirePath, a.expireRecords)
	r.With(authz.RequireDuty(dutyView)).Get(workspacePath, a.workspace)
	r.With(authz.RequireDuty(dutyCommissionView)).Get(settlementExportPath, a.settlementExport)
	r.With(authz.RequireDuty(dutyJournalPost)).Post(journalsImportPath, a.importJournalCSV)
}
