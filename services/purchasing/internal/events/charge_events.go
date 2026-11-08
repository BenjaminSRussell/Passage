package events

// Charge events (FR-17.9). Each is written to the outbox in the transaction that changes the row;
// the subject is the charge code id or the auto charge id.
const (
	// ChargeCodeCreated is written when a charge code is created.
	ChargeCodeCreated = "dev.passage.procurement.chargecode.created.v1"
	// ChargeCodeChanged is written when a charge code is changed or deactivated.
	ChargeCodeChanged = "dev.passage.procurement.chargecode.changed.v1"
	// AutoChargeCreated is written when an auto charge is created.
	AutoChargeCreated = "dev.passage.procurement.autocharge.created.v1"
	// AutoChargeChanged is written when an auto charge is changed or deactivated.
	AutoChargeChanged = "dev.passage.procurement.autocharge.changed.v1"
)

// ChargeCodeData is the data member of chargecode.created.v1 and chargecode.changed.v1.
type ChargeCodeData struct {
	ChargeCodeID    string `json:"charge_code_id"`
	LegalEntityID   string `json:"legal_entity_id"`
	Code            string `json:"code"`
	Name            string `json:"name"`
	Calc            string `json:"calc"`
	DefaultValue    string `json:"default_value"`
	DebitType       string `json:"debit_type"`
	CreditType      string `json:"credit_type"`
	DebitAccountID  string `json:"debit_account_id,omitempty"`
	CreditAccountID string `json:"credit_account_id,omitempty"`
	VendorID        string `json:"vendor_id,omitempty"`
	AllocationRule  string `json:"allocation_rule"`
	Status          string `json:"status"`
	Version         int    `json:"version"`
	ChangedBy       string `json:"changed_by,omitempty"`
}

// AutoChargeData is the data member of autocharge.created.v1 and autocharge.changed.v1.
type AutoChargeData struct {
	AutoChargeID   string `json:"auto_charge_id"`
	LegalEntityID  string `json:"legal_entity_id"`
	ChargeCodeID   string `json:"charge_code_id"`
	AccountCode    string `json:"account_code"`
	AccountID      string `json:"account_id,omitempty"`
	ItemCode       string `json:"item_code"`
	ItemID         string `json:"item_id,omitempty"`
	Value          string `json:"value,omitempty"`
	MinOrderAmount string `json:"min_order_amount,omitempty"`
	Currency       string `json:"currency,omitempty"`
	ValidFrom      string `json:"valid_from"`
	ValidTo        string `json:"valid_to,omitempty"`
	Status         string `json:"status"`
	Version        int    `json:"version"`
	ChangedBy      string `json:"changed_by,omitempty"`
}
