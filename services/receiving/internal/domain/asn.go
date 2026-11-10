package domain

import "github.com/shopspring/decimal"

const (
	CodeAsnOverQty   = "SUP-ASN-001"
	CodeAsnTracking  = "SUP-ASN-002"
	CodeAsnConsumed  = "SUP-ASN-003"
	AsnOpen          = "OPEN"
	AsnReceiving     = "RECEIVING"
	MaxAsnLines      = 200
	AsnPackingPrefix = "ASN-"
)

type AsnLine struct {
	POLineNo   int             `json:"poLineNo"`
	Qty        decimal.Decimal `json:"qty"`
	Batch      string          `json:"batch,omitempty"`
	Serials    []string        `json:"serials,omitempty"`
	ExpiryDate string          `json:"expiryDate,omitempty"`
}
