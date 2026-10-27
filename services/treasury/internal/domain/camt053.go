package domain

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// CodeStatementAccount is raised when the statement's account (IBAN or the
// bank's account number) is not the bank account the upload was made against.
const CodeStatementAccount = "TRS-0409"

var (
	camtVersion = regexp.MustCompile(`camt\.053\.001\.\d{2}`)
	dateOnly    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// camtDoc matches elements by local name, so the ISO 20022 namespace version
// (camt.053.001.02 or .08) does not need a separate struct.
type camtDoc struct {
	XMLName xml.Name   `xml:""`
	Stmts   []camtStmt `xml:"BkToCstmrStmt>Stmt"`
}

type camtStmt struct {
	ID      string     `xml:"Id"`
	Seq     string     `xml:"ElctrncSeqNb"`
	IBAN    string     `xml:"Acct>Id>IBAN"`
	Other   string     `xml:"Acct>Id>Othr>Id"`
	Ccy     string     `xml:"Acct>Ccy"`
	Bals    []camtBal  `xml:"Bal"`
	Entries []camtNtry `xml:"Ntry"`
}

type camtAmt struct {
	Ccy   string `xml:"Ccy,attr"`
	Value string `xml:",chardata"`
}

type camtBal struct {
	Code string  `xml:"Tp>CdOrPrtry>Cd"`
	Amt  camtAmt `xml:"Amt"`
	Ind  string  `xml:"CdtDbtInd"`
	Date string  `xml:"Dt>Dt"`
}

type camtNtry struct {
	Amt       camtAmt `xml:"Amt"`
	Ind       string  `xml:"CdtDbtInd"`
	Booking   string  `xml:"BookgDt>Dt"`
	Value     string  `xml:"ValDt>Dt"`
	Domain    string  `xml:"BkTxCd>Domn>Cd"`
	Family    string  `xml:"BkTxCd>Domn>Fmly>Cd"`
	SubFamily string  `xml:"BkTxCd>Domn>Fmly>SubFmlyCd"`
	EndToEnd  string  `xml:"NtryDtls>TxDtls>Refs>EndToEndId"`
	Remit     string  `xml:"NtryDtls>TxDtls>RmtInf>Ustrd"`
	Creditor  string  `xml:"NtryDtls>TxDtls>RltdPties>Cdtr>Nm"`
	Debtor    string  `xml:"NtryDtls>TxDtls>RltdPties>Dbtr>Nm"`
	Inner     string  `xml:",innerxml"`
}

// ParseCAMT053 parses a camt.053 file into one Statement per Stmt element.
// Structural problems are TRS-0493 with the entry number when it is known;
// a statement whose balances do not add up is TRS-0494.
func ParseCAMT053(data []byte) ([]Statement, error) {
	var doc camtDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, formatError(0, "XML is not well formed: "+err.Error())
	}
	version := camtVersion.FindString(doc.XMLName.Space)
	if version == "" {
		return nil, formatError(0, "root namespace is not camt.053.001.xx")
	}
	if len(doc.Stmts) == 0 {
		return nil, formatError(0, "no Stmt element in BkToCstmrStmt")
	}
	out := make([]Statement, 0, len(doc.Stmts))
	for _, s := range doc.Stmts {
		st, err := s.statement(version)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// entryError is TRS-0493 at the given camt.053 entry (0 for the statement header).
func entryError(entry int, detail string) *Error {
	return &Error{Code: CodeStatementFormat, Detail: detail, Entry: entry}
}

// formatError is TRS-0493 at the given line of a text format (0 when the
// problem is not tied to one line).
func formatError(line int, detail string) *Error {
	return &Error{Code: CodeStatementFormat, Detail: detail, Line: line}
}

func (s camtStmt) statement(format string) (Statement, error) {
	account := s.IBAN
	if strings.TrimSpace(account) == "" {
		account = s.Other // a non-IBAN account (for example a US account number)
	}
	st := Statement{Format: format, StatementID: strings.TrimSpace(s.ID), Account: NormalizeIBAN(account), Currency: strings.TrimSpace(s.Ccy)}
	if st.StatementID == "" {
		return Statement{}, formatError(0, "Stmt/Id is required")
	}
	if seq := strings.TrimSpace(s.Seq); seq != "" {
		n, err := strconv.Atoi(seq)
		if err != nil || n < 0 {
			return Statement{}, formatError(0, "ElctrncSeqNb must be a non-negative integer")
		}
		st.Sequence = n
	}
	if len(s.Entries) > MaxEntries {
		return Statement{}, formatError(0, fmt.Sprintf("more than %d entries", MaxEntries))
	}
	if err := st.setBalances(s.Bals); err != nil {
		return Statement{}, err
	}
	if st.Currency == "" {
		return Statement{}, formatError(0, "Acct/Ccy is required")
	}
	for i, e := range s.Entries {
		line, err := e.line(i+1, st.Currency)
		if err != nil {
			return Statement{}, err
		}
		st.Lines = append(st.Lines, line)
	}
	if err := CheckBalances(st); err != nil {
		return Statement{}, err
	}
	return st, nil
}

func (st *Statement) setBalances(bals []camtBal) error {
	var seenOpen, seenClose bool
	for _, b := range bals {
		switch strings.TrimSpace(b.Code) {
		case "OPBD":
			amt, err := signedAmount(b.Amt, b.Ind, 0)
			if err != nil {
				return err
			}
			st.OpeningAmount, st.OpeningDate, seenOpen = amt, b.Date, true
			if st.Currency == "" {
				st.Currency = b.Amt.Ccy
			}
		case "CLBD":
			amt, err := signedAmount(b.Amt, b.Ind, 0)
			if err != nil {
				return err
			}
			st.ClosingAmount, st.ClosingDate, seenClose = amt, b.Date, true
		}
	}
	if !seenOpen || !seenClose {
		return formatError(0, "OPBD and CLBD balances are both required")
	}
	for _, d := range []string{st.OpeningDate, st.ClosingDate} {
		if !validDate(d) {
			return formatError(0, "balance date must be YYYY-MM-DD")
		}
	}
	return nil
}

func (e camtNtry) line(no int, currency string) (Line, error) {
	amt, err := signedAmount(e.Amt, e.Ind, no)
	if err != nil {
		return Line{}, err
	}
	if e.Amt.Ccy != currency {
		return Line{}, entryError(no, fmt.Sprintf("entry currency %s differs from statement currency %s", e.Amt.Ccy, currency))
	}
	if !validDate(e.Booking) {
		return Line{}, entryError(no, "BookgDt/Dt must be YYYY-MM-DD")
	}
	value := e.Value
	if value == "" {
		value = e.Booking
	}
	if !validDate(value) {
		return Line{}, entryError(no, "ValDt/Dt must be YYYY-MM-DD")
	}
	counterparty := e.Debtor
	if e.Ind == "DBIT" {
		counterparty = e.Creditor
	}
	return Line{
		LineNo:           no,
		BookingDate:      e.Booking,
		ValueDate:        value,
		Amount:           amt,
		Currency:         currency,
		BTCCode:          strings.Join(nonEmpty(e.Domain, e.Family, e.SubFamily), "/"),
		EndToEndRef:      strings.TrimSpace(e.EndToEnd),
		Reference:        strings.TrimSpace(e.Remit),
		CounterpartyName: strings.TrimSpace(counterparty),
		RawFragment:      "<Ntry>" + e.Inner + "</Ntry>",
	}, nil
}

// signedAmount applies CdtDbtInd: CRDT is positive, DBIT negative.
func signedAmount(a camtAmt, ind string, entry int) (decimal.Decimal, error) {
	v, err := decimal.NewFromString(strings.TrimSpace(a.Value))
	if err != nil || v.IsNegative() {
		return decimal.Zero, entryError(entry, "amount must be a non-negative decimal")
	}
	switch strings.TrimSpace(ind) {
	case "CRDT":
		return v, nil
	case "DBIT":
		return v.Neg(), nil
	default:
		return decimal.Zero, entryError(entry, "CdtDbtInd must be CRDT or DBIT")
	}
}

func validDate(s string) bool {
	if !dateOnly.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func nonEmpty(parts ...string) []string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
