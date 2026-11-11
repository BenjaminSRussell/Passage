package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

const avalaraName = "avalara"

// Avalara is the AvaTax REST v2 adapter. A document with a DocumentRef is saved as an invoice (so Commit and Void can name it);
// without one it is a quote that AvaTax does not keep.
type Avalara struct {
	c           client
	accountID   string
	licenseKey  string
	companyCode string
}

func NewAvalara(baseURL, accountID, licenseKey, companyCode string, hc *http.Client) (*Avalara, error) {
	c, err := newClient(baseURL, hc)
	if err != nil {
		return nil, err
	}
	if accountID == "" || licenseKey == "" || companyCode == "" {
		return nil, errors.New("avalara needs an account id, a license key and a company code")
	}
	return &Avalara{c: c, accountID: accountID, licenseKey: licenseKey, companyCode: companyCode}, nil
}

// AvalaraFromEnv builds the adapter from AVALARA_URL, AVALARA_ACCOUNT_ID, AVALARA_LICENSE_KEY and AVALARA_COMPANY_CODE; the
// account id and license key come from a Secret. It returns nil, nil when AVALARA_URL is unset.
func AvalaraFromEnv(getenv func(string) string) (*Avalara, error) {
	base := getenv("AVALARA_URL")
	if base == "" {
		return nil, nil
	}
	return NewAvalara(base, getenv("AVALARA_ACCOUNT_ID"), getenv("AVALARA_LICENSE_KEY"), getenv("AVALARA_COMPANY_CODE"), nil)
}

func (a *Avalara) Name() string { return avalaraName }

func (a *Avalara) auth(r *http.Request) { r.SetBasicAuth(a.accountID, a.licenseKey) }

type avaLine struct {
	Number      string  `json:"number"`
	Quantity    int     `json:"quantity"`
	Amount      float64 `json:"amount"`
	TaxCode     string  `json:"taxCode,omitempty"`
	ItemCode    string  `json:"itemCode,omitempty"`
	TaxIncluded bool    `json:"taxIncluded,omitempty"`
}

type avaAddress struct {
	Country string `json:"country,omitempty"`
}

type avaCreate struct {
	Type         string                `json:"type"`
	CompanyCode  string                `json:"companyCode"`
	Date         string                `json:"date"`
	CustomerCode string                `json:"customerCode"`
	CurrencyCode string                `json:"currencyCode"`
	BusinessID   string                `json:"businessIdentificationNo,omitempty"`
	ExemptionNo  string                `json:"exemptionNo,omitempty"`
	Code         string                `json:"code,omitempty"`
	Commit       bool                  `json:"commit"`
	Lines        []avaLine             `json:"lines"`
	Addresses    map[string]avaAddress `json:"addresses"`
}

type avaDetail struct {
	JurisName string      `json:"jurisName"`
	JurisType string      `json:"jurisType"`
	TaxName   string      `json:"taxName"`
	Rate      json.Number `json:"rate"`
	Tax       json.Number `json:"tax"`
}

type avaResponseLine struct {
	LineNumber    string      `json:"lineNumber"`
	LineAmount    json.Number `json:"lineAmount"`
	TaxableAmount json.Number `json:"taxableAmount"`
	Tax           json.Number `json:"tax"`
	Details       []avaDetail `json:"details"`
}

type avaTransaction struct {
	Code  string            `json:"code"`
	Lines []avaResponseLine `json:"lines"`
}

func (a *Avalara) Calculate(ctx context.Context, doc domain.Document) (domain.Result, error) {
	kind := "SalesOrder"
	switch {
	case doc.Direction == domain.DirInput && doc.DocumentRef != "":
		kind = "PurchaseInvoice"
	case doc.Direction == domain.DirInput:
		kind = "PurchaseOrder"
	case doc.DocumentRef != "":
		kind = "SalesInvoice"
	}
	date := doc.TaxDate
	if date.IsZero() {
		date = doc.DocumentDate
	}
	customer := doc.Party.TaxID
	if customer == "" {
		customer = doc.Party.TaxGroup
	}
	req := avaCreate{Type: kind, CompanyCode: a.companyCode, Date: date.Format("2006-01-02"), CustomerCode: customer, CurrencyCode: doc.Currency,
		BusinessID: doc.Party.TaxID, ExemptionNo: doc.Party.Certificate, Code: doc.DocumentRef,
		Addresses: map[string]avaAddress{"shipTo": {Country: doc.Party.Country}}}
	if doc.ShipFromCountry != "" {
		req.Addresses["shipFrom"] = avaAddress{Country: doc.ShipFromCountry}
	}
	for _, l := range doc.Lines {
		f, _ := l.Amount.Float64()
		req.Lines = append(req.Lines, avaLine{Number: l.LineRef, Quantity: 1, Amount: f, TaxCode: l.ItemTaxGroup, ItemCode: l.LineRef, TaxIncluded: l.PriceIncludesTax})
	}
	var out avaTransaction
	if err := a.c.do(ctx, http.MethodPost, "/api/v2/transactions/create", a.auth, req, &out); err != nil {
		return domain.Result{}, err
	}
	return avalaraResult(doc, out)
}

func avalaraResult(doc domain.Document, tx avaTransaction) (domain.Result, error) {
	exp := money.MinorUnits(doc.Currency)
	byRef := map[string]avaResponseLine{}
	for _, l := range tx.Lines {
		byRef[l.LineNumber] = l
	}
	lines := make([]domain.LineResult, 0, len(doc.Lines))
	for _, dl := range doc.Lines {
		l, ok := byRef[dl.LineRef]
		if !ok {
			return domain.Result{}, fmt.Errorf("avalara answered without line %s", dl.LineRef)
		}
		base := num(l.TaxableAmount)
		if dl.PriceIncludesTax {
			base = num(l.LineAmount).Sub(num(l.Tax))
		}
		lr := domain.LineResult{LineRef: dl.LineRef, Base: amount(base, exp), Taxes: []domain.TaxLine{}}
		for _, d := range l.Details {
			tax := num(d.Tax)
			name := d.TaxName
			if name == "" {
				name = d.JurisName
			}
			lr.Taxes = append(lr.Taxes, domain.TaxLine{Code: name, Jurisdiction: strings.TrimSpace(d.JurisName + " " + d.JurisType),
				RatePct: domain.Dec{Decimal: num(d.Rate).Mul(hundred)}, TaxAmount: amount(tax, exp), Recoverable: amount(tax, exp), Nonrecoverable: amount(decimal.Zero, exp), Direction: doc.Direction})
		}
		lines = append(lines, lr)
	}
	return assemble(avalaraName, lines), nil
}

func (a *Avalara) transactionPath(ref, action string) string {
	return "/api/v2/companies/" + url.PathEscape(a.companyCode) + "/transactions/" + url.PathEscape(ref) + "/" + action
}

func (a *Avalara) Commit(ctx context.Context, ref string) error {
	if ref == "" {
		return errors.New("avalara: no document ref to commit")
	}
	return a.c.do(ctx, http.MethodPost, a.transactionPath(ref, "commit"), a.auth, map[string]any{"commit": true}, nil)
}

func (a *Avalara) Void(ctx context.Context, ref string) error {
	if ref == "" {
		return errors.New("avalara: no document ref to void")
	}
	return a.c.do(ctx, http.MethodPost, a.transactionPath(ref, "void"), a.auth, map[string]any{"code": "DocVoided"}, nil)
}

func (a *Avalara) ValidateAddress(ctx context.Context, addr domain.Address) error {
	var out struct {
		Messages []struct {
			Severity string `json:"severity"`
			Summary  string `json:"summary"`
		} `json:"messages"`
	}
	q := url.Values{"line1": {addr.Line1}, "city": {addr.City}, "region": {addr.Region}, "postalCode": {addr.PostalCode}, "country": {addr.Country}}
	if err := a.c.do(ctx, http.MethodGet, "/api/v2/addresses/resolve?"+q.Encode(), a.auth, nil, &out); err != nil {
		return err
	}
	for _, m := range out.Messages {
		if strings.EqualFold(m.Severity, "Error") {
			return &domain.ProviderError{Status: http.StatusUnprocessableEntity, Err: errors.New(m.Summary)}
		}
	}
	return nil
}
