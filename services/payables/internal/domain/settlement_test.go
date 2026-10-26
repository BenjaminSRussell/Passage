package domain_test

import (
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

var settlementDate = time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func eurInvoice(id, amount, rate string, due time.Time) domain.Invoice {
	return domain.Invoice{
		ID: id, Currency: "EUR", Amount: dec(amount), Open: dec(amount), Rate: dec(rate), DueDate: due,
		DiscountPct: dec("2"), DiscountDate: settlementDate.AddDate(0, 0, 1),
	}
}

func baseRequest(credit, creditRate string, invoices ...domain.Invoice) domain.Request {
	return domain.Request{
		SettlementDate: settlementDate, AccountingCurrency: "USD", CreditCurrency: "EUR",
		Credit: dec(credit), CreditRate: dec(creditRate), Invoices: invoices,
		Policy: domain.Policy{DiscountOnPartial: true, AutoWriteOff: true, WriteOffLimit: dec("1.00")},
	}
}

func journalAmounts(t *testing.T, res domain.Result) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, l := range res.Journal {
		out[l.Side+":"+l.Account] = l.Amount.Amount
	}
	return out
}

func TestSettle_WorkedExample(t *testing.T) {
	res, err := domain.Settle(baseRequest("980.00", "1.08", eurInvoice("inv-1", "1000.00", "1.10", settlementDate.AddDate(0, 0, 20))))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"debit:ap_control": "1100.00", "credit:bank_clearing": "1058.40", "credit:discount_taken": "22.00", "credit:fx_gain": "19.60",
	}
	got := journalAmounts(t, res)
	if len(got) != len(want) {
		t.Fatalf("journal %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
	line := res.Lines[0]
	if line.Status != domain.StatusSettled || line.Applied.Amount != "980.00" || line.Discount.Amount != "20.00" || line.OpenAfter.Amount != "0.00" {
		t.Errorf("unexpected line %+v", line)
	}
}

func TestSettle_Scenarios(t *testing.T) {
	late := func(inv domain.Invoice) domain.Invoice {
		inv.DiscountDate = settlementDate.AddDate(0, 0, -1)
		return inv
	}
	tests := []struct {
		name       string
		req        domain.Request
		wantStatus []string
		wantOpen   []string
		wantCredit string
		wantJrnl   map[string]string
	}{
		{
			"discount lost after the discount date",
			baseRequest("1000.00", "1.10", late(eurInvoice("a", "1000.00", "1.10", settlementDate))),
			[]string{domain.StatusSettled}, []string{"0.00"}, "0.00",
			map[string]string{"debit:ap_control": "1100.00", "credit:bank_clearing": "1100.00"},
		},
		{
			"excess credit stays unapplied",
			baseRequest("1500.00", "1.10", eurInvoice("a", "1000.00", "1.10", settlementDate)),
			[]string{domain.StatusSettled}, []string{"0.00"}, "520.00",
			map[string]string{"debit:ap_control": "1100.00", "credit:bank_clearing": "1078.00", "credit:discount_taken": "22.00"},
		},
		{
			"partial settlement pro-rates the discount",
			baseRequest("490.00", "1.10", eurInvoice("a", "1000.00", "1.10", settlementDate)),
			[]string{domain.StatusPartiallySettled}, []string{"500.20"}, "0.00",
			map[string]string{"debit:ap_control": "549.78", "credit:bank_clearing": "539.00", "credit:discount_taken": "10.78"},
		},
		{
			"residual within the limit is written off",
			baseRequest("979.50", "1.10", eurInvoice("a", "1000.00", "1.10", settlementDate)),
			[]string{domain.StatusSettled}, []string{"0.00"}, "0.00",
			map[string]string{"debit:ap_control": "1100.00", "credit:bank_clearing": "1077.45", "credit:discount_taken": "21.55", "credit:write_off": "1.00"},
		},
		{
			"marked invoice is settled before an earlier due date",
			func() domain.Request {
				marked := eurInvoice("late-marked", "100.00", "1.00", settlementDate.AddDate(0, 1, 0))
				marked.Marked = true
				return baseRequest("98.00", "1.00", eurInvoice("early", "100.00", "1.00", settlementDate), marked)
			}(),
			[]string{domain.StatusSettled}, []string{"0.00"}, "0.00",
			map[string]string{"debit:ap_control": "100.00", "credit:bank_clearing": "98.00", "credit:discount_taken": "2.00"},
		},
		{
			"cross-currency settlement uses the accounting currency as unit",
			func() domain.Request {
				r := baseRequest("1078.00", "1", eurInvoice("a", "1000.00", "1.10", settlementDate))
				r.CreditCurrency = "USD"
				return r
			}(),
			[]string{domain.StatusSettled}, []string{"0.00"}, "0.00",
			map[string]string{"debit:ap_control": "1100.00", "credit:bank_clearing": "1078.00", "credit:discount_taken": "22.00"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := domain.Settle(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Lines) != len(tc.wantStatus) {
				t.Fatalf("got %d lines, want %d", len(res.Lines), len(tc.wantStatus))
			}
			for i, l := range res.Lines {
				if l.Status != tc.wantStatus[i] || l.OpenAfter.Amount != tc.wantOpen[i] {
					t.Errorf("line %d = %+v", i, l)
				}
			}
			if res.CreditRemaining.Amount != tc.wantCredit {
				t.Errorf("credit remaining %s, want %s", res.CreditRemaining.Amount, tc.wantCredit)
			}
			got := journalAmounts(t, res)
			if len(got) != len(tc.wantJrnl) {
				t.Fatalf("journal %v, want %v", got, tc.wantJrnl)
			}
			for k, v := range tc.wantJrnl {
				if got[k] != v {
					t.Errorf("%s = %s, want %s", k, got[k], v)
				}
			}
		})
	}
}

func TestSettle_DiscountNotProRatedWhenPolicyOff(t *testing.T) {
	req := baseRequest("490.00", "1.10", eurInvoice("a", "1000.00", "1.10", settlementDate))
	req.Policy.DiscountOnPartial = false
	res, err := domain.Settle(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Lines[0].Discount.Amount != "0.00" || res.Lines[0].OpenAfter.Amount != "510.00" {
		t.Errorf("unexpected line %+v", res.Lines[0])
	}
}

func TestSettle_NoWriteOffWhenDisabled(t *testing.T) {
	req := baseRequest("979.50", "1.10", eurInvoice("a", "1000.00", "1.10", settlementDate))
	req.Policy.AutoWriteOff = false
	res, err := domain.Settle(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Lines[0].WriteOff.Amount != "0.00" || res.Lines[0].OpenAfter.Amount != "0.91" {
		t.Errorf("unexpected line %+v", res.Lines[0])
	}
}

func TestSettle_WriteOffGuard(t *testing.T) {
	noDiscount := func(id, amount string) domain.Invoice {
		inv := eurInvoice(id, amount, "1.00", settlementDate)
		inv.DiscountPct = decimal.Zero
		return inv
	}
	tests := []struct {
		name         string
		credit       string
		autoWriteOff bool
		wantWriteOff string
		wantOpen     string
		wantStatus   string
	}{
		{"residual one cent over the limit is not written off", "998.99", true, "0.00", "1.01", domain.StatusPartiallySettled},
		{"residual exactly at the limit is written off", "999.00", true, "1.00", "0.00", domain.StatusSettled},
		{"residual under the limit is written off", "999.50", true, "0.50", "0.00", domain.StatusSettled},
		{"residual under the limit stays open when auto write-off is off", "999.50", false, "0.00", "0.50", domain.StatusPartiallySettled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := baseRequest(tc.credit, "1.00", noDiscount("inv", "1000.00"))
			req.Policy.AutoWriteOff = tc.autoWriteOff
			res, err := domain.Settle(req)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Lines) != 1 {
				t.Fatalf("got %d lines, want 1", len(res.Lines))
			}
			l := res.Lines[0]
			if l.WriteOff.Amount != tc.wantWriteOff || l.OpenAfter.Amount != tc.wantOpen || l.Status != tc.wantStatus {
				t.Errorf("writeOff %s open %s status %s; want %s %s %s", l.WriteOff.Amount, l.OpenAfter.Amount, l.Status, tc.wantWriteOff, tc.wantOpen, tc.wantStatus)
			}
			if got := amountOf(res, domain.SideCredit, domain.AccountWriteOff); got.Cmp(rat(tc.wantWriteOff)) != 0 {
				t.Errorf("journal write_off = %s, want %s", got.FloatString(2), tc.wantWriteOff)
			}
		})
	}
}

func TestSettle_InvoiceWithoutCreditIsNotTouched(t *testing.T) {
	req := baseRequest("490.00", "1.00", eurInvoice("big", "490.00", "1.00", settlementDate), eurInvoice("tiny", "0.80", "1.00", settlementDate.AddDate(0, 0, 5)))
	req.Policy.DiscountOnPartial = false
	req.Invoices[0].DiscountPct = decimal.Zero
	res, err := domain.Settle(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 1 || res.Lines[0].InvoiceID != "big" {
		t.Fatalf("lines %+v, want only big", res.Lines)
	}
	if res.CreditRemaining.Amount != "0.00" {
		t.Errorf("credit remaining %s", res.CreditRemaining.Amount)
	}
}

func rat(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		panic(s)
	}
	return r
}

func roundRat(r *big.Rat) *big.Rat {
	scaled := new(big.Rat).Mul(r, big.NewRat(100, 1))
	n := new(big.Int).Mul(scaled.Num(), big.NewInt(2))
	n.Add(n, scaled.Denom())
	q := new(big.Int).Div(n, new(big.Int).Mul(big.NewInt(2), scaled.Denom()))
	return new(big.Rat).SetFrac(q, big.NewInt(100))
}

func mul(a, b *big.Rat) *big.Rat { return new(big.Rat).Mul(a, b) }
func add(a, b *big.Rat) *big.Rat { return new(big.Rat).Add(a, b) }
func sub(a, b *big.Rat) *big.Rat { return new(big.Rat).Sub(a, b) }

func within(got, want, tolerance *big.Rat) bool {
	return new(big.Rat).Abs(sub(got, want)).Cmp(tolerance) <= 0
}

func amountOf(res domain.Result, side, account string) *big.Rat {
	for _, l := range res.Journal {
		if l.Side == side && l.Account == account {
			return rat(l.Amount.Amount)
		}
	}
	return new(big.Rat)
}

func journalTotals(res domain.Result) (debit, credit *big.Rat) {
	debit, credit = new(big.Rat), new(big.Rat)
	for _, l := range res.Journal {
		if l.Side == domain.SideDebit {
			debit = add(debit, rat(l.Amount.Amount))
		} else {
			credit = add(credit, rat(l.Amount.Amount))
		}
	}
	return debit, credit
}

func TestSettle_Grid300(t *testing.T) {
	amounts := []string{"20.00", "250.00", "1000.00"}
	ratePairs := [][2]string{{"1.10", "1.08"}, {"1.10", "1.10"}, {"1.10", "1.15"}, {"0.92", "0.95"}, {"1.25", "1.20"}}
	fractions := []*big.Rat{big.NewRat(1, 4), big.NewRat(1, 2), big.NewRat(3, 4), big.NewRat(39, 40), big.NewRat(1, 1)}
	writeOffLimit := rat("1")
	count := 0
	for _, amount := range amounts {
		for _, pair := range ratePairs {
			for fi, fraction := range fractions {
				for _, taken := range []bool{true, false} {
					for _, cross := range []bool{false, true} {
						count++
						name := fmt.Sprintf("%s/%v/f%d/taken=%v/cross=%v", amount, pair, fi, taken, cross)
						t.Run(name, func(t *testing.T) {
							amt, invoiceRate, creditRate := rat(amount), rat(pair[0]), rat(pair[1])
							termsDiscount := new(big.Rat)
							if taken {
								termsDiscount = roundRat(mul(amt, rat("0.02")))
							}
							net := sub(amt, termsDiscount)
							credit := roundRat(mul(fraction, net))
							inv := eurInvoice("inv", amount, pair[0], settlementDate)
							if !taken {
								inv.DiscountDate = settlementDate.AddDate(0, 0, -1)
							}
							req := baseRequest(credit.FloatString(2), pair[1], inv)
							if cross {
								credit = roundRat(mul(credit, invoiceRate))
								req = baseRequest(credit.FloatString(2), "1", inv)
								req.CreditCurrency = "USD"
							}
							res, err := domain.Settle(req)
							if err != nil {
								t.Fatal(err)
							}
							if len(res.Lines) != 1 {
								t.Fatalf("got %d lines, want 1", len(res.Lines))
							}
							l := res.Lines[0]
							applied, discount, writeOff := rat(l.Applied.Amount), rat(l.Discount.Amount), rat(l.WriteOff.Amount)
							openAfter := rat(l.OpenAfter.Amount)
							tolerance := mul(rat("0.005"), maxRat(invoiceRate, big.NewRat(1, 1)))
							bank := amountOf(res, domain.SideCredit, domain.AccountBankClearing)
							ap := amountOf(res, domain.SideDebit, domain.AccountAPControl)
							fx := sub(amountOf(res, domain.SideCredit, domain.AccountFXGain), amountOf(res, domain.SideDebit, domain.AccountFXLoss))

							if applied.Sign() <= 0 || applied.Cmp(amt) > 0 {
								t.Errorf("applied %s outside (0, %s]", applied.FloatString(2), amount)
							}
							if openAfter.Sign() < 0 || openAfter.Cmp(sub(sub(sub(amt, applied), discount), writeOff)) != 0 {
								t.Errorf("open after %s is not amount - applied - discount - write-off", openAfter.FloatString(2))
							}
							if (openAfter.Sign() == 0) != (l.Status == domain.StatusSettled) {
								t.Errorf("status %s with open %s", l.Status, openAfter.FloatString(2))
							}
							if discount.Sign() < 0 || discount.Cmp(termsDiscount) > 0 {
								t.Errorf("discount %s outside terms 0..%s", discount.FloatString(2), termsDiscount.FloatString(2))
							}
							if fraction.Cmp(big.NewRat(1, 1)) == 0 && (applied.Cmp(net) != 0 || discount.Cmp(termsDiscount) != 0 || l.Status != domain.StatusSettled) {
								t.Errorf("full payment must settle at net: applied %s discount %s status %s", applied.FloatString(2), discount.FloatString(2), l.Status)
							}
							if proRata := mul(termsDiscount, applied); fraction.Cmp(big.NewRat(1, 1)) < 0 && !within(mul(discount, amt), proRata, mul(amt, rat("0.005"))) {
								t.Errorf("discount %s is not pro-rata to the applied share", discount.FloatString(2))
							}
							residual := sub(sub(amt, applied), discount)
							wantWriteOff := new(big.Rat)
							if residual.Sign() > 0 && residual.Cmp(writeOffLimit) <= 0 {
								wantWriteOff = residual
							}
							if writeOff.Cmp(wantWriteOff) != 0 {
								t.Errorf("write-off %s, want %s for residual %s", writeOff.FloatString(2), wantWriteOff.FloatString(2), residual.FloatString(2))
							}
							if got := amountOf(res, domain.SideCredit, domain.AccountWriteOff); !within(got, mul(writeOff, invoiceRate), tolerance) {
								t.Errorf("journal write-off %s does not carry the line write-off %s", got.FloatString(2), writeOff.FloatString(2))
							}
							if got := amountOf(res, domain.SideCredit, domain.AccountDiscountTaken); !within(got, mul(discount, invoiceRate), tolerance) {
								t.Errorf("journal discount %s does not carry the line discount %s", got.FloatString(2), discount.FloatString(2))
							}
							debit, journalCredit := journalTotals(res)
							if debit.Cmp(journalCredit) != 0 {
								t.Errorf("journal unbalanced: debit %s credit %s", debit.FloatString(2), journalCredit.FloatString(2))
							}
							remaining := rat(res.CreditRemaining.Amount)
							consumed := sub(credit, remaining)
							if remaining.Sign() < 0 || consumed.Sign() <= 0 {
								t.Errorf("credit remaining %s of %s", remaining.FloatString(2), credit.FloatString(2))
							}
							if cross && consumed.Cmp(bank) != 0 {
								t.Errorf("accounting credit consumed %s, bank clearing %s", consumed.FloatString(2), bank.FloatString(2))
							}
							if !cross && consumed.Cmp(applied) != 0 {
								t.Errorf("credit consumed %s, applied %s", consumed.FloatString(2), applied.FloatString(2))
							}
							if exact := mul(add(add(applied, discount), writeOff), invoiceRate); !within(ap, exact, mul(tolerance, rat("3"))) {
								t.Errorf("AP control %s, exact conversion %s", ap.FloatString(2), exact.FloatString(2))
							}
							if cross && !within(fx, sub(mul(applied, invoiceRate), bank), mul(tolerance, rat("2"))) {
								t.Errorf("cross-currency fx %s, applied x rate - bank %s", fx.FloatString(2), sub(mul(applied, invoiceRate), bank).FloatString(2))
							}
							if !cross {
								if !within(bank, mul(applied, creditRate), tolerance) {
									t.Errorf("bank clearing %s, exact conversion %s", bank.FloatString(2), mul(applied, creditRate).FloatString(2))
								}
								if !within(fx, sub(mul(applied, invoiceRate), mul(applied, creditRate)), mul(tolerance, rat("2"))) {
									t.Errorf("fx %s, exact difference %s", fx.FloatString(2), sub(mul(applied, invoiceRate), mul(applied, creditRate)).FloatString(2))
								}
							}
						})
					}
				}
			}
		}
	}
	if count != 300 {
		t.Fatalf("grid has %d scenarios, want 300", count)
	}
}

func maxRat(a, b *big.Rat) *big.Rat {
	if a.Cmp(b) > 0 {
		return a
	}
	return b
}

func TestSettle_JournalAlwaysBalances(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		cents := func(name string) string {
			return decimal.New(rapid.Int64Range(1, 1_000_000_000).Draw(rt, name), -2).StringFixed(2)
		}
		rate := func(name string) string {
			return decimal.New(rapid.Int64Range(10_000, 300_000).Draw(rt, name), -5).StringFixed(5)
		}
		count := rapid.IntRange(1, 6).Draw(rt, "count")
		invoices := make([]domain.Invoice, count)
		for i := range invoices {
			inv := eurInvoice(fmt.Sprintf("inv-%d", i), cents("amount"), rate("rate"), settlementDate.AddDate(0, 0, rapid.IntRange(0, 60).Draw(rt, "due")))
			inv.Marked = rapid.Bool().Draw(rt, "marked")
			if rapid.Bool().Draw(rt, "late") {
				inv.DiscountDate = settlementDate.AddDate(0, 0, -1)
			}
			invoices[i] = inv
		}
		req := baseRequest(cents("credit"), rate("creditRate"), invoices...)
		req.Policy.AutoWriteOff = rapid.Bool().Draw(rt, "autoWriteOff")
		req.Policy.DiscountOnPartial = rapid.Bool().Draw(rt, "onPartial")
		if rapid.Bool().Draw(rt, "cross") {
			req.CreditCurrency = "USD"
			req.CreditRate = dec("1")
		}
		res, err := domain.Settle(req)
		if err != nil {
			rt.Fatalf("settle: %v", err)
		}
		for _, l := range res.Lines {
			if dec(l.OpenAfter.Amount).IsNegative() {
				rt.Fatalf("negative open amount: %+v", l)
			}
		}
		if dec(res.CreditRemaining.Amount).IsNegative() || dec(res.CreditRemaining.Amount).GreaterThan(req.Credit) {
			rt.Fatalf("credit remaining out of range: %s", res.CreditRemaining.Amount)
		}
	})
}

func validDTO() domain.RequestDTO {
	return domain.RequestDTO{
		SettlementDate: "2026-01-10", AccountingCurrency: "USD",
		Credit: domain.CreditDTO{Currency: "EUR", Amount: "980.00", Rate: "1.08"},
		Invoices: []domain.InvoiceDTO{{
			ID: "inv-1", Currency: "EUR", Amount: "1000.00", Rate: "1.10", DueDate: "2026-01-30",
			DiscountPct: "2", DiscountDate: "2026-01-11",
		}},
	}
}

func TestParseRequest(t *testing.T) {
	now := settlementDate
	tests := []struct {
		name     string
		mutate   func(*domain.RequestDTO)
		wantCode string
	}{
		{"valid", func(*domain.RequestDTO) {}, ""},
		{"missing settlement date", func(d *domain.RequestDTO) { d.SettlementDate = "" }, domain.CodeValidation},
		{"non-ISO settlement date", func(d *domain.RequestDTO) { d.SettlementDate = "10/01/2026" }, domain.CodeValidation},
		{"settlement date far in the future", func(d *domain.RequestDTO) { d.SettlementDate = "2030-01-01" }, domain.CodeValidation},
		{"empty invoices", func(d *domain.RequestDTO) { d.Invoices = nil }, domain.CodeValidation},
		{"too many invoices", func(d *domain.RequestDTO) {
			d.Invoices = make([]domain.InvoiceDTO, 501)
		}, domain.CodeValidation},
		{"duplicate invoice id", func(d *domain.RequestDTO) { d.Invoices = append(d.Invoices, d.Invoices[0]) }, domain.CodeValidation},
		{"NaN amount", func(d *domain.RequestDTO) { d.Credit.Amount = "NaN" }, domain.CodeValidation},
		{"negative amount", func(d *domain.RequestDTO) { d.Credit.Amount = "-5.00" }, domain.CodeValidation},
		{"too many decimals for currency", func(d *domain.RequestDTO) { d.Credit.Amount = "1.005" }, domain.CodeValidation},
		{"zero rate", func(d *domain.RequestDTO) { d.Invoices[0].Rate = "0" }, domain.CodeValidation},
		{"missing rate for foreign currency", func(d *domain.RequestDTO) { d.Credit.Rate = "" }, domain.CodeCurrency},
		{"rate other than 1 for accounting currency", func(d *domain.RequestDTO) {
			d.Invoices[0].Currency, d.Invoices[0].Rate = "USD", "1.2"
		}, domain.CodeCurrency},
		{"discount above 100", func(d *domain.RequestDTO) { d.Invoices[0].DiscountPct = "101" }, domain.CodeValidation},
		{"open amount above amount", func(d *domain.RequestDTO) { d.Invoices[0].OpenAmount = "1000.01" }, domain.CodeValidation},
		{"bad write-off limit", func(d *domain.RequestDTO) { d.Policy.WriteOffLimit = "abc" }, domain.CodeValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dto := validDTO()
			tc.mutate(&dto)
			_, err := domain.ParseRequest(dto, now)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				return
			}
			var pe *domain.Error
			if err == nil || !asDomainError(err, &pe) || pe.Code != tc.wantCode {
				t.Fatalf("got %v, want code %s", err, tc.wantCode)
			}
		})
	}
}

func asDomainError(err error, target **domain.Error) bool {
	e, ok := err.(*domain.Error)
	if ok {
		*target = e
	}
	return ok
}
