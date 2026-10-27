package domain

import (
	"bytes"
	"encoding/csv"
)

// ExportRow is one approved pay line as written to a payroll export. Numbers
// are kept as the stored decimal text so the file keeps the ledger's scale.
type ExportRow struct {
	WorkerID   string
	WorkDate   string
	PayType    string
	Hours      string
	Multiplier string
	Amount     string
	Currency   string
}

// ExportHeader is the first line of every payroll CSV export.
var ExportHeader = []string{"batchId", "workerId", "workDate", "payType", "hours", "multiplier", "amount", "currency"}

// ExportCSV renders the pay lines of one export batch as CSV with a header row.
// Every row carries the batch id so the file stands alone at payroll. Amounts
// and hours keep their stored decimal scale so the file ties to the pay lines.
func ExportCSV(batchID string, rows []ExportRow) (string, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(ExportHeader); err != nil {
		return "", err
	}
	for _, r := range rows {
		rec := []string{
			batchID,
			r.WorkerID,
			r.WorkDate,
			r.PayType,
			r.Hours,
			r.Multiplier,
			r.Amount,
			r.Currency,
		}
		if err := w.Write(rec); err != nil {
			return "", err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", err
	}
	return buf.String(), nil
}
