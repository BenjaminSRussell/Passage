package saft

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

const (
	Namespace      = "urn:OECD:StandardAuditFile-Tax:2.00"
	FileVersion    = "2.00"
	softwareName   = "Passage"
	softwareID     = "passage-tax"
	softwareBuild  = "1"
	dateLayout     = "2006-01-02"
	dateTimeLayout = "2006-01-02T15:04:05"
	maxNameLength  = 200
)

var (
	countryPattern  = regexp.MustCompile(`^[A-Z]{2}$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
)

type Entry struct {
	Code      string
	Direction string
	Amount    decimal.Decimal
	Count     int
}

type Input struct {
	CompanyName        string
	RegistrationNumber string
	Country            string
	Currency           string
	Period             domain.Period
	Created            time.Time
	Entries            []Entry
	Accounts           []Account
	Customers          []Party
	Suppliers          []Party
	Journals           []Journal
}

type Account struct {
	ID, Description, Type string
	Opening, Closing      decimal.Decimal
}

type Party struct{ ID, AccountID, TaxID, Name string }

type Line struct {
	RecordID, AccountID, Description string
	CustomerID, SupplierID           string
	Debit, Credit                    decimal.Decimal
}

type Transaction struct {
	ID, Description string
	Date            time.Time
	Lines           []Line
}

type Journal struct {
	ID, Description, Type string
	Transactions          []Transaction
}

type auditFile struct {
	XMLName     xml.Name    `xml:"urn:OECD:StandardAuditFile-Tax:2.00 AuditFile"`
	Header      header      `xml:"Header"`
	MasterFiles masterFiles `xml:"MasterFiles"`
	GLEntries   *glEntries  `xml:"GeneralLedgerEntries"`
}

type header struct {
	AuditFileVersion     string      `xml:"AuditFileVersion"`
	AuditFileCountry     string      `xml:"AuditFileCountry"`
	AuditFileDateCreated string      `xml:"AuditFileDateCreated"`
	SoftwareCompanyName  string      `xml:"SoftwareCompanyName"`
	SoftwareID           string      `xml:"SoftwareID"`
	SoftwareVersion      string      `xml:"SoftwareVersion"`
	Company              company     `xml:"Company"`
	DefaultCurrencyCode  string      `xml:"DefaultCurrencyCode"`
	SelectionCriteria    selectionOf `xml:"SelectionCriteria"`
}

type company struct {
	RegistrationNumber string `xml:"RegistrationNumber"`
	Name               string `xml:"Name"`
}

type selectionOf struct {
	PeriodStart string `xml:"PeriodStart"`
	PeriodEnd   string `xml:"PeriodEnd"`
}

type masterFiles struct {
	Accounts  *accountList  `xml:"GeneralLedgerAccounts"`
	Customers *customerList `xml:"Customers"`
	Suppliers *supplierList `xml:"Suppliers"`
	TaxTable  taxTable      `xml:"TaxTable"`
}

type accountList struct {
	Items []accountXML `xml:"Account"`
}

type accountXML struct {
	AccountID          string `xml:"AccountID"`
	AccountDescription string `xml:"AccountDescription"`
	AccountType        string `xml:"AccountType"`
	OpeningDebit       string `xml:"OpeningDebitBalance,omitempty"`
	OpeningCredit      string `xml:"OpeningCreditBalance,omitempty"`
	ClosingDebit       string `xml:"ClosingDebitBalance,omitempty"`
	ClosingCredit      string `xml:"ClosingCreditBalance,omitempty"`
}

type amountXML struct {
	Amount string `xml:"Amount"`
}

type customerList struct {
	Items []customer `xml:"Customer"`
}

type supplierList struct {
	Items []supplier `xml:"Supplier"`
}

type customer struct {
	CustomerID    string `xml:"CustomerID"`
	AccountID     string `xml:"AccountID"`
	CustomerTaxID string `xml:"CustomerTaxID"`
	CompanyName   string `xml:"CompanyName"`
}

type supplier struct {
	SupplierID    string `xml:"SupplierID"`
	AccountID     string `xml:"AccountID"`
	SupplierTaxID string `xml:"SupplierTaxID"`
	CompanyName   string `xml:"CompanyName"`
}

type glEntries struct {
	NumberOfEntries int          `xml:"NumberOfEntries"`
	TotalDebit      string       `xml:"TotalDebit"`
	TotalCredit     string       `xml:"TotalCredit"`
	Journals        []journalXML `xml:"Journal"`
}

type journalXML struct {
	JournalID    string           `xml:"JournalID"`
	Description  string           `xml:"Description"`
	Type         string           `xml:"Type"`
	Transactions []transactionXML `xml:"Transaction"`
}

type transactionXML struct {
	TransactionID   string    `xml:"TransactionID"`
	Period          int       `xml:"Period"`
	PeriodYear      int       `xml:"PeriodYear"`
	TransactionDate string    `xml:"TransactionDate"`
	Description     string    `xml:"Description"`
	SystemEntryDate string    `xml:"SystemEntryDate"`
	GLPostingDate   string    `xml:"GLPostingDate"`
	Lines           []lineXML `xml:"Line"`
}

type lineXML struct {
	RecordID     string     `xml:"RecordID"`
	AccountID    string     `xml:"AccountID"`
	Description  string     `xml:"Description"`
	DebitAmount  *amountXML `xml:"DebitAmount"`
	CreditAmount *amountXML `xml:"CreditAmount"`
	CustomerID   string     `xml:"CustomerID,omitempty"`
	SupplierID   string     `xml:"SupplierID,omitempty"`
}

type taxTable struct {
	Entries []taxTableEntry `xml:"TaxTableEntry"`
}

type taxTableEntry struct {
	TaxType              string `xml:"TaxType"`
	TaxCode              string `xml:"TaxCode"`
	TaxAmount            string `xml:"TaxAmount"`
	NumberOfTransactions int    `xml:"NumberOfTransactions"`
}

func Build(in Input) ([]byte, error) {
	if err := Validate(in); err != nil {
		return nil, err
	}
	doc := auditFile{
		Header: header{
			AuditFileVersion: FileVersion, AuditFileCountry: in.Country, AuditFileDateCreated: in.Created.UTC().Format(dateLayout),
			SoftwareCompanyName: softwareName, SoftwareID: softwareID, SoftwareVersion: softwareBuild,
			Company:             company{RegistrationNumber: in.RegistrationNumber, Name: in.CompanyName},
			DefaultCurrencyCode: in.Currency,
			SelectionCriteria:   selectionOf{PeriodStart: in.Period.From.Format(dateLayout), PeriodEnd: in.Period.To.Format(dateLayout)},
		},
	}
	for _, e := range in.Entries {
		doc.MasterFiles.TaxTable.Entries = append(doc.MasterFiles.TaxTable.Entries,
			taxTableEntry{TaxType: e.Direction, TaxCode: e.Code, TaxAmount: e.Amount.String(), NumberOfTransactions: e.Count})
	}
	doc.addAccounts(in)
	doc.addParties(in)
	doc.addLedger(in)
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal SAF-T: %w", err)
	}
	out := append([]byte(xml.Header), body...)
	if err := CheckDocument(out); err != nil {
		return nil, err
	}
	return out, nil
}

func balanceSides(net decimal.Decimal) (debit, credit string) {
	if net.IsNegative() {
		return "", net.Neg().String()
	}
	return net.String(), ""
}

func (d *auditFile) addAccounts(in Input) {
	if len(in.Accounts) == 0 {
		return
	}
	d.MasterFiles.Accounts = &accountList{}
	for _, a := range in.Accounts {
		x := accountXML{AccountID: a.ID, AccountDescription: a.Description, AccountType: a.Type}
		x.OpeningDebit, x.OpeningCredit = balanceSides(a.Opening)
		x.ClosingDebit, x.ClosingCredit = balanceSides(a.Closing)
		d.MasterFiles.Accounts.Items = append(d.MasterFiles.Accounts.Items, x)
	}
}

func (d *auditFile) addParties(in Input) {
	if len(in.Customers) > 0 {
		d.MasterFiles.Customers = &customerList{}
		for _, c := range in.Customers {
			d.MasterFiles.Customers.Items = append(d.MasterFiles.Customers.Items, customer{c.ID, c.AccountID, c.TaxID, c.Name})
		}
	}
	if len(in.Suppliers) > 0 {
		d.MasterFiles.Suppliers = &supplierList{}
		for _, s := range in.Suppliers {
			d.MasterFiles.Suppliers.Items = append(d.MasterFiles.Suppliers.Items, supplier{s.ID, s.AccountID, s.TaxID, s.Name})
		}
	}
}

func (d *auditFile) addLedger(in Input) {
	if len(in.Journals) == 0 {
		return
	}
	gl := &glEntries{}
	debit, credit := decimal.Zero, decimal.Zero
	for _, j := range in.Journals {
		jx := journalXML{JournalID: j.ID, Description: j.Description, Type: j.Type}
		for _, t := range j.Transactions {
			tx := transactionXML{TransactionID: t.ID, Period: int(t.Date.Month()), PeriodYear: t.Date.Year(), TransactionDate: t.Date.Format(dateLayout),
				Description: t.Description, SystemEntryDate: t.Date.Format(dateTimeLayout), GLPostingDate: t.Date.Format(dateLayout)}
			for _, l := range t.Lines {
				lx := lineXML{RecordID: l.RecordID, AccountID: l.AccountID, Description: l.Description, CustomerID: l.CustomerID, SupplierID: l.SupplierID}
				if !l.Debit.IsZero() {
					lx.DebitAmount = &amountXML{Amount: l.Debit.String()}
					debit = debit.Add(l.Debit)
				}
				if !l.Credit.IsZero() {
					lx.CreditAmount = &amountXML{Amount: l.Credit.String()}
					credit = credit.Add(l.Credit)
				}
				tx.Lines = append(tx.Lines, lx)
			}
			jx.Transactions = append(jx.Transactions, tx)
			gl.NumberOfEntries++
		}
		gl.Journals = append(gl.Journals, jx)
	}
	gl.TotalDebit, gl.TotalCredit = debit.String(), credit.String()
	d.GLEntries = gl
}

func Validate(in Input) error {
	switch {
	case strings.TrimSpace(in.CompanyName) == "" || len(in.CompanyName) > maxNameLength:
		return &domain.Error{Code: domain.CodeValidation, Field: "company_name", Detail: "company_name is required and at most 200 characters"}
	case strings.TrimSpace(in.RegistrationNumber) == "" || len(in.RegistrationNumber) > maxNameLength:
		return &domain.Error{Code: domain.CodeValidation, Field: "registration_number", Detail: "registration_number is required and at most 200 characters"}
	case !countryPattern.MatchString(in.Country):
		return &domain.Error{Code: domain.CodeValidation, Field: "country", Detail: "country must be a 2-letter ISO 3166 code"}
	case !currencyPattern.MatchString(in.Currency):
		return &domain.Error{Code: domain.CodeValidation, Field: "currency", Detail: "currency must be a 3-letter ISO 4217 code"}
	case in.Period.To.Before(in.Period.From.Time):
		return &domain.Error{Code: domain.CodeValidation, Field: "period_to", Detail: "period_to must not precede period_from"}
	}
	return nil
}
