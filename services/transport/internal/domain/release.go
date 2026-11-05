package domain

import "time"

// ReleasedCarrier is one route-guide entry of a load release, in rank order.
// RateQuoted is a decimal string with two places, empty when none was quoted.
type ReleasedCarrier struct {
	CarrierID  string `json:"carrierId"`
	RateQuoted string `json:"rateQuoted,omitempty"`
}

// LoadRelease is a planner's release of a load for tendering (FR-34.5): the
// workflow service starts the tender cascade from it. ReleasedBy and
// LegalEntityScope are the verified caller of the release, never request input.
type LoadRelease struct {
	ID               string            `json:"id"`
	LoadID           string            `json:"loadId"`
	LegalEntityID    string            `json:"legalEntityId"`
	Carriers         []ReleasedCarrier `json:"carriers"`
	Currency         string            `json:"currency,omitempty"`
	TimeoutMin       int               `json:"timeoutMin"`
	ReleasedBy       string            `json:"releasedBy"`
	LegalEntityScope []string          `json:"legalEntityScope"`
	ReleasedAt       time.Time         `json:"releasedAt"`
	// LoadStatus and LoadVersion are the load's status and version after the
	// release (TENDERED), or as they are now when the release is replayed.
	LoadStatus  string `json:"loadStatus"`
	LoadVersion int    `json:"loadVersion"`
}

// ReleaseOf returns the release of the tender t would start: its carriers in
// rank order with their quoted rates, its currency and timeout.
func ReleaseOf(t Tender) LoadRelease {
	r := LoadRelease{LoadID: t.LoadID, Currency: t.Currency, TimeoutMin: int(t.Timeout / time.Minute),
		Carriers: make([]ReleasedCarrier, 0, len(t.Carriers))}
	for _, c := range t.Carriers {
		rc := ReleasedCarrier{CarrierID: c}
		if rate, ok := t.Rates[c]; ok {
			rc.RateQuoted = rate.StringFixed(2)
		}
		r.Carriers = append(r.Carriers, rc)
	}
	return r
}
