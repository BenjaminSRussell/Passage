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

func TestSettle_NoWriteOffWhenDisabledOrNothingApplied(t *testing.T) {
	req := baseRequest("979.50", "1.10", eurInvoice("a", "1000.00", "1.10", settlementDate))
	req.Policy.AutoWriteOff = false
	res, err := domain.Settle(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Lines[0].WriteOff.Amount != "0.00" || res.Lines[0].OpenAfter.Amount != "0.91" {
		t.Errorf("unexpected line %+v", res.Lines[0])
	}
	small := baseRequest("500.00", "1.00", eurInvoice("big", "490.00", "1.00", settlementDate), eurInvoice("tiny", "0.80", "1.00", settlementDate.AddDate(0, 0, 5)))
	res, err = domain.Settle(small)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range res.Lines {
		if l.InvoiceID == "tiny" && l.WriteOff.Amount != "0.00" && l.Applied.Amount == "0.00" {
			t.Errorf("untouched invoice written off: %+v", l)
		}
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

func minRat(a, b *big.Rat) *big.Rat {
	if a.Cmp(b) < 0 {
		return a
	}
	return b
}

func mul(a, b *big.Rat) *big.Rat { return new(big.Rat).Mul(a, b) }
func add(a, b *big.Rat) *big.Rat { return new(big.Rat).Add(a, b) }
func sub(a, b *big.Rat) *big.Rat { return new(big.Rat).Sub(a, b) }
func quo(a, b *big.Rat) *big.Rat { return new(big.Rat).Quo(a, b) }

type expected struct{ ap, bank, discount, writeOff, fx *big.Rat }

func oracle(amount, invoiceRate, creditRate string, fraction *big.Rat, taken, cross bool) (expected, *big.Rat) {
	amt, ri, rc := rat(amount), rat(invoiceRate), rat(creditRate)
	full := new(big.Rat)
	if taken {
		full = roundRat(quo(mul(amt, rat("2")), rat("100")))
	}
	net := sub(amt, full)
	var applied, appliedAcct, bank, credit *big.Rat
	var partial bool
	if cross {
		netAcct := roundRat(mul(net, ri))
		credit = roundRat(mul(fraction, netAcct))
		used := minRat(credit, netAcct)
		partial = used.Cmp(netAcct) < 0
		applied = net
		if partial {
			applied = roundRat(quo(used, ri))
		}
		appliedAcct, bank = used, used
	} else {
		credit = roundRat(mul(fraction, net))
		applied = minRat(credit, net)
		partial = applied.Cmp(net) < 0
		appliedAcct = roundRat(mul(applied, ri))
		bank = roundRat(mul(applied, rc))
	}
	discount := full
	if partial {
		discount = roundRat(quo(mul(full, applied), amt))
	}
	residual := sub(sub(amt, applied), discount)
	writeOff := new(big.Rat)
	if residual.Sign() > 0 && residual.Cmp(rat("1")) <= 0 {
		writeOff = residual
	}
	discountAcct, writeOffAcct := roundRat(mul(discount, ri)), roundRat(mul(writeOff, ri))
	return expected{
		ap: add(add(appliedAcct, discountAcct), writeOffAcct), bank: bank, discount: discountAcct,
		writeOff: writeOffAcct, fx: sub(bank, appliedAcct),
	}, credit
}

func amountOf(res domain.Result, side, account string) *big.Rat {
	for _, l := range res.Journal {
		if l.Side == side && l.Account == account {
			return rat(l.Amount.Amount)
		}
	}
	return new(big.Rat)
}

func TestSettle_Grid300(t *testing.T) {
	amounts := []string{"20.00", "250.00", "1000.00"}
	ratePairs := [][2]string{{"1.10", "1.08"}, {"1.10", "1.10"}, {"1.10", "1.15"}, {"0.92", "0.95"}, {"1.25", "1.20"}}
	fractions := []*big.Rat{big.NewRat(1, 4), big.NewRat(1, 2), big.NewRat(3, 4), big.NewRat(39, 40), big.NewRat(1, 1)}
	count := 0
	for _, amount := range amounts {
		for _, pair := range ratePairs {
			for fi, fraction := range fractions {
				for _, taken := range []bool{true, false} {
					for _, cross := range []bool{false, true} {
						count++
						name := fmt.Sprintf("%s/%v/f%d/taken=%v/cross=%v", amount, pair, fi, taken, cross)
						t.Run(name, func(t *testing.T) {
							want, credit := oracle(amount, pair[0], pair[1], fraction, taken, cross)
							inv := eurInvoice("inv", amount, pair[0], settlementDate)
							if !taken {
								inv.DiscountDate = settlementDate.AddDate(0, 0, -1)
							}
							req := baseRequest(credit.FloatString(2), pair[1], inv)
							if cross {
								req.CreditCurrency, req.CreditRate = "USD", dec("1")
							}
							res, err := domain.Settle(req)
							if err != nil {
								t.Fatal(err)
							}
							fx := sub(amountOf(res, domain.SideCredit, domain.AccountFXGain), amountOf(res, domain.SideDebit, domain.AccountFXLoss))
							checks := []struct {
								name      string
								got, want *big.Rat
							}{
								{"ap", amountOf(res, domain.SideDebit, domain.AccountAPControl), want.ap},
								{"bank", amountOf(res, domain.SideCredit, domain.AccountBankClearing), want.bank},
								{"discount", amountOf(res, domain.SideCredit, domain.AccountDiscountTaken), want.discount},
								{"writeOff", amountOf(res, domain.SideCredit, domain.AccountWriteOff), want.writeOff},
								{"fx gain(+)/loss(-)", fx, new(big.Rat).Neg(want.fx)},
							}
							for _, c := range checks {
								if c.got.Cmp(c.want) != 0 {
									t.Errorf("%s = %s, want %s", c.name, c.got.FloatString(2), c.want.FloatString(2))
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
