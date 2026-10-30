package domain

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
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
	FileNumber    string         `json:"fileNumber,omitempty"`
	Debtor        PartyDTO       `json:"debtor"`
	DebtorAccount AccountDTO     `json:"debtorAccount"`
	DebtorBIC     string         `json:"debtorBic"`
	Profile       BankProfileDTO `json:"profile"`
}

type PayeeSummary struct {
	VendorID string `json:"vendorId"`
	Name     string `json:"name"`
	Account  string `json:"account"`
	Bank     string `json:"bank"`
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

func CheckRunnable(currency string, groups []ProposalGroup, creditors map[string]CreditorDTO) error {
	for _, g := range groups {
		switch {
		case !RunSupportsMethod(g.Method):
			return &Error{Code: CodeRunConfig, Detail: "payment method " + g.Method + " cannot be sent as pain.001; a run supports TRANSFER and WIRE groups only"}
		case g.Currency != currency:
			return &Error{Code: CodeRunConfig, Detail: "group " + g.ID + " is in " + g.Currency + " but the legal entity currency is " + currency + "; foreign-currency payments need realized FX, which is not built"}
		}
		if _, ok := creditors[g.VendorID]; !ok {
			return &Error{Code: CodeRunConfig, Detail: "the proposal has no bank details for vendor " + g.VendorID}
		}
	}
	return nil
}

func ValidateCreditors(creditors map[string]CreditorDTO) map[string]string {
	fields := map[string]string{}
	if len(creditors) > 500 {
		fields["creditors"] = "at most 500 entries"
	}
	for vendor, c := range creditors {
		if !ids.Valid(vendor) {
			fields["creditors"] = "keys must be vendor UUIDs"
		} else if strings.TrimSpace(c.Name) == "" || (c.IBAN == "" && c.OtherID == "") {
			fields["creditors."+vendor] = "needs a name and an iban or otherId"
		}
	}
	return fields
}

func SummarizePayee(vendorID string, c CreditorDTO) PayeeSummary {
	account := c.IBAN
	if account == "" {
		account = c.OtherID
	}
	bank := c.BIC
	if bank == "" {
		bank = c.RoutingNumber
	}
	return PayeeSummary{VendorID: vendorID, Name: c.Name, Account: MaskBank(account), Bank: bank}
}

func MaskBank(v string) string {
	v = strings.TrimSpace(v)
	if len(v) < 4 {
		return "****"
	}
	return "****" + v[len(v)-4:]
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
		if _, ok := creditors[g.VendorID]; !ok || !RunSupportsMethod(g.Method) {
			return PaymentFile{}, nil, &Error{Code: CodeRunConfig, Detail: "group " + g.ID + " cannot be sent: no bank details or unsupported method"}
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
