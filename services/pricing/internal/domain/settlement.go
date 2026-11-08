package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"sort"
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

// Statuses of a recorded settlement export.
const (
	ExportIssued = "ISSUED"
	ExportVoided = "VOIDED"
)

// Problem codes of the settlement export record (FR-26.18).
const (
	// CodeExportOverlap: an ISSUED export of the same legal entity and target already covers
	// part of the period, so the commission would be handed over twice.
	CodeExportOverlap = "PRC-0065"
	// CodeExportEmpty: the period holds no commission to hand over.
	CodeExportEmpty = "PRC-0066"
	// CodeExportNotFound: no settlement export with the id.
	CodeExportNotFound = "PRC-0067"
	// CodeExportDigest: the file rebuilt from a record's frozen rows does not match the digest
	// recorded when it was handed over (the CSV layout changed), so it is not served.
	CodeExportDigest = "PRC-0068"
)

// SettlementExport is one recorded hand-over of commission to AP or payroll. Lines are the
// rows as they were sent; later commission postings do not change them.
type SettlementExport struct {
	ID            string
	LegalEntityID string
	Target        string
	From          string
	To            string
	Status        string
	Lines         []SettlementRow
	CSVSHA256     string
	ExportedBy    string
	VoidReason    string
	VoidedBy      string
	Version       int
	CreatedAt     string
}

// CurrencyTotal is the sum of an export's rows in one currency.
type CurrencyTotal struct {
	Currency string
	Amount   decimal.Decimal
}

// ExportTotals sums rows per currency, ordered by currency, each rounded to the currency's
// minor units like the CSV amounts.
func ExportTotals(rows []SettlementRow) []CurrencyTotal {
	sums := map[string]decimal.Decimal{}
	for _, r := range rows {
		sums[r.Currency] = sums[r.Currency].Add(r.Amount.RoundBank(money.MinorUnits(r.Currency)))
	}
	out := make([]CurrencyTotal, 0, len(sums))
	for c, a := range sums {
		out = append(out, CurrencyTotal{Currency: c, Amount: a})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}

// CSVDigest is the lowercase hex SHA-256 of an export file, kept on the record so a second
// download can be checked against the file that was handed over.
func CSVDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
