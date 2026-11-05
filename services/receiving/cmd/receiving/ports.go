package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/compliance"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/inventory"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/ocr"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/party"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/partydata"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/periods"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/purchasing"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/tax"
)

func buildPorts(getenv func(string) string) (api.Ports, error) {
	allowFakes := getenv("PASSAGE_ALLOW_FAKES") == "1"
	var ports api.Ports
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return api.Ports{}, err
	}
	switch url := getenv("LEDGER_URL"); {
	case url != "":
		ports.Ledger = ledger.New(url, nil).WithSigner(signer)
	case allowFakes:
		slog.Warn("LEDGER_URL is empty: journal entries go to an in-memory fake and are lost on restart", "flag", "PASSAGE_ALLOW_FAKES")
		ports.Ledger = &domain.FakeLedger{}
	default:
		return api.Ports{}, fmt.Errorf("LEDGER_URL is required (set PASSAGE_ALLOW_FAKES=1 only for local development)")
	}
	switch url := getenv("CLOSE_URL"); {
	case url != "":
		ports.Periods = periods.New(url, nil).WithSigner(signer)
	case allowFakes:
		slog.Warn("CLOSE_URL is empty: every AP period reads as open", "flag", "PASSAGE_ALLOW_FAKES")
		ports.Periods = domain.FakePeriods{Status: domain.PeriodOpen}
	default:
		return api.Ports{}, fmt.Errorf("CLOSE_URL is required (set PASSAGE_ALLOW_FAKES=1 only for local development)")
	}
	switch url := getenv("PURCHASING_URL"); {
	case url != "":
		client := purchasing.New(url, nil).WithSigner(signer)
		ports.PurchaseOrders, ports.InvoicingPlans, ports.PlanRowSettlements = client, client, client
		ports.RequirePOLine = true
	case allowFakes:
		slog.Warn("PURCHASING_URL is empty: purchase order and invoicing plan lookups find nothing", "flag", "PASSAGE_ALLOW_FAKES")
		ports.PurchaseOrders, ports.InvoicingPlans = domain.FakePurchaseOrders{}, domain.FakeInvoicingPlans{}
		ports.PlanRowSettlements = domain.FakePlanRowSettlements{}
	default:
		return api.Ports{}, fmt.Errorf("PURCHASING_URL is required (set PASSAGE_ALLOW_FAKES=1 only for local development)")
	}
	switch url := getenv("INVENTORY_URL"); {
	case url != "":
		ports.OwnershipChanges = inventory.New(url, nil).WithSigner(signer)
	case allowFakes:
		slog.Warn("INVENTORY_URL is empty: consignment settlement finds no ownership changes", "flag", "PASSAGE_ALLOW_FAKES")
		ports.OwnershipChanges = domain.FakeOwnershipChanges{}
	default:
		slog.Warn("INVENTORY_URL is empty: consignment settlement answers 503 until it is set")
	}
	switch url := getenv("PARTY_URL"); {
	case url != "":
		ports.VendorGroups = party.New(url, nil).WithSigner(signer)
	case allowFakes:
		slog.Warn("PARTY_URL is empty: invoice.posted carries no vendor_group_id", "flag", "PASSAGE_ALLOW_FAKES")
		ports.VendorGroups = domain.FakeVendorGroups{}
	default:
		slog.Warn("PARTY_URL is empty: invoice.posted carries no vendor_group_id")
	}
	switch url := getenv("COMPLIANCE_URL"); {
	case url != "":
		ports.LegalHolds = compliance.New(url, nil).WithSigner(signer)
	case allowFakes:
		slog.Warn("COMPLIANCE_URL is empty: no legal hold covers anything", "flag", "PASSAGE_ALLOW_FAKES")
		ports.LegalHolds = &domain.FakeLegalHolds{}
	default:
		slog.Warn("COMPLIANCE_URL is empty: one-time payee erasure answers 503 until it is set")
	}
	if url := getenv("TAX_URL"); url != "" {
		cfg, err := taxConfig(getenv)
		if err != nil {
			return api.Ports{}, err
		}
		client := tax.New(url, cfg, nil).WithSigner(signer)
		ports.Tax, ports.TaxRecorder = client, client
		partyURL, orgURL := getenv("PARTY_URL"), getenv("ORG_URL")
		if partyURL == "" || orgURL == "" {
			return api.Ports{}, fmt.Errorf("TAX_URL needs PARTY_URL and ORG_URL: the self-billing document prints the supplier and the legal entity")
		}
		ports.SelfBillParties = partydata.New(partyURL, orgURL, nil).WithSigner(signer)
	} else if allowFakes {
		slog.Warn("TAX_URL is empty: ERS settlement skips every receipt as TAX_NOT_DETERMINED unless faked", "flag", "PASSAGE_ALLOW_FAKES")
	} else {
		slog.Warn("TAX_URL is empty: ERS settlement skips every receipt as TAX_NOT_DETERMINED")
	}
	if ports.Extractor, err = ocr.New(getenv("OCR_PROVIDER")); err != nil {
		return api.Ports{}, err
	}
	if ports.Extractor == nil {
		slog.Warn("OCR_PROVIDER is empty: OCR invoice captures answer 503 until a provider is set (\"local\" reads PDF text layers)")
	}
	if ports.LegalEntityCountry, err = domain.ParseLegalEntityCountries(getenv("LEGAL_ENTITY_COUNTRIES")); err != nil {
		return api.Ports{}, err
	}
	if len(ports.LegalEntityCountry) == 0 {
		slog.Warn("LEGAL_ENTITY_COUNTRIES is empty: one-time payee erasure keeps every payee")
	}
	profile, err := buildProfile(getenv, allowFakes)
	ports.Profile = profile
	if err != nil {
		return ports, err
	}
	var sampler *domain.Sampler
	switch seed := getenv("AUDIT_SAMPLE_SEED"); {
	case seed != "":
		sampler, err = domain.NewSampler([]byte(seed))
	case allowFakes:
		slog.Warn("AUDIT_SAMPLE_SEED is empty: using a random seed, so audit draws are not reproducible across restarts", "flag", "PASSAGE_ALLOW_FAKES")
		sampler, err = domain.NewRandomSampler()
	default:
		return api.Ports{}, fmt.Errorf("AUDIT_SAMPLE_SEED is required (at least 16 characters, from a secret; set PASSAGE_ALLOW_FAKES=1 only for local development)")
	}
	if err != nil {
		return api.Ports{}, fmt.Errorf("AUDIT_SAMPLE_SEED: %w", err)
	}
	ports.AuditSampler = sampler
	return ports, nil
}

var dimensionRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,19}$`)

var profileEnv = []struct {
	key, fake string
	field     func(*domain.PostingProfile) *string
}{
	{"POSTING_LEDGER_ID", "00000000-0000-4000-8000-000000000001", func(p *domain.PostingProfile) *string { return &p.LedgerID }},
	{"POSTING_ACCOUNT_INVENTORY", "1400", func(p *domain.PostingProfile) *string { return &p.Inventory }},
	{"POSTING_ACCOUNT_GRIR", "2100", func(p *domain.PostingProfile) *string { return &p.GRIR }},
	{"POSTING_ACCOUNT_PPV", "5200", func(p *domain.PostingProfile) *string { return &p.PPV }},
	{"POSTING_ACCOUNT_VENDOR_CONTROL", "2000", func(p *domain.PostingProfile) *string { return &p.VendorControl }},
	{"POSTING_ACCOUNT_EXPENSE", "6000", func(p *domain.PostingProfile) *string { return &p.Expense }},
	{"POSTING_ACCOUNT_INPUT_TAX", "1570", func(p *domain.PostingProfile) *string { return &p.InputTax }},
}

type taxRuleEnv struct {
	Country      string `json:"country"`
	Code         string `json:"code"`
	Jurisdiction string `json:"jurisdiction"`
	RatePct      string `json:"ratePct"`
}

func taxConfig(getenv func(string) string) (tax.Config, error) {
	cfg := tax.Config{Group: getenv("TAX_GROUP"), ItemGroup: getenv("TAX_ITEM_GROUP"), Rounding: getenv("TAX_ROUNDING"), Rules: map[string]tax.Rule{}}
	var rules []taxRuleEnv
	if err := json.Unmarshal([]byte(getenv("TAX_RULES")), &rules); err != nil || len(rules) == 0 {
		return tax.Config{}, fmt.Errorf(`TAX_URL needs TAX_RULES, a JSON array of {"country","code","jurisdiction","ratePct"}`)
	}
	for _, r := range rules {
		country := strings.ToUpper(strings.TrimSpace(r.Country))
		rate, err := decimal.NewFromString(r.RatePct)
		if len(country) != 2 || r.Code == "" || r.Jurisdiction == "" || err != nil || rate.IsNegative() {
			return tax.Config{}, fmt.Errorf("TAX_RULES entry %q needs a two-letter country, a code, a jurisdiction and a non-negative ratePct", r.Country)
		}
		if _, dup := cfg.Rules[country]; dup {
			return tax.Config{}, fmt.Errorf("TAX_RULES lists country %s twice", country)
		}
		cfg.Rules[country] = tax.Rule{Code: r.Code, Jurisdiction: r.Jurisdiction, RatePct: rate}
	}
	return cfg, nil
}

func buildProfile(getenv func(string) string, allowFakes bool) (domain.PostingProfile, error) {
	var profile domain.PostingProfile
	for _, e := range profileEnv {
		value := getenv(e.key)
		switch {
		case value != "":
		case allowFakes:
			slog.Warn("posting account is empty: using the development default", "key", e.key, "account", e.fake)
			value = e.fake
		default:
			return domain.PostingProfile{}, fmt.Errorf("%s is required (set PASSAGE_ALLOW_FAKES=1 only for local development)", e.key)
		}
		*e.field(&profile) = value
	}
	if !ids.Valid(profile.LedgerID) {
		return domain.PostingProfile{}, fmt.Errorf("POSTING_LEDGER_ID must be a UUID")
	}
	// The dimension that carries the vendor group code on the vendor-control
	// line. The ledger must define it (PATCH /v1/ledger/ledgers/{id} with
	// addDimensionAttributes) before this service posts it; "-" posts none.
	switch dim := getenv("POSTING_VENDOR_GROUP_DIMENSION"); dim {
	case "":
		profile.VendorGroupDimension = domain.DefaultVendorGroupDimension
	case "-":
	default:
		if !dimensionRe.MatchString(dim) {
			return domain.PostingProfile{}, fmt.Errorf("POSTING_VENDOR_GROUP_DIMENSION must be a ledger dimension attribute (A-Z, digits, underscore) or -")
		}
		profile.VendorGroupDimension = dim
	}
	return profile, nil
}
