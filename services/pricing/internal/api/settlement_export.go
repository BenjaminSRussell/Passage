package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	settlementExportsPath    = "/v1/pricing/commission/settlement-exports"
	settlementExportPathByID = "/v1/pricing/commission/settlement-exports/{id}"
	settlementExportFilePath = "/v1/pricing/commission/settlement-exports/{id}/file"
	settlementExportVoidPath = "/v1/pricing/commission/settlement-exports/{id}:void"
	maxVoidReason            = 500
)

// settlementPeriod parses from and to (YYYY-MM-DD, to not before from, at most 366 days) and
// writes what is wrong into fields.
func settlementPeriod(fields map[string]string, fromText, toText string) (time.Time, time.Time) {
	from, errFrom := time.Parse(dateLayout, fromText)
	if errFrom != nil {
		fields["from"] = "must be a date in YYYY-MM-DD form"
	}
	to, errTo := time.Parse(dateLayout, toText)
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
	return from, to
}

type settlementExportRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	From          string `json:"from"`
	To            string `json:"to"`
	Target        string `json:"target"`
}

type exportTotalView struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

type exportLineView struct {
	RepID        string `json:"repId"`
	Currency     string `json:"currency"`
	Amount       string `json:"amount"`
	Transactions int    `json:"transactions"`
}

type settlementExportView struct {
	ID            string            `json:"id"`
	LegalEntityID string            `json:"legalEntityId"`
	Target        string            `json:"target"`
	From          string            `json:"from"`
	To            string            `json:"to"`
	Status        string            `json:"status"`
	LineCount     int               `json:"lineCount"`
	Totals        []exportTotalView `json:"totals"`
	CSVSHA256     string            `json:"csvSha256"`
	ExportedBy    string            `json:"exportedBy"`
	VoidReason    string            `json:"voidReason"`
	VoidedBy      string            `json:"voidedBy"`
	Version       int               `json:"version"`
	CreatedAt     string            `json:"createdAt"`
	Lines         []exportLineView  `json:"lines,omitempty"`
}

func settlementExportResponse(e domain.SettlementExport, withLines bool) settlementExportView {
	totals := domain.ExportTotals(e.Lines)
	out := settlementExportView{ID: e.ID, LegalEntityID: e.LegalEntityID, Target: e.Target, From: e.From, To: e.To, Status: e.Status,
		LineCount: len(e.Lines), Totals: make([]exportTotalView, len(totals)), CSVSHA256: e.CSVSHA256, ExportedBy: e.ExportedBy,
		VoidReason: e.VoidReason, VoidedBy: e.VoidedBy, Version: e.Version, CreatedAt: e.CreatedAt}
	for i, t := range totals {
		out.Totals[i] = exportTotalView{Currency: t.Currency, Amount: t.Amount.StringFixed(money.MinorUnits(t.Currency))}
	}
	if withLines {
		out.Lines = make([]exportLineView, len(e.Lines))
		for i, l := range e.Lines {
			out.Lines[i] = exportLineView{RepID: l.RepID, Currency: l.Currency, Amount: l.Amount.String(), Transactions: l.Transactions}
		}
	}
	return out
}

func exportID(w http.ResponseWriter, r *http.Request) (string, bool) {
	return uuidParam(w, r, domain.CodeExportNotFound, "settlement export")
}

// createSettlementExport records the hand-over of one period's commission to AP or payroll
// (FR-26.18). The rows are frozen on the record; the file is fetched from it.
func (a *API) createSettlementExport(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req settlementExportRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	le, ok := ids.Canonical(req.LegalEntityID)
	if !ok {
		fields["legalEntityId"] = "must be a UUID"
	}
	req.LegalEntityID = le
	from, to := settlementPeriod(fields, req.From, req.To)
	if req.Target == "" {
		req.Target = domain.SettlementTargetAP
	}
	if !domain.ValidSettlementTarget(req.Target) {
		fields["target"] = "must be AP or PAYROLL"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !legalEntityInScope(w, r, req.LegalEntityID) {
		return
	}
	e, err := a.store.CreateSettlementExport(r.Context(), store.SettlementExportInput{LegalEntityID: req.LegalEntityID,
		Target: req.Target, From: from, To: to}, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, settlementExportResponse(e, true))
}

func (a *API) listSettlementExports(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.SettlementExportFilter{LegalEntityID: q.Get("legalEntityId"), Target: q.Get("target"), Status: q.Get("status")}
	fields := map[string]string{}
	if f.LegalEntityID != "" {
		le, ok := ids.Canonical(f.LegalEntityID)
		if !ok {
			fields["legalEntityId"] = "must be a UUID"
		}
		f.LegalEntityID = le
	}
	if f.Target != "" && !domain.ValidSettlementTarget(f.Target) {
		fields["target"] = "must be AP or PAYROLL"
	}
	if f.Status != "" && f.Status != domain.ExportIssued && f.Status != domain.ExportVoided {
		fields["status"] = "must be ISSUED or VOIDED"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rows, err := a.store.ListSettlementExports(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]settlementExportView, len(rows))
	for i, e := range rows {
		items[i] = settlementExportResponse(e, false)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getSettlementExport(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := exportID(w, r)
	if !ok {
		return
	}
	e, err := a.store.GetSettlementExport(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, settlementExportResponse(e, true))
}

// settlementExportFile writes the CSV of a recorded export from its frozen rows, so a second
// download is byte for byte the first one even after more commission was posted. The rebuilt
// file is checked against the digest recorded when it was handed over: if the two differ (the
// CSV layout changed since), the service refuses rather than serve a different file under the
// record's name.
func (a *API) settlementExportFile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := exportID(w, r)
	if !ok {
		return
	}
	e, err := a.store.GetSettlementExport(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	raw, err := domain.SettlementCSV(e.Target, e.From, e.To, e.Lines)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if digest := domain.CSVDigest(raw); digest != e.CSVSHA256 {
		slog.Error("settlement export file does not match its recorded digest", "exportId", e.ID, "recorded", e.CSVSHA256, "rebuilt", digest, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusInternalServerError, domain.CodeExportDigest, "the file of settlement export "+e.ID+" no longer matches the one handed over"))
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="commission-settlement-`+e.Target+`-`+e.From+`-`+e.To+`.csv"`)
	w.Header().Set("X-Content-SHA256", e.CSVSHA256)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// voidSettlementExport voids an ISSUED export with a reason so its period can be exported again.
func (a *API) voidSettlementExport(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := exportID(w, r)
	if !ok {
		return
	}
	var req versionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "is required and must be at least 1"
	}
	if n := utf8.RuneCountInString(req.Reason); n < 1 || n > maxVoidReason {
		fields["reason"] = "is required and must be 1 to 500 characters"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	e, err := a.store.VoidSettlementExport(r.Context(), id, req.Version, req.Reason, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, settlementExportResponse(e, true))
}

// registerSettlementExportRoutes mounts the recorded settlement exports (FR-26.18): record and
// void need CommissionMaintain, reads and the file need CommissionView.
func (a *API) registerSettlementExportRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyCommissionMaintain)).Post(settlementExportsPath, a.createSettlementExport)
	r.With(authz.RequireDuty(dutyCommissionView)).Get(settlementExportsPath, a.listSettlementExports)
	r.With(authz.RequireDuty(dutyCommissionView)).Get(settlementExportPathByID, a.getSettlementExport)
	r.With(authz.RequireDuty(dutyCommissionView)).Get(settlementExportFilePath, a.settlementExportFile)
	r.With(authz.RequireDuty(dutyCommissionMaintain)).Post(settlementExportVoidPath, a.voidSettlementExport)
}
