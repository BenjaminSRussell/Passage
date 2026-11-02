package domain

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	CodeSheetOverLine   = "AP-SES-001"
	CodeSheetSelfAccept = "AP-SES-002"
	CodeSheetNotPending = "AP-SES-003"

	SheetPending  = "PENDING"
	SheetAccepted = "ACCEPTED"
	SheetRejected = "REJECTED"

	MaxSheetLines = 200
)

type SheetLine struct {
	POLineNo    int             `json:"poLineNo"`
	Description string          `json:"description"`
	Unit        string          `json:"unit"`
	Qty         decimal.Decimal `json:"qty"`
	UnitPrice   decimal.Decimal `json:"unitPrice"`
	Amount      decimal.Decimal `json:"amount"`
}

func SheetAmount(qty, unitPrice decimal.Decimal, currency string) decimal.Decimal {
	return qty.Mul(unitPrice).RoundBank(money.MinorUnits(currency))
}

func (l PurchaseOrderLine) Amount(currency string) decimal.Decimal {
	return SheetAmount(l.Qty, l.NetUnitPrice(), currency)
}
