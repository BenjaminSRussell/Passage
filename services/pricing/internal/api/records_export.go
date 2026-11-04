package api

import (
	"bufio"
	"encoding/csv"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// recordsExportColumns is the header of the trade agreements export. Where a column exists in
// the journal CSV import it has the same name and format; status and version are read-only.
var recordsExportColumns = []string{"recordId", "type", "customerKind", "customerKey", "productKind", "productKey",
	"variant", "currency", "unit", "fromQuantity", "amount", "discPct1", "discPct2", "validFrom", "validTo",
	"searchNext", "exclusive", "campaignId", "status", "version"}

// exportFlushRows is how many CSV rows are buffered before they are flushed to the client.
const exportFlushRows = 500

// errExportStarted marks a failure after the 200 and the first bytes were sent.
var errExportStarted = errors.New("export failed after the response started")

// exportRecords streams the condition records that match the list filters as CSV (§5.26.14
// trade agreements grid, export). It takes the list's filters, not its limit: up to
// store.MaxExportRows rows, with X-Passage-Truncated: true when more matched. Rows go to the
// client as they are read; every text cell is guarded against spreadsheet formula injection.
func (a *API) exportRecords(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f, fields := recordFilter(r.URL.Query())
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
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
	started, n := false, 0
	start := func(truncated bool) error {
		h := w.Header()
		h.Set("Content-Type", "text/csv; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="trade-agreements-`+time.Now().UTC().Format(dateLayout)+`.csv"`)
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		if truncated {
			h.Set("X-Passage-Truncated", "true")
		}
		w.WriteHeader(http.StatusOK)
		started = true
		return cw.Write(recordsExportColumns)
	}
	row := func(e store.Entry) error {
		v := viewOf(e)
		c := domain.CSVCell
		if err := cw.Write([]string{v.ID, v.Type, v.CustomerKind, c(v.CustomerKey), v.ProductKind, c(v.ProductKey), c(v.Variant),
			c(v.Currency), c(v.Unit), c(v.FromQuantity), c(v.Amount), c(v.DiscPct1), c(v.DiscPct2), v.ValidFrom, v.ValidTo,
			strconv.FormatBool(v.SearchNext), strconv.FormatBool(v.Exclusive), v.CampaignID, v.Status, strconv.Itoa(v.Version)}); err != nil {
			return err
		}
		if n++; n%exportFlushRows == 0 {
			return flush()
		}
		return nil
	}
	err := a.store.ExportRecords(r.Context(), f, store.MaxExportRows, start, row)
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
	// The status line is sent: abort the connection so the client sees a failed download, not a
	// short file that looks complete.
	slog.Error("trade agreements export failed", "err", errors.Join(errExportStarted, err), "rows", n,
		"correlationId", middleware.GetReqID(r.Context()))
	panic(http.ErrAbortHandler)
}
