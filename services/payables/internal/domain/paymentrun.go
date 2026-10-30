package domain

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	CodeRunConflict    = "AP-RUN-010"
	CodeRunState       = "AP-RUN-011"
	CodeRunOutOfOrder  = "AP-RUN-012"
	CodeRunConfig      = "AP-RUN-013"
	CodeRunLedger      = "AP-RUN-014"
	CodeRunItemChanged = "AP-RUN-015"

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
	FileNumber    string                 `json:"fileNumber,omitempty"`
	Debtor        PartyDTO               `json:"debtor"`
	DebtorAccount AccountDTO             `json:"debtorAccount"`
	DebtorBIC     string                 `json:"debtorBic"`
	Profile       BankProfileDTO         `json:"profile"`
	Creditors     map[string]CreditorDTO `json:"creditors"`
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
}

func RunSupportsMethod(method string) bool { return method == MethodTransfer || method == MethodWire }

func (c RunFileConfig) CheckVendors(groups []ProposalGroup) error {
	for _, g := range groups {
		if !RunSupportsMethod(g.Method) {
			return &Error{Code: CodeRunConfig, Detail: "payment method " + g.Method + " cannot be sent as pain.001; a run supports TRANSFER and WIRE groups only"}
		}
		if _, ok := c.Creditors[g.VendorID]; !ok {
			return &Error{Code: CodeRunConfig, Detail: "file.creditors has no bank details for vendor " + g.VendorID}
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

func BuildRunFile(cfg RunFileConfig, runID string, runDate, created time.Time, groups []ProposalGroup, invoiceNumbers map[string]string) (PaymentFile, []RunPayment, error) {
	if err := cfg.CheckVendors(groups); err != nil {
		return PaymentFile{}, nil, err
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
			Amount: g.Net.String(), Currency: g.Currency, Creditor: cfg.Creditors[g.VendorID],
		}
		for _, l := range g.Lines {
			payment.Invoices = append(payment.Invoices, RemittanceInvoiceDTO{Number: invoiceNumbers[l.OpenItemID], Amount: l.Pay.String()})
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

func RunJournal(currency string, groups []ProposalGroup, controlAccount, bankAccount, discountAccount string) []RunJournalLine {
	minor := money.MinorUnits(currency)
	var lines []RunJournalLine
	paid, discount := decimal.Zero, decimal.Zero
	for _, g := range groups {
		gross := decimal.Zero
		for _, l := range g.Lines {
			gross = gross.Add(l.OpenAmount.Mul(l.Rate).Round(minor))
		}
		lines = append(lines, RunJournalLine{Account: controlAccount, Amount: gross, Text: "vendor " + g.VendorID})
		paid = paid.Add(g.Valued)
		discount = discount.Add(gross.Sub(g.Valued))
	}
	lines = append(lines, RunJournalLine{Account: bankAccount, Credit: true, Amount: paid, Text: "payment run"})
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
