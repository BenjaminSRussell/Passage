package main

import (
	"fmt"
	"log/slog"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/inventory"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/periods"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/purchasing"
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

var profileEnv = []struct {
	key, fake string
	field     func(*domain.PostingProfile) *string
}{
	{"POSTING_LEDGER_ID", "00000000-0000-4000-8000-000000000001", func(p *domain.PostingProfile) *string { return &p.LedgerID }},
	{"POSTING_ACCOUNT_INVENTORY", "1400", func(p *domain.PostingProfile) *string { return &p.Inventory }},
	{"POSTING_ACCOUNT_GRIR", "2100", func(p *domain.PostingProfile) *string { return &p.GRIR }},
	{"POSTING_ACCOUNT_PPV", "5200", func(p *domain.PostingProfile) *string { return &p.PPV }},
	{"POSTING_ACCOUNT_VENDOR_CONTROL", "2000", func(p *domain.PostingProfile) *string { return &p.VendorControl }},
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
	return profile, nil
}
