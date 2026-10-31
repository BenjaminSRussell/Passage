// Package fixtures loads the wave 30 acceptance fixtures under
// services/receivables/testdata: cash-app-500 (AC-30.1) and dunning-60
// (AC-30.3). Tests import it; nothing in the service does. It depends on no
// receivables package, so the expected values it reads stay independent of the
// engine under test.
package fixtures

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/shopspring/decimal"
)

// Legal entities of the fixtures: the US entity books USD (lockbox and
// remittances), the DE entity books EUR (camt.053).
const (
	LegalEntityUS = "00000000-0000-0000-0000-0000000000a1"
	LegalEntityDE = "00000000-0000-0000-0000-0000000000a2"
	// CashAppReceivedDate is the lockbox file date and the remittances' value date.
	CashAppReceivedDate = "2027-03-15"
	// DunningRunDate is the run date of dunning-60 (grace 5 days, interval 10).
	DunningRunDate = "2027-03-31"
)

// Answer-key categories of cash-app-500.
const (
	CatSingle          = "SINGLE"            // one invoice, exact amount, referenced
	CatDiscount        = "DISCOUNT"          // one invoice less its 2% cash discount, inside the discount window, referenced
	CatMulti           = "MULTI"             // two to four invoices of one customer, exact sum, all referenced
	CatShortWithReason = "SHORT_WITH_REASON" // one invoice short by less than 50.00, referenced, with a short-pay reason
	CatShortNoReason   = "SHORT_NO_REASON"   // one invoice short by 1-5%, referenced, no reason
	CatUnidentified    = "UNIDENTIFIED"      // exact amount of one invoice, no usable reference
	CatWrongReference  = "WRONG_REFERENCE"   // the amount of one invoice, remittance cites another invoice of the same customer
)

// Sources of cash-app-500 payments.
const (
	SourceLockbox    = "LOCKBOX"
	SourceCAMT053    = "CAMT053"
	SourceRemittance = "REMITTANCE"
)

// OpenItem is one receivable of a fixture.
type OpenItem struct {
	LegalEntityID  string
	CustomerID     string
	DocumentNumber string
	DocumentDate   string
	DueDate        string
	Currency       string
	Amount         decimal.Decimal
	OpenAmount     decimal.Decimal
	DiscountDate   string // "" when the item has no cash discount
	DiscountAmount decimal.Decimal
}

// RemittanceLine is one line of a remittance advice.
type RemittanceLine struct {
	Reference string          `json:"reference"`
	Amount    decimal.Decimal `json:"amount"`
}

// Remittance is a payment whose payer sent a remittance advice: the customer is
// known and each line names an invoice.
type Remittance struct {
	Key            string           `json:"key"`
	LegalEntityID  string           `json:"legalEntityId"`
	CustomerID     string           `json:"customerId"`
	Currency       string           `json:"currency"`
	ValueDate      string           `json:"valueDate"`
	Amount         decimal.Decimal  `json:"amount"`
	ShortPayReason string           `json:"shortPayReason,omitempty"`
	Lines          []RemittanceLine `json:"lines"`
}

// Answer is the answer-key row of one payment: the invoices it truly pays.
// The key is the lockbox bank reference, the camt.053 entry reference or the
// remittance key.
type Answer struct {
	PaymentKey string
	Source     string
	Category   string
	Documents  []string
}

// CashApp is the cash-app-500 fixture.
type CashApp struct {
	OpenItems   []OpenItem
	Lockbox     string // BAI2 file, 300 checks (USD)
	Statement   []byte // camt.053.001.08 file, 150 credits (EUR)
	Remittances []Remittance
	Key         map[string]Answer
}

// Dir is the path of a fixture directory under services/receivables/testdata.
func Dir(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", name)
}

// LoadCashApp reads testdata/cash-app-500.
func LoadCashApp() (CashApp, error) {
	dir := Dir("cash-app-500")
	var out CashApp
	rows, err := readCSV(filepath.Join(dir, "open_items.csv"))
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		it := OpenItem{LegalEntityID: r["legal_entity_id"], CustomerID: r["customer_id"], DocumentNumber: r["document_number"],
			DocumentDate: r["document_date"], DueDate: r["due_date"], Currency: r["currency"], DiscountDate: r["discount_date"]}
		if it.Amount, err = decimal.NewFromString(r["amount"]); err != nil {
			return out, fmt.Errorf("open_items.csv %s amount: %w", it.DocumentNumber, err)
		}
		it.OpenAmount = it.Amount
		if it.DiscountAmount, err = decimal.NewFromString(r["discount_amount"]); err != nil {
			return out, fmt.Errorf("open_items.csv %s discount: %w", it.DocumentNumber, err)
		}
		out.OpenItems = append(out.OpenItems, it)
	}
	lockbox, err := os.ReadFile(filepath.Join(dir, "lockbox.bai2"))
	if err != nil {
		return out, err
	}
	out.Lockbox = string(lockbox)
	if out.Statement, err = os.ReadFile(filepath.Join(dir, "statement.camt053.xml")); err != nil {
		return out, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "remittances.json"))
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out.Remittances); err != nil {
		return out, fmt.Errorf("remittances.json: %w", err)
	}
	keyRows, err := readCSV(filepath.Join(dir, "answer_key.csv"))
	if err != nil {
		return out, err
	}
	out.Key = map[string]Answer{}
	for _, r := range keyRows {
		out.Key[r["payment_key"]] = Answer{PaymentKey: r["payment_key"], Source: r["source"], Category: r["category"],
			Documents: strings.Fields(r["documents"])}
	}
	return out, nil
}

// DunningItem is one open item of dunning-60 with its collections state.
type DunningItem struct {
	Customer       string // C01..C60
	OpenItem       OpenItem
	DunningLevel   int
	Blocked        bool
	DisputedAmount decimal.Decimal // zero when not disputed
	PromiseDate    string          // "" when no promise
	LastDunned     string          // "" when never dunned
}

// ExpectedLetter is one hand-derived letter of dunning-60.
type ExpectedLetter struct {
	Customer  string
	Currency  string
	Level     int
	Fee       decimal.Decimal
	Amount    decimal.Decimal
	Documents []string
}

// Dunning is the dunning-60 fixture and its hand-derived expectations.
type Dunning struct {
	Items      []DunningItem
	Letters    map[string]ExpectedLetter // by customer
	Exclusions map[string]string         // reason by document number
}

// DunningCustomerID maps a dunning-60 customer code (C01..C60) to its UUID.
func DunningCustomerID(code string) string {
	return "00000000-0000-0000-0003-0000000000" + strings.TrimPrefix(code, "C")
}

// LoadDunning reads testdata/dunning-60.
func LoadDunning() (Dunning, error) {
	dir := Dir("dunning-60")
	out := Dunning{Letters: map[string]ExpectedLetter{}, Exclusions: map[string]string{}}
	rows, err := readCSV(filepath.Join(dir, "items.csv"))
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		it := DunningItem{Customer: r["customer"], Blocked: r["blocked"] == "Y", PromiseDate: r["promise_date"],
			LastDunned: r["last_dunned"], DisputedAmount: decimal.Zero}
		it.OpenItem = OpenItem{LegalEntityID: LegalEntityUS, CustomerID: DunningCustomerID(r["customer"]),
			DocumentNumber: r["document"], DocumentDate: "2026-12-01", DueDate: r["due_date"], Currency: r["currency"],
			DiscountAmount: decimal.Zero}
		if it.OpenItem.Amount, err = decimal.NewFromString(r["amount"]); err != nil {
			return out, fmt.Errorf("items.csv %s amount: %w", r["document"], err)
		}
		if it.OpenItem.OpenAmount, err = decimal.NewFromString(r["open_amount"]); err != nil {
			return out, fmt.Errorf("items.csv %s open amount: %w", r["document"], err)
		}
		if _, err := fmt.Sscan(r["dunning_level"], &it.DunningLevel); err != nil {
			return out, fmt.Errorf("items.csv %s level: %w", r["document"], err)
		}
		if v := r["disputed_amount"]; v != "" {
			if it.DisputedAmount, err = decimal.NewFromString(v); err != nil {
				return out, fmt.Errorf("items.csv %s disputed amount: %w", r["document"], err)
			}
		}
		out.Items = append(out.Items, it)
	}
	letters, err := readCSV(filepath.Join(dir, "expected_letters.csv"))
	if err != nil {
		return out, err
	}
	for _, r := range letters {
		l := ExpectedLetter{Customer: r["customer"], Currency: r["currency"], Documents: strings.Fields(r["documents"])}
		if _, err := fmt.Sscan(r["level"], &l.Level); err != nil {
			return out, fmt.Errorf("expected_letters.csv %s level: %w", l.Customer, err)
		}
		if l.Fee, err = decimal.NewFromString(r["fee"]); err != nil {
			return out, fmt.Errorf("expected_letters.csv %s fee: %w", l.Customer, err)
		}
		if l.Amount, err = decimal.NewFromString(r["amount"]); err != nil {
			return out, fmt.Errorf("expected_letters.csv %s amount: %w", l.Customer, err)
		}
		out.Letters[l.Customer] = l
	}
	exclusions, err := readCSV(filepath.Join(dir, "expected_exclusions.csv"))
	if err != nil {
		return out, err
	}
	for _, r := range exclusions {
		out.Exclusions[r["document"]] = r["reason"]
	}
	return out, nil
}

// readCSV reads a headed CSV file into one map per row. Lines starting with #
// are comments.
func readCSV(path string) ([]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comment = '#'
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%s: empty", filepath.Base(path))
	}
	header := records[0]
	out := make([]map[string]string, 0, len(records)-1)
	for _, rec := range records[1:] {
		row := map[string]string{}
		for i, h := range header {
			row[h] = strings.TrimSpace(rec[i])
		}
		out = append(out, row)
	}
	return out, nil
}
