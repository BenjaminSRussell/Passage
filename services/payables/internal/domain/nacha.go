package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	NachaPPD = "PPD"
	NachaCCD = "CCD"
	NachaCTX = "CTX"

	AccountChecking = "CHECKING"
	AccountSavings  = "SAVINGS"

	nachaWidth        = 94
	nachaBlockLines   = 10
	nachaAddendaWidth = 80
	nachaMaxAddenda   = 9999
	nachaMaxCents     = 9999999999
	nachaHashModulus  = 10000000000
	nachaCurrency     = "USD"
	nachaCreditClass  = "220"
	nachaDateLayout   = "060102"
	nachaTimeLayout   = "1504"
)

var (
	nachaModifier = regexp.MustCompile(`^[A-Z0-9]$`)
	nachaAccount  = regexp.MustCompile(`^[0-9A-Za-z]{1,17}$`)
	nachaInvoice  = regexp.MustCompile(`^[0-9A-Za-z._/ -]{1,30}$`)

	nachaMaxAmount = decimal.New(nachaMaxCents, -2)
)

type NachaInvoiceDTO struct {
	Number string `json:"number"`
	Amount string `json:"amount"`
}

type NachaEntryDTO struct {
	SECCode       string            `json:"secCode"`
	EffectiveDate string            `json:"effectiveDate"`
	PaymentNumber string            `json:"paymentNumber"`
	AccountType   string            `json:"accountType"`
	RoutingNumber string            `json:"routingNumber"`
	AccountNumber string            `json:"accountNumber"`
	Amount        string            `json:"amount"`
	Currency      string            `json:"currency"`
	IndividualID  string            `json:"individualId"`
	Name          string            `json:"name"`
	Invoices      []NachaInvoiceDTO `json:"invoices"`
}

type NachaInputDTO struct {
	FileNumber         string          `json:"fileNumber"`
	Created            string          `json:"created"`
	FileIDModifier     string          `json:"fileIdModifier"`
	DestinationRouting string          `json:"destinationRouting"`
	DestinationName    string          `json:"destinationName"`
	OriginID           string          `json:"originId"`
	OriginName         string          `json:"originName"`
	CompanyName        string          `json:"companyName"`
	CompanyID          string          `json:"companyId"`
	EntryDescription   string          `json:"entryDescription"`
	ODFIRouting        string          `json:"odfiRouting"`
	Prenote            bool            `json:"prenote"`
	Entries            []NachaEntryDTO `json:"entries"`
}

type NachaInvoice struct {
	Number string
	Amount decimal.Decimal
}

type NachaEntry struct {
	SECCode       string
	EffectiveDate time.Time
	PaymentNumber string
	AccountType   string
	RoutingNumber string
	AccountNumber string
	Amount        decimal.Decimal
	Currency      string
	IndividualID  string
	Name          string
	Invoices      []NachaInvoice
}

type NachaInput struct {
	FileNumber         string
	Created            time.Time
	FileIDModifier     string
	DestinationRouting string
	DestinationName    string
	OriginID           string
	OriginName         string
	CompanyName        string
	CompanyID          string
	EntryDescription   string
	ODFIRouting        string
	Prenote            bool
	Entries            []NachaEntry
}

func ParseNachaInput(dto NachaInputDTO) (NachaInput, error) {
	created, err := time.Parse(time.RFC3339, dto.Created)
	if err != nil {
		return NachaInput{}, invalid("created must be an RFC 3339 timestamp")
	}
	entries := make([]NachaEntry, 0, len(dto.Entries))
	for i, e := range dto.Entries {
		entry, err := parseNachaEntry(fmt.Sprintf("entries[%d]", i), e)
		if err != nil {
			return NachaInput{}, err
		}
		entries = append(entries, entry)
	}
	return NachaInput{
		FileNumber: dto.FileNumber, Created: created, FileIDModifier: dto.FileIDModifier,
		DestinationRouting: dto.DestinationRouting, DestinationName: dto.DestinationName,
		OriginID: dto.OriginID, OriginName: dto.OriginName, CompanyName: dto.CompanyName,
		CompanyID: dto.CompanyID, EntryDescription: dto.EntryDescription, ODFIRouting: dto.ODFIRouting,
		Prenote: dto.Prenote, Entries: entries,
	}, nil
}

func parseNachaEntry(path string, e NachaEntryDTO) (NachaEntry, error) {
	date, err := parseDate(path+".effectiveDate", e.EffectiveDate)
	if err != nil {
		return NachaEntry{}, err
	}
	amount := decimal.Zero
	if e.Amount != "" {
		if amount, err = parseDecimal(path+".amount", e.Amount, 2); err != nil {
			return NachaEntry{}, err
		}
	}
	invoices := make([]NachaInvoice, 0, len(e.Invoices))
	for j, inv := range e.Invoices {
		invoiceAmount, err := parseDecimal(fmt.Sprintf("%s.invoices[%d].amount", path, j), inv.Amount, 2)
		if err != nil {
			return NachaEntry{}, err
		}
		invoices = append(invoices, NachaInvoice{Number: inv.Number, Amount: invoiceAmount})
	}
	return NachaEntry{
		SECCode: e.SECCode, EffectiveDate: date, PaymentNumber: e.PaymentNumber, AccountType: e.AccountType,
		RoutingNumber: e.RoutingNumber, AccountNumber: e.AccountNumber, Amount: amount, Currency: e.Currency,
		IndividualID: e.IndividualID, Name: e.Name, Invoices: invoices,
	}, nil
}

type fieldWriter struct{ err error }

func (f *fieldWriter) text(field, raw string, width int) string {
	if f.err != nil {
		return ""
	}
	t, err := Transliterate(raw, CharsetASCII)
	if err != nil {
		var de *Error
		errors.As(err, &de)
		f.err = &Error{Code: de.Code, Detail: field + ": " + de.Detail}
		return ""
	}
	return nachaPad(strings.ToUpper(t), width)
}

func (f *fieldWriter) optionalText(field, raw string, width int) string {
	if strings.TrimSpace(raw) == "" {
		return nachaPad("", width)
	}
	return f.text(field, raw, width)
}

func nachaPad(s string, width int) string {
	if len(s) > width {
		return s[:width]
	}
	return s + strings.Repeat(" ", width-len(s))
}

func nachaNum(v int64, width int) string {
	return fmt.Sprintf("%0*d", width, v)
}

func validateNachaEntry(prenote bool, e NachaEntry) error {
	who := "payment " + e.PaymentNumber
	switch e.SECCode {
	case NachaPPD, NachaCCD, NachaCTX:
	default:
		return invalid("%s: secCode must be PPD, CCD or CTX", who)
	}
	if e.AccountType != AccountChecking && e.AccountType != AccountSavings {
		return invalid("%s: accountType must be CHECKING or SAVINGS", who)
	}
	if e.EffectiveDate.IsZero() {
		return invalid("%s: effectiveDate is required", who)
	}
	if err := ValidateABA(e.RoutingNumber); err != nil {
		return err
	}
	if !nachaAccount.MatchString(e.AccountNumber) {
		return invalid("%s: account number %s must be 1 to 17 letters or digits", who, MaskAccount(e.AccountNumber))
	}
	if e.Currency != nachaCurrency {
		return invalid("%s: currency must be %s", who, nachaCurrency)
	}
	if e.Amount.IsNegative() || !e.Amount.Equal(e.Amount.Round(2)) || e.Amount.GreaterThan(nachaMaxAmount) {
		return invalid("%s: amount must be non-negative with at most 2 decimals and 10 digits of cents", who)
	}
	if prenote && !e.Amount.IsZero() {
		return invalid("%s: a prenote entry must have a zero amount", who)
	}
	if !prenote && !e.Amount.IsPositive() {
		return invalid("%s: amount must be greater than zero", who)
	}
	if prenote && len(e.Invoices) > 0 {
		return invalid("%s: a prenote entry carries no invoices", who)
	}
	for _, inv := range e.Invoices {
		if !nachaInvoice.MatchString(inv.Number) || inv.Amount.IsNegative() || !inv.Amount.Equal(inv.Amount.Round(2)) {
			return invalid("%s: invoice number or amount is invalid", who)
		}
	}
	return nil
}

func nachaTransactionCode(accountType string, prenote bool) string {
	code := "2"
	if accountType == AccountSavings {
		code = "3"
	}
	if prenote {
		return code + "3"
	}
	return code + "2"
}

func nachaAddendaPayloads(e NachaEntry) ([]string, error) {
	if len(e.Invoices) == 0 {
		return nil, nil
	}
	if e.SECCode != NachaCTX {
		numbers := make([]string, len(e.Invoices))
		for i, inv := range e.Invoices {
			numbers[i] = strings.ToUpper(inv.Number)
		}
		return []string{nachaPad(strings.Join(numbers, ","), nachaAddendaWidth)}, nil
	}
	var remittance strings.Builder
	for _, inv := range e.Invoices {
		fmt.Fprintf(&remittance, "RMR*IV*%s**%s\\", strings.ToUpper(inv.Number), inv.Amount.StringFixed(2))
	}
	text := remittance.String()
	var payloads []string
	for len(text) > 0 {
		n := min(len(text), nachaAddendaWidth)
		payloads = append(payloads, nachaPad(text[:n], nachaAddendaWidth))
		text = text[n:]
	}
	if len(payloads) > nachaMaxAddenda {
		return nil, invalid("payment %s: remittance needs more than %d addenda records", e.PaymentNumber, nachaMaxAddenda)
	}
	return payloads, nil
}

type nachaBatch struct {
	date    time.Time
	sec     string
	entries []NachaEntry
}

func nachaBatches(entries []NachaEntry) []*nachaBatch {
	byKey := map[string]*nachaBatch{}
	var batches []*nachaBatch
	for _, e := range entries {
		key := e.EffectiveDate.Format(dateLayout) + e.SECCode
		if byKey[key] == nil {
			byKey[key] = &nachaBatch{date: e.EffectiveDate, sec: e.SECCode}
			batches = append(batches, byKey[key])
		}
		byKey[key].entries = append(byKey[key].entries, e)
	}
	sort.SliceStable(batches, func(i, j int) bool {
		if !batches[i].date.Equal(batches[j].date) {
			return batches[i].date.Before(batches[j].date)
		}
		return batches[i].sec < batches[j].sec
	})
	for _, b := range batches {
		sort.SliceStable(b.entries, func(i, j int) bool { return b.entries[i].PaymentNumber < b.entries[j].PaymentNumber })
	}
	return batches
}

func GenerateNacha(in NachaInput) (PaymentFile, error) {
	if len(in.Entries) == 0 {
		return PaymentFile{}, invalid("at least one entry is required")
	}
	if !nachaModifier.MatchString(in.FileIDModifier) {
		return PaymentFile{}, invalid("fileIdModifier must be one letter A-Z or digit 0-9")
	}
	for _, routing := range []string{in.DestinationRouting, in.ODFIRouting} {
		if err := ValidateABA(routing); err != nil {
			return PaymentFile{}, err
		}
	}
	for _, e := range in.Entries {
		if err := validateNachaEntry(in.Prenote, e); err != nil {
			return PaymentFile{}, err
		}
	}

	w := &fieldWriter{}
	companyName := w.text("companyName", in.CompanyName, 16)
	companyID := w.text("companyId", in.CompanyID, 10)
	description := w.text("entryDescription", in.EntryDescription, 10)
	odfi := in.ODFIRouting[:8]
	lines := []string{strings.Join([]string{
		"101", " " + in.DestinationRouting, w.text("originId", in.OriginID, 10),
		in.Created.Format(nachaDateLayout), in.Created.Format(nachaTimeLayout), in.FileIDModifier, "094", "10", "1",
		w.text("destinationName", in.DestinationName, 23), w.text("originName", in.OriginName, 23),
		w.text("fileNumber", in.FileNumber, 8),
	}, "")}

	batches := nachaBatches(in.Entries)
	var fileCount, fileCredit, fileHash int64
	total := decimal.Zero
	trace := int64(0)
	for i, b := range batches {
		batchNumber := nachaNum(int64(i+1), 7)
		lines = append(lines, strings.Join([]string{
			"5", nachaCreditClass, companyName, nachaPad("", 20), companyID, b.sec, description,
			nachaPad("", 6), b.date.Format(nachaDateLayout), nachaPad("", 3), "1", odfi, batchNumber,
		}, ""))
		var count, hash, credit int64
		for _, e := range b.entries {
			trace++
			traceNumber := odfi + nachaNum(trace, 7)
			payloads, err := nachaAddendaPayloads(e)
			if err != nil {
				return PaymentFile{}, err
			}
			rdfi, _ := strconv.ParseInt(e.RoutingNumber[:8], 10, 64)
			cents := e.Amount.Shift(2).IntPart()
			identity := w.optionalText("individualId", e.IndividualID, 15)
			name := w.text("name", e.Name, 22)
			if b.sec == NachaCTX {
				name = nachaNum(int64(len(payloads)), 4) + nachaPad(name, 16) + "  "
			}
			indicator := "0"
			if len(payloads) > 0 {
				indicator = "1"
			}
			lines = append(lines, strings.Join([]string{
				"6", nachaTransactionCode(e.AccountType, in.Prenote), e.RoutingNumber[:8], e.RoutingNumber[8:],
				nachaPad(e.AccountNumber, 17), nachaNum(cents, 10), identity, name, "  ", indicator, traceNumber,
			}, ""))
			for n, payload := range payloads {
				lines = append(lines, strings.Join([]string{"705", payload, nachaNum(int64(n+1), 4), traceNumber[8:]}, ""))
			}
			count += int64(1 + len(payloads))
			hash += rdfi
			credit += cents
			total = total.Add(e.Amount)
		}
		hash %= nachaHashModulus
		lines = append(lines, strings.Join([]string{
			"8", nachaCreditClass, nachaNum(count, 6), nachaNum(hash, 10), nachaNum(0, 12), nachaNum(credit, 12),
			companyID, nachaPad("", 19), nachaPad("", 6), odfi, batchNumber,
		}, ""))
		fileCount += count
		fileCredit += credit
		fileHash += hash
	}
	if w.err != nil {
		return PaymentFile{}, w.err
	}

	blocks := (len(lines) + 1 + nachaBlockLines - 1) / nachaBlockLines
	lines = append(lines, strings.Join([]string{
		"9", nachaNum(int64(len(batches)), 6), nachaNum(int64(blocks), 6), nachaNum(fileCount, 8),
		nachaNum(fileHash%nachaHashModulus, 10), nachaNum(0, 12), nachaNum(fileCredit, 12), nachaPad("", 39),
	}, ""))
	for len(lines)%nachaBlockLines != 0 {
		lines = append(lines, strings.Repeat("9", nachaWidth))
	}
	for _, line := range lines {
		if len(line) != nachaWidth {
			return PaymentFile{}, invalid("a NACHA record is %d characters instead of %d", len(line), nachaWidth)
		}
	}

	content := []byte(strings.Join(lines, "\n") + "\n")
	sum := sha256.Sum256(content)
	slog.Info("nacha generated", "file", in.FileNumber, "count", len(in.Entries), "batches", len(batches))
	return PaymentFile{
		Format: FormatNacha, FileNumber: in.FileNumber, Content: content,
		PaymentCount: len(in.Entries), ControlSum: total, SHA256: hex.EncodeToString(sum[:]),
	}, nil
}
