package domain

import (
	"errors"
	"strings"
)

// Ownership transfer events for goods in transit (BR-34.11).
const (
	TransferOriginShipment     = "ORIGIN_SHIPMENT"
	TransferDestinationReceipt = "DESTINATION_RECEIPT"
)

// CodeOwnershipMoved is the info message raised when a goods-in-transit order is
// opened at the transfer event (TMS-0121).
const CodeOwnershipMoved = "TMS-0121"

// ErrUnknownIncoterm is returned for a rule that is not in Incoterms 2020.
var ErrUnknownIncoterm = errors.New("transport: unknown Incoterms 2020 rule")

// incotermTransfer is the default transfer event per Incoterms 2020 rule. Risk
// passes at origin for the group of rules that start the carriage at the seller's
// premises or port, and at destination for the delivered rules.
var incotermTransfer = map[string]string{
	"EXW": TransferOriginShipment,
	"FCA": TransferOriginShipment,
	"FAS": TransferOriginShipment,
	"FOB": TransferOriginShipment,
	"CFR": TransferOriginShipment,
	"CIF": TransferOriginShipment,
	"CPT": TransferOriginShipment,
	"CIP": TransferOriginShipment,
	"DAP": TransferDestinationReceipt,
	"DPU": TransferDestinationReceipt,
	"DDP": TransferDestinationReceipt,
}

// TransferRule is the ownership timing for one Incoterm.
type TransferRule struct {
	Incoterm       string `json:"incoterm"`
	Event          string `json:"event"`
	GoodsInTransit bool   `json:"goodsInTransit"`
	Code           string `json:"code,omitempty"`
}

// TransferFor returns the default transfer event for an Incoterm. Only origin
// transfers open a goods-in-transit order (DAP, DPU and DDP do not). A contract may
// override the event; the override is outside this default table.
func TransferFor(incoterm string) (TransferRule, error) {
	code := strings.ToUpper(strings.TrimSpace(incoterm))
	event, ok := incotermTransfer[code]
	if !ok {
		return TransferRule{}, ErrUnknownIncoterm
	}
	rule := TransferRule{Incoterm: code, Event: event, GoodsInTransit: event == TransferOriginShipment}
	if rule.GoodsInTransit {
		rule.Code = CodeOwnershipMoved
	}
	return rule, nil
}
