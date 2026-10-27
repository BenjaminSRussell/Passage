// Package events holds the event types and payloads the assets service
// publishes through the outbox (events/asyncapi.yaml).
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded-context segment of every event type.
const Context = "assets"

var (
	AssetAcquired   = events.Type(Context, "asset", "acquired", 1)
	AssetDisposed   = events.Type(Context, "asset", "disposed", 1)
	LeaseRecognized = events.Type(Context, "lease", "recognized", 1)
)

// AssetAcquiredData is the payload of AssetAcquired. Amounts are decimal strings.
type AssetAcquiredData struct {
	AssetID    string   `json:"assetId"`
	BookIDs    []string `json:"bookIds"`
	Cost       string   `json:"cost"`
	Currency   string   `json:"currency"`
	SourceType string   `json:"sourceType"`
	SourceID   *string  `json:"sourceId"`
}

// AssetDisposedData is the payload of AssetDisposed, one per disposed book.
type AssetDisposedData struct {
	AssetID         string `json:"assetId"`
	BookID          string `json:"bookId"`
	Kind            string `json:"kind"`
	TransactionDate string `json:"transactionDate"`
	Percentage      string `json:"percentage"`
	Proceeds        string `json:"proceeds"`
	NBV             string `json:"nbv"`
	GainLoss        string `json:"gainLoss"`
	Currency        string `json:"currency"`
}

// LeaseRecognizedData is the payload of LeaseRecognized.
type LeaseRecognizedData struct {
	LeaseID    string `json:"leaseId"`
	Liability  string `json:"liability"`
	ROUAsset   string `json:"rouAsset"`
	Currency   string `json:"currency"`
	TermMonths int    `json:"termMonths"`
	Rate       string `json:"rate"`
}
