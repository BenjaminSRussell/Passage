package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

type PostalAddressDTO struct {
	StreetName string `json:"streetName"`
	PostCode   string `json:"postCode"`
	TownName   string `json:"townName"`
	Country    string `json:"country"`
}

type PartyDTO struct {
	Name    string           `json:"name"`
	Address PostalAddressDTO `json:"address"`
}

type AccountDTO struct {
	IBAN    string `json:"iban"`
	OtherID string `json:"otherId"`
}

type CreditorDTO struct {
	PartyDTO
	AccountDTO
	BIC           string `json:"bic"`
	RoutingNumber string `json:"routingNumber"`
}

type RemittanceInvoiceDTO struct {
	Number string `json:"number"`
	Amount string `json:"amount"`
}

type Pain001PaymentDTO struct {
	Method        PaymentMethodDTO       `json:"method"`
	ExecutionDate string                 `json:"executionDate"`
	Number        string                 `json:"number"`
	Amount        string                 `json:"amount"`
	Currency      string                 `json:"currency"`
	Creditor      CreditorDTO            `json:"creditor"`
	Invoices      []RemittanceInvoiceDTO `json:"invoices"`
}

type Pain001RequestDTO struct {
	FileNumber    string              `json:"fileNumber"`
	Created       string              `json:"created"`
	Debtor        PartyDTO            `json:"debtor"`
	DebtorAccount AccountDTO          `json:"debtorAccount"`
	DebtorBIC     string              `json:"debtorBic"`
	Profile       BankProfileDTO      `json:"profile"`
	Payments      []Pain001PaymentDTO `json:"payments"`
}

func (a PostalAddressDTO) parse() PostalAddress {
	return PostalAddress{StreetName: a.StreetName, PostCode: a.PostCode, TownName: a.TownName, Country: a.Country}
}

func (p PartyDTO) parse() Pain001Party {
	return Pain001Party{Name: p.Name, Address: p.Address.parse()}
}

func (a AccountDTO) parse() Pain001Account {
	return Pain001Account{IBAN: a.IBAN, OtherID: a.OtherID}
}

func ParsePain001Request(dto Pain001RequestDTO) (Pain001Input, error) {
	created, err := time.Parse(time.RFC3339, dto.Created)
	if err != nil {
		return Pain001Input{}, invalid("created must be an RFC 3339 timestamp")
	}
	profile, err := ParseBankProfile(dto.Profile)
	if err != nil {
		return Pain001Input{}, err
	}
	in := Pain001Input{
		FileNumber: dto.FileNumber, Created: created, Debtor: dto.Debtor.parse(),
		DebtorAccount: dto.DebtorAccount.parse(), DebtorBIC: dto.DebtorBIC, Profile: profile,
	}
	for _, p := range dto.Payments {
		payment, err := parsePain001Payment(p)
		if err != nil {
			return Pain001Input{}, err
		}
		in.Payments = append(in.Payments, payment)
	}
	return in, nil
}

func parsePain001Payment(p Pain001PaymentDTO) (Pain001Payment, error) {
	method, err := ParsePaymentMethod(p.Method)
	if err != nil {
		return Pain001Payment{}, err
	}
	date, err := time.Parse(dateLayout, p.ExecutionDate)
	if err != nil {
		return Pain001Payment{}, invalid("payment %s: executionDate must be YYYY-MM-DD", p.Number)
	}
	amount, err := decimal.NewFromString(p.Amount)
	if err != nil {
		return Pain001Payment{}, invalid("payment %s: amount must be a decimal string", p.Number)
	}
	out := Pain001Payment{
		Method: method, ExecutionDate: date, Number: p.Number, Amount: amount, Currency: p.Currency,
		Creditor: Pain001Creditor{
			Party: p.Creditor.PartyDTO.parse(), Account: p.Creditor.AccountDTO.parse(),
			BIC: p.Creditor.BIC, RoutingNumber: p.Creditor.RoutingNumber,
		},
	}
	for _, inv := range p.Invoices {
		invAmount, err := decimal.NewFromString(inv.Amount)
		if err != nil {
			return Pain001Payment{}, invalid("payment %s: invoice amount must be a decimal string", p.Number)
		}
		out.Invoices = append(out.Invoices, Pain001Invoice{Number: inv.Number, Amount: invAmount})
	}
	return out, nil
}
