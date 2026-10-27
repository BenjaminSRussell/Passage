package domain

import (
	"errors"
	"time"
)

// Tender statuses (BR-34.3). A tender is ACCEPTED by one carrier or fails after
// the last carrier in the route guide declines or times out.
const (
	TenderPending  = "PENDING"
	TenderAccepted = "ACCEPTED"
	TenderFailed   = "TENDER_FAILED"
)

// Attempt responses for one carrier in the cascade.
const (
	ResponsePending  = "PENDING"
	ResponseAccepted = "ACCEPTED"
	ResponseDeclined = "DECLINED"
	ResponseExpired  = "EXPIRED"
)

// ErrNotPending means a response came from a carrier that is not the one the
// tender is waiting on.
var ErrNotPending = errors.New("tender: carrier is not the pending recipient")

// Attempt is one carrier's turn in the cascade.
type Attempt struct {
	Rank      int       `json:"rank"`
	CarrierID string    `json:"carrierId"`
	SentAt    time.Time `json:"sentAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Response  string    `json:"response"`
}

// Tender sends one load to carriers in route-guide rank order. Time is an input
// to every call, so the cascade is testable with a fake clock; a Temporal
// TenderWorkflow timer drives the same calls in production.
type Tender struct {
	LoadID          string
	Carriers        []string
	Timeout         time.Duration
	Attempts        []Attempt
	Status          string
	PlannerTask     bool
	AcceptedCarrier string
}

// NewTender starts a tender over the ranked carriers. Each carrier gets timeout to respond.
func NewTender(loadID string, rankedCarriers []string, timeout time.Duration) (Tender, error) {
	if len(rankedCarriers) == 0 {
		return Tender{}, errors.New("tender: at least one carrier is required")
	}
	if timeout <= 0 {
		return Tender{}, errors.New("tender: timeout must be positive")
	}
	return Tender{LoadID: loadID, Carriers: rankedCarriers, Timeout: timeout, Status: TenderPending}, nil
}

// Send offers the load to the first carrier. It does nothing once started.
func (t *Tender) Send(now time.Time) {
	if len(t.Attempts) == 0 && t.Status == TenderPending {
		t.next(now)
	}
}

// Tick expires pending attempts whose deadline has passed and cascades to the
// next carrier. A late tick walks through every missed deadline in order, so the
// result does not depend on how often the timer fires.
func (t *Tender) Tick(now time.Time) {
	for t.Status == TenderPending {
		a := t.pending()
		if a == nil || now.Before(a.ExpiresAt) {
			return
		}
		a.Response = ResponseExpired
		t.next(a.ExpiresAt)
	}
}

// Respond records a carrier's accept or decline. Deadlines are applied first, so
// a response after the carrier's deadline is rejected with TMS-0105 and the next
// carrier has already been tried.
func (t *Tender) Respond(carrierID string, accept bool, now time.Time) error {
	t.Tick(now)
	a := t.pending()
	if a == nil || a.CarrierID != carrierID {
		if t.expiredFor(carrierID) {
			return &Error{Code: CodeTenderExpired, Detail: "tender expired; acceptance rejected"}
		}
		return ErrNotPending
	}
	if accept {
		a.Response = ResponseAccepted
		t.Status = TenderAccepted
		t.AcceptedCarrier = carrierID
		return nil
	}
	a.Response = ResponseDeclined
	t.next(now)
	return nil
}

func (t *Tender) next(now time.Time) {
	if len(t.Attempts) == len(t.Carriers) {
		t.Status = TenderFailed
		t.PlannerTask = true
		return
	}
	t.Attempts = append(t.Attempts, Attempt{
		Rank:      len(t.Attempts) + 1,
		CarrierID: t.Carriers[len(t.Attempts)],
		SentAt:    now,
		ExpiresAt: now.Add(t.Timeout),
		Response:  ResponsePending,
	})
}

func (t *Tender) pending() *Attempt {
	if n := len(t.Attempts); n > 0 && t.Attempts[n-1].Response == ResponsePending {
		return &t.Attempts[n-1]
	}
	return nil
}

func (t *Tender) expiredFor(carrierID string) bool {
	for _, a := range t.Attempts {
		if a.CarrierID == carrierID && a.Response == ResponseExpired {
			return true
		}
	}
	return false
}
