package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Target load of the AP lines of an open_items run (49-3): the payables service takes them as opening batches
// (POST /v1/payables/opening-batches, all or nothing, 1 to 1000 items, amounts in minor units, a control total per
// currency) and reports each stored batch back (GET), which is what the factory reconciles against.

// TargetPayables is the payables opening-batch target.
const TargetPayables = "payables"

// MaxPayablesBatchItems is the most items one opening batch takes.
const MaxPayablesBatchItems = 1000

// Reject codes the factory raises for an AP line it cannot build an opening item from.
const (
	CodeAPDebitBalance = "MIG-LOAD-AP-DEBIT-BALANCE"
	CodeAPPrecision    = "MIG-LOAD-AP-PRECISION"
)

// maxJSONInteger is the largest integer a JSON number carries exactly; payables refuses a larger amountMinor.
const maxJSONInteger = int64(1) << 53

// PayablesConfig is what a run fixes for the AP side of its load: the map from legacy vendor to the Passage vendor
// id (a vendor that is itself a UUID needs no entry).
type PayablesConfig struct {
	Vendors map[string]string
}

// PayablesRow is one item of a payables opening batch.
type PayablesRow struct {
	Reference     string `json:"reference"`
	VendorID      string `json:"vendorId"`
	InvoiceNumber string `json:"invoiceNumber"`
	InvoiceDate   string `json:"invoiceDate"`
	DueDate       string `json:"dueDate"`
	Currency      string `json:"currency"`
	AmountMinor   int64  `json:"amountMinor"`
}

// Hash identifies the item as payables would store it; any change to a field, the import source or the legal
// entity changes it.
func (r PayablesRow) Hash(importSource, legalEntityID string) string {
	h := sha256.New()
	for _, p := range []string{importSource, legalEntityID, r.Reference, strings.ToLower(r.VendorID), r.InvoiceNumber, r.InvoiceDate, r.DueDate,
		r.Currency, fmt.Sprint(r.AmountMinor)} {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func buildPayablesRow(rec Record, x TargetExtra, cfg TargetConfig) (PayablesRow, string, string) {
	var missing []string
	if x.DocumentDate == "" {
		missing = append(missing, "documentDate")
	}
	if x.DueDate == "" {
		missing = append(missing, "dueDate")
	}
	if len(missing) > 0 {
		return PayablesRow{}, CodeFieldRequired, "the extract record has no " + strings.Join(missing, ", ")
	}
	vendor := strings.TrimSpace(x.CustomerID)
	if vendor == "" {
		vendor = strings.TrimSpace(cfg.Payables.Vendors[rec.Party])
	}
	if vendor == "" && uuidPattern.MatchString(rec.Party) {
		vendor = rec.Party
	}
	if !uuidPattern.MatchString(vendor) {
		return PayablesRow{}, CodeRefUnresolved, fmt.Sprintf("party %q has no Passage vendor id: name it in load.payables.vendorIds or send vendorId on the record", rec.Party)
	}
	// An AP amount carries the ledger sign (what Passage owes the vendor is a credit, negative), as the AP control
	// balance in the GL does; payables takes the magnitude owed. A debit balance (a vendor owes Passage) is not an
	// open payable and has no place in an opening batch.
	if !rec.Amount.IsNegative() {
		return PayablesRow{}, CodeAPDebitBalance, "payables takes only an amount owed to the vendor (a credit, negative in the extract); a vendor debit balance cannot be loaded as an opening item"
	}
	minor, ok := toMinor(rec.Amount.Abs(), rec.Currency)
	if !ok {
		return PayablesRow{}, CodeAPPrecision, fmt.Sprintf("amount %s is not a whole number of minor units of %s or does not fit payables' column", rec.Amount, rec.Currency)
	}
	doc := strings.TrimSpace(x.DocumentNumber)
	if doc == "" {
		doc = rec.LegacyID
	}
	return PayablesRow{Reference: rec.LegacyID, VendorID: strings.ToLower(vendor), InvoiceNumber: doc, InvoiceDate: x.DocumentDate, DueDate: x.DueDate,
		Currency: rec.Currency, AmountMinor: minor}, "", ""
}

// toMinor converts an amount to whole minor units of the currency; ok is false for a fraction of a minor unit or a
// value above what a JSON integer carries.
func toMinor(amount decimal.Decimal, currency string) (int64, bool) {
	scaled := amount.Shift(money.MinorUnits(currency))
	if !scaled.Equal(scaled.Truncate(0)) || scaled.Abs().GreaterThan(decimal.NewFromInt(maxJSONInteger)) {
		return 0, false
	}
	return scaled.IntPart(), true
}

// PayablesBatchKey is the idempotency key of one opening batch: stable for the same items of an import source, so a
// retry, or a later load of an unchanged snapshot, is the same request to payables. The suffix has a fixed length so
// the keys of one import source can be told from those of another whose name it prefixes.
func PayablesBatchKey(importSource string, hashes []string) string {
	sum := sha256.Sum256([]byte(strings.Join(hashes, ",")))
	return PayablesBatchPrefix(importSource) + hex.EncodeToString(sum[:])[:PayablesKeyHash]
}

// PayablesKeyHash is the length of the content hash at the end of a batch key.
const PayablesKeyHash = 24

// PayablesBatchPrefix starts every batch key of the import source.
func PayablesBatchPrefix(importSource string) string { return "mf-ap-" + importSource + "-" }

// IsPayablesBatchKey reports whether a payables batch key was written by the factory for the import source.
func IsPayablesBatchKey(importSource, key string) bool {
	p := PayablesBatchPrefix(importSource)
	return strings.HasPrefix(key, p) && len(key) == len(p)+PayablesKeyHash
}

// PayablesLoad is the AP load of one run.
type PayablesLoad struct {
	RunID         string
	LegalEntityID string
	ImportSource  string
	Status        string
	Staged        int
	Loaded        int
	Unchanged     int
	Skipped       int
	Rejected      int
	Pending       int
	Attempts      int
	LastError     string
	StartedBy     string
	StartedAt     *time.Time
	FinishedAt    *time.Time
	Version       int
	CreatedAt     time.Time
}

// Clean reports that every AP line reached payables and its totals match.
func (l PayablesLoad) Clean() bool {
	return l.Status == LoadReconciled && l.Pending == 0 && l.Rejected == 0
}

// PayablesTotals is what an import source holds in payables: the number of items, and the total per currency in
// minor units. A batch of several currencies counts its items once, so the count is not split by currency.
type PayablesTotals struct {
	Items      int
	TotalMinor map[string]int64
}

// ReconcilePayables compares what the factory has loaded (expected) with what payables reports for the import
// source's batches (actual): the item count, and the total per currency in minor units. A currency on one side only
// compares with zero; a difference is MISMATCH, there is no tolerance.
func ReconcilePayables(importSource, legalEntityID string, expected, actual PayablesTotals) ([]ControlTotal, error) {
	key := map[string]string{"importSource": importSource, "legalEntityId": legalEntityID}
	count, err := NewControlTotal("target.payables.item_count", key, decimal.NewFromInt(int64(expected.Items)), decimal.NewFromInt(int64(actual.Items)), Tolerance{})
	if err != nil {
		return nil, err
	}
	out := []ControlTotal{count}
	curs := map[string]bool{}
	for c := range expected.TotalMinor {
		curs[c] = true
	}
	for c := range actual.TotalMinor {
		curs[c] = true
	}
	names := make([]string, 0, len(curs))
	for c := range curs {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, c := range names {
		ck := map[string]string{"importSource": importSource, "legalEntityId": legalEntityID, "currency": c}
		t, err := NewControlTotal("target.payables.total_minor", ck, decimal.NewFromInt(expected.TotalMinor[c]), decimal.NewFromInt(actual.TotalMinor[c]), Tolerance{})
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}
