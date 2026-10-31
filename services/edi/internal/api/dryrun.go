package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
)

// Dry-run routes evaluate a domain rule on the request body and persist nothing.
// They back the operator screens and the contract tests. The persisted flows that
// call the same rules (cart return intake, self-billing reconciliation, invoice
// validation) arrive with their tables in a later slice.

// maxDryRunItems caps the number of shipments, lines or invoice lines in one
// request.
const maxDryRunItems = 5000

type supplierDTO struct {
	Currencies      []string          `json:"currencies"`
	UoMMap          map[string]string `json:"uomMap"`
	TolerancePct    string            `json:"tolerancePct"`
	ReferencePrices map[string]string `json:"referencePrices"`
}

type punchoutRequest struct {
	Format   string            `json:"format"`
	Document string            `json:"document"`
	Fields   map[string]string `json:"fields"`
	Supplier supplierDTO       `json:"supplier"`
}

// toDomain converts the supplier settings. Field errors use the request path.
func (s supplierDTO) toDomain() (domain.SupplierPunchout, map[string]string) {
	fields := map[string]string{}
	out := domain.SupplierPunchout{
		Currencies:      s.Currencies,
		UoMMap:          s.UoMMap,
		TolerancePct:    decimal.Zero,
		ReferencePrices: map[string]decimal.Decimal{},
	}
	if s.TolerancePct != "" {
		v, err := decimal.NewFromString(s.TolerancePct)
		if err != nil || v.IsNegative() || v.GreaterThan(decimal.NewFromInt(20)) {
			fields["supplier.tolerancePct"] = "must be a number between 0 and 20"
		} else {
			out.TolerancePct = v
		}
	}
	for part, price := range s.ReferencePrices {
		v, err := decimal.NewFromString(price)
		if err != nil || v.IsNegative() {
			fields["supplier.referencePrices."+part] = "must be a non-negative number"
			continue
		}
		out.ReferencePrices[part] = v
	}
	return out, fields
}

// punchoutParse validates a returned punchout cart (cXML PunchOutOrderMessage or
// OCI form post) against BR-35.13 and reports the lines that would be created.
func (a *API) punchoutParse(w http.ResponseWriter, r *http.Request) {
	var req punchoutRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	sup, fields := req.Supplier.toDomain()
	var (
		cart domain.RawCart
		err  error
	)
	switch strings.ToUpper(strings.TrimSpace(req.Format)) {
	case "CXML":
		if strings.TrimSpace(req.Document) == "" {
			fields["document"] = "is required for CXML"
		} else {
			cart, err = domain.ParsePunchoutCXML([]byte(req.Document))
		}
	case "OCI":
		if len(req.Fields) == 0 {
			fields["fields"] = "is required for OCI"
		} else {
			cart, err = domain.ParsePunchoutOCI(req.Fields)
		}
	default:
		fields["format"] = "must be CXML or OCI"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeSyntax, err.Error()))
		return
	}
	res := domain.ValidatePunchout(cart, sup)
	if res.Lines == nil {
		res.Lines = []domain.CartLine{}
	}
	if res.Issues == nil {
		res.Issues = []domain.Issue{}
	}
	httpx.JSON(w, http.StatusOK, res)
}

type sbShipmentDTO struct {
	ID           string `json:"id"`
	DeliveryNote string `json:"deliveryNote"`
	POLine       string `json:"poLine"`
	Item         string `json:"item"`
	Quantity     string `json:"quantity"`
	UnitPrice    string `json:"unitPrice"`
	TaxRatePct   string `json:"taxRatePct"`
	ShippedOn    string `json:"shippedOn"`
}

type sbLineDTO struct {
	Line         string `json:"line"`
	DeliveryNote string `json:"deliveryNote"`
	POLine       string `json:"poLine"`
	Item         string `json:"item"`
	Quantity     string `json:"quantity"`
	UnitPrice    string `json:"unitPrice"`
	Tax          string `json:"tax"`
}

type selfBillRequest struct {
	AsOf      string `json:"asOf"`
	WaitDays  int    `json:"waitDays"`
	Tolerance struct {
		Quantity string `json:"quantity"`
		Price    string `json:"price"`
	} `json:"tolerance"`
	Shipments []sbShipmentDTO `json:"shipments"`
	Lines     []sbLineDTO     `json:"lines"`
}

// parseDec reads a decimal field, recording a field error when it is malformed.
// An empty string is zero.
func parseDec(fields map[string]string, key, s string) decimal.Decimal {
	if strings.TrimSpace(s) == "" {
		return decimal.Zero
	}
	v, err := decimal.NewFromString(strings.TrimSpace(s))
	if err != nil {
		fields[key] = "must be a decimal number"
		return decimal.Zero
	}
	return v
}

func parseDay(fields map[string]string, key, s string) time.Time {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		fields[key] = "must be a date in YYYY-MM-DD"
	}
	return t
}

// selfBillingMatch runs BR-35.15 over a supplied shipment set and self-billed
// document.
func (a *API) selfBillingMatch(w http.ResponseWriter, r *http.Request) {
	var req selfBillRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if len(req.Shipments) > maxDryRunItems {
		fields["shipments"] = fmt.Sprintf("at most %d shipments per request", maxDryRunItems)
	}
	if len(req.Lines) > maxDryRunItems {
		fields["lines"] = fmt.Sprintf("at most %d lines per request", maxDryRunItems)
	}
	waitDays := req.WaitDays
	if waitDays == 0 {
		waitDays = domain.SelfBillDefaultWaitDays
	}
	if waitDays < 1 || waitDays > 120 {
		fields["waitDays"] = "must be between 1 and 120"
	}
	asOf := a.now().UTC()
	if strings.TrimSpace(req.AsOf) != "" {
		asOf = parseDay(fields, "asOf", req.AsOf)
	}
	tol := domain.DefaultSBTolerance()
	if req.Tolerance.Quantity != "" {
		tol.Quantity = parseDec(fields, "tolerance.quantity", req.Tolerance.Quantity)
	}
	if req.Tolerance.Price != "" {
		tol.Price = parseDec(fields, "tolerance.price", req.Tolerance.Price)
	}
	if tol.Quantity.IsNegative() {
		fields["tolerance.quantity"] = "must not be negative"
	}
	if tol.Price.IsNegative() {
		fields["tolerance.price"] = "must not be negative"
	}
	var ships []domain.SBShipment
	for i, s := range req.Shipments {
		key := fmt.Sprintf("shipments[%d]", i)
		if strings.TrimSpace(s.ID) == "" {
			fields[key+".id"] = "is required"
		}
		ships = append(ships, domain.SBShipment{
			ID: s.ID, DeliveryNote: s.DeliveryNote, POLine: s.POLine, Item: s.Item,
			Quantity:   parseDec(fields, key+".quantity", s.Quantity),
			UnitPrice:  parseDec(fields, key+".unitPrice", s.UnitPrice),
			TaxRatePct: parseDec(fields, key+".taxRatePct", s.TaxRatePct),
			ShippedOn:  parseDay(fields, key+".shippedOn", s.ShippedOn),
		})
	}
	var lines []domain.SBLine
	for i, l := range req.Lines {
		key := fmt.Sprintf("lines[%d]", i)
		if strings.TrimSpace(l.Line) == "" {
			fields[key+".line"] = "is required"
		}
		lines = append(lines, domain.SBLine{
			Line: l.Line, DeliveryNote: l.DeliveryNote, POLine: l.POLine, Item: l.Item,
			Quantity:  parseDec(fields, key+".quantity", l.Quantity),
			UnitPrice: parseDec(fields, key+".unitPrice", l.UnitPrice),
			Tax:       parseDec(fields, key+".tax", l.Tax),
		})
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	res, err := domain.MatchSelfBilling(lines, ships, tol, asOf, waitDays)
	if err != nil { // the checks above cover the domain's parameter rules
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeValidation, err.Error()))
		return
	}
	if res.Lines == nil {
		res.Lines = []domain.SBLineResult{}
	}
	if res.Unbilled == nil {
		res.Unbilled = []string{}
	}
	if res.Issues == nil {
		res.Issues = []domain.Issue{}
	}
	httpx.JSON(w, http.StatusOK, res)
}

type ublPartyDTO struct {
	Name           string `json:"name"`
	LegalName      string `json:"legalName"`
	EndpointScheme string `json:"endpointScheme"`
	EndpointID     string `json:"endpointId"`
	CountryCode    string `json:"countryCode"`
	VATID          string `json:"vatId"`
	LegalID        string `json:"legalId"`
	StreetName     string `json:"streetName"`
	CityName       string `json:"cityName"`
	PostalZone     string `json:"postalZone"`
	ContactName    string `json:"contactName"`
	ContactPhone   string `json:"contactPhone"`
	ContactEmail   string `json:"contactEmail"`
}

type ublLineDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UnitCode  string `json:"unitCode"`
	Quantity  string `json:"quantity"`
	UnitPrice string `json:"unitPrice"`
	Category  string `json:"category"`
	Percent   string `json:"percent"`
}

type ublAllowanceChargeDTO struct {
	Charge   bool   `json:"charge"`
	Reason   string `json:"reason"`
	Amount   string `json:"amount"`
	Category string `json:"category"`
	Percent  string `json:"percent"`
}

type ublRequest struct {
	CreditNote       bool                    `json:"creditNote"`
	ID               string                  `json:"id"`
	IssueDate        string                  `json:"issueDate"`
	DueDate          string                  `json:"dueDate"`
	Currency         string                  `json:"currency"`
	BuyerReference   string                  `json:"buyerReference"`
	OrderReference   string                  `json:"orderReference"`
	Supplier         ublPartyDTO             `json:"supplier"`
	Customer         ublPartyDTO             `json:"customer"`
	PaymentMeansCode string                  `json:"paymentMeansCode"`
	IBAN             string                  `json:"iban"`
	AllowanceCharges []ublAllowanceChargeDTO `json:"allowanceCharges"`
	Prepaid          string                  `json:"prepaid"`
	ExemptionReasons map[string]string       `json:"exemptionReasons"`
	DeliveryDate     string                  `json:"deliveryDate"`
	DeliveryCountry  string                  `json:"deliveryCountry"`
	Lines            []ublLineDTO            `json:"lines"`
}

func toUBLParty(p ublPartyDTO) domain.UBLParty {
	return domain.UBLParty{Name: p.Name, LegalName: p.LegalName, EndpointScheme: p.EndpointScheme,
		EndpointID: p.EndpointID, CountryCode: p.CountryCode, VATID: p.VATID, LegalID: p.LegalID,
		StreetName: p.StreetName, CityName: p.CityName, PostalZone: p.PostalZone,
		ContactName: p.ContactName, ContactPhone: p.ContactPhone, ContactEmail: p.ContactEmail}
}

// ublBuild builds Peppol BIS Billing 3.0 UBL for an invoice or credit note and
// returns the XML, or the BR-35.4 and arithmetic issues that block sending. Like
// maps:test, a dry run that finds blocking issues still answers 200: the
// request was understood and the outcome is in `valid` and `issues`.
func (a *API) ublBuild(w http.ResponseWriter, r *http.Request) {
	var req ublRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(req.Lines) > maxDryRunItems || len(req.AllowanceCharges) > maxDryRunItems {
		httpx.Fail(w, r, validation(map[string]string{"lines": fmt.Sprintf("at most %d lines per invoice", maxDryRunItems)}))
		return
	}
	fields := map[string]string{}
	inv := domain.UBLInvoice{
		CreditNote: req.CreditNote, ID: req.ID, IssueDate: req.IssueDate, DueDate: req.DueDate,
		Currency: req.Currency, BuyerReference: req.BuyerReference, OrderReference: req.OrderReference,
		Supplier: toUBLParty(req.Supplier), Customer: toUBLParty(req.Customer),
		PaymentMeansCode: req.PaymentMeansCode, IBAN: req.IBAN,
		Prepaid:          parseDec(fields, "prepaid", req.Prepaid),
		ExemptionReasons: req.ExemptionReasons,
		DeliveryDate:     req.DeliveryDate, DeliveryCountry: req.DeliveryCountry,
	}
	for i, ac := range req.AllowanceCharges {
		key := fmt.Sprintf("allowanceCharges[%d]", i)
		inv.AllowanceCharges = append(inv.AllowanceCharges, domain.UBLAllowanceCharge{
			Charge: ac.Charge, Reason: ac.Reason, Category: ac.Category,
			Amount:  parseDec(fields, key+".amount", ac.Amount),
			Percent: parseDec(fields, key+".percent", ac.Percent),
		})
	}
	for i, l := range req.Lines {
		key := fmt.Sprintf("lines[%d]", i)
		inv.Lines = append(inv.Lines, domain.UBLLine{
			ID: l.ID, Name: l.Name, UnitCode: l.UnitCode, Category: l.Category,
			Quantity:  parseDec(fields, key+".quantity", l.Quantity),
			UnitPrice: parseDec(fields, key+".unitPrice", l.UnitPrice),
			Percent:   parseDec(fields, key+".percent", l.Percent),
		})
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	xmlOut, issues := domain.BuildUBL(inv)
	if issues == nil {
		issues = []domain.Issue{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"xml": xmlOut, "issues": issues, "valid": len(issues) == 0})
}
