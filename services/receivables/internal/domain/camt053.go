package domain

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// CodeCAMTMalformed is the catalog's malformed-file code (AR-0004), which the
// camt.053 importer shares with BAI2: the detail names the line.
const CodeCAMTMalformed = CodeBAI2Malformed

// CAMTCredit is one booked customer credit (CdtDbtInd=CRDT) from an ISO 20022
// camt.053 statement, ready to become a CAMT053 payment (FR-30.5).
type CAMTCredit struct {
	Line          int
	StatementID   string
	Reference     string
	Currency      string
	Amount        decimal.Decimal
	ValueDate     string
	DebtorName    string
	RemittanceRef string
	// DebtorAccountHash is AccountHash of the debtor's IBAN or other account id
	// (RltdPties/DbtrAcct), for matcher rule R4; "" when the bank gave none.
	DebtorAccountHash string
}

// CAMTStatement is a parsed statement file. Skipped counts the entries that are
// not customer credits: debits, reversals and entries not yet booked.
type CAMTStatement struct {
	Statements int
	Credits    []CAMTCredit
	Skipped    int
}

// CAMTError is a malformed camt.053 document; Line is 1-based.
type CAMTError struct {
	Line    int
	Message string
}

func (e *CAMTError) Error() string { return fmt.Sprintf("Line %d: %s", e.Line, e.Message) }

type camtAmount struct {
	Ccy   string `xml:"Ccy,attr"`
	Value string `xml:",chardata"`
}

// camtDate holds a date given as Dt or as DtTm.
type camtDate struct {
	Dt   string `xml:"Dt"`
	DtTm string `xml:"DtTm"`
}

func (d camtDate) day() string {
	if v := strings.TrimSpace(d.Dt); v != "" {
		return v
	}
	if v := strings.TrimSpace(d.DtTm); len(v) >= 10 {
		return v[:10]
	}
	return ""
}

// camtParty reads a party name from camt.053.001.02 (Dbtr/Nm) or .001.08 (Dbtr/Pty/Nm).
type camtParty struct {
	Nm  string `xml:"Nm"`
	Pty struct {
		Nm string `xml:"Nm"`
	} `xml:"Pty"`
}

func (p camtParty) name() string {
	if v := strings.TrimSpace(p.Nm); v != "" {
		return v
	}
	return strings.TrimSpace(p.Pty.Nm)
}

type camtTx struct {
	RltdPties struct {
		Dbtr     camtParty `xml:"Dbtr"`
		DbtrAcct struct {
			ID struct {
				IBAN string `xml:"IBAN"`
				Othr struct {
					ID string `xml:"Id"`
				} `xml:"Othr"`
			} `xml:"Id"`
		} `xml:"DbtrAcct"`
	} `xml:"RltdPties"`
	RmtInf struct {
		Ustrd []string `xml:"Ustrd"`
		Strd  []struct {
			CdtrRefInf struct {
				Ref string `xml:"Ref"`
			} `xml:"CdtrRefInf"`
		} `xml:"Strd"`
	} `xml:"RmtInf"`
}

// camtStatus is the entry status: a code text in .001.02 (<Sts>BOOK</Sts>), a
// <Cd> child in .001.08.
type camtStatus struct {
	Text string `xml:",chardata"`
	Cd   string `xml:"Cd"`
}

func (s camtStatus) code() string {
	if v := strings.TrimSpace(s.Cd); v != "" {
		return v
	}
	return strings.TrimSpace(s.Text)
}

type camtEntry struct {
	NtryRef     string     `xml:"NtryRef"`
	Amt         camtAmount `xml:"Amt"`
	CdtDbtInd   string     `xml:"CdtDbtInd"`
	RvslInd     string     `xml:"RvslInd"`
	Sts         camtStatus `xml:"Sts"`
	BookgDt     camtDate   `xml:"BookgDt"`
	ValDt       camtDate   `xml:"ValDt"`
	AcctSvcrRef string     `xml:"AcctSvcrRef"`
	NtryDtls    []struct {
		TxDtls []camtTx `xml:"TxDtls"`
	} `xml:"NtryDtls"`
}

// ParseCAMT053 reads an ISO 20022 camt.053 statement file. It walks the document
// token by token so a malformed entry is reported with its line (AR-0004). Only
// booked, non-reversal CRDT entries become credits; the others are counted as skipped.
func ParseCAMT053(content []byte) (CAMTStatement, error) {
	var res CAMTStatement
	dec := xml.NewDecoder(bytes.NewReader(content))
	var stack []string
	stmtID := ""
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return CAMTStatement{}, &CAMTError{Line: lineAt(content, dec.InputOffset()), Message: "malformed XML: " + err.Error()}
		}
		switch t := tok.(type) {
		case xml.StartElement:
			parent := ""
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			line := lineAt(content, dec.InputOffset())
			switch {
			case t.Name.Local == "Stmt":
				res.Statements++
				stmtID = ""
				stack = append(stack, t.Name.Local)
			case t.Name.Local == "Id" && parent == "Stmt":
				var v string
				if err := dec.DecodeElement(&v, &t); err != nil {
					return CAMTStatement{}, &CAMTError{Line: line, Message: "malformed statement id"}
				}
				if stmtID = strings.TrimSpace(v); stmtID == "" || len(stmtID) > 35 {
					return CAMTStatement{}, &CAMTError{Line: line, Message: "statement Id must be 1 to 35 characters"}
				}
			case t.Name.Local == "Ntry" && parent == "Stmt":
				var e camtEntry
				if err := dec.DecodeElement(&e, &t); err != nil {
					return CAMTStatement{}, &CAMTError{Line: line, Message: "malformed entry: " + err.Error()}
				}
				if stmtID == "" {
					return CAMTStatement{}, &CAMTError{Line: line, Message: "entry appears before the statement Id"}
				}
				credit, isCredit, err := entryToCredit(e, stmtID, line)
				if err != nil {
					return CAMTStatement{}, err
				}
				if isCredit {
					res.Credits = append(res.Credits, credit)
				} else {
					res.Skipped++
				}
			default:
				stack = append(stack, t.Name.Local)
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if res.Statements == 0 {
		return CAMTStatement{}, &CAMTError{Line: 1, Message: "no statement (BkToCstmrStmt/Stmt) found"}
	}
	return res, nil
}

// entryToCredit validates one entry; isCredit is false for an entry that is not
// a booked customer credit.
func entryToCredit(e camtEntry, stmtID string, line int) (CAMTCredit, bool, error) {
	ind := strings.TrimSpace(e.CdtDbtInd)
	if ind != "CRDT" && ind != "DBIT" {
		return CAMTCredit{}, false, &CAMTError{Line: line, Message: "CdtDbtInd must be CRDT or DBIT"}
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(e.Amt.Value))
	if err != nil || !amount.IsPositive() {
		return CAMTCredit{}, false, &CAMTError{Line: line, Message: "Amt must be a positive decimal"}
	}
	ccy := strings.TrimSpace(e.Amt.Ccy)
	if len(ccy) != 3 || strings.ToUpper(ccy) != ccy {
		return CAMTCredit{}, false, &CAMTError{Line: line, Message: "Amt Ccy must be a 3-letter ISO 4217 code"}
	}
	valueDate := e.ValDt.day()
	if valueDate == "" {
		valueDate = e.BookgDt.day()
	}
	if _, err := time.Parse("2006-01-02", valueDate); err != nil {
		return CAMTCredit{}, false, &CAMTError{Line: line, Message: "ValDt (or BookgDt) must be a YYYY-MM-DD date"}
	}
	status := e.Sts.code()
	if ind == "DBIT" || strings.EqualFold(strings.TrimSpace(e.RvslInd), "true") || (status != "" && status != "BOOK") {
		return CAMTCredit{}, false, nil
	}
	ref := strings.TrimSpace(e.NtryRef)
	if ref == "" {
		ref = strings.TrimSpace(e.AcctSvcrRef)
	}
	if ref == "" || len(ref) > 35 {
		return CAMTCredit{}, false, &CAMTError{Line: line, Message: "a credit needs an NtryRef or AcctSvcrRef of 1 to 35 characters"}
	}
	credit := CAMTCredit{Line: line, StatementID: stmtID, Reference: ref, Currency: ccy, Amount: amount, ValueDate: valueDate}
	for _, d := range e.NtryDtls {
		for _, tx := range d.TxDtls {
			if credit.DebtorName == "" {
				credit.DebtorName = tx.RltdPties.Dbtr.name()
			}
			if credit.DebtorAccountHash == "" {
				acct := tx.RltdPties.DbtrAcct.ID.IBAN
				if strings.TrimSpace(acct) == "" {
					acct = tx.RltdPties.DbtrAcct.ID.Othr.ID
				}
				credit.DebtorAccountHash = AccountHash(acct)
			}
			if credit.RemittanceRef == "" {
				credit.RemittanceRef = remittanceRef(tx)
			}
		}
	}
	return credit, true, nil
}

// remittanceRef prefers a structured creditor reference, then the first unstructured line.
func remittanceRef(tx camtTx) string {
	for _, s := range tx.RmtInf.Strd {
		if ref := strings.TrimSpace(s.CdtrRefInf.Ref); ref != "" {
			return ref
		}
	}
	for _, u := range tx.RmtInf.Ustrd {
		if u = strings.TrimSpace(u); u != "" {
			return u
		}
	}
	return ""
}

// lineAt converts a byte offset into a 1-based line number.
func lineAt(content []byte, offset int64) int {
	if offset > int64(len(content)) {
		offset = int64(len(content))
	}
	return bytes.Count(content[:offset], []byte("\n")) + 1
}
