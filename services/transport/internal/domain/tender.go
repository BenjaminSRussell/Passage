package domain

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
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
	ID              string
	LoadID          string
	Carriers        []string
	Timeout         time.Duration
	Attempts        []Attempt
	Status          string
	PlannerTask     bool
	AcceptedCarrier string
	// Currency and Rates carry the rate quoted to each carrier (rate_quoted),
	// usually the carrier's rate-shop total. A carrier without a rate is tendered
	// at the contract rate and its events carry no rate.
	Currency string
	Rates    map[string]decimal.Decimal
	// Version and CreatedAt are set by the store.
	Version   int
	CreatedAt time.Time
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

// TickAt is Tick that returns the attempts it expired, in cascade order, so the
// caller can publish one tender.expired event for each.
func (t *Tender) TickAt(now time.Time) []Attempt {
	before := t.responses()
	t.Tick(now)
	return t.newlyExpired(before)
}

// RespondAt is Respond that also returns the attempts whose deadline passed
// before the response was applied. The expiry happened even when the response
// is rejected, so callers persist the tender before they report the error.
func (t *Tender) RespondAt(carrierID string, accept bool, now time.Time) ([]Attempt, error) {
	before := t.responses()
	err := t.Respond(carrierID, accept, now)
	return t.newlyExpired(before), err
}

// Pending returns the attempt the tender is waiting on, if any.
func (t *Tender) Pending() (Attempt, bool) {
	if a := t.pending(); a != nil {
		return *a, true
	}
	return Attempt{}, false
}

// Overdue reports whether the pending attempt's deadline has passed at now, so
// a sweep (or the Temporal timer) should cascade it.
func (t *Tender) Overdue(now time.Time) bool {
	a := t.pending()
	return t.Status == TenderPending && a != nil && !now.Before(a.ExpiresAt)
}

// Responded reports whether carrierID's most recent attempt already carries
// the given response, which makes a repeated response a safe replay.
func (t *Tender) Responded(carrierID string, accept bool) bool {
	want := ResponseDeclined
	if accept {
		want = ResponseAccepted
	}
	for i := len(t.Attempts) - 1; i >= 0; i-- {
		if t.Attempts[i].CarrierID == carrierID {
			return t.Attempts[i].Response == want
		}
	}
	return false
}

func (t *Tender) responses() []string {
	out := make([]string, len(t.Attempts))
	for i, a := range t.Attempts {
		out[i] = a.Response
	}
	return out
}

// newlyExpired returns the attempts that are EXPIRED now but were not in before.
func (t *Tender) newlyExpired(before []string) []Attempt {
	out := []Attempt{}
	for i, a := range t.Attempts {
		if a.Response == ResponseExpired && (i >= len(before) || before[i] != ResponseExpired) {
			out = append(out, a)
		}
	}
	return out
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
