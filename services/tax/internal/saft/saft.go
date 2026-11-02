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
	Namespace     = "urn:OECD:StandardAuditFile-Tax:2.00"
	FileVersion   = "2.00"
	softwareName  = "Passage"
	softwareID    = "passage-tax"
	softwareBuild = "1"
	dateLayout    = "2006-01-02"
	maxNameLength = 200
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
}

type auditFile struct {
	XMLName     xml.Name    `xml:"urn:OECD:StandardAuditFile-Tax:2.00 AuditFile"`
	Header      header      `xml:"Header"`
	MasterFiles masterFiles `xml:"MasterFiles"`
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
	TaxTable taxTable `xml:"TaxTable"`
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
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal SAF-T: %w", err)
	}
	return append([]byte(xml.Header), body...), nil
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
