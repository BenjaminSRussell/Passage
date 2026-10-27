package events

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// CustomerBalanceChanged is the PRD's customer-balance.changed.v1 (§5.30.5); the
// kernel's naming rule allows no hyphen, so the entity is "customerbalance". The
// aging snapshot job (§5.30.16) emits it for each customer and currency whose
// balance, overdue amount, dunning level or risk score moved since the latest
// earlier snapshot, so sales credit and the portal read balances without polling.
var CustomerBalanceChanged = events.Type(Context, "customerbalance", "changed", 1)

// CustomerBalanceChangedData is the payload of dev.passage.receivables.customerbalance.changed.v1
// (customer_id, balance, overdue, oldest_item_days, dunning_level, risk_score).
type CustomerBalanceChangedData struct {
	LegalEntityID  string          `json:"legal_entity_id"`
	CustomerID     string          `json:"customer_id"`
	Currency       string          `json:"currency"`
	AsOf           string          `json:"as_of"`
	Balance        decimal.Decimal `json:"balance"`
	Overdue        decimal.Decimal `json:"overdue"`
	OldestItemDays int             `json:"oldest_item_days"`
	DunningLevel   int             `json:"dunning_level"`
	RiskScore      int             `json:"risk_score"`
}
