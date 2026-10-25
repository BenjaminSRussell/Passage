package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	pain001Namespace  = "urn:iso:std:iso:20022:tech:xsd:pain.001.001.09"
	creationLayout    = "2006-01-02T15:04:05Z"
	maxNameLength     = 140
	maxStreetLength   = 70
	maxTownLength     = 35
	maxPostCodeLength = 16
	maxAccountLength  = 34
	maxReferenceLen   = 35
	clearingSystemABA = "USABA"
)

var (
	isoReference   = regexp.MustCompile(`^[A-Za-z0-9/\-?:().,'+ ]{1,35}$`)
	countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)
)

type PaymentFile struct {
	Format       string
	FileNumber   string
	Content      []byte
	PaymentCount int
	ControlSum   decimal.Decimal
	SHA256       string
}

type PostalAddress struct {
	StreetName string
	PostCode   string
	TownName   string
	Country    string
}

type Pain001Party struct {
	Name    string
	Address PostalAddress
}

type Pain001Account struct {
	IBAN    string
	OtherID string
}

type Pain001Invoice struct {
	Number string
	Amount decimal.Decimal
}

type Pain001Creditor struct {
	Party         Pain001Party
	Account       Pain001Account
	BIC           string
	RoutingNumber string
}

type Pain001Payment struct {
	Method        PaymentMethod
	ExecutionDate time.Time
	Number        string
	Amount        decimal.Decimal
	Currency      string
	Creditor      Pain001Creditor
	Invoices      []Pain001Invoice
}

type Pain001Input struct {
	FileNumber    string
	Created       time.Time
	Debtor        Pain001Party
	DebtorAccount Pain001Account
	DebtorBIC     string
	Profile       BankProfile
	Payments      []Pain001Payment
}

type xmlDocument struct {
	XMLName xml.Name      `xml:"urn:iso:std:iso:20022:tech:xsd:pain.001.001.09 Document"`
	Init    xmlInitiation `xml:"CstmrCdtTrfInitn"`
}

type xmlInitiation struct {
	GrpHdr xmlGroupHeader
	PmtInf []xmlPaymentInstruction
}

type xmlGroupHeader struct {
	MsgId    string
	CreDtTm  string
	NbOfTxs  int
	CtrlSum  string
	InitgPty xmlInitiatingParty
}

type xmlInitiatingParty struct {
	Nm string
	Id *xmlPartyID `xml:",omitempty"`
}

type xmlPartyID struct {
	OrgId struct{ Othr xmlOther }
}

type xmlOther struct {
	Id string
}

type xmlPaymentInstruction struct {
	PmtInfId    string
	PmtMtd      string
	BtchBookg   bool
	NbOfTxs     int
	CtrlSum     string
	PmtTpInf    struct{ SvcLvl xmlCode }
	ReqdExctnDt struct{ Dt string }
	Dbtr        xmlParty
	DbtrAcct    xmlAccount
	DbtrAgt     xmlAgent
	ChrgBr      string
	CdtTrfTxInf []xmlTransfer
}

type xmlCode struct {
	Cd string
}

type xmlParty struct {
	Nm      string
	PstlAdr *xmlAddress `xml:",omitempty"`
}

type xmlAddress struct {
	StrtNm string `xml:",omitempty"`
	PstCd  string `xml:",omitempty"`
	TwnNm  string `xml:",omitempty"`
	Ctry   string
}

type xmlAccount struct {
	Id struct {
		IBAN string    `xml:",omitempty"`
		Othr *xmlOther `xml:",omitempty"`
	}
}

type xmlAgent struct {
	FinInstnId xmlFinInstn
}

type xmlFinInstn struct {
	BICFI       string       `xml:",omitempty"`
	ClrSysMmbId *xmlClearing `xml:",omitempty"`
}

type xmlClearing struct {
	ClrSysId xmlCode
	MmbId    string
}

type xmlCurrencyAmount struct {
	Ccy   string `xml:"Ccy,attr"`
	Value string `xml:",chardata"`
}

type xmlTransfer struct {
	PmtId    struct{ InstrId, EndToEndId string }
	Amt      struct{ InstdAmt xmlCurrencyAmount }
	CdtrAgt  *xmlAgent `xml:",omitempty"`
	Cdtr     xmlParty
	CdtrAcct xmlAccount
	Purp     xmlCode
	RmtInf   *xmlRemittance `xml:",omitempty"`
}

type xmlRemittance struct {
	Ustrd string          `xml:",omitempty"`
	Strd  []xmlStructured `xml:",omitempty"`
}

type xmlStructured struct {
	RfrdDocInf struct{ Nb string }
	RfrdDocAmt struct{ RmtdAmt xmlCurrencyAmount }
}

type groupKey struct {
	date   string
	method string
}

type paymentGroup struct {
	transfers []xmlTransfer
	total     decimal.Decimal
	scale     int32
}

func GeneratePain001(in Pain001Input) (PaymentFile, error) {
	if err := validatePain001Header(in); err != nil {
		return PaymentFile{}, err
	}
	o := in.Profile.Overrides
	debtorName, err := fileText(in.Debtor.Name, o.Charset, "debtor", maxNameLength)
	if err != nil {
		return PaymentFile{}, err
	}
	debtorAddress, err := buildAddress(in.Debtor.Address, true, false, o.Charset, "debtor")
	if err != nil {
		return PaymentFile{}, err
	}
	debtorAccount, err := buildAccount(in.DebtorAccount, "debtor")
	if err != nil {
		return PaymentFile{}, err
	}
	if in.DebtorBIC == "" {
		return PaymentFile{}, invalid("debtor BIC is required")
	}
	if err := ValidateBIC(in.DebtorBIC); err != nil {
		return PaymentFile{}, err
	}

	groups := map[groupKey]*paymentGroup{}
	seen := map[string]bool{}
	for _, p := range in.Payments {
		transfer, err := buildTransfer(p, in.Profile)
		if err != nil {
			return PaymentFile{}, err
		}
		if seen[p.Number] {
			return PaymentFile{}, invalid("payment %s appears more than once", p.Number)
		}
		seen[p.Number] = true
		key := groupKey{date: p.ExecutionDate.Format(dateLayout), method: p.Method.Type}
		g := groups[key]
		if g == nil {
			g = &paymentGroup{}
			groups[key] = g
		}
		g.transfers = append(g.transfers, transfer)
		g.total = g.total.Add(p.Amount)
		g.scale = max(g.scale, money.MinorUnits(p.Currency))
	}

	keys := make([]groupKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].date != keys[j].date {
			return keys[i].date < keys[j].date
		}
		return keys[i].method < keys[j].method
	})

	doc := xmlDocument{}
	fileTotal := decimal.Zero
	fileScale := int32(0)
	for i, key := range keys {
		g := groups[key]
		pmtInfID := fmt.Sprintf("%s-%d", in.FileNumber, i+1)
		if !isoReference.MatchString(pmtInfID) {
			return PaymentFile{}, invalid("PmtInfId must match the BR-19.9 pattern")
		}
		inf := xmlPaymentInstruction{
			PmtInfId: pmtInfID, PmtMtd: "TRF", BtchBookg: o.BatchBooking,
			NbOfTxs: len(g.transfers), CtrlSum: g.total.StringFixed(g.scale),
			Dbtr: xmlParty{Nm: debtorName, PstlAdr: debtorAddress}, DbtrAcct: debtorAccount,
			DbtrAgt: xmlAgent{FinInstnId: xmlFinInstn{BICFI: strings.ToUpper(in.DebtorBIC)}},
			ChrgBr:  chargeBearer(in.Profile), CdtTrfTxInf: g.transfers,
		}
		inf.PmtTpInf.SvcLvl.Cd = serviceLevel(in.Profile, key.method)
		inf.ReqdExctnDt.Dt = key.date
		doc.Init.PmtInf = append(doc.Init.PmtInf, inf)
		fileTotal = fileTotal.Add(g.total)
		fileScale = max(fileScale, g.scale)
	}

	doc.Init.GrpHdr = xmlGroupHeader{
		MsgId: in.FileNumber, CreDtTm: in.Created.UTC().Format(creationLayout),
		NbOfTxs: len(in.Payments), CtrlSum: fileTotal.StringFixed(fileScale),
		InitgPty: xmlInitiatingParty{Nm: debtorName},
	}
	if o.InitiatingPartyID != "" {
		id := &xmlPartyID{}
		id.OrgId.Othr.Id = o.InitiatingPartyID
		doc.Init.GrpHdr.InitgPty.Id = id
	}

	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return PaymentFile{}, fmt.Errorf("marshal pain.001: %w", err)
	}
	content := append([]byte(xml.Header), body...)
	sum := sha256.Sum256(content)
	slog.Info("pain.001 generated", "file", in.FileNumber, "count", len(in.Payments), "groups", len(keys))
	return PaymentFile{
		Format: FormatPain001, FileNumber: in.FileNumber, Content: content,
		PaymentCount: len(in.Payments), ControlSum: fileTotal, SHA256: hex.EncodeToString(sum[:]),
	}, nil
}

func validatePain001Header(in Pain001Input) error {
	o := in.Profile.Overrides
	switch {
	case !isoReference.MatchString(in.FileNumber):
		return invalid("MsgId must match the BR-19.9 pattern")
	case in.Created.IsZero():
		return invalid("created timestamp is required")
	case len(in.Payments) == 0:
		return invalid("at least one payment is required")
	case !variants[in.Profile.Variant] || !charsets[o.Charset] || !chargeBearers[o.ChargeBearer] || o.MaxUstrdLength < 1:
		return invalid("a parsed bank profile is required")
	}
	return nil
}

func chargeBearer(p BankProfile) string {
	if p.Variant == VariantSEPA {
		return "SLEV"
	}
	return p.Overrides.ChargeBearer
}

func serviceLevel(p BankProfile, method string) string {
	switch {
	case p.Variant == VariantSEPA:
		return "SEPA"
	case method == MethodWire:
		return "URGP"
	}
	return "NURG"
}

func buildTransfer(p Pain001Payment, profile BankProfile) (xmlTransfer, error) {
	o := profile.Overrides
	if !isoReference.MatchString(p.Number) {
		return xmlTransfer{}, invalid("InstrId and EndToEndId must match the BR-19.9 pattern")
	}
	if format, err := p.Method.MediaFormat(); err != nil || format != FormatPain001 {
		return xmlTransfer{}, invalid("payment %s: method %s cannot be sent as pain.001", p.Number, p.Method.Type)
	}
	if p.ExecutionDate.IsZero() {
		return xmlTransfer{}, invalid("payment %s: execution date is required", p.Number)
	}
	amount, err := formatAmount(p.Number, p.Amount, p.Currency)
	if err != nil {
		return xmlTransfer{}, err
	}
	subject := "payment " + p.Number
	name, err := fileText(p.Creditor.Party.Name, o.Charset, subject, maxNameLength)
	if err != nil {
		return xmlTransfer{}, err
	}
	address, err := buildAddress(p.Creditor.Party.Address, o.StructuredAddressRequired, o.StructuredAddressRequired, o.Charset, subject)
	if err != nil {
		return xmlTransfer{}, err
	}
	account, err := buildAccount(p.Creditor.Account, subject)
	if err != nil {
		return xmlTransfer{}, err
	}
	agent, err := buildCreditorAgent(p, o)
	if err != nil {
		return xmlTransfer{}, err
	}
	remittance, err := buildRemittance(p, o)
	if err != nil {
		return xmlTransfer{}, err
	}
	t := xmlTransfer{CdtrAgt: agent, Cdtr: xmlParty{Nm: name, PstlAdr: address}, CdtrAcct: account, Purp: xmlCode{Cd: "SUPP"}, RmtInf: remittance}
	t.PmtId.InstrId, t.PmtId.EndToEndId = p.Number, p.Number
	t.Amt.InstdAmt = xmlCurrencyAmount{Ccy: p.Currency, Value: amount}
	return t, nil
}

func formatAmount(number string, d decimal.Decimal, currency string) (string, error) {
	if !validCurrency(currency) {
		return "", invalid("payment %s: currency must be an ISO 4217 code", number)
	}
	minor := money.MinorUnits(currency)
	if !d.IsPositive() || !d.Equal(d.Round(minor)) {
		return "", invalid("payment %s: amount must be greater than zero with at most %d decimals", number, minor)
	}
	return d.StringFixed(minor), nil
}

func fileText(raw, charset, subject string, limit int) (string, error) {
	out, err := Transliterate(raw, charset)
	if err != nil {
		detail := err.Error()
		var e *Error
		if errors.As(err, &e) {
			detail = e.Detail
		}
		return "", &Error{Code: CodeFileInvalid, Detail: subject + ": " + detail}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func buildAddress(a PostalAddress, structured, required bool, charset, subject string) (*xmlAddress, error) {
	if !countryPattern.MatchString(a.Country) {
		return nil, invalid("%s address country must be an ISO 3166 alpha-2 code", subject)
	}
	out := &xmlAddress{Ctry: a.Country}
	if !structured {
		return out, nil
	}
	fields := []struct {
		dst   *string
		raw   string
		name  string
		limit int
	}{
		{&out.StrtNm, a.StreetName, "street name", maxStreetLength},
		{&out.PstCd, a.PostCode, "post code", maxPostCodeLength},
		{&out.TwnNm, a.TownName, "town name", maxTownLength},
	}
	for _, f := range fields {
		if f.raw == "" {
			if required {
				return nil, invalid("%s address %s is required by the bank profile", subject, f.name)
			}
			continue
		}
		text, err := fileText(f.raw, charset, subject, f.limit)
		if err != nil {
			return nil, err
		}
		*f.dst = text
	}
	return out, nil
}

func buildAccount(a Pain001Account, subject string) (xmlAccount, error) {
	var out xmlAccount
	switch {
	case (a.IBAN == "") == (a.OtherID == ""):
		return out, invalid("%s account needs exactly one of IBAN or other id", subject)
	case a.IBAN != "":
		if err := ValidateIBAN(a.IBAN); err != nil {
			return out, err
		}
		out.Id.IBAN = strings.ToUpper(strings.ReplaceAll(a.IBAN, " ", ""))
	case len(a.OtherID) > maxAccountLength:
		return out, invalid("%s account id must be at most %d characters", subject, maxAccountLength)
	default:
		out.Id.Othr = &xmlOther{Id: a.OtherID}
	}
	return out, nil
}

func buildCreditorAgent(p Pain001Payment, o Overrides) (*xmlAgent, error) {
	c := p.Creditor
	var fin xmlFinInstn
	switch {
	case c.BIC != "":
		if err := ValidateBIC(c.BIC); err != nil {
			return nil, err
		}
		if o.BICRequired || c.Account.IBAN == "" {
			fin.BICFI = strings.ToUpper(c.BIC)
		}
	case o.BICRequired:
		return nil, invalid("payment %s: creditor BIC is required by the bank profile", p.Number)
	}
	if c.RoutingNumber != "" {
		if err := ValidateABA(c.RoutingNumber); err != nil {
			return nil, err
		}
		fin.ClrSysMmbId = &xmlClearing{ClrSysId: xmlCode{Cd: clearingSystemABA}, MmbId: c.RoutingNumber}
	}
	if fin.BICFI == "" && fin.ClrSysMmbId == nil {
		if c.Account.IBAN == "" {
			return nil, invalid("payment %s: creditor agent BIC or routing number is required without an IBAN", p.Number)
		}
		return nil, nil
	}
	return &xmlAgent{FinInstnId: fin}, nil
}

func buildRemittance(p Pain001Payment, o Overrides) (*xmlRemittance, error) {
	if len(p.Invoices) == 0 {
		return nil, nil
	}
	numbers := make([]string, len(p.Invoices))
	for i, inv := range p.Invoices {
		if inv.Number == "" || strings.IndexFunc(inv.Number, func(r rune) bool { return !inCharset(r, o.Charset) }) >= 0 {
			return nil, invalid("payment %s: invoice number must be non-empty and inside the %s charset", p.Number, o.Charset)
		}
		numbers[i] = inv.Number
	}
	if !o.StructuredRemittance {
		joined := strings.Join(numbers, ",")
		if len(joined) > o.MaxUstrdLength {
			joined = joined[:o.MaxUstrdLength]
		}
		return &xmlRemittance{Ustrd: joined}, nil
	}
	out := &xmlRemittance{}
	for _, inv := range p.Invoices {
		amount, err := formatAmount(p.Number, inv.Amount, p.Currency)
		if err != nil {
			return nil, err
		}
		var s xmlStructured
		s.RfrdDocInf.Nb = inv.Number
		if len(s.RfrdDocInf.Nb) > maxReferenceLen {
			s.RfrdDocInf.Nb = s.RfrdDocInf.Nb[:maxReferenceLen]
		}
		s.RfrdDocAmt.RmtdAmt = xmlCurrencyAmount{Ccy: p.Currency, Value: amount}
		out.Strd = append(out.Strd, s)
	}
	return out, nil
}
