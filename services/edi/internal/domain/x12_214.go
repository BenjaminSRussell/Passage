package domain

import (
	"regexp"
	"strconv"
)

// StatusEvent is one LX loop of a 214: the AT7 status (AT701 code such as AF
// picked up, X6 en route, X1 arrived, D1 delivered; AT702 reason, NS normal),
// its date and time (AT705/AT706, local time) and the MS1 place.
type StatusEvent struct {
	Code    string `json:"code"`
	Reason  string `json:"reason"`
	Date    string `json:"date"`
	Time    string `json:"time"`
	City    string `json:"city,omitempty"`
	State   string `json:"state,omitempty"`
	Country string `json:"country,omitempty"`
}

// ShipmentStatus is the canonical carrier shipment status of an X12 214
// (004010). ProNumber is B1001 (the carrier's reference), ShipmentID is B1002
// (our shipment or load id, which the event is matched to) and SCAC is B1003.
type ShipmentStatus struct {
	ProNumber  string        `json:"proNumber"`
	ShipmentID string        `json:"shipmentId"`
	SCAC       string        `json:"scac"`
	BOL        string        `json:"bol,omitempty"`
	PONumber   string        `json:"poNumber,omitempty"`
	ShipFrom   Location      `json:"shipFrom"`
	ShipTo     Location      `json:"shipTo"`
	Events     []StatusEvent `json:"events"`
}

var (
	statusCodeRe = regexp.MustCompile(`^[A-Z0-9]{2}$`)
	scacRe       = regexp.MustCompile(`^[A-Z]{2,4}$`)
)

func validStatusEvent(e StatusEvent) bool {
	return statusCodeRe.MatchString(e.Code) && statusCodeRe.MatchString(e.Reason) && isDigits(e.Date, 8) && isDigits(e.Time, 4)
}

// Build214 maps a shipment status to the body of an X12 214: B10, L11 BM and
// PO, N1 SF and ST, then one LX loop per event with AT7 and MS1.
func Build214(s ShipmentStatus) (Transaction, error) {
	if s.ProNumber == "" || len(s.ProNumber) > 30 || s.ShipmentID == "" || len(s.ShipmentID) > 30 {
		return Transaction{}, outboundf("PRO number and shipment id must be 1 to 30 characters")
	}
	if !scacRe.MatchString(s.SCAC) {
		return Transaction{}, outboundf("SCAC must be 2 to 4 capital letters")
	}
	if !isGLN(s.ShipFrom.GLN) || !isGLN(s.ShipTo.GLN) {
		return Transaction{}, outboundf("ship-from and ship-to GLNs must be 13 digits")
	}
	if len(s.Events) == 0 {
		return Transaction{}, outboundf("a shipment status needs at least one event")
	}
	for i, e := range s.Events {
		if !validStatusEvent(e) {
			return Transaction{}, outboundf("event %d needs a two-character status and reason code, CCYYMMDD date and HHMM time", i+1)
		}
	}
	b := &segWriter{}
	b.seg("B10", s.ProNumber, s.ShipmentID, s.SCAC)
	if s.BOL != "" {
		b.seg("L11", s.BOL, "BM")
	}
	if s.PONumber != "" {
		b.seg("L11", s.PONumber, "PO")
	}
	b.seg("N1", "SF", s.ShipFrom.Name, "92", s.ShipFrom.GLN)
	b.seg("N1", "ST", s.ShipTo.Name, "92", s.ShipTo.GLN)
	for i, e := range s.Events {
		b.seg("LX", strconv.Itoa(i+1))
		b.seg("AT7", e.Code, e.Reason, "", "", e.Date, e.Time, "LT")
		if e.City != "" || e.State != "" || e.Country != "" {
			b.seg("MS1", e.City, e.State, e.Country)
		}
	}
	return Transaction{SetID: "214", Body: b.body}, nil
}

// Map214 reads an inbound 214 into the canonical shipment status, reporting
// every problem as an Issue.
func Map214(t Transaction) (ShipmentStatus, []Issue) {
	st := ShipmentStatus{Events: []StatusEvent{}}
	var issues []Issue
	malformed := func(detail string) { issues = append(issues, Issue{Code: CodeMalformed, Detail: detail, Reject: true}) }
	sawB10 := false
	for _, s := range t.Body {
		switch s.ID {
		case "B10":
			sawB10 = true
			st.ProNumber, st.ShipmentID, st.SCAC = element(s, 0), element(s, 1), element(s, 2)
		case "L11":
			switch element(s, 1) {
			case "BM":
				st.BOL = element(s, 0)
			case "PO":
				st.PONumber = element(s, 0)
			}
		case "N1":
			loc := Location{Name: element(s, 1), GLN: element(s, 3)}
			switch element(s, 0) {
			case "SF":
				st.ShipFrom = loc
			case "ST":
				st.ShipTo = loc
			}
		case "LX":
			st.Events = append(st.Events, StatusEvent{})
		case "AT7":
			if len(st.Events) == 0 {
				malformed("AT7 outside an LX loop")
				continue
			}
			e := &st.Events[len(st.Events)-1]
			e.Code, e.Reason, e.Date, e.Time = element(s, 0), element(s, 1), element(s, 4), element(s, 5)
		case "MS1":
			if len(st.Events) > 0 {
				e := &st.Events[len(st.Events)-1]
				e.City, e.State, e.Country = element(s, 0), element(s, 1), element(s, 2)
			}
		}
	}
	if !sawB10 || st.ShipmentID == "" {
		malformed("B10 with a B1002 shipment id is required")
	}
	if len(st.Events) == 0 {
		malformed("at least one LX/AT7 status is required")
	}
	for i, e := range st.Events {
		if !validStatusEvent(e) {
			malformed("status " + strconv.Itoa(i+1) + " needs AT701, AT702, AT705 (CCYYMMDD) and AT706 (HHMM)")
		}
	}
	return st, issues
}
