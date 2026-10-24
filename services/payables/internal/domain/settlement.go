package domain

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	CodeValidation = "PAY-0001"
	CodeCurrency   = "PAY-0002"

	maxInvoices     = 500
	maxFutureDays   = 366
	maxRawLength    = 28
	maxIntegerDigit = 15
	maxRateScale    = 12
	maxPctScale     = 6
	maxIDLength     = 64
	dateLayout      = "2006-01-02"

	StatusSettled          = "SETTLED"
	StatusPartiallySettled = "PARTIALLY_SETTLED"

	AccountAPControl     = "ap_control"
	AccountBankClearing  = "bank_clearing"
	AccountDiscountTaken = "discount_taken"
	AccountWriteOff      = "write_off"
	AccountFXGain        = "fx_gain"
	AccountFXLoss        = "fx_loss"
	SideDebit            = "debit"
	SideCredit           = "credit"
)

type Error struct{ Code, Detail string }

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func invalid(format string, a ...any) *Error {
	return &Error{Code: CodeValidation, Detail: fmt.Sprintf(format, a...)}
}

type CreditDTO struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
	Rate     string `json:"rate"`
}

type InvoiceDTO struct {
	ID             string `json:"id"`
	Currency       string `json:"currency"`
	Amount         string `json:"amount"`
	OpenAmount     string `json:"openAmount"`
	Rate           string `json:"rate"`
	DueDate        string `json:"dueDate"`
	DiscountPct    string `json:"discountPct"`
	DiscountDate   string `json:"discountDate"`
	AlwaysDiscount bool   `json:"alwaysDiscount"`
	Marked         bool   `json:"marked"`
}

type PolicyDTO struct {
	DiscountOnPartial *bool  `json:"discountOnPartial"`
	AutoWriteOff      *bool  `json:"autoWriteOff"`
	WriteOffLimit     string `json:"writeOffLimit"`
}

type RequestDTO struct {
	SettlementDate     string       `json:"settlementDate"`
	AccountingCurrency string       `json:"accountingCurrency"`
	Credit             CreditDTO    `json:"credit"`
	Invoices           []InvoiceDTO `json:"invoices"`
	Policy             PolicyDTO    `json:"policy"`
}

type Invoice struct {
	ID           string
	Currency     string
	Amount       decimal.Decimal
	Open         decimal.Decimal
	Rate         decimal.Decimal
	DueDate      time.Time
	DiscountPct  decimal.Decimal
	DiscountDate time.Time
	Always       bool
	Marked       bool
}

type Policy struct {
	DiscountOnPartial bool
	AutoWriteOff      bool
	WriteOffLimit     decimal.Decimal
}

type Request struct {
	SettlementDate     time.Time
	AccountingCurrency string
	CreditCurrency     string
	Credit             decimal.Decimal
	CreditRate         decimal.Decimal
	Invoices           []Invoice
	Policy             Policy
}

type Money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type Line struct {
	InvoiceID         string `json:"invoiceId"`
	Applied           Money  `json:"applied"`
	Discount          Money  `json:"discount"`
	WriteOff          Money  `json:"writeOff"`
	AppliedAccounting Money  `json:"appliedAccounting"`
	FXDifference      Money  `json:"fxDifference"`
	OpenAfter         Money  `json:"openAfter"`
	Status            string `json:"status"`
}

type JournalLine struct {
	Account string `json:"account"`
	Side    string `json:"side"`
	Amount  Money  `json:"amount"`
}

type Result struct {
	Lines           []Line        `json:"lines"`
	Journal         []JournalLine `json:"journal"`
	CreditRemaining Money         `json:"creditRemaining"`
}

func ParseRequest(dto RequestDTO, now time.Time) (Request, error) {
	date, err := parseDate("settlementDate", dto.SettlementDate)
	if err != nil {
		return Request{}, err
	}
	if date.After(now.AddDate(0, 0, maxFutureDays)) {
		return Request{}, invalid("settlementDate is more than %d days ahead", maxFutureDays)
	}
	acct := dto.AccountingCurrency
	if !validCurrency(acct) {
		return Request{}, invalid("accountingCurrency must be a 3-letter ISO code")
	}
	req := Request{SettlementDate: date, AccountingCurrency: acct, CreditCurrency: dto.Credit.Currency}
	if !validCurrency(req.CreditCurrency) {
		return Request{}, invalid("credit.currency must be a 3-letter ISO code")
	}
	if req.Credit, err = parseAmount("credit.amount", dto.Credit.Amount, req.CreditCurrency); err != nil {
		return Request{}, err
	}
	if req.CreditRate, err = parseRate("credit.rate", dto.Credit.Rate, req.CreditCurrency, acct); err != nil {
		return Request{}, err
	}
	if req.Policy, err = parsePolicy(dto.Policy); err != nil {
		return Request{}, err
	}
	if len(dto.Invoices) == 0 || len(dto.Invoices) > maxInvoices {
		return Request{}, invalid("invoices must hold 1 to %d items", maxInvoices)
	}
	seen := make(map[string]bool, len(dto.Invoices))
	for i, d := range dto.Invoices {
		inv, err := parseInvoice(fmt.Sprintf("invoices[%d]", i), d, acct)
		if err != nil {
			return Request{}, err
		}
		if seen[inv.ID] {
			return Request{}, invalid("invoices[%d].id %q is duplicated", i, inv.ID)
		}
		seen[inv.ID] = true
		req.Invoices = append(req.Invoices, inv)
	}
	return req, nil
}

func parsePolicy(dto PolicyDTO) (Policy, error) {
	p := Policy{DiscountOnPartial: true, WriteOffLimit: decimal.NewFromInt(1)}
	if dto.DiscountOnPartial != nil {
		p.DiscountOnPartial = *dto.DiscountOnPartial
	}
	if dto.AutoWriteOff != nil {
		p.AutoWriteOff = *dto.AutoWriteOff
	}
	if dto.WriteOffLimit != "" {
		limit, err := parseDecimal("policy.writeOffLimit", dto.WriteOffLimit, maxPctScale)
		if err != nil {
			return Policy{}, err
		}
		p.WriteOffLimit = limit
	}
	return p, nil
}

func parseInvoice(field string, d InvoiceDTO, acct string) (Invoice, error) {
	if d.ID == "" || len(d.ID) > maxIDLength {
		return Invoice{}, invalid("%s.id must be 1 to %d characters", field, maxIDLength)
	}
	if !validCurrency(d.Currency) {
		return Invoice{}, invalid("%s.currency must be a 3-letter ISO code", field)
	}
	inv := Invoice{ID: d.ID, Currency: d.Currency, Always: d.AlwaysDiscount, Marked: d.Marked}
	var err error
	if inv.Amount, err = parseAmount(field+".amount", d.Amount, d.Currency); err != nil {
		return Invoice{}, err
	}
	inv.Open = inv.Amount
	if d.OpenAmount != "" {
		if inv.Open, err = parseAmount(field+".openAmount", d.OpenAmount, d.Currency); err != nil {
			return Invoice{}, err
		}
	}
	if inv.Open.GreaterThan(inv.Amount) {
		return Invoice{}, invalid("%s.openAmount exceeds amount", field)
	}
	if inv.Rate, err = parseRate(field+".rate", d.Rate, d.Currency, acct); err != nil {
		return Invoice{}, err
	}
	if inv.DueDate, err = parseDate(field+".dueDate", d.DueDate); err != nil {
		return Invoice{}, err
	}
	if d.DiscountDate != "" {
		if inv.DiscountDate, err = parseDate(field+".discountDate", d.DiscountDate); err != nil {
			return Invoice{}, err
		}
	}
	if d.DiscountPct != "" {
		if inv.DiscountPct, err = parseDecimal(field+".discountPct", d.DiscountPct, maxPctScale); err != nil {
			return Invoice{}, err
		}
		if inv.DiscountPct.GreaterThan(decimal.NewFromInt(100)) {
			return Invoice{}, invalid("%s.discountPct must not exceed 100", field)
		}
	}
	return inv, nil
}

func validCurrency(c string) bool {
	if len(c) != 3 {
		return false
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func parseDate(field, raw string) (time.Time, error) {
	t, err := time.Parse(dateLayout, raw)
	if err != nil {
		return time.Time{}, invalid("%s must be an ISO date (YYYY-MM-DD)", field)
	}
	return t, nil
}

func parseDecimal(field, raw string, maxScale int32) (decimal.Decimal, error) {
	if raw == "" || len(raw) > maxRawLength {
		return decimal.Zero, invalid("%s must be a decimal string of 1 to %d characters", field, maxRawLength)
	}
	dot := -1
	for i, c := range raw {
		switch {
		case c >= '0' && c <= '9':
		case c == '.' && dot < 0 && i > 0 && i < len(raw)-1:
			dot = i
		default:
			return decimal.Zero, invalid("%s must be a non-negative decimal string", field)
		}
	}
	integerDigits := len(raw)
	if dot >= 0 {
		integerDigits = dot
	}
	d, err := decimal.NewFromString(raw)
	if err != nil || integerDigits > maxIntegerDigit {
		return decimal.Zero, invalid("%s must be a decimal string with at most %d integer digits", field, maxIntegerDigit)
	}
	if -d.Exponent() > maxScale {
		return decimal.Zero, invalid("%s has more than %d decimal places", field, maxScale)
	}
	return d, nil
}

func parseAmount(field, raw, currency string) (decimal.Decimal, error) {
	d, err := parseDecimal(field, raw, money.MinorUnits(currency))
	if err != nil {
		return decimal.Zero, err
	}
	if !d.IsPositive() {
		return decimal.Zero, invalid("%s must be greater than zero", field)
	}
	return d, nil
}

func parseRate(field, raw, currency, accounting string) (decimal.Decimal, error) {
	if raw == "" {
		if currency != accounting {
			return decimal.Zero, &Error{Code: CodeCurrency, Detail: fmt.Sprintf("%s is required to convert %s to %s", field, currency, accounting)}
		}
		return decimal.NewFromInt(1), nil
	}
	r, err := parseDecimal(field, raw, maxRateScale)
	if err != nil {
		return decimal.Zero, err
	}
	if !r.IsPositive() {
		return decimal.Zero, invalid("%s must be greater than zero", field)
	}
	if currency == accounting && !r.Equal(decimal.NewFromInt(1)) {
		return decimal.Zero, &Error{Code: CodeCurrency, Detail: fmt.Sprintf("%s must be 1 when the currency is the accounting currency", field)}
	}
	return r, nil
}

func round(v decimal.Decimal, currency string) decimal.Decimal {
	return money.Amount{Value: v, Currency: currency}.Round().Value
}

func orderInvoices(in []Invoice) []Invoice {
	out := append([]Invoice(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Marked != b.Marked {
			return a.Marked
		}
		if !a.DueDate.Equal(b.DueDate) {
			return a.DueDate.Before(b.DueDate)
		}
		if c := a.Amount.Cmp(b.Amount); c != 0 {
			return c > 0
		}
		return a.ID < b.ID
	})
	return out
}

func fullDiscount(req Request, inv Invoice) decimal.Decimal {
	eligible := inv.Always || (!inv.DiscountDate.IsZero() && !req.SettlementDate.After(inv.DiscountDate))
	if !eligible || inv.DiscountPct.IsZero() {
		return decimal.Zero
	}
	d := round(inv.Amount.Mul(inv.DiscountPct).Div(decimal.NewFromInt(100)), inv.Currency)
	return decimal.Min(d, inv.Open)
}

type outcome struct {
	applied, discount, writeOff             decimal.Decimal
	appliedAcct, discountAcct, writeOffAcct decimal.Decimal
	bankAcct, fx, consumed                  decimal.Decimal
}

func settleInvoice(req Request, inv Invoice, credit decimal.Decimal) outcome {
	acct := req.AccountingCurrency
	full := fullDiscount(req, inv)
	net := inv.Open.Sub(full)
	var o outcome
	var partial bool
	if inv.Currency == req.CreditCurrency {
		o.applied = decimal.Min(credit, net)
		partial = o.applied.LessThan(net)
		o.consumed = o.applied
		o.appliedAcct = round(o.applied.Mul(inv.Rate), acct)
		o.bankAcct = round(o.applied.Mul(req.CreditRate), acct)
		o.fx = o.bankAcct.Sub(o.appliedAcct)
	} else {
		available := round(credit.Mul(req.CreditRate), acct)
		netAcct := round(net.Mul(inv.Rate), acct)
		used := decimal.Min(available, netAcct)
		partial = used.LessThan(netAcct)
		o.applied = net
		if partial {
			o.applied = round(used.Div(inv.Rate), inv.Currency)
		}
		o.consumed = credit
		if used.LessThan(available) {
			o.consumed = decimal.Min(credit, round(used.Div(req.CreditRate), req.CreditCurrency))
		}
		o.appliedAcct, o.bankAcct = used, used
	}
	if !o.applied.IsPositive() {
		return outcome{}
	}
	o.discount = full
	if partial {
		o.discount = decimal.Zero
		if req.Policy.DiscountOnPartial {
			o.discount = round(full.Mul(o.applied).Div(inv.Amount), inv.Currency)
		}
	}
	residual := inv.Open.Sub(o.applied).Sub(o.discount)
	if req.Policy.AutoWriteOff && residual.IsPositive() && residual.LessThanOrEqual(req.Policy.WriteOffLimit) {
		o.writeOff = residual
	}
	o.discountAcct = round(o.discount.Mul(inv.Rate), acct)
	o.writeOffAcct = round(o.writeOff.Mul(inv.Rate), acct)
	return o
}

func Settle(req Request) (Result, error) {
	acct := req.AccountingCurrency
	remaining := req.Credit
	res := Result{Lines: []Line{}, Journal: []JournalLine{}}
	var ap, bank, discount, writeOff, fx decimal.Decimal
	for _, inv := range orderInvoices(req.Invoices) {
		if !remaining.IsPositive() {
			break
		}
		o := settleInvoice(req, inv, remaining)
		if o.applied.IsZero() {
			continue
		}
		remaining = remaining.Sub(o.consumed)
		ap = ap.Add(o.appliedAcct).Add(o.discountAcct).Add(o.writeOffAcct)
		bank, discount, writeOff, fx = bank.Add(o.bankAcct), discount.Add(o.discountAcct), writeOff.Add(o.writeOffAcct), fx.Add(o.fx)
		open := inv.Open.Sub(o.applied).Sub(o.discount).Sub(o.writeOff)
		status := StatusPartiallySettled
		if open.IsZero() {
			status = StatusSettled
		}
		res.Lines = append(res.Lines, Line{
			InvoiceID: inv.ID, Applied: toMoney(o.applied, inv.Currency), Discount: toMoney(o.discount, inv.Currency),
			WriteOff: toMoney(o.writeOff, inv.Currency), AppliedAccounting: toMoney(o.appliedAcct, acct),
			FXDifference: toMoney(o.fx, acct), OpenAfter: toMoney(open, inv.Currency), Status: status,
		})
	}
	res.CreditRemaining = toMoney(remaining, req.CreditCurrency)
	res.Journal = journal(acct, ap, bank, discount, writeOff, fx)
	if !balanced(res.Journal) {
		return Result{}, errors.New("domain: settlement journal does not balance")
	}
	return res, nil
}

func journal(acct string, ap, bank, discount, writeOff, fx decimal.Decimal) []JournalLine {
	candidates := []JournalLine{
		{AccountAPControl, SideDebit, toMoney(ap, acct)},
		{AccountBankClearing, SideCredit, toMoney(bank, acct)},
		{AccountDiscountTaken, SideCredit, toMoney(discount, acct)},
		{AccountWriteOff, SideCredit, toMoney(writeOff, acct)},
	}
	switch {
	case fx.IsPositive():
		candidates = append(candidates, JournalLine{AccountFXLoss, SideDebit, toMoney(fx, acct)})
	case fx.IsNegative():
		candidates = append(candidates, JournalLine{AccountFXGain, SideCredit, toMoney(fx.Neg(), acct)})
	}
	lines := []JournalLine{}
	for _, l := range candidates {
		if l.Amount.Amount != toMoney(decimal.Zero, acct).Amount {
			lines = append(lines, l)
		}
	}
	return lines
}

func balanced(lines []JournalLine) bool {
	var net decimal.Decimal
	for _, l := range lines {
		v := decimal.RequireFromString(l.Amount.Amount)
		if l.Side == SideCredit {
			v = v.Neg()
		}
		net = net.Add(v)
	}
	return net.IsZero()
}

func toMoney(v decimal.Decimal, currency string) Money {
	return Money{Amount: v.StringFixed(money.MinorUnits(currency)), Currency: currency}
}
