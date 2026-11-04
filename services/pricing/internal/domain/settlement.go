package domain

import (
	"bytes"
	"encoding/csv"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Settlement targets of the commission settlement export (FR-26.18, §5.26.13
// commission_settlement_target). NONE means the legal entity exports nothing, so it is not
// a target an export can name.
const (
	SettlementTargetAP      = "AP"
	SettlementTargetPayroll = "PAYROLL"
)

// SettlementRow is one rep's commission total in one currency for a period. Only the rep id
// leaves the commission context; rep names never do.
type SettlementRow struct {
	RepID        string
	Currency     string
	Amount       decimal.Decimal
	Transactions int
}

// SettlementHeader is the column order of the settlement export.
var SettlementHeader = []string{"target", "rep_id", "currency", "amount", "transactions", "from", "to"}

// ValidSettlementTarget reports whether target names an export destination.
func ValidSettlementTarget(target string) bool {
	return target == SettlementTargetAP || target == SettlementTargetPayroll
}

// SettlementCSV renders the export for payroll (w43) or AP (w19). Amounts are written with
// the currency's minor units (300.00 USD, 300 JPY). Rows keep the order they are given in
// (the store orders them by rep, then currency), so the file is deterministic. A rep id that
// a spreadsheet would read as a formula is written with a leading apostrophe.
func SettlementCSV(target, from, to string, rows []SettlementRow) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(SettlementHeader); err != nil {
		return nil, err
	}
	for _, r := range rows {
		amount := r.Amount.RoundBank(money.MinorUnits(r.Currency)).StringFixed(money.MinorUnits(r.Currency))
		rec := []string{target, safeCell(r.RepID), r.Currency, amount, strconv.Itoa(r.Transactions), from, to}
		if err := w.Write(rec); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// CSVCell guards one exported CSV cell against spreadsheet formula injection (OWASP CSV
// injection): a value that starts with = + - @, a tab or a carriage return gets a leading
// apostrophe, unless the whole value is a decimal number (a negative amount stays a number).
func CSVCell(v string) string {
	if v != "" && strings.ContainsRune("+-", rune(v[0])) {
		if _, err := decimal.NewFromString(v); err == nil && !strings.ContainsAny(v, "eE") {
			return v
		}
	}
	return safeCell(v)
}

// safeCell neutralises spreadsheet formula injection in a free-text cell (OWASP CSV injection).
func safeCell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}
