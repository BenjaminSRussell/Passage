package events

// CheckVoided is published when a check is voided, including the old check of
// a reprint; AP (wave 19) positive pay consumes it (7.36.5, FR-36.6, BR-36.5).
// It equals events.Type(Context, "check", "voided", 1); a test checks that.
const CheckVoided = "dev.passage.treasury.check.voided.v1"

// CheckVoidedData is the payload of CheckVoided.
type CheckVoidedData struct {
	CheckID       string `json:"checkId"`
	BankAccountID string `json:"bankAccountId"`
	Number        int64  `json:"number"`
	Reason        string `json:"reason"`
	// PositivePayVoid is true when the void must also reach the bank's positive pay file.
	PositivePayVoid bool `json:"positivePayVoid"`
}
