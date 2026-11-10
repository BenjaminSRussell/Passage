package domain

import (
	"strings"
	"testing"
)

func TestPlanInvoiceItems(t *testing.T) {
	three := []BillingInstallment{{1, d("333.34"), "2027-04-30"}, {2, d("333.33"), "2027-05-30"}, {3, d("333.33"), "2027-06-30"}}
	items, err := PlanInvoiceItems("INV-1", "USD", d("1000.00"), "2027-04-30", three)
	if err != nil || len(items) != 3 || items[0].DocumentNumber != "INV-1/1" || items[2].DocumentNumber != "INV-1/3" ||
		!items[0].Amount.Equal(d("333.34")) || items[2].DueDate != "2027-06-30" {
		t.Fatalf("three installments: %+v %v", items, err)
	}

	whole, err := PlanInvoiceItems("INV-1", "USD", d("1000.00"), "2027-04-30", nil)
	if err != nil || len(whole) != 1 || whole[0].DocumentNumber != "INV-1" || whole[0].DueDate != "2027-04-30" || !whole[0].Amount.Equal(d("1000")) {
		t.Fatalf("no plan: %+v %v", whole, err)
	}

	one, err := PlanInvoiceItems("INV-1", "USD", d("1000.00"), "2027-04-30", []BillingInstallment{{1, d("1000.00"), "2027-05-15"}})
	if err != nil || len(one) != 1 || one[0].DocumentNumber != "INV-1" || one[0].DueDate != "2027-05-15" {
		t.Fatalf("one installment: %+v %v", one, err)
	}

	yen, err := PlanInvoiceItems("INV-2", "JPY", d("1000"), "2027-04-30", []BillingInstallment{{1, d("501"), "2027-04-30"}, {2, d("499"), "2027-05-30"}})
	if err != nil || len(yen) != 2 {
		t.Fatalf("JPY: %+v %v", yen, err)
	}
}

func TestPlanInvoiceItemsRefusals(t *testing.T) {
	ok := []BillingInstallment{{1, d("500.00"), "2027-04-30"}, {2, d("500.00"), "2027-05-30"}}
	cases := map[string]struct {
		number, currency, gross, due string
		in                           []BillingInstallment
		want                         string
	}{
		"no number":              {"", "USD", "1000.00", "2027-04-30", nil, "no number"},
		"zero gross":             {"INV", "USD", "0", "2027-04-30", nil, "positive"},
		"negative gross":         {"INV", "USD", "-5.00", "2027-04-30", nil, "positive"},
		"gross finer than cents": {"INV", "USD", "10.005", "2027-04-30", nil, "decimals"},
		"yen with decimals":      {"INV", "JPY", "10.50", "2027-04-30", nil, "decimals"},
		"bad due date":           {"INV", "USD", "10.00", "30/04/2027", nil, "not a date"},
		"short sum":              {"INV", "USD", "1000.00", "", []BillingInstallment{{1, d("500.00"), "2027-04-30"}, {2, d("499.99"), "2027-05-30"}}, "add up"},
		"long sum":               {"INV", "USD", "999.99", "", ok, "add up"},
		"out of order":           {"INV", "USD", "1000.00", "", []BillingInstallment{{2, d("500.00"), "2027-04-30"}, {1, d("500.00"), "2027-05-30"}}, "in order"},
		"gap in numbers":         {"INV", "USD", "1000.00", "", []BillingInstallment{{1, d("500.00"), "2027-04-30"}, {3, d("500.00"), "2027-05-30"}}, "in order"},
		"zero installment":       {"INV", "USD", "1000.00", "", []BillingInstallment{{1, d("1000.00"), "2027-04-30"}, {2, d("0"), "2027-05-30"}}, "positive"},
		"installment bad date":   {"INV", "USD", "1000.00", "", []BillingInstallment{{1, d("500.00"), "soon"}, {2, d("500.00"), "2027-05-30"}}, "not a date"},
		"installment sub-cent":   {"INV", "USD", "1000.00", "", []BillingInstallment{{1, d("500.001"), "2027-04-30"}, {2, d("499.999"), "2027-05-30"}}, "decimals"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := PlanInvoiceItems(c.number, c.currency, d(c.gross), c.due, c.in)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want it to mention %q", err, c.want)
			}
		})
	}
}

func TestPlanCreditItem(t *testing.T) {
	it, err := PlanCreditItem("CN-1", "USD", d("150.00"), "2027-04-10")
	if err != nil || it.DocumentNumber != "CN-1" || !it.Amount.Equal(d("-150")) || it.DueDate != "2027-04-10" || it.InstallmentNo != 1 {
		t.Fatalf("credit item %+v %v", it, err)
	}
	for name, args := range map[string][3]string{"no number": {"", "150.00", "2027-04-10"}, "zero": {"CN", "0", "2027-04-10"},
		"negative": {"CN", "-1.00", "2027-04-10"}, "sub-cent": {"CN", "1.001", "2027-04-10"}, "bad date": {"CN", "1.00", "x"}} {
		if _, err := PlanCreditItem(args[0], "USD", d(args[1]), args[2]); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
