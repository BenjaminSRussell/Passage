package domain

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const billingDateLayout = "2006-01-02"

type BillingInstallment struct {
	No      int
	Amount  decimal.Decimal
	DueDate string
}

type PlannedItem struct {
	InstallmentNo  int
	DocumentNumber string
	DueDate        string
	Amount         decimal.Decimal
}

func validBillingDate(s string) bool {
	_, err := time.Parse(billingDateLayout, s)
	return err == nil
}

func atMinorUnit(a decimal.Decimal, currency string) bool {
	return a.Exponent() >= -money.MinorUnits(currency)
}

// PlanInvoiceItems turns a posted invoice into its open items: one per installment, numbered "<number>/<no>", or the whole
// gross as one item numbered <number> when billing sent no plan or one installment. The installments must run 1..n, be positive
// at the currency's minor unit and add up to the gross exactly; nothing is rounded or adjusted here, because billing already
// put the rounding remainder on the last installment.
func PlanInvoiceItems(number, currency string, gross decimal.Decimal, dueDate string, installments []BillingInstallment) ([]PlannedItem, error) {
	if number == "" {
		return nil, errors.New("the invoice has no number")
	}
	if !gross.IsPositive() || !atMinorUnit(gross, currency) {
		return nil, fmt.Errorf("the gross %s must be positive with at most %d decimals for %s", gross, money.MinorUnits(currency), currency)
	}
	if len(installments) == 0 {
		if !validBillingDate(dueDate) {
			return nil, fmt.Errorf("the due date %q is not a date", dueDate)
		}
		return []PlannedItem{{InstallmentNo: 1, DocumentNumber: number, DueDate: dueDate, Amount: gross}}, nil
	}
	sum := decimal.Zero
	out := make([]PlannedItem, len(installments))
	for i, in := range installments {
		switch {
		case in.No != i+1:
			return nil, fmt.Errorf("installment %d is numbered %d; installments run 1 to %d in order", i+1, in.No, len(installments))
		case !in.Amount.IsPositive() || !atMinorUnit(in.Amount, currency):
			return nil, fmt.Errorf("installment %d amount %s must be positive with at most %d decimals", in.No, in.Amount, money.MinorUnits(currency))
		case !validBillingDate(in.DueDate):
			return nil, fmt.Errorf("installment %d due date %q is not a date", in.No, in.DueDate)
		}
		sum = sum.Add(in.Amount)
		out[i] = PlannedItem{InstallmentNo: in.No, DocumentNumber: number, DueDate: in.DueDate, Amount: in.Amount}
		if len(installments) > 1 {
			out[i].DocumentNumber = number + "/" + strconv.Itoa(in.No)
		}
	}
	if !sum.Equal(gross) {
		return nil, fmt.Errorf("the installments add up to %s, not the gross %s", sum, gross)
	}
	return out, nil
}

// PlanCreditItem is the credit side of a posted credit note: one item of minus the gross, due on the posting date like an
// RMA credit.
func PlanCreditItem(number, currency string, gross decimal.Decimal, postingDate string) (PlannedItem, error) {
	switch {
	case number == "":
		return PlannedItem{}, errors.New("the credit note has no number")
	case !gross.IsPositive() || !atMinorUnit(gross, currency):
		return PlannedItem{}, fmt.Errorf("the gross %s must be positive with at most %d decimals for %s", gross, money.MinorUnits(currency), currency)
	case !validBillingDate(postingDate):
		return PlannedItem{}, fmt.Errorf("the posting date %q is not a date", postingDate)
	}
	return PlannedItem{InstallmentNo: 1, DocumentNumber: number, DueDate: postingDate, Amount: gross.Neg()}, nil
}
