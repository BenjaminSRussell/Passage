package domain

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// camt.052 is the bank-to-customer account report: the intraday balances of an account (FR-36.8). It carries
// balances (opening booked, interim booked, interim and closing available, forward available) and the
// entries booked so far; the cash position uses the balances and ignores the entries, which arrive again
// in the day's camt.053 statement.

var camt052Version = regexp.MustCompile(`camt\.052\.001\.\d{2}`)

type camt052Doc struct {
	XMLName xml.Name    `xml:""`
	Reports []camt052Rp `xml:"BkToCstmrAcctRpt>Rpt"`
}

type camt052Rp struct {
	ID      string      `xml:"Id"`
	Created string      `xml:"CreDtTm"`
	IBAN    string      `xml:"Acct>Id>IBAN"`
	Other   string      `xml:"Acct>Id>Othr>Id"`
	Ccy     string      `xml:"Acct>Ccy"`
	Bals    []camt052Bl `xml:"Bal"`
}

type camt052Bl struct {
	Code     string  `xml:"Tp>CdOrPrtry>Cd"`
	Amt      camtAmt `xml:"Amt"`
	Ind      string  `xml:"CdtDbtInd"`
	Date     string  `xml:"Dt>Dt"`
	DateTime string  `xml:"Dt>DtTm"`
}

// IntradayBalanceCodes are the camt.052 balance types kept: opening booked, interim booked, interim available,
// closing available, forward available, previously closed booked and closing booked. Others are ignored.
var IntradayBalanceCodes = map[string]bool{"OPBD": true, "ITBD": true, "ITAV": true, "CLAV": true, "FWAV": true, "PRCD": true, "CLBD": true}

// IntradayBalance is one signed balance of a report.
type IntradayBalance struct {
	Code   string
	Amount decimal.Decimal // signed: credit balances positive, debit balances negative
	AsOf   time.Time       // the balance's own date or time; the report's creation time when it has none
}

// IntradayReport is one parsed camt.052 report: the balances of one account at one moment.
type IntradayReport struct {
	Format    string
	ReportID  string
	CreatedAt time.Time
	Account   string // IBAN or the bank's own account identifier
	Currency  string
	Balances  []IntradayBalance
}

// ParseCAMT052 parses a camt.052 file into one IntradayReport per Rpt element. Structural problems are TRS-0493.
func ParseCAMT052(data []byte) ([]IntradayReport, error) {
	var doc camt052Doc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, formatError(0, "XML is not well formed: "+err.Error())
	}
	version := camt052Version.FindString(doc.XMLName.Space)
	if version == "" {
		return nil, formatError(0, "root namespace is not camt.052.001.xx")
	}
	if len(doc.Reports) == 0 {
		return nil, formatError(0, "no Rpt element in BkToCstmrAcctRpt")
	}
	out := make([]IntradayReport, 0, len(doc.Reports))
	for _, r := range doc.Reports {
		rep, err := r.report(version)
		if err != nil {
			return nil, err
		}
		out = append(out, rep)
	}
	return out, nil
}

func (r camt052Rp) report(format string) (IntradayReport, error) {
	account := strings.TrimSpace(r.IBAN)
	if account == "" {
		account = strings.TrimSpace(r.Other)
	}
	rep := IntradayReport{Format: format, ReportID: strings.TrimSpace(r.ID), Account: NormalizeIBAN(account), Currency: strings.TrimSpace(r.Ccy)}
	if rep.ReportID == "" {
		return IntradayReport{}, formatError(0, "Rpt/Id is required")
	}
	if rep.Account == "" {
		return IntradayReport{}, formatError(0, "Rpt/Acct/Id is required")
	}
	if rep.Currency == "" {
		return IntradayReport{}, formatError(0, "Rpt/Acct/Ccy is required")
	}
	created, err := parseBankTime(r.Created)
	if err != nil {
		return IntradayReport{}, formatError(0, "Rpt/CreDtTm must be an ISO 8601 date and time")
	}
	rep.CreatedAt = created
	for _, b := range r.Bals {
		code := strings.TrimSpace(b.Code)
		if !IntradayBalanceCodes[code] {
			continue
		}
		amt, err := signedAmount(b.Amt, b.Ind, 0)
		if err != nil {
			return IntradayReport{}, err
		}
		if b.Amt.Ccy != rep.Currency {
			return IntradayReport{}, formatError(0, fmt.Sprintf("%s balance currency %s differs from account currency %s", code, b.Amt.Ccy, rep.Currency))
		}
		asOf := created
		switch {
		case strings.TrimSpace(b.DateTime) != "":
			if asOf, err = parseBankTime(b.DateTime); err != nil {
				return IntradayReport{}, formatError(0, "balance date and time must be ISO 8601")
			}
		case strings.TrimSpace(b.Date) != "":
			if !validDate(strings.TrimSpace(b.Date)) {
				return IntradayReport{}, formatError(0, "balance date must be YYYY-MM-DD")
			}
			asOf, _ = time.Parse("2006-01-02", strings.TrimSpace(b.Date))
		}
		rep.Balances = append(rep.Balances, IntradayBalance{Code: code, Amount: amt, AsOf: asOf.UTC()})
	}
	if len(rep.Balances) == 0 {
		return IntradayReport{}, formatError(0, "the report holds no OPBD, ITBD, ITAV, CLAV, FWAV, PRCD or CLBD balance")
	}
	return rep, nil
}

// parseBankTime reads an ISO 8601 date and time with or without an offset (no offset is UTC).
func parseBankTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("not a date and time: %q", s)
}

// PositionSource names where an account's position came from.
const (
	SourceIntraday  = "camt.052"
	SourceStatement = "camt.053"
	SourceNone      = "none"
)

// DefaultStaleHours is the position.staleHours default: a position older than this is marked stale.
const DefaultStaleHours = 24

// StatementBalance is the closing balance of the account's latest statement.
type StatementBalance struct {
	ClosingDate   string // the day the balance closes, YYYY-MM-DD
	ClosingAmount decimal.Decimal
}

// PositionInput is what is known about one bank account. Either part may be missing.
type PositionInput struct {
	Account   string // the account the position is for, as the caller names it
	Currency  string
	Statement *StatementBalance
	Intraday  *IntradayReport
}

// AccountPosition is the booked and available balance of one account and where it comes from.
type AccountPosition struct {
	Account   string          `json:"account"`
	Currency  string          `json:"currency"`
	Booked    decimal.Decimal `json:"booked"`
	Available decimal.Decimal `json:"available"`
	Source    string          `json:"source"`
	AsOf      *time.Time      `json:"asOf,omitempty"`
	Stale     bool            `json:"stale"`
}

// Position picks the freshest figure of the account: the intraday report when it was created after the
// latest statement closed (the statement's balance is as of the end of its closing day), otherwise the
// statement. Booked is the interim booked balance (or the opening booked one when the report has no interim);
// available is the interim or closing available balance and falls back to booked. A position older than
// staleHours at now is stale; an account with neither report nor statement has source none and is stale.
func Position(in PositionInput, now time.Time, staleHours int) AccountPosition {
	pos := AccountPosition{Account: in.Account, Currency: in.Currency, Source: SourceNone, Stale: true}
	var stmtAt time.Time
	if in.Statement != nil {
		if d, err := time.Parse("2006-01-02", in.Statement.ClosingDate); err == nil {
			stmtAt = d.AddDate(0, 0, 1).UTC()
		}
	}
	if in.Intraday != nil && (in.Statement == nil || in.Intraday.CreatedAt.After(stmtAt)) {
		booked, hasBooked := balanceOf(in.Intraday, "ITBD", "OPBD", "CLBD")
		available, hasAvail := balanceOf(in.Intraday, "ITAV", "CLAV")
		if hasBooked || hasAvail {
			if !hasBooked {
				booked = available
			}
			if !hasAvail {
				available = booked
			}
			at := in.Intraday.CreatedAt
			pos.Booked, pos.Available, pos.Source, pos.AsOf = booked, available, SourceIntraday, &at
		}
	}
	if pos.Source == SourceNone && in.Statement != nil {
		at := stmtAt
		pos.Booked, pos.Available, pos.Source, pos.AsOf = in.Statement.ClosingAmount, in.Statement.ClosingAmount, SourceStatement, &at
	}
	if pos.AsOf != nil {
		pos.Stale = now.Sub(*pos.AsOf) > time.Duration(staleHours)*time.Hour
	}
	return pos
}

// balanceOf returns the first of the codes the report holds, latest balance first when a code repeats.
func balanceOf(r *IntradayReport, codes ...string) (decimal.Decimal, bool) {
	for _, code := range codes {
		var best *IntradayBalance
		for i := range r.Balances {
			b := &r.Balances[i]
			if b.Code == code && (best == nil || b.AsOf.After(best.AsOf)) {
				best = b
			}
		}
		if best != nil {
			return best.Amount, true
		}
	}
	return decimal.Zero, false
}

// CurrencyTotal is the sum of the positions in one currency.
type CurrencyTotal struct {
	Currency  string          `json:"currency"`
	Booked    decimal.Decimal `json:"booked"`
	Available decimal.Decimal `json:"available"`
}

// Totals sums booked and available per currency, never across currencies, in currency order.
func Totals(positions []AccountPosition) []CurrencyTotal {
	by := map[string]*CurrencyTotal{}
	var order []string
	for _, p := range positions {
		t, ok := by[p.Currency]
		if !ok {
			t = &CurrencyTotal{Currency: p.Currency}
			by[p.Currency] = t
			order = append(order, p.Currency)
		}
		t.Booked = t.Booked.Add(p.Booked)
		t.Available = t.Available.Add(p.Available)
	}
	out := make([]CurrencyTotal, 0, len(order))
	for _, c := range sortedStrings(order) {
		out = append(out, *by[c])
	}
	return out
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
