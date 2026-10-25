package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	maxPayeeLength  = 140
	maskedPrefix    = "****"
	maskedTailWidth = 4
	flagIssued      = "I"
	flagVoided      = "V"
)

var (
	positivePayAccount = regexp.MustCompile(`^[0-9A-Za-z]{4,34}$`)
	positivePayCheckNo = regexp.MustCompile(`^[0-9]{1,15}$`)
)

type CheckPaymentDTO struct {
	AccountNumber string `json:"accountNumber"`
	CheckNumber   string `json:"checkNumber"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	Payee         string `json:"payee"`
	IssueDate     string `json:"issueDate"`
	Status        string `json:"status"`
}

type CheckPayment struct {
	AccountNumber string
	CheckNumber   string
	Amount        decimal.Decimal
	Currency      string
	Payee         string
	IssueDate     time.Time
	Void          bool
}

type PositivePayFile struct {
	Content []byte
	Count   int
	Total   decimal.Decimal
	SHA256  string
}

func MaskAccount(account string) string {
	if len(account) <= maskedTailWidth {
		return maskedPrefix
	}
	return maskedPrefix + account[len(account)-maskedTailWidth:]
}

func ParseCheckPayment(dto CheckPaymentDTO) (CheckPayment, error) {
	if !positivePayAccount.MatchString(dto.AccountNumber) {
		return CheckPayment{}, invalid("accountNumber must be 4 to 34 letters or digits")
	}
	if !positivePayCheckNo.MatchString(dto.CheckNumber) {
		return CheckPayment{}, invalid("checkNumber must be 1 to 15 digits")
	}
	if !validCurrency(dto.Currency) {
		return CheckPayment{}, invalid("currency must be an ISO 4217 code")
	}
	amount, err := parseAmount("amount", dto.Amount, dto.Currency)
	if err != nil {
		return CheckPayment{}, err
	}
	if err := validatePayee(dto.Payee); err != nil {
		return CheckPayment{}, err
	}
	issued, err := parseDate("issueDate", dto.IssueDate)
	if err != nil {
		return CheckPayment{}, err
	}
	void, err := voidFlag(dto.Status)
	if err != nil {
		return CheckPayment{}, err
	}
	return CheckPayment{
		AccountNumber: dto.AccountNumber, CheckNumber: dto.CheckNumber, Amount: amount,
		Currency: dto.Currency, Payee: dto.Payee, IssueDate: issued, Void: void,
	}, nil
}

func validatePayee(payee string) error {
	if strings.TrimSpace(payee) == "" || len(payee) > maxPayeeLength {
		return invalid("payee must be 1 to %d characters", maxPayeeLength)
	}
	if strings.ContainsRune("=+-@\t\r", rune(payee[0])) {
		return invalid("payee must not start with a formula or control character")
	}
	return nil
}

func voidFlag(status string) (bool, error) {
	switch status {
	case PaymentVoid:
		return true, nil
	case PaymentPosted, PaymentSent, PaymentAccepted, PaymentSettledAtBank:
		return false, nil
	}
	return false, invalid("status %q cannot appear in a positive pay file", status)
}

func GeneratePositivePay(checks []CheckPayment) (PositivePayFile, error) {
	if len(checks) == 0 {
		return PositivePayFile{}, invalid("at least one check payment is required")
	}
	ordered := append([]CheckPayment(nil), checks...)
	sort.SliceStable(ordered, func(i, j int) bool { return checkOrder(ordered[i].CheckNumber) < checkOrder(ordered[j].CheckNumber) })
	currency := ordered[0].Currency
	minor := money.MinorUnits(currency)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"account_number", "check_number", "amount", "payee_name", "issue_date", "void_flag"})
	total := decimal.Zero
	masked := make([]string, 0, len(ordered))
	for i, c := range ordered {
		if c.Currency != currency {
			return PositivePayFile{}, invalid("check %s currency %s differs from %s", c.CheckNumber, c.Currency, currency)
		}
		if i > 0 && checkOrder(ordered[i-1].CheckNumber) == checkOrder(c.CheckNumber) {
			return PositivePayFile{}, invalid("duplicate check number %s", c.CheckNumber)
		}
		flag := flagIssued
		if c.Void {
			flag = flagVoided
		} else {
			total = total.Add(c.Amount)
		}
		_ = w.Write([]string{c.AccountNumber, c.CheckNumber, c.Amount.StringFixed(minor), c.Payee, c.IssueDate.Format(dateLayout), flag})
		masked = append(masked, MaskAccount(c.AccountNumber))
	}
	_ = w.Write([]string{"TRAILER", strconv.Itoa(len(ordered)), total.StringFixed(minor)})
	w.Flush()
	sum := sha256.Sum256(buf.Bytes())
	slog.Info("positive pay generated", "count", len(ordered), "accounts", masked)
	return PositivePayFile{Content: buf.Bytes(), Count: len(ordered), Total: total, SHA256: hex.EncodeToString(sum[:])}, nil
}

func checkOrder(number string) string {
	trimmed := strings.TrimLeft(number, "0")
	return strings.Repeat("0", 16-len(trimmed)) + trimmed
}
