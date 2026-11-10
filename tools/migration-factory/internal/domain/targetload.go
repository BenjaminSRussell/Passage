package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Target load of the open_items pack (integration track S8): the accepted rows of a run are sent to
// the receivables service, and the target's own totals are reconciled with what the factory sent.

// Ledger says which subledger an open item belongs to.
const (
	LedgerAR = "AR"
	LedgerAP = "AP"
)

// Reject codes the factory raises itself; a target's own refusal keeps the target's code (AR-OPENING-*).
const (
	CodeTargetNotAvailable = "MIG-TARGET-NOT-AVAILABLE"
	CodeTargetUnavailable  = "MIG-TARGET-UNAVAILABLE"
	CodeTargetRefused      = "MIG-TARGET-REFUSED"
	CodeFieldRequired      = "MIG-LOAD-FIELD-REQUIRED"
	CodeRefUnresolved      = "MIG-REF-UNRESOLVED"
)

// TargetReceivables is the only target today.
const TargetReceivables = "receivables"

// MaxImportSource is the longest importSource receivables accepts.
const MaxImportSource = 40

// Statuses of a target load (target_load.status).
const (
	LoadStaged       = "STAGED"
	LoadLoading      = "LOADING"
	LoadPartial      = "PARTIAL"      // rows are still pending: the target did not answer, retry the load
	LoadUnreconciled = "UNRECONCILED" // every row was sent, but the target's totals could not be read
	LoadMismatch     = "MISMATCH"     // every row was sent and the target's totals differ from what was accepted
	LoadReconciled   = "RECONCILED"   // every row was sent and the target's totals equal what was accepted
	LoadFailed       = "FAILED"       // the load stopped (the target refused the factory's service token)
)

// Statuses of a staged row (target_row.status).
const (
	RowPending      = "PENDING"
	RowLoaded       = "LOADED"
	RowUnchanged    = "UNCHANGED"
	RowSkipped      = "SKIPPED"
	RowRejected     = "REJECTED"
	RowNotAvailable = "NOT_AVAILABLE"
)

// MaxBatchRows is the most rows POST /v1/receivables/open-items:batch takes.
const MaxBatchRows = 1000

var importSourceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// TargetConfig is what a run fixes for its load: the legal entity the open items belong to, the
// import source name receivables keys them under, and the map from legacy party to the Passage
// customer id (a party that is itself a UUID needs no entry).
type TargetConfig struct {
	LegalEntityID string
	ImportSource  string
	Customers     map[string]string
	// Payables, when set, stages the run's AP lines for the payables opening-batch import (49-3). Without it
	// AP lines are NOT_AVAILABLE, as before.
	Payables *PayablesConfig
}

// ValidateImportSource checks the import source name against receivables' rule.
func ValidateImportSource(s string) error {
	if !importSourceName.MatchString(s) || len(s) > MaxImportSource {
		return fmt.Errorf("%w: importSource %q must be 1 to %d letters, digits, dots, dashes or underscores", ErrInvalid, s, MaxImportSource)
	}
	return nil
}

// DefaultImportSource names the import source of a (system, company): "ax2012.usmf".
func DefaultImportSource(system SourceSystem, company string) string {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
				return r
			case r >= 'A' && r <= 'Z':
				return r + 'a' - 'A'
			}
			return '-'
		}, s)
	}
	return clean(string(system)) + "." + clean(company)
}

// TargetExtra holds the extract fields the receivables row needs beyond the control record.
type TargetExtra struct {
	Ledger             string
	DocumentNumber     string
	CustomerID         string
	Kind               string
	DocumentDate       string
	DueDate            string
	OriginalAmount     *decimal.Decimal
	CashDiscountDate   *string
	CashDiscountAmount decimal.Decimal
}

// ReceivablesRow is one row of POST /v1/receivables/open-items:batch. Amounts are magnitudes; Side gives the sign.
type ReceivablesRow struct {
	LegacyKey          string          `json:"legacyKey"`
	DocumentNumber     string          `json:"documentNumber"`
	CustomerID         string          `json:"customerId"`
	Kind               string          `json:"kind"`
	Side               string          `json:"side"`
	DocumentDate       string          `json:"documentDate"`
	DueDate            string          `json:"dueDate"`
	Currency           string          `json:"currency"`
	OriginalAmount     decimal.Decimal `json:"originalAmount"`
	OpenAmount         decimal.Decimal `json:"openAmount"`
	CashDiscountDate   *string         `json:"cashDiscountDate"`
	CashDiscountAmount decimal.Decimal `json:"cashDiscountAmount"`
}

// Hash identifies the row as the target would store it: any change to a field, to the import
// source or to the legal entity changes it.
func (r ReceivablesRow) Hash(importSource, legalEntityID string) string {
	disc := ""
	if r.CashDiscountDate != nil {
		disc = *r.CashDiscountDate
	}
	h := sha256.New()
	for _, p := range []string{importSource, legalEntityID, r.LegacyKey, r.DocumentNumber, strings.ToLower(r.CustomerID), r.Kind, r.Side,
		r.DocumentDate, r.DueDate, r.Currency, r.OriginalAmount.String(), r.OpenAmount.String(), disc, r.CashDiscountAmount.String()} {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SignedAmount is the row's amount with the side's sign, as the extract's amount carries it.
func (r ReceivablesRow) SignedAmount() decimal.Decimal {
	if r.Side == "CREDIT" {
		return r.OpenAmount.Neg()
	}
	return r.OpenAmount
}

// StagedRow is an accepted record on its way to the target. Row is nil for a row that never leaves
// the factory (open AP, or a record the factory cannot build a receivables row from).
type StagedRow struct {
	Seq      int
	LegacyID string
	Ledger   string
	Hash     string
	Row      *ReceivablesRow
	// AP is the payables opening item of an AP line staged for payables (TargetConfig.Payables).
	AP     *PayablesRow
	Status string
	Code   string
	Detail string
}

// StageRows builds the receivables row of each accepted record. An AP record is NOT_AVAILABLE (payables has no
// opening-item API yet); a record with no resolvable customer or no dates is REJECTED here, with the reason, and
// never sent. Everything else is PENDING.
func StageRows(accepted []Record, extras map[string]TargetExtra, cfg TargetConfig) []StagedRow {
	out := make([]StagedRow, 0, len(accepted))
	for i, rec := range accepted {
		x := extras[OpenItemKey(rec.Side(), rec.LegacyID)]
		st := StagedRow{Seq: i + 1, LegacyID: rec.LegacyID, Ledger: LedgerAR, Status: RowPending}
		if x.Ledger != "" {
			st.Ledger = x.Ledger
		}
		if st.Ledger == LedgerAP && cfg.Payables != nil {
			ap, code, detail := buildPayablesRow(rec, x, cfg)
			if code != "" {
				st.Status, st.Code, st.Detail = RowRejected, code, detail
				out = append(out, st)
				continue
			}
			st.AP = &ap
			st.Hash = ap.Hash(cfg.ImportSource, cfg.LegalEntityID)
			out = append(out, st)
			continue
		}
		if st.Ledger == LedgerAP {
			st.Status, st.Code = RowNotAvailable, CodeTargetNotAvailable
			st.Detail = "payables has no opening-item batch API yet; open AP is a handoff (tools/migration-factory/NOT-IMPLEMENTED.md)"
			out = append(out, st)
			continue
		}
		row, code, detail := buildRow(rec, x, cfg)
		if code != "" {
			st.Status, st.Code, st.Detail = RowRejected, code, detail
			out = append(out, st)
			continue
		}
		st.Row = &row
		st.Hash = row.Hash(cfg.ImportSource, cfg.LegalEntityID)
		out = append(out, st)
	}
	return out
}

func buildRow(rec Record, x TargetExtra, cfg TargetConfig) (ReceivablesRow, string, string) {
	var missing []string
	if x.DocumentDate == "" {
		missing = append(missing, "documentDate")
	}
	if x.DueDate == "" {
		missing = append(missing, "dueDate")
	}
	if len(missing) > 0 {
		return ReceivablesRow{}, CodeFieldRequired, "the extract record has no " + strings.Join(missing, ", ")
	}
	customer := strings.TrimSpace(x.CustomerID)
	if customer == "" {
		customer = strings.TrimSpace(cfg.Customers[rec.Party])
	}
	if customer == "" && uuidPattern.MatchString(rec.Party) {
		customer = rec.Party
	}
	if !uuidPattern.MatchString(customer) {
		return ReceivablesRow{}, CodeRefUnresolved, fmt.Sprintf("party %q has no Passage customer id: name it in load.customerIds or send customerId on the record", rec.Party)
	}
	side, kind := "DEBIT", x.Kind
	if rec.Amount.IsNegative() {
		side = "CREDIT"
	}
	if kind == "" {
		kind = "INVOICE"
		if side == "CREDIT" {
			kind = "CREDIT_NOTE"
		}
	}
	open := rec.Amount.Abs()
	original := open
	if x.OriginalAmount != nil {
		original = *x.OriginalAmount
	}
	doc := strings.TrimSpace(x.DocumentNumber)
	if doc == "" {
		doc = rec.LegacyID
	}
	return ReceivablesRow{LegacyKey: rec.LegacyID, DocumentNumber: doc, CustomerID: strings.ToLower(customer), Kind: kind, Side: side,
		DocumentDate: x.DocumentDate, DueDate: x.DueDate, Currency: rec.Currency, OriginalAmount: original, OpenAmount: open,
		CashDiscountDate: x.CashDiscountDate, CashDiscountAmount: x.CashDiscountAmount}, "", ""
}

// TargetLoad is the load of one run: where its rows went and how they came out.
type TargetLoad struct {
	RunID         string
	Target        string
	LegalEntityID string
	ImportSource  string
	Status        string
	Staged        int
	Created       int
	Updated       int
	Unchanged     int
	Skipped       int
	Rejected      int
	NotAvailable  int
	Pending       int
	Attempts      int
	LastError     string
	StartedBy     string
	StartedAt     *time.Time
	FinishedAt    *time.Time
	Version       int
	CreatedAt     time.Time
}

// Clean reports that every row reached the target and its totals match: nothing pending, rejected or unavailable.
func (l TargetLoad) Clean() bool {
	return l.Status == LoadReconciled && l.Pending == 0 && l.Rejected == 0 && l.NotAvailable == 0
}

// TargetRowView is a staged row as the API lists it.
type TargetRowView struct {
	Seq        int
	LegacyID   string
	Ledger     string
	Status     string
	Outcome    string
	PassageID  string
	Code       string
	Detail     string
	Amount     string
	Currency   string
	Side       string
	CustomerID string
}

// ImportTotals is what one currency of an import source holds, on either side of the reconciliation.
// CreditOpen is negative, as receivables reports it.
type ImportTotals struct {
	Currency   string
	ItemCount  int
	DebitOpen  decimal.Decimal
	CreditOpen decimal.Decimal
	NetOpen    decimal.Decimal
}

// ReconcileTarget compares what the factory has loaded (expected) with what receivables reports for the import
// source (actual), per currency: item count, debit, credit and net open. A currency on either side only compares
// against zero. A difference is MISMATCH; there is no tolerance.
func ReconcileTarget(importSource, legalEntityID string, expected, actual []ImportTotals) ([]ControlTotal, error) {
	byCur := map[string][2]ImportTotals{}
	for _, e := range expected {
		p := byCur[e.Currency]
		p[0] = e
		byCur[e.Currency] = p
	}
	for _, a := range actual {
		p := byCur[a.Currency]
		p[1] = a
		byCur[a.Currency] = p
	}
	curs := make([]string, 0, len(byCur))
	for c := range byCur {
		curs = append(curs, c)
	}
	sort.Strings(curs)
	var out []ControlTotal
	for _, c := range curs {
		e, a := byCur[c][0], byCur[c][1]
		key := map[string]string{"importSource": importSource, "legalEntityId": legalEntityID, "currency": c}
		for _, m := range []struct {
			name string
			src  decimal.Decimal
			tgt  decimal.Decimal
		}{
			{"target.receivables.item_count", decimal.NewFromInt(int64(e.ItemCount)), decimal.NewFromInt(int64(a.ItemCount))},
			{"target.receivables.debit_open", e.DebitOpen, a.DebitOpen},
			{"target.receivables.credit_open", e.CreditOpen, a.CreditOpen},
			{"target.receivables.net_open", e.NetOpen, a.NetOpen},
		} {
			t, err := NewControlTotal(m.name, key, m.src, m.tgt, Tolerance{})
			if err != nil {
				return nil, err
			}
			out = append(out, t)
		}
	}
	return out, nil
}

// IsTargetTotal reports whether a control total came from the target reconciliation.
func IsTargetTotal(name string) bool { return strings.HasPrefix(name, "target.") }
