package domain

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func activePartner() Partner {
	return Partner{SellerEntity: "A", BuyerEntity: "B", Active: true, Pricing: PricePriceList, Tolerance: DefaultTolerance()}
}

// orderFromSeed builds a reproducible random order from a math/rand source.
func orderFromSeed(r *rand.Rand) Order {
	currencies := []string{"EUR", "USD", "JPY"}
	o := Order{Currency: currencies[r.Intn(len(currencies))]}
	for n := 1 + r.Intn(5); n > 0; n-- {
		qty := decimal.NewFromInt(int64(1 + r.Intn(1000)))
		price := decimal.New(int64(1+r.Intn(1000000)), -2)
		if o.Currency == "JPY" {
			price = decimal.NewFromInt(int64(1 + r.Intn(100000)))
		}
		o.Lines = append(o.Lines, OrderLine{Qty: qty, UnitPrice: price})
	}
	return o
}

func faultFromSeed(r *rand.Rand) Fault {
	steps := append([]string{""}, SagaSteps...)
	return Fault{Step: steps[r.Intn(len(steps))], CompensationFails: r.Intn(4) == 0}
}

func TestRunMirrorSaga_WorkedExampleLinks(t *testing.T) {
	// AC-40.1: 100 × 25.00 EUR sales order in A becomes a PO of the same amount in B.
	src := Order{Currency: "EUR", Lines: []OrderLine{{Qty: decimal.NewFromInt(100), UnitPrice: decimal.New(2500, -2)}}}
	s := RunMirrorSaga(activePartner(), src, "pair-1", "so-1", "po-1", Fault{})
	if s.Status != StatusLinked || s.Link == nil {
		t.Fatalf("status = %s reason %q; want Linked", s.Status, s.Reason)
	}
	if got := s.Docs[1].Amount.String(); got != "2500.00 EUR" {
		t.Fatalf("mirror amount = %s; want 2500.00 EUR", got)
	}
	if err := s.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestRunMirrorSaga_FailureTable(t *testing.T) {
	src := Order{Currency: "USD", Lines: []OrderLine{{Qty: decimal.NewFromInt(2), UnitPrice: decimal.NewFromInt(10)}}}
	inactive := activePartner()
	inactive.Active = false
	cases := []struct {
		name       string
		partner    Partner
		fault      Fault
		status     SagaStatus
		failedStep string
		docs       int
	}{
		{"inactive policy fails validate", inactive, Fault{}, StatusCompensated, StepValidate, 0},
		{"sequence failure creates nothing", activePartner(), Fault{Step: StepReserve}, StatusCompensated, StepReserve, 0},
		{"mirror failure cancels source", activePartner(), Fault{Step: StepCreateMirror}, StatusCompensated, StepCreateMirror, 1},
		{"link failure cancels both", activePartner(), Fault{Step: StepLink}, StatusCompensated, StepLink, 2},
		{"compensation failure leaves orphan", activePartner(), Fault{Step: StepLink, CompensationFails: true}, StatusFailed, StepLink, 2},
		{"happy path", activePartner(), Fault{}, StatusLinked, "", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := RunMirrorSaga(c.partner, src, "p", "so", "po", c.fault)
			if s.Status != c.status || s.FailedStep != c.failedStep || len(s.Docs) != c.docs {
				t.Fatalf("got status=%s failed=%q docs=%d; want %s %q %d", s.Status, s.FailedStep, len(s.Docs), c.status, c.failedStep, c.docs)
			}
			if err := s.CheckInvariants(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestRunMirrorSaga_ThousandRandomOrders is AC-40.2 and the mirror amount
// equality property: 1,000 random orders, each with a random planted fault.
func TestRunMirrorSaga_ThousandRandomOrders(t *testing.T) {
	r := rand.New(rand.NewSource(40))
	linked, compensated, failed := 0, 0, 0
	for i := 0; i < 1000; i++ {
		src := orderFromSeed(r)
		s := RunMirrorSaga(activePartner(), src, fmt.Sprintf("pair-%d", i), "so", "po", faultFromSeed(r))
		if err := s.CheckInvariants(); err != nil {
			t.Fatalf("order %d: %v (saga %+v)", i, err, s)
		}
		switch s.Status {
		case StatusLinked:
			linked++
		case StatusCompensated:
			compensated++
		case StatusFailed:
			failed++
		}
	}
	if linked == 0 || compensated == 0 || failed == 0 {
		t.Fatalf("fault mix too narrow: linked=%d compensated=%d failed=%d", linked, compensated, failed)
	}
}

func TestRunMirrorSaga_RapidProperty(t *testing.T) {
	line := rapid.Custom(func(t *rapid.T) OrderLine {
		return OrderLine{
			Qty:       decimal.NewFromInt(int64(rapid.IntRange(1, 500).Draw(t, "qty"))),
			UnitPrice: decimal.New(int64(rapid.IntRange(1, 100000).Draw(t, "cents")), -2),
		}
	})
	faults := rapid.SampledFrom(append([]string{""}, SagaSteps...))
	rapid.Check(t, func(t *rapid.T) {
		src := Order{Currency: "EUR", Lines: rapid.SliceOfN(line, 1, 5).Draw(t, "lines")}
		f := Fault{Step: faults.Draw(t, "step"), CompensationFails: rapid.Bool().Draw(t, "comp")}
		s := RunMirrorSaga(activePartner(), src, "p", "so", "po", f)
		if err := s.CheckInvariants(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOrderTotal_RoundsPerLineInDocumentCurrency(t *testing.T) {
	o := Order{Currency: "EUR", Lines: []OrderLine{
		{Qty: decimal.NewFromInt(3), UnitPrice: decimal.RequireFromString("0.335")},
	}}
	total, err := o.Total()
	if err != nil {
		t.Fatal(err)
	}
	// 3 × 0.335 = 1.005, rounded half away from zero to 1.01.
	if total.String() != "1.01 EUR" {
		t.Fatalf("total = %s", total)
	}
}

func TestCheckInvariants_RejectsBrokenSagas(t *testing.T) {
	src := Order{Currency: "EUR", Lines: []OrderLine{{Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(5)}}}
	linked := RunMirrorSaga(activePartner(), src, "p", "so", "po", Fault{})
	linked.Docs[1].Live = false
	if err := linked.CheckInvariants(); err == nil {
		t.Fatal("linked saga with a canceled mirror passed")
	}
	orphan := RunMirrorSaga(activePartner(), src, "p", "so", "po", Fault{Step: StepLink})
	orphan.Docs[0].Live = true
	if err := orphan.CheckInvariants(); err == nil {
		t.Fatal("compensated saga with a live document passed")
	}
	var de *Error
	_, err := ActivePartner(nil, "A", "B")
	if !errors.As(err, &de) || de.Code != CodeNoPolicy {
		t.Fatalf("err = %v; want IC-0801", err)
	}
}
