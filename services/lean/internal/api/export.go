package api

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/services/lean/internal/store"
)

// exportHeader is the column order of the flow costing period report (PRD
// 4.24.14): period, status, the amounts and the cards settled.
var exportHeader = []string{
	"period_start", "period_end", "status", "currency", "material_cost", "conversion_cost", "output_value", "variance",
	"kanbans", "settled_at", "settled_by", "journal_entry_id",
}

func cell(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// textCell keeps free text from being read as a formula by a spreadsheet.
func textCell(s *string) string {
	v := cell(s)
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

// exportPeriods writes a flow's costing periods as CSV, oldest first (the flow
// costing period report of PRD 4.24.14). Open periods are listed with empty
// amounts. The body is built before anything is written, so a failure is
// still a problem response.
func (a *API) exportPeriods(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	flowID, ok := pathID(w, r)
	if !ok {
		return
	}
	periods, err := a.store.ListPeriods(r.Context(), flowID, store.MaxList)
	if err != nil {
		fail(w, r, err)
		return
	}
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	_ = cw.Write(exportHeader)
	for i := len(periods) - 1; i >= 0; i-- {
		p := periods[i]
		kanbans, settledAt := "", ""
		if p.Kanbans != nil {
			kanbans = strconv.Itoa(*p.Kanbans)
		}
		if p.SettledAt != nil {
			settledAt = p.SettledAt.UTC().Format(time.RFC3339)
		}
		_ = cw.Write([]string{p.PeriodStart, p.PeriodEnd, p.Status, p.Currency, cell(p.MaterialCost), cell(p.ConversionCost), cell(p.OutputValue),
			cell(p.Variance), kanbans, settledAt, textCell(p.SettledBy), cell(p.JournalEntryID)})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="flow-`+flowID+`-costing-periods.csv"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}
