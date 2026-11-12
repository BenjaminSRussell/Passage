package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

const (
	vertexName     = "vertex"
	tokenSkew      = 30 * time.Second
	vertexSalePath = "/vertex-restapi/v2/sale"
)

// Vertex is the Vertex O Series REST adapter. Calls carry an OAuth bearer token fetched from the token endpoint and refreshed
// shortly before it expires. A document with a DocumentRef is sent as an INVOICE, which Vertex records in its tax journal; without
// one it is a QUOTATION that Vertex does not keep.
type Vertex struct {
	c        client
	tokenURL string
	form     url.Values
	company  string
	now      func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

func NewVertex(baseURL, tokenURL, clientID, clientSecret, username, password, company string, hc *http.Client) (*Vertex, error) {
	c, err := newClient(baseURL, hc)
	if err != nil {
		return nil, err
	}
	if tokenURL == "" || clientID == "" || clientSecret == "" || username == "" || password == "" || company == "" {
		return nil, errors.New("vertex needs a token URL, client id and secret, a user and password and a company")
	}
	if err := requireHTTPS("the Vertex token URL", tokenURL); err != nil {
		return nil, err
	}
	return &Vertex{c: c, tokenURL: tokenURL, company: company, now: time.Now, form: url.Values{
		"grant_type": {"password"}, "scope": {"calc-rest-api"}, "client_id": {clientID}, "client_secret": {clientSecret},
		"username": {username}, "password": {password}}}, nil
}

// VertexFromEnv builds the adapter from VERTEX_URL, VERTEX_TOKEN_URL, VERTEX_CLIENT_ID, VERTEX_CLIENT_SECRET, VERTEX_USERNAME,
// VERTEX_PASSWORD and VERTEX_COMPANY; the secrets come from a Secret. It returns nil, nil when VERTEX_URL is unset.
func VertexFromEnv(getenv func(string) string) (*Vertex, error) {
	base := getenv("VERTEX_URL")
	if base == "" {
		return nil, nil
	}
	return NewVertex(base, getenv("VERTEX_TOKEN_URL"), getenv("VERTEX_CLIENT_ID"), getenv("VERTEX_CLIENT_SECRET"),
		getenv("VERTEX_USERNAME"), getenv("VERTEX_PASSWORD"), getenv("VERTEX_COMPANY"), nil)
}

func (v *Vertex) Name() string { return vertexName }

// bearer returns a valid token, fetching a new one when the cached one is about to expire. The lock is held across the fetch so
// concurrent calls share one token request.
func (v *Vertex) bearer(ctx context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.token != "" && v.now().Add(tokenSkew).Before(v.expires) {
		return v.token, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.tokenURL, strings.NewReader(v.form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := v.c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("vertex token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// A refused login is a provider problem, never the document's: 502 keeps it from reading as a 4xx the document caused.
		return "", &domain.ProviderError{Status: http.StatusBadGateway, Err: fmt.Errorf("vertex token endpoint answered %d", resp.StatusCode)}
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.AccessToken == "" {
		return "", &domain.ProviderError{Status: http.StatusBadGateway, Err: errors.New("vertex token endpoint answered without a token")}
	}
	v.token, v.expires = out.AccessToken, v.now().Add(time.Duration(out.ExpiresIn)*time.Second)
	return v.token, nil
}

type vertexCurrency struct {
	Code string `json:"isoCurrencyCodeAlpha"`
}

type vertexLocation struct {
	Country string `json:"country,omitempty"`
}

type vertexParty struct {
	Company  string          `json:"company,omitempty"`
	Code     string          `json:"customerCode,omitempty"`
	Dest     *vertexLocation `json:"destination,omitempty"`
	Physical *vertexLocation `json:"physicalOrigin,omitempty"`
	TaxReg   *struct {
		ID string `json:"taxRegistrationNumber"`
	} `json:"taxRegistration,omitempty"`
}

type vertexLine struct {
	Number   int            `json:"lineItemNumber"`
	ID       string         `json:"lineItemId"`
	Price    json.Number    `json:"extendedPrice"`
	Quantity map[string]int `json:"quantity"`
	Product  map[string]any `json:"product,omitempty"`
}

type vertexSale struct {
	Type      string         `json:"saleMessageType"`
	Date      string         `json:"documentDate"`
	TxType    string         `json:"transactionType"`
	DocNumber string         `json:"documentNumber,omitempty"`
	Currency  vertexCurrency `json:"currency"`
	Seller    vertexParty    `json:"seller"`
	Customer  vertexParty    `json:"customer"`
	LineItems []vertexLine   `json:"lineItems"`
}

type vertexTax struct {
	Jurisdiction struct {
		Level string `json:"jurisdictionLevel"`
		Value string `json:"value"`
	} `json:"jurisdiction"`
	ImpositionType struct {
		Value string `json:"value"`
	} `json:"impositionType"`
	Rate json.Number `json:"effectiveRate"`
	Tax  json.Number `json:"calculatedTax"`
}

type vertexResponse struct {
	LineItems []struct {
		ID    string      `json:"lineItemId"`
		Price json.Number `json:"extendedPrice"`
		Taxes []vertexTax `json:"taxes"`
	} `json:"lineItems"`
}

func (v *Vertex) Calculate(ctx context.Context, doc domain.Document) (domain.Result, error) {
	token, err := v.bearer(ctx)
	if err != nil {
		return domain.Result{}, err
	}
	kind, txType := "QUOTATION", "SALE"
	if doc.DocumentRef != "" {
		kind = "INVOICE"
	}
	if doc.Direction == domain.DirInput {
		txType = "PURCHASE"
	}
	date := doc.TaxDate
	if date.IsZero() {
		date = doc.DocumentDate
	}
	customer := vertexParty{Code: doc.Party.TaxGroup, Dest: &vertexLocation{Country: doc.Party.Country}}
	if doc.Party.TaxID != "" {
		customer.TaxReg = &struct {
			ID string `json:"taxRegistrationNumber"`
		}{ID: doc.Party.TaxID}
	}
	sale := vertexSale{Type: kind, Date: date.Format("2006-01-02"), TxType: txType, DocNumber: doc.DocumentRef, Currency: vertexCurrency{Code: doc.Currency},
		Seller: vertexParty{Company: v.company}, Customer: customer}
	if doc.ShipFromCountry != "" {
		sale.Seller.Physical = &vertexLocation{Country: doc.ShipFromCountry}
	}
	for i, l := range doc.Lines {
		sale.LineItems = append(sale.LineItems, vertexLine{Number: i + 1, ID: l.LineRef, Price: json.Number(l.Amount.String()),
			Quantity: map[string]int{"value": 1}, Product: map[string]any{"value": l.ItemTaxGroup}})
	}
	var out vertexResponse
	if err := v.c.do(ctx, http.MethodPost, vertexSalePath, bearerAuth(token), sale, &out); err != nil {
		return domain.Result{}, err
	}
	return vertexResult(doc, out)
}

func bearerAuth(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

func vertexResult(doc domain.Document, out vertexResponse) (domain.Result, error) {
	exp := money.MinorUnits(doc.Currency)
	byRef := map[string]int{}
	for i, l := range out.LineItems {
		byRef[l.ID] = i
	}
	lines := make([]domain.LineResult, 0, len(doc.Lines))
	for _, dl := range doc.Lines {
		i, ok := byRef[dl.LineRef]
		if !ok {
			return domain.Result{}, fmt.Errorf("vertex answered without line %s", dl.LineRef)
		}
		l := out.LineItems[i]
		total := decimal.Zero
		for _, t := range l.Taxes {
			total = total.Add(num(t.Tax))
		}
		base := num(l.Price)
		if dl.PriceIncludesTax {
			base = base.Sub(total)
		}
		lr := domain.LineResult{LineRef: dl.LineRef, Base: amount(base, exp), Taxes: []domain.TaxLine{}}
		for _, t := range l.Taxes {
			tax := num(t.Tax)
			code := t.ImpositionType.Value
			if code == "" {
				code = t.Jurisdiction.Value
			}
			lr.Taxes = append(lr.Taxes, domain.TaxLine{Code: code, Jurisdiction: strings.TrimSpace(t.Jurisdiction.Value + " " + t.Jurisdiction.Level),
				RatePct: domain.Dec{Decimal: num(t.Rate).Mul(hundred)}, TaxAmount: amount(tax, exp), Recoverable: amount(tax, exp),
				Nonrecoverable: amount(decimal.Zero, exp), Direction: doc.Direction})
		}
		lines = append(lines, lr)
	}
	return assemble(vertexName, lines), nil
}

// Commit has nothing to do: Vertex recorded the document when Calculate sent it as an INVOICE.
func (v *Vertex) Commit(_ context.Context, ref string) error {
	if ref == "" {
		return errors.New("vertex: no document ref to commit")
	}
	return nil
}

// Void reverses a recorded document by sending its number as a ROLLBACK.
func (v *Vertex) Void(ctx context.Context, ref string) error {
	if ref == "" {
		return errors.New("vertex: no document ref to void")
	}
	token, err := v.bearer(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{"saleMessageType": "ROLLBACK", "documentNumber": ref, "seller": map[string]string{"company": v.company}}
	return v.c.do(ctx, http.MethodPost, vertexSalePath, bearerAuth(token), body, nil)
}

// ValidateAddress is not offered: Vertex cleanses addresses in a separate product this adapter does not call.
func (v *Vertex) ValidateAddress(context.Context, domain.Address) error { return errors.ErrUnsupported }
