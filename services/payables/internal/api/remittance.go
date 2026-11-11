package api

import (
	"encoding/csv"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

// getRemittance serves GET /v1/payables/payment-runs/{id}/remittances/{vendorId} (issue #528): the remittance
// advice of one vendor in a completed run, as JSON or, with ?format=csv, as a CSV download. It lists each
// payment to the vendor and the invoices it settled with the discount taken, the tax withheld and the amount
// paid. It carries no bank details, so it needs the read duty only.
func (a *API) getRemittance(w http.ResponseWriter, r *http.Request) {
	id, ok := a.runID(w, r)
	if !ok {
		return
	}
	vendor := chi.URLParam(r, "vendorId")
	format := r.URL.Query().Get("format")
	fields := map[string]string{}
	if !ids.Valid(vendor) {
		fields["vendorId"] = "must be a UUID"
	}
	if format != "" && format != "json" && format != "csv" {
		fields["format"] = "must be json or csv"
	}
	if len(fields) > 0 {
		a.invalid(w, r, fields)
		return
	}
	ctx := r.Context()
	run, err := a.runs.store.GetRun(ctx, id)
	switch {
	case errors.Is(err, store.ErrRunNotFound):
		a.problem(w, r, http.StatusNotFound, codeNotFound, "payment run not found")
		return
	case err != nil:
		a.fault(w, r, "payment run get failed", err)
		return
	case run.Status != domain.RunCompleted:
		a.problem(w, r, http.StatusConflict, domain.CodeRunNotCompleted, "a remittance advice is available once the payment run is completed; the run is "+run.Status)
		return
	}
	remittance, err := a.runs.store.RunRemittance(ctx, id, vendor)
	switch {
	case errors.Is(err, store.ErrRunNotFound):
		a.problem(w, r, http.StatusNotFound, codeNotFound, "payment run not found")
		return
	case errors.Is(err, store.ErrNotFound):
		a.problem(w, r, http.StatusNotFound, codeNotFound, "the payment run has no payment to this vendor")
		return
	case err != nil:
		a.fault(w, r, "remittance advice failed", err)
		return
	}
	caller, _ := tenant.FromContext(ctx)
	slog.Info("remittance advice read", "runId", id, "vendorId", vendor, "format", format, "userId", caller.UserID, "correlationId", middleware.GetReqID(ctx))
	if format == "csv" {
		writeRemittanceCSV(w, remittance)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, remittance)
}

// plainNumber matches a decimal a spreadsheet reads as a number; it is written as is.
var plainNumber = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// csvSafe neutralizes a cell a spreadsheet would run as a formula (OWASP CSV injection): one starting with
// =, +, -, @, tab or carriage return gets a leading apostrophe, unless it is a plain number.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) && !plainNumber.MatchString(s) {
		return "'" + s
	}
	return s
}

func writeRemittanceCSV(w http.ResponseWriter, rem store.Remittance) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="remittance-advice-`+rem.RunID+`-`+rem.VendorID+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "\xEF\xBB\xBF")
	cw := csv.NewWriter(w)
	row := func(cells ...string) {
		for i, v := range cells {
			cells[i] = csvSafe(v)
		}
		_ = cw.Write(cells)
	}
	row("Payment reference", "Payment date", "Currency", "Invoice", "Invoice date", "Amount open", "Discount", "Tax withheld", "Paid")
	for _, p := range rem.Payments {
		for _, l := range p.Lines {
			row(p.EndToEndID, rem.PaymentDate, p.Currency, l.InvoiceNumber, l.InvoiceDate, l.OpenAmount.String(), l.Discount.String(), l.Withheld.String(), l.Paid.String())
		}
		row(p.EndToEndID, rem.PaymentDate, p.Currency, "Total", "", "", "", "", p.Amount.String())
	}
	cw.Flush()
}
