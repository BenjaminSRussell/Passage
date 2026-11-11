package domain

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	CodeRunState       = "AP-RUN-011"
	CodeRunOutOfOrder  = "AP-RUN-012"
	CodeRunConfig      = "AP-RUN-013"
	CodeRunLedger      = "AP-RUN-014"
	CodeRunItemChanged = "AP-RUN-015"
	CodeRunIdentity    = "AP-RUN-016"
	CodeRunPayee       = "AP-RUN-017"
	// CodeRunNotCompleted refuses a remittance advice for a run that is not completed.
	CodeRunNotCompleted = "AP-RUN-018"
	// CodeRunWithholdingParked: the tax service refused the run's withholding entries for good; the run is parked for an operator.
	CodeRunWithholdingParked = "AP-RUN-019"

	RunRunning   = "RUNNING"
	RunCompleted = "COMPLETED"
	RunFailed    = "FAILED"

	ProposalExecuting = "EXECUTING"
	ProposalExecuted  = "EXECUTED"
	ProposalFailed    = "FAILED"

	paymentNumberPrefix = "P-"
	fileNumberPrefix    = "R"
	fileNumberSuffixLen = 24
)

type RunFileConfig struct {
	FileNumber    string         `json:"fileNumber,omitempty"`
	Debtor        PartyDTO       `json:"debtor"`
	DebtorAccount AccountDTO     `json:"debtorAccount"`
	DebtorBIC     string         `json:"debtorBic"`
	Profile       BankProfileDTO `json:"profile"`
}

type RunPayment struct {
	ID         string          `json:"id,omitempty"`
	GroupID    string          `json:"groupId"`
	VendorID   string          `json:"vendorId"`
	Currency   string          `json:"currency"`
	Method     string          `json:"paymentMethod"`
	EndToEndID string          `json:"endToEndId"`
	Amount     decimal.Decimal `json:"amount"`
	Valued     decimal.Decimal `json:"valuedAmount"`
}

type RunJournalLine struct {
	Account string
	Credit  bool
	Amount  decimal.Decimal
	Text    string
	// Dimensions are ledger dimension values of the line (the vendor group code on a control-account debit).
	Dimensions map[string]string
}

func RunSupportsMethod(method string) bool { return method == MethodTransfer || method == MethodWire }

func CheckRunnable(currency string, groups []ProposalGroup) error {
	for _, g := range groups {
		switch {
		case !RunSupportsMethod(g.Method):
			return &Error{Code: CodeRunConfig, Detail: "payment method " + g.Method + " cannot be sent as pain.001; a run supports TRANSFER and WIRE groups only"}
		case g.Currency != currency:
			return &Error{Code: CodeRunConfig, Detail: "group " + g.ID + " is in " + g.Currency + " but the legal entity currency is " + currency + "; foreign-currency payments need realized FX, which is not built"}
		}
	}
	return nil
}

func PaymentNumber(groupID string) string {
	return paymentNumberPrefix + strings.ReplaceAll(groupID, "-", "")
}

func RunFileNumber(runID string) string {
	hex := strings.ReplaceAll(runID, "-", "")
	return fileNumberPrefix + hex[max(0, len(hex)-fileNumberSuffixLen):]
}

func BuildRunFile(cfg RunFileConfig, creditors map[string]CreditorDTO, runID string, runDate, created time.Time, groups []ProposalGroup, invoiceNumbers map[string]string) (PaymentFile, []RunPayment, error) {
	for _, g := range groups {
		if _, ok := creditors[g.VendorID]; !ok {
			return PaymentFile{}, nil, &Error{Code: CodeRunPayee, Detail: "vendor " + g.VendorID + " has no bank details"}
		}
		if !RunSupportsMethod(g.Method) {
			return PaymentFile{}, nil, &Error{Code: CodeRunConfig, Detail: "group " + g.ID + " cannot be sent: unsupported method"}
		}
	}
	fileNumber := cfg.FileNumber
	if fileNumber == "" {
		fileNumber = RunFileNumber(runID)
	}
	dto := Pain001RequestDTO{
		FileNumber: fileNumber, Created: created.UTC().Format(time.RFC3339), Debtor: cfg.Debtor, DebtorAccount: cfg.DebtorAccount,
		DebtorBIC: cfg.DebtorBIC, Profile: cfg.Profile,
	}
	payments := make([]RunPayment, 0, len(groups))
	for _, g := range groups {
		number := PaymentNumber(g.ID)
		payment := Pain001PaymentDTO{
			Method: PaymentMethodDTO{Type: g.Method, Format: FormatPain001}, ExecutionDate: runDate.Format(dateLayout), Number: number,
			Amount: g.Net.String(), Currency: g.Currency, Creditor: creditors[g.VendorID],
		}
		for _, l := range g.Lines {
			payment.Invoices = append(payment.Invoices, RemittanceInvoiceDTO{Number: invoiceNumbers[l.OpenItemID], Amount: l.Pay.Sub(l.Withheld).String()})
		}
		dto.Payments = append(dto.Payments, payment)
		payments = append(payments, RunPayment{GroupID: g.ID, VendorID: g.VendorID, Currency: g.Currency, Method: g.Method, EndToEndID: number, Amount: g.Net, Valued: g.Valued})
	}
	in, err := ParsePain001Request(dto)
	if err != nil {
		return PaymentFile{}, nil, err
	}
	file, err := GeneratePain001(in)
	return file, payments, err
}

// VendorGroupDims tells RunJournal which dimension carries the vendor group code and the code of each open
// item; a zero value posts no dimension.
type VendorGroupDims struct {
	Attribute string
	// Codes maps an open item id to its vendor group code; an item without one is untagged.
	Codes map[string]string
}

func RunJournal(currency string, groups []ProposalGroup, controlAccount, bankAccount, discountAccount, withholdingAccount string, dims VendorGroupDims) []RunJournalLine {
	minor := money.MinorUnits(currency)
	var lines []RunJournalLine
	paid, withheld, discount := decimal.Zero, decimal.Zero, decimal.Zero
	for _, g := range groups {
		gross, groupWithheld := decimal.Zero, decimal.Zero
		// The control debit is split per vendor group code so each group's ledger balance falls with its own items.
		var codes []string
		perCode := map[string]decimal.Decimal{}
		for _, l := range g.Lines {
			amount := l.OpenAmount.Mul(l.Rate).Round(minor)
			gross = gross.Add(amount)
			groupWithheld = groupWithheld.Add(l.Withheld.Mul(l.Rate).Round(minor))
			code := ""
			if dims.Attribute != "" {
				code = dims.Codes[l.OpenItemID]
			}
			if _, seen := perCode[code]; !seen {
				codes = append(codes, code)
			}
			perCode[code] = perCode[code].Add(amount)
		}
		for _, code := range codes {
			line := RunJournalLine{Account: controlAccount, Amount: perCode[code], Text: "vendor " + g.VendorID}
			if code != "" {
				line.Dimensions = map[string]string{dims.Attribute: code}
			}
			lines = append(lines, line)
		}
		if len(codes) == 0 {
			lines = append(lines, RunJournalLine{Account: controlAccount, Amount: gross, Text: "vendor " + g.VendorID})
		}
		paid = paid.Add(g.Valued)
		withheld = withheld.Add(groupWithheld)
		discount = discount.Add(gross.Sub(g.Valued).Sub(groupWithheld))
	}
	lines = append(lines, RunJournalLine{Account: bankAccount, Credit: true, Amount: paid, Text: "payment run"})
	if withheld.IsPositive() {
		lines = append(lines, RunJournalLine{Account: withholdingAccount, Credit: true, Amount: withheld, Text: "tax withheld"})
	}
	if discount.IsPositive() {
		lines = append(lines, RunJournalLine{Account: discountAccount, Credit: true, Amount: discount, Text: "cash discount taken"})
	}
	return lines
}

func HasDiscount(groups []ProposalGroup) bool {
	for _, g := range groups {
		if g.Discount.IsPositive() {
			return true
		}
	}
	return false
}
