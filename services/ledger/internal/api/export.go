package api

import (
	"encoding/csv"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

// CSV exports (PRD 2.11 exports: chart of accounts, trial balance, ledger transactions). Each is the
// ordinary GET of its list with ?format=csv: same duty, same filters and the same validation, one file
// in place of one page. The file is UTF-8 with a byte order mark so a spreadsheet opens umlauts right,
// and text cells that start like a formula are prefixed with an apostrophe (CSV injection).
const (
	formatCSV = "csv"
	// maxExportRows bounds one export file; a longer result is cut at a page boundary and says so in
	// X-Passage-Export-Truncated (and, for lines, X-Passage-Export-Next-Cursor to continue from).
	maxExportRows = 50000

	headerExportTruncated = "X-Passage-Export-Truncated"
	headerExportNext      = "X-Passage-Export-Next-Cursor"
)

// exportFormat reads ?format=. It is "" for the JSON default and formatCSV for a CSV export; anything
// else is a validation problem named in fields.
func exportFormat(q map[string][]string, fields map[string]string) string {
	v := ""
	if vals := q["format"]; len(vals) > 0 {
		v = vals[0]
	}
	switch v {
	case "", "json":
		return ""
	case formatCSV:
		return formatCSV
	}
	fields["format"] = "must be json or csv"
	return ""
}

// plainNumber matches a decimal a spreadsheet reads as a number (amounts, quantities, counts): it is
// written as is, so a negative amount stays a number.
var plainNumber = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// csvSafe neutralizes a cell that a spreadsheet would run as a formula: one starting with =, +, -, @,
// tab or carriage return gets a leading apostrophe (OWASP CSV injection), unless it is a plain number.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) && !plainNumber.MatchString(s) {
		return "'" + s
	}
	return s
}

// csvStream writes a CSV download straight to the response: headers, byte order mark, then one record
// at a time (every cell through csvSafe), so an export never holds the file in memory.
type csvStream struct {
	cw *csv.Writer
}

func startCSV(w http.ResponseWriter, filename string, extra map[string]string) *csvStream {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	for k, v := range extra {
		w.Header().Set(k, v)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "\xEF\xBB\xBF")
	return &csvStream{cw: csv.NewWriter(w)}
}

func (c *csvStream) row(cells ...string) {
	for i, v := range cells {
		cells[i] = csvSafe(v)
	}
	_ = c.cw.Write(cells)
}

func (c *csvStream) end() {
	c.cw.Flush()
}

// abortExport ends a download whose headers are already sent: the failure is logged and the
// connection is cut, so the client sees a failed download rather than a short file that looks whole.
func abortExport(r *http.Request, err error) {
	slog.Error("ledger export failed mid-stream", "err", err, "path", r.URL.Path, "correlationId", middleware.GetReqID(r.Context()))
	panic(http.ErrAbortHandler)
}

// exportAccounts writes the chart of accounts (from the cursor) as CSV. A chart is small (thousands of
// accounts), so its accounts are read before the first byte to know whether the file is cut.
func (a *API) exportAccounts(w http.ResponseWriter, r *http.Request, chart, after string) {
	var accounts []store.Account
	for len(accounts) <= maxExportRows {
		items, err := a.store.ListAccounts(r.Context(), chart, after, maxAccountPage)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		accounts = append(accounts, items...)
		if len(items) < maxAccountPage {
			break
		}
		after = items[len(items)-1].Number
	}
	extra := map[string]string{}
	if len(accounts) > maxExportRows {
		accounts = accounts[:maxExportRows]
		extra[headerExportTruncated] = "true"
		extra[headerExportNext] = accounts[maxExportRows-1].Number
	}
	out := startCSV(w, "chart-"+chart+"-accounts.csv", extra)
	out.row("chart", "account_number", "name", "account_type", "is_postable", "suspended_from", "reconciliation_account_type")
	for _, acct := range accounts {
		sus := ""
		if acct.SuspendedFrom != nil {
			sus = *acct.SuspendedFrom
		}
		out.row(acct.ChartCode, acct.Number, acct.Name, acct.Type, strconv.FormatBool(acct.Postable), sus, acct.ReconType)
	}
	out.end()
}

// quantityCell renders per-unit totals as "KG=12.500000;PC=3.000000" (net per unit; units are never added).
func quantityCell(qs []store.QuantityTotal) string {
	parts := make([]string, len(qs))
	for i, q := range qs {
		parts[i] = q.Unit + "=" + q.Net
	}
	return strings.Join(parts, ";")
}

// exportTrialBalance writes the trial balance rows and a closing TOTAL row. The rows are the ones the
// JSON read returns (one per account, or at most 10,000 grouped rows: a larger grouping is refused
// with 422 before this), streamed out without a second copy; the totals cover every row, so the
// file is never cut.
func exportTrialBalance(w http.ResponseWriter, tb store.TrialBalance) {
	head := []string{"account", "name"}
	for _, g := range tb.GroupBy {
		head = append(head, "dim."+g)
	}
	head = append(head, "debit", "credit", "net", "line_count", "quantity_net_by_unit")
	out := startCSV(w, "trial-balance-"+tb.Currency+".csv", map[string]string{"X-Passage-Currency": tb.Currency})
	out.row(head...)
	for _, row := range tb.Rows {
		rec := []string{row.Account, row.Name}
		for _, g := range tb.GroupBy {
			rec = append(rec, row.Dimensions[g])
		}
		out.row(append(rec, row.Debit, row.Credit, row.Net, strconv.FormatInt(row.LineCount, 10), quantityCell(row.Quantities))...)
	}
	total := []string{"TOTAL", ""}
	for range tb.GroupBy {
		total = append(total, "")
	}
	out.row(append(total, tb.Debit, tb.Credit, tb.Net, "", quantityCell(tb.Quantities))...)
	out.end()
}

// exportLines streams the filtered lines (posting order) as CSV, one keyset page at a time. Before the
// first byte it asks the store for the ledger's dimension attributes (the dim.* columns) and where the
// 50,000-row cap cuts the result, so the truncation headers are known up front.
func (a *API) exportLines(w http.ResponseWriter, r *http.Request, f store.LineFilter) {
	plan, err := a.store.PlanLineExport(r.Context(), f, maxExportRows)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	dims := slices.Sorted(slices.Values(plan.DimensionAttributes))
	extra := map[string]string{}
	if plan.Boundary != nil {
		extra[headerExportTruncated] = "true"
		extra[headerExportNext] = formatCursor(plan.Boundary)
	}
	head := []string{"posting_date", "voucher", "posting_sequence", "line_number", "account", "account_name", "debit_credit", "amount", "currency", "amount_acct",
		"quantity", "unit", "text", "description", "source_system", "source_type", "source_id", "source_line_ref", "negative_posting", "reversal_of_entry_id", "entry_id"}
	for _, d := range dims {
		head = append(head, "dim."+d)
	}
	out := startCSV(w, "ledger-lines.csv", extra)
	out.row(head...)
	f.Limit = store.MaxPageSize
	written := 0
	for {
		page, err := a.store.ListLines(r.Context(), f)
		if err != nil {
			abortExport(r, err)
		}
		for _, l := range page.Items {
			rec := []string{l.PostingDate, l.Voucher, strconv.FormatInt(l.PostingSequence, 10), strconv.Itoa(l.LineNumber), l.Account, l.DisplayName,
				l.DebitCredit, l.Amount, l.Currency, l.AmountAcct, l.Quantity, l.Unit, l.Text, l.Description,
				l.SourceSystem, l.SourceType, l.SourceID, l.SourceLineRef, strconv.FormatBool(l.NegativePosting), l.ReversalOfEntry, l.EntryID}
			for _, d := range dims {
				rec = append(rec, l.Dimensions[d])
			}
			out.row(rec...)
			written++
			// The cap: at the planned boundary, or at the row count when lines were posted after the plan.
			if written == maxExportRows || (plan.Boundary != nil && l.PostingSequence == plan.Boundary.PostingSequence && l.LineNumber == plan.Boundary.LineNumber) {
				out.end()
				return
			}
		}
		if page.Next == nil {
			out.end()
			return
		}
		f.After = page.Next
	}
}

// getAccount returns one account with its lockVersion as ETag, for an If-Match on the next PUT.
func (a *API) getAccount(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	chart, number := chi.URLParam(r, "chart"), chi.URLParam(r, "number")
	if !codeRe.MatchString(chart) || !accountNumberRe.MatchString(number) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "LEDGER_ACCOUNT_NOT_FOUND", "account not found"))
		return
	}
	acct, err := a.store.GetAccount(r.Context(), chart, number)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeTagged(w, http.StatusOK, acct.LockVersion, acct)
}
