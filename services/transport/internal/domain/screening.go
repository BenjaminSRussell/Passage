package domain

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Screening statuses returned per party by a ComplianceScreeningProvider (FR-34.19).
const (
	ScreenClear          = "CLEAR"
	ScreenPotentialMatch = "POTENTIAL_MATCH"
	ScreenBlocked        = "BLOCKED"
	ScreenPending        = "SCREENING_PENDING"
	ScreenNotScreened    = "NOT_SCREENED"
)

// Load-level screening decisions (BR-34.17). The hook never auto-clears a hold.
const (
	DecisionClear = "CLEAR"
	DecisionWarn  = "WARN"
	DecisionHold  = "HOLD"
)

// Screening codes (PRD message catalogue).
const (
	CodeScreeningHold = "TMS-0133"
	CodeNotScreened   = "TMS-0134"
)

// ScreeningTimeout bounds each provider call (§6.34.19: timeout 5 s); a timeout
// is SCREENING_PENDING, which holds the load.
const ScreeningTimeout = 5 * time.Second

// MaxScreenParties bounds one screening request.
const MaxScreenParties = 20

var partyRoles = map[string]bool{
	"SOLD_TO": true, "SHIP_TO": true, "CONSIGNEE": true, "FORWARDER": true, "CARRIER": true,
}

// ScreeningParty is a party on the shipment screened at release, tender and ship
// confirmation (BR-34.17).
type ScreeningParty struct {
	Role    string `json:"role"`
	Name    string `json:"name"`
	Country string `json:"country,omitempty"`
}

// PartyScreening is the provider result for one party with its hold code, if any.
type PartyScreening struct {
	Role   string `json:"role"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
}

// ScreeningDecision is the outcome for a load: CLEAR, WARN (cross-border without a
// provider) or HOLD (a party needs trade-compliance release).
type ScreeningDecision struct {
	Decision string           `json:"decision"`
	Parties  []PartyScreening `json:"parties"`
}

// ScreeningProvider is the ComplianceScreeningProvider hook (FR-34.19). Wave 48
// supplies the implementations; the transport service only consumes the status.
// An implementation must honour ctx cancellation.
type ScreeningProvider interface {
	Screen(ctx context.Context, party ScreeningParty) (string, error)
}

// ValidateScreeningParties reports field errors for a screening request.
func ValidateScreeningParties(parties []ScreeningParty) map[string]string {
	f := map[string]string{}
	if len(parties) == 0 || len(parties) > MaxScreenParties {
		f["parties"] = fmt.Sprintf("must hold 1 to %d parties", MaxScreenParties)
	}
	for i, p := range parties {
		k := fmt.Sprintf("parties[%d]", i)
		if !partyRoles[p.Role] {
			f[k+".role"] = "must be SOLD_TO, SHIP_TO, CONSIGNEE, FORWARDER or CARRIER"
		}
		if n := len(strings.TrimSpace(p.Name)); n == 0 || n > 200 {
			f[k+".name"] = "must be 1 to 200 characters"
		}
		if p.Country != "" && (len(p.Country) != 2 || strings.ToUpper(p.Country) != p.Country) {
			f[k+".country"] = "must be an ISO 3166-1 alpha-2 code"
		}
	}
	return f
}

// CrossBorder reports whether the parties name more than one country. A
// shipment the caller marks cross-border is treated as such whatever the parties say.
func CrossBorder(parties []ScreeningParty) bool {
	first := ""
	for _, p := range parties {
		if p.Country == "" {
			continue
		}
		if first == "" {
			first = p.Country
		} else if p.Country != first {
			return true
		}
	}
	return false
}

// ScreenParties screens every party (BR-34.17). With no provider, a cross-border
// shipment is NOT_SCREENED (TMS-0134, a warning) and a domestic one is CLEAR. A
// provider error, a timeout or a status outside the known set is
// SCREENING_PENDING, which holds the load: the hook never clears on its own.
func ScreenParties(ctx context.Context, p ScreeningProvider, parties []ScreeningParty, crossBorder bool) ScreeningDecision {
	out := ScreeningDecision{Parties: make([]PartyScreening, 0, len(parties))}
	for _, party := range parties {
		status := ScreenClear
		switch {
		case p == nil && crossBorder:
			status = ScreenNotScreened
		case p == nil:
		default:
			status = screenOne(ctx, p, party)
		}
		out.Parties = append(out.Parties, partyScreening(party, status))
	}
	out.Decision = decideScreening(out.Parties)
	return out
}

func screenOne(ctx context.Context, p ScreeningProvider, party ScreeningParty) string {
	ctx, cancel := context.WithTimeout(ctx, ScreeningTimeout)
	defer cancel()
	s, err := p.Screen(ctx, party)
	if err != nil || ctx.Err() != nil || !knownScreenStatus(s) {
		return ScreenPending
	}
	return s
}

func knownScreenStatus(s string) bool {
	switch s {
	case ScreenClear, ScreenPotentialMatch, ScreenBlocked, ScreenPending, ScreenNotScreened:
		return true
	}
	return false
}

func partyScreening(p ScreeningParty, status string) PartyScreening {
	ps := PartyScreening{Role: p.Role, Name: p.Name, Status: status}
	switch status {
	case ScreenPotentialMatch, ScreenBlocked, ScreenPending:
		ps.Code = CodeScreeningHold
	case ScreenNotScreened:
		ps.Code = CodeNotScreened
	}
	return ps
}

func decideScreening(parties []PartyScreening) string {
	decision := DecisionClear
	for _, p := range parties {
		switch p.Status {
		case ScreenPotentialMatch, ScreenBlocked, ScreenPending:
			return DecisionHold
		case ScreenNotScreened:
			decision = DecisionWarn
		}
	}
	return decision
}
