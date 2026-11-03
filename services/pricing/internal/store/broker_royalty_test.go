package store_test

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	brokerVendor   = "00000000-0000-0000-0008-0000000000b1"
	licensorVendor = "00000000-0000-0000-0008-0000000000c1"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func brokerContract() domain.BrokerContract {
	return domain.BrokerContract{LegalEntityID: legalEntity, Name: "Agent Smith", BrokerVendorID: brokerVendor, ProductGroupID: "BRANDED",
		RateKind: domain.RatePercentNet, Rate: dec("2.5"), Currency: "USD", ValidFrom: day("2027-01-01"), ValidTo: day("2027-12-31"),
		PayableAccount: domain.BrokerPayableAccount, ExpenseAccount: domain.BrokerExpenseAccount}
}

func royaltyAgreement(minimum string) domain.RoyaltyAgreement {
	return domain.RoyaltyAgreement{LegalEntityID: legalEntity, Name: "Brand licence", LicensorVendorID: licensorVendor, ProductGroupID: "BRANDED",
		Basis: domain.RoyaltyNetSales, Period: domain.PeriodMonth, Tiers: []domain.Tier{tier("0", "4"), tier("50000", "5")}, MinimumRoyalty: dec(minimum),
		Currency: "USD", ValidFrom: day("2027-01-01"), ValidTo: day("2027-12-31"), PayableAccount: domain.RoyaltyPayableAccount,
		ExpenseAccount: domain.RoyaltyExpenseAccount}
}

func contractLine(id, date, net, qty string) domain.ContractLine {
	return domain.ContractLine{InvoiceLineID: id, InvoiceDate: day(date), CustomerID: "00000000-0000-0000-0005-000000000001",
		ProductID: "p1", ProductGroupID: "BRANDED", Currency: "USD", NetAmount: dec(net), Quantity: dec(qty)}
}

func creditLine(id, reverses, date, net, qty string) domain.ContractLine {
	l := contractLine(id, date, net, qty)
	l.ReversesInvoiceLineID = reverses
	return l
}

// FR-26.16 against Postgres: a broker contract accrues 2.5% of net per invoice line
// (Dr 6210 / Cr 2175), a credit note reverses the original in proportion and never beyond it,
// a redelivered line posts once, and the period is claimed as BC-n and approved by a second user.
func TestPostgres_BrokerContractAccrualCreditAndClaim(t *testing.T) {
	pool := appPool(t)
	maker, checker := user(1, "u1"), user(1, "u2")
	s := store.New(pool)
	c, err := s.CreateBrokerContract(maker, brokerContract(), "bc-1")
	if err != nil || c.Status != domain.AgreementActive || c.Version != 1 || c.Rate.String() != "2.5" || c.PayableAccount != "2175" {
		t.Fatalf("create = %+v, %v", c, err)
	}
	if replay, err := s.CreateBrokerContract(maker, brokerContract(), "bc-1"); err != nil || replay.ID != c.ID {
		t.Fatalf("replayed create = %+v, %v; want %s", replay, err, c.ID)
	}
	other := brokerContract()
	other.Rate = dec("3")
	if _, err := s.CreateBrokerContract(maker, other, "bc-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key, other body = %v; want %s", err, store.CodeIdempotencyUse)
	}
	if got, err := s.GetBrokerContract(maker, c.ID); err != nil || got.BrokerVendorID != brokerVendor || got.ProductGroupID != "BRANDED" || got.CustomerID != "" {
		t.Fatalf("get = %+v, %v", got, err)
	}

	accrue := func(l domain.ContractLine) (store.ContractAccrual, bool, error) {
		return s.AccrueContractLine(maker, store.ContractAccrualInput{Kind: domain.ContractBroker, ContractID: c.ID, Line: l})
	}
	a, created, err := accrue(contractLine("INV-1/1", "2027-03-10", "10000.00", "5"))
	if err != nil || !created || !a.Amount.Equal(dec("250")) || a.Kind != domain.AccrualLine || a.PeriodKey != "2027-03" {
		t.Fatalf("accrual = %+v created=%v, %v; want 250.00 ACCRUAL in 2027-03", a, created, err)
	}
	if again, created, err := accrue(contractLine("INV-1/1", "2027-03-10", "10000.00", "5")); err != nil || created || again.ID != a.ID {
		t.Fatalf("redelivered line = %+v created=%v, %v; want the first accrual", again, created, err)
	}
	if _, _, err := accrue(contractLine("INV-1/1", "2027-03-10", "9000.00", "5")); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("redelivery with another value = %v; want %s", err, store.CodeIdempotencyUse)
	}
	wrongGroup := contractLine("INV-9/1", "2027-03-10", "100", "1")
	wrongGroup.ProductGroupID = "OTHER"
	if _, _, err := accrue(wrongGroup); problemCode(err) != domain.CodeValidation || httpStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("line outside the product scope = %v; want 422 validation", err)
	}

	if _, _, err := accrue(creditLine("CN-0/1", "INV-404/1", "2027-03-20", "100", "1")); problemCode(err) != domain.CodeCreditExceedsOriginal {
		t.Fatalf("credit of an unknown line = %v; want %s", err, domain.CodeCreditExceedsOriginal)
	}
	first, _, err := accrue(creditLine("CN-1/1", "INV-1/1", "2027-03-20", "4000.00", "2"))
	if err != nil || first.Kind != domain.AccrualCredit || !first.Amount.Equal(dec("-100")) || first.ReversesLineKey != "INV-1/1" {
		t.Fatalf("partial credit = %+v, %v; want -100.00 CREDIT", first, err)
	}
	if _, _, err := accrue(creditLine("CN-2/1", "INV-1/1", "2027-03-21", "6000.01", "3")); problemCode(err) != domain.CodeCreditExceedsOriginal {
		t.Fatalf("credit beyond the original = %v; want %s", err, domain.CodeCreditExceedsOriginal)
	}
	last, _, err := accrue(creditLine("CN-2/1", "INV-1/1", "2027-03-21", "6000.00", "3"))
	if err != nil || !last.Amount.Equal(dec("-150")) {
		t.Fatalf("final credit = %+v, %v; want -150.00", last, err)
	}

	if _, err := s.CreateContractClaim(maker, domain.ContractBroker, c.ID, "2027-03", ""); problemCode(err) != store.CodeNothingToClaim {
		t.Fatalf("claim of a fully credited period = %v; want %s", err, store.CodeNothingToClaim)
	}
	if _, _, err := accrue(contractLine("INV-2/1", "2027-03-25", "2000.00", "1")); err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewContractClaim(maker, domain.ContractBroker, c.ID, "2027-03")
	if err != nil || !preview.Amount.Equal(dec("50")) || preview.Claims != 0 {
		t.Fatalf("preview = %+v, %v; want 50.00", preview, err)
	}
	claim, err := s.CreateContractClaim(maker, domain.ContractBroker, c.ID, "2027-03", "bcl-1")
	if err != nil || claim.Number != "BC-000001" || !claim.Amount.Equal(dec("50")) || claim.Status != domain.ClaimOpen || claim.VendorID != brokerVendor {
		t.Fatalf("claim = %+v, %v; want BC-000001 for 50.00", claim, err)
	}
	if replay, err := s.CreateContractClaim(maker, domain.ContractBroker, c.ID, "2027-03", "bcl-1"); err != nil || replay.ID != claim.ID {
		t.Fatalf("replayed claim = %+v, %v", replay, err)
	}
	if _, err := s.CreateContractClaim(maker, domain.ContractBroker, c.ID, "2027-03", "bcl-2"); problemCode(err) != store.CodeClaimAlreadyProcessed {
		t.Fatalf("second claim of a claimed period = %v; want %s", err, store.CodeClaimAlreadyProcessed)
	}
	if _, err := s.ApproveContractClaim(maker, claim.ID, 1, ""); problemCode(err) != store.CodeClaimFourEyes {
		t.Fatalf("approval by the creator = %v; want %s", err, store.CodeClaimFourEyes)
	}
	if _, err := s.ApproveContractClaim(user(1, ""), claim.ID, 1, ""); problemCode(err) != store.CodeClaimFourEyes {
		t.Fatalf("approval without a user identity = %v; want %s", err, store.CodeClaimFourEyes)
	}
	approved, err := s.ApproveContractClaim(checker, claim.ID, 1, "ap-1")
	if err != nil || approved.Status != domain.ClaimApproved || approved.Version != 2 || approved.ApprovedBy != "u2" {
		t.Fatalf("approval = %+v, %v", approved, err)
	}
	if _, err := s.ApproveContractClaim(checker, claim.ID, 2, ""); problemCode(err) != store.CodeStateInvalid {
		t.Fatalf("second approval = %v; want %s", err, store.CodeStateInvalid)
	}

	rows, err := s.ListContractAccruals(maker, store.ContractAccrualFilter{Kind: domain.ContractBroker, ContractID: c.ID, PeriodKey: "2027-03"})
	if err != nil || len(rows) != 4 {
		t.Fatalf("accrual rows = %+v, %v; want 4", rows, err)
	}
	var posted, claims int
	for _, e := range outboxRows(t, maker, pool) {
		switch e.Type {
		case "dev.passage.pricing.contractaccrual.posted.v1":
			posted++
			if e.Data["debit_account"] != "6210" || e.Data["credit_account"] != "2175" || e.Data["vendor_id"] != brokerVendor || e.Data["contract_kind"] != "BROKER" {
				t.Fatalf("accrual payload = %v; want Dr 6210 / Cr 2175 for the broker", e.Data)
			}
		case "dev.passage.pricing.contractclaim.created.v1", "dev.passage.pricing.contractclaim.approved.v1":
			claims++
		}
	}
	if posted != 4 || claims != 2 {
		t.Fatalf("events: %d accruals, %d claim events; want 4 and 2", posted, claims)
	}

	closed, err := s.CloseBrokerContract(maker, c.ID, 1, "close-1")
	if err != nil || closed.Status != domain.AgreementClosed || closed.Version != 2 {
		t.Fatalf("close = %+v, %v", closed, err)
	}
	if _, err := s.CloseBrokerContract(maker, c.ID, 2, ""); problemCode(err) != store.CodeStateInvalid {
		t.Fatalf("second close = %v; want %s", err, store.CodeStateInvalid)
	}
	if _, _, err := accrue(contractLine("INV-3/1", "2027-04-01", "100", "1")); problemCode(err) != domain.CodeContractClosed {
		t.Fatalf("accrual on a closed contract = %v; want %s", err, domain.CodeContractClosed)
	}
	if again, created, err := accrue(contractLine("INV-2/1", "2027-03-25", "2000.00", "1")); err != nil || created {
		t.Fatalf("redelivery after the close = %+v created=%v, %v; want the stored accrual", again, created, err)
	}
	// A credit note of an accrued line still reverses after the close (and after the
	// validity), in the period of its own date, so the broker does not keep the commission.
	late, created, err := accrue(creditLine("CN-3/1", "INV-2/1", "2028-01-05", "2000.00", "1"))
	if err != nil || !created || !late.Amount.Equal(dec("-50")) || late.PeriodKey != "2028-01" {
		t.Fatalf("credit after the close = %+v created=%v, %v; want -50.00 in 2028-01", late, created, err)
	}
	eur := creditLine("CN-4/1", "INV-1/1", "2028-01-05", "1", "0")
	eur.Currency = "EUR"
	if _, _, err := accrue(eur); problemCode(err) != domain.CodeValidation {
		t.Fatalf("credit in another currency = %v; want %s", err, domain.CodeValidation)
	}

	if _, err := s.GetBrokerContract(user(2, "u9"), c.ID); problemCode(err) != domain.CodeContractNotFound {
		t.Fatalf("other tenant read = %v; want %s", err, domain.CodeContractNotFound)
	}
	if rows, err := s.ListBrokerContracts(principal(1, otherLegalEntity), store.ContractFilter{}); err != nil || len(rows) != 0 {
		t.Fatalf("contracts seen from another legal entity's scope = %+v, %v; want none", rows, err)
	}
	if rows, err := s.ListContractClaims(principal(1, otherLegalEntity), store.ContractClaimFilter{}); err != nil || len(rows) != 0 {
		t.Fatalf("claims seen from another legal entity's scope = %+v, %v; want none", rows, err)
	}
}

// A per-unit broker contract accrues quantity x rate and credits in proportion to quantity.
func TestPostgres_BrokerPerUnitCredit(t *testing.T) {
	s := store.New(appPool(t))
	ctx := user(1, "u1")
	in := brokerContract()
	in.RateKind, in.Rate = domain.RatePerUnit, dec("0.35")
	c, err := s.CreateBrokerContract(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	accrue := func(l domain.ContractLine) store.ContractAccrual {
		t.Helper()
		a, _, err := s.AccrueContractLine(ctx, store.ContractAccrualInput{Kind: domain.ContractBroker, ContractID: c.ID, Line: l})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	if a := accrue(contractLine("INV-1/1", "2027-05-02", "999", "12")); !a.Amount.Equal(dec("4.20")) {
		t.Fatalf("12 x 0.35 = %s; want 4.20", a.Amount)
	}
	if a := accrue(creditLine("CN-1/1", "INV-1/1", "2027-05-09", "100", "3")); !a.Amount.Equal(dec("-1.05")) {
		t.Fatalf("credit of 3 units = %s; want -1.05", a.Amount)
	}
	if _, _, err := s.AccrueContractLine(ctx, store.ContractAccrualInput{Kind: domain.ContractBroker, ContractID: c.ID,
		Line: creditLine("CN-2/1", "INV-1/1", "2027-05-10", "100", "9.5")}); problemCode(err) != domain.CodeCreditExceedsOriginal {
		t.Fatalf("credit of 9.5 of the 9 units left = %v; want %s", err, domain.CodeCreditExceedsOriginal)
	}
}

// FR-26.17 against Postgres: tiered royalty recomputes the period at the tier its cumulative
// net reaches, a period closed below the minimum posts the shortfall once (PRC-0017) and takes
// no more accruals (PRC-0060), and a royalty period is claimed only once closed (PRC-0062) as RY-n.
func TestPostgres_RoyaltyTiersMinimumAndClaims(t *testing.T) {
	pool := appPool(t)
	maker, checker := user(1, "u1"), user(1, "u2")
	s := store.New(pool)
	a, err := s.CreateRoyaltyAgreement(maker, royaltyAgreement("5000"), "ry-1")
	if err != nil || a.MinimumRoyalty.String() != "5000" || len(a.Tiers) != 2 || a.ExpenseAccount != "6220" {
		t.Fatalf("create = %+v, %v", a, err)
	}
	if replay, err := s.CreateRoyaltyAgreement(maker, royaltyAgreement("5000"), "ry-1"); err != nil || replay.ID != a.ID {
		t.Fatalf("replayed create = %+v, %v", replay, err)
	}
	accrue := func(l domain.ContractLine) (store.ContractAccrual, error) {
		acc, _, err := s.AccrueContractLine(maker, store.ContractAccrualInput{Kind: domain.ContractRoyalty, ContractID: a.ID, Line: l})
		return acc, err
	}
	if acc, err := accrue(contractLine("INV-1/1", "2027-03-10", "40000", "1")); err != nil || !acc.Amount.Equal(dec("1600")) {
		t.Fatalf("first accrual = %+v, %v; want 1600.00", acc, err)
	}
	if acc, err := accrue(contractLine("INV-2/1", "2027-03-11", "20000", "1")); err != nil || !acc.Amount.Equal(dec("1400")) {
		t.Fatalf("tier crossing = %+v, %v; want 1400.00 so the period is 5%% of 60,000", acc, err)
	}
	if _, err := accrue(creditLine("CN-1/1", "INV-2/1", "2027-03-12", "30000", "1")); problemCode(err) != domain.CodeCreditExceedsOriginal {
		t.Fatalf("credit of 30,000 on a 20,000 line = %v; want %s", err, domain.CodeCreditExceedsOriginal)
	}
	if acc, err := accrue(creditLine("CN-1/1", "INV-2/1", "2027-03-12", "15000", "1")); err != nil || !acc.Amount.Equal(dec("-1200")) {
		t.Fatalf("credit under the tier = %+v, %v; want -1200.00 so the period is 4%% of 45,000", acc, err)
	}
	if _, err := accrue(creditLine("CN-3/1", "INV-2/1", "2027-03-12", "5000.01", "1")); problemCode(err) != domain.CodeCreditExceedsOriginal {
		t.Fatalf("credit beyond what is left of the line = %v; want %s", err, domain.CodeCreditExceedsOriginal)
	}
	if _, err := accrue(creditLine("CN-2/1", "INV-404/1", "2027-03-12", "10", "1")); problemCode(err) != domain.CodeCreditExceedsOriginal {
		t.Fatalf("credit of an unknown line = %v; want %s", err, domain.CodeCreditExceedsOriginal)
	}

	if _, err := s.CreateContractClaim(maker, domain.ContractRoyalty, a.ID, "2027-03", ""); problemCode(err) != store.CodeContractPeriodOpen {
		t.Fatalf("claim of an open royalty period = %v; want %s", err, store.CodeContractPeriodOpen)
	}
	if _, err := s.CloseRoyaltyPeriod(maker, a.ID, "2027-Q1"); httpStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("closing a quarter of a monthly agreement = %v; want 422", err)
	}
	closed, err := s.CloseRoyaltyPeriod(maker, a.ID, "2027-03")
	if err != nil || closed.AlreadyClosed || !closed.Accrued.Equal(dec("1800")) || !closed.Shortfall.Equal(dec("3200")) || closed.Accrual == nil ||
		closed.Accrual.Kind != domain.AccrualShortfall || closed.Accrual.LineKey != "MIN:2027-03" {
		t.Fatalf("close = %+v, %v; want 1,800.00 accrued and a 3,200.00 shortfall", closed, err)
	}
	again, err := s.CloseRoyaltyPeriod(maker, a.ID, "2027-03")
	if err != nil || !again.AlreadyClosed || !again.Shortfall.Equal(dec("3200")) || again.Accrual != nil {
		t.Fatalf("second close = %+v, %v; want the same shortfall, nothing posted", again, err)
	}
	if _, err := accrue(contractLine("INV-3/1", "2027-03-28", "100", "1")); problemCode(err) != domain.CodePeriodClosed {
		t.Fatalf("accrual into a closed period = %v; want %s", err, domain.CodePeriodClosed)
	}

	claim, err := s.CreateContractClaim(maker, domain.ContractRoyalty, a.ID, "2027-03", "ryc-1")
	if err != nil || claim.Number != "RY-000001" || !claim.Amount.Equal(dec("5000")) || !claim.AccruedTotal.Equal(dec("5000")) || claim.VendorID != licensorVendor {
		t.Fatalf("claim = %+v, %v; want RY-000001 for the 5,000.00 minimum", claim, err)
	}
	if _, err := s.ApproveContractClaim(checker, claim.ID, 1, ""); err != nil {
		t.Fatal(err)
	}

	// An empty period closes with the full minimum as shortfall; a period above the minimum has none.
	if empty, err := s.CloseRoyaltyPeriod(maker, a.ID, "2027-06"); err != nil || !empty.Shortfall.Equal(dec("5000")) {
		t.Fatalf("empty period close = %+v, %v; want a 5,000.00 shortfall", empty, err)
	}
	none, err := s.CreateRoyaltyAgreement(maker, royaltyAgreement("0"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AccrueContractLine(maker, store.ContractAccrualInput{Kind: domain.ContractRoyalty, ContractID: none.ID, Line: contractLine("INV-9/1", "2027-03-10", "1000", "1")}); err != nil {
		t.Fatal(err)
	}
	if c, err := s.CloseRoyaltyPeriod(maker, none.ID, "2027-03"); err != nil || !c.Shortfall.IsZero() || c.Accrual != nil {
		t.Fatalf("close above the minimum = %+v, %v; want no shortfall", c, err)
	}
	if _, err := s.CloseRoyaltyPeriod(maker, none.ID, "2026-12"); httpStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("closing a period before the validity = %v; want 422", err)
	}

	var shortfalls int
	for _, e := range outboxRows(t, maker, pool) {
		if e.Type == "dev.passage.pricing.contractaccrual.posted.v1" && e.Data["kind"] == "SHORTFALL" {
			shortfalls++
			if e.Data["debit_account"] != "6220" || e.Data["credit_account"] != "2176" || e.Data["contract_kind"] != "ROYALTY" ||
				(e.Data["period_id"] == "2027-03" && e.Data["posting_date"] != "2027-03-31") {
				t.Fatalf("shortfall payload = %v; want Dr 6220 / Cr 2176", e.Data)
			}
		}
	}
	if shortfalls != 2 {
		t.Fatalf("shortfall events = %d; want 2 (2027-03 and 2027-06, the second close posted nothing)", shortfalls)
	}

	rows, err := s.ListRoyaltyAgreements(maker, store.ContractFilter{VendorID: licensorVendor})
	if err != nil || len(rows) != 2 {
		t.Fatalf("agreements of the licensor = %+v, %v; want 2", rows, err)
	}
	if c, err := s.CloseRoyaltyAgreement(maker, a.ID, 1, ""); err != nil || c.Status != domain.AgreementClosed {
		t.Fatalf("close = %+v, %v", c, err)
	}
	if _, err := s.CloseRoyaltyAgreement(maker, a.ID, 1, ""); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("close at a stale version = %v; want %s", err, store.CodeVersionConflict)
	}
}

// Claim numbers run per legal entity and kind without gaps or duplicates when claims of
// different contracts are created at the same time (the number is taken under a per-entity
// lock, the unique index is the backstop).
func TestPostgres_ContractClaimNumbersUnderConcurrency(t *testing.T) {
	s := store.New(appPool(t))
	ctx := user(1, "u1")
	const n = 8
	contracts := make([]string, n)
	for i := range contracts {
		c, err := s.CreateBrokerContract(ctx, brokerContract(), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.AccrueContractLine(ctx, store.ContractAccrualInput{Kind: domain.ContractBroker, ContractID: c.ID,
			Line: contractLine(fmt.Sprintf("INV-%d/1", i), "2027-03-10", "1000", "1")}); err != nil {
			t.Fatal(err)
		}
		contracts[i] = c.ID
	}
	numbers := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i, id := range contracts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := s.CreateContractClaim(ctx, domain.ContractBroker, id, "2027-03", "")
			numbers[i], errs[i] = c.Number, err
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(numbers)
	for i, got := range numbers {
		if want := fmt.Sprintf("BC-%06d", i+1); got != want {
			t.Fatalf("claim numbers = %v; want BC-000001 to BC-%06d", numbers, n)
		}
	}
}
