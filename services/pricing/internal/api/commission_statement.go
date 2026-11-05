package api

import (
	"bufio"
	"encoding/csv"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	commissionStatementPath = "/v1/pricing/commission/reps/{repId}/statement"
	// maxCommissionStatementJSONLines caps the lines of the JSON statement; the CSV carries every line.
	maxCommissionStatementJSONLines = 5000
)

type commissionStatementTotalView struct {
	Currency string `json:"currency"`
	Lines    int    `json:"lineCount"`
	Earned   string `json:"earnedAmount"`
	Credited string `json:"creditedAmount"`
	Net      string `json:"netAmount"`
}

type commissionStatementView struct {
	LegalEntityID  string                         `json:"legalEntityId"`
	RepID          string                         `json:"repId"`
	Period         string                         `json:"period"`
	LineCount      int                            `json:"lineCount"`
	LinesTruncated bool                           `json:"linesTruncated"`
	Totals         []commissionStatementTotalView `json:"totals"`
	Lines          []commissionTxnView            `json:"lines"`
}

// commissionStatementColumns is the header of the commission statement CSV: rowType is LINE
// for a transaction and TOTAL_EARNED, TOTAL_CREDITED or TOTAL_NET for the footer rows of each
// currency (amount and currency only).
var commissionStatementColumns = []string{"rowType", "period", "repId", "invoiceLineId", "reversesInvoiceLineId", "invoiceDate",
	"customerId", "customerGroup", "itemGroup", "salesGroupId", "basis", "lineNet", "basisAmount", "sharePct", "ratePct", "amount", "currency", "status"}

func commissionStatementTotals(totals []store.CommissionStatementTotal) ([]commissionStatementTotalView, int) {
	out, n := make([]commissionStatementTotalView, 0, len(totals)), 0
	for _, t := range totals {
		out = append(out, commissionStatementTotalView{Currency: t.Currency, Lines: t.Lines, Earned: minor(t.Earned, t.Currency),
			Credited: minor(t.Credited, t.Currency), Net: minor(t.Net, t.Currency)})
		n += t.Lines
	}
	return out, n
}

// commissionStatement is the commission statement of one rep for one invoice month (PRD
// §5.26.15): every commission transaction of the rep (credit notes negative) and, per currency,
// the earned, credited and net totals, which the database sums so they stay exact however many
// lines there are. CSV (format=csv) streams every line as it is read and aborts the connection
// if the read fails after the 200, so a client never keeps a short file that looks complete;
// JSON (default) lists at most maxCommissionStatementJSONLines and flags linesTruncated. Read
// only; only the rep id appears, never a name.
func (a *API) commissionStatement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.CommissionStatementFilter{LegalEntityID: q.Get("legalEntityId"), RepID: chi.URLParam(r, "repId"), PeriodKey: q.Get("period")}
	fields := map[string]string{}
	if !ids.Valid(f.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !domain.ValidRepID(f.RepID) {
		fields["repId"] = domain.RepIDRule
	}
	if !monthKey.MatchString(f.PeriodKey) {
		fields["period"] = "must be a month such as 2027-03"
	}
	format := q.Get("format")
	if format != "" && format != "json" && format != "csv" {
		fields["format"] = "must be json or csv"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	// Row security hides another legal entity's rows, which would read as a quiet month; say why instead.
	if !legalEntityInScope(w, r, f.LegalEntityID) {
		return
	}
	if format == "csv" {
		a.commissionStatementCSV(w, r, f)
		return
	}
	v := commissionStatementView{LegalEntityID: f.LegalEntityID, RepID: f.RepID, Period: f.PeriodKey, Lines: []commissionTxnView{}}
	start := func(totals []store.CommissionStatementTotal) error {
		v.Totals, v.LineCount = commissionStatementTotals(totals)
		v.Lines = make([]commissionTxnView, 0, min(v.LineCount, maxCommissionStatementJSONLines))
		return nil
	}
	line := func(t store.CommissionTxn) error {
		if len(v.Lines) == maxCommissionStatementJSONLines {
			v.LinesTruncated = true
			return errStatementFull
		}
		v.Lines = append(v.Lines, commissionTxnResponse(t))
		return nil
	}
	if err := a.store.StreamCommissionStatement(r.Context(), f, start, line); err != nil && !errors.Is(err, errStatementFull) {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, v)
}

// errStatementFull stops the read once the JSON statement holds one line past its cap.
var errStatementFull = errors.New("commission statement JSON is full")

func (a *API) commissionStatementCSV(w http.ResponseWriter, r *http.Request, f store.CommissionStatementFilter) {
	bw := bufio.NewWriter(w)
	cw := csv.NewWriter(bw)
	flusher, _ := w.(http.Flusher)
	flush := func() error {
		cw.Flush()
		if err := cw.Error(); err != nil {
			return err
		}
		if err := bw.Flush(); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	c := domain.CSVCell
	var totals []commissionStatementTotalView
	started, n := false, 0
	start := func(t []store.CommissionStatementTotal) error {
		totals, _ = commissionStatementTotals(t)
		h := w.Header()
		h.Set("Content-Type", "text/csv; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="commission-statement-`+f.PeriodKey+`.csv"`)
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		started = true
		return cw.Write(commissionStatementColumns)
	}
	line := func(t store.CommissionTxn) error {
		v := commissionTxnResponse(t)
		if err := cw.Write([]string{"LINE", f.PeriodKey, c(v.RepID), c(v.InvoiceLineID), c(v.ReversesInvoiceLineID), v.InvoiceDate, c(v.CustomerID),
			c(v.CustomerGroup), c(v.ItemGroup), v.SalesGroupID, v.Basis, c(v.LineNet), c(v.BasisAmount), c(v.SharePct), c(v.RatePct),
			c(v.Amount), v.Currency, v.Status}); err != nil {
			return err
		}
		if n++; n%exportFlushRows == 0 {
			return flush()
		}
		return nil
	}
	err := a.store.StreamCommissionStatement(r.Context(), f, start, line)
	if err == nil {
		for _, t := range totals {
			for _, row := range [][2]string{{"TOTAL_EARNED", t.Earned}, {"TOTAL_CREDITED", t.Credited}, {"TOTAL_NET", t.Net}} {
				if err = cw.Write([]string{row[0], f.PeriodKey, c(f.RepID), "", "", "", "", "", "", "", "", "", "", "", "", c(row[1]), t.Currency, ""}); err != nil {
					break
				}
			}
		}
	}
	if err == nil {
		err = flush()
	}
	if err == nil {
		return
	}
	if !started {
		httpx.Fail(w, r, err)
		return
	}
	// The status line is sent: abort the connection so the client sees a failed download.
	slog.Error("commission statement failed", "err", errors.Join(errExportStarted, err), "period", f.PeriodKey,
		"rows", n, "correlationId", middleware.GetReqID(r.Context()))
	panic(http.ErrAbortHandler)
}

// registerCommissionStatementRoutes mounts the commission statement (PRD §5.26.15).
func (a *API) registerCommissionStatementRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyCommissionView)).Get(commissionStatementPath, a.commissionStatement)
}
