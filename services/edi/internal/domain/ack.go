package domain

import (
	"fmt"
	"strconv"
	"time"
)

// Functional acknowledgment (ASC X12 997, version 004010) codes.
const (
	AckAccepted          = "A" // AK5/AK9: accepted
	AckAcceptedWithError = "E" // AK5/AK9: accepted with errors
	AckPartial           = "P" // AK9 only: some transactions accepted, some rejected
	AckRejected          = "R" // AK5/AK9: rejected
)

// TxOutcome is the per-transaction result that becomes AK2/AK5.
type TxOutcome struct {
	SetID   string
	Control string
	// Code is AckAccepted, AckAcceptedWithError or AckRejected.
	Code string
}

// AckStatus is the interchange-level result stored on the interchange row.
type AckStatus string

const (
	StatusAccepted           AckStatus = "ACCEPTED"
	StatusAcceptedWithErrors AckStatus = "ACCEPTED_WITH_ERRORS"
	StatusRejected           AckStatus = "REJECTED"
)

// SummarizeOutcomes folds transaction outcomes into the interchange status,
// consistent with the AK9 group codes: every transaction rejected is REJECTED;
// any accepted transaction alongside rejected ones, or any accepted with errors,
// is ACCEPTED_WITH_ERRORS (AK9 P or E); all clean is ACCEPTED. An empty outcome
// list is rejected: there was nothing to accept.
func SummarizeOutcomes(outcomes []TxOutcome) AckStatus {
	if len(outcomes) == 0 {
		return StatusRejected
	}
	switch ak9Code(outcomes) {
	case AckRejected:
		return StatusRejected
	case AckAccepted:
		return StatusAccepted
	default:
		return StatusAcceptedWithErrors
	}
}

// Ack997 builds the 997 that acknowledges one inbound functional group. The
// returned interchange swaps sender and receiver, carries ourControl in ISA13
// and groupControl in GS06, and is written with Serialize.
func Ack997(received Interchange, group Group, outcomes []TxOutcome, ourControl, groupControl string, at time.Time) (Interchange, error) {
	if len(outcomes) == 0 {
		return Interchange{}, fmt.Errorf("997 for group %s needs at least one transaction outcome", group.Control)
	}
	body := []Segment{{ID: "AK1", Elements: []string{group.FunctionalID, group.Control}}}
	accepted := 0
	for _, o := range outcomes {
		body = append(body,
			Segment{ID: "AK2", Elements: []string{o.SetID, o.Control}},
			Segment{ID: "AK5", Elements: []string{o.Code}},
		)
		if o.Code != AckRejected {
			accepted++
		}
	}
	count := strconv.Itoa(len(outcomes))
	body = append(body, Segment{ID: "AK9", Elements: []string{ak9Code(outcomes), count, count, strconv.Itoa(accepted)}})
	ack := Interchange{
		Delims:   received.Delims,
		Sender:   received.Receiver,
		Receiver: received.Sender,
		Date:     at.UTC().Format("060102"),
		Time:     at.UTC().Format("1504"),
		Version:  received.Version,
		Control:  ourControl,
		Usage:    received.Usage,
		Groups: []Group{{
			FunctionalID: "FA",
			Sender:       received.Receiver.ID,
			Receiver:     received.Sender.ID,
			Date:         at.UTC().Format("20060102"),
			Time:         at.UTC().Format("1504"),
			Control:      groupControl,
			Version:      group.Version,
			Transactions: []Transaction{{SetID: "997", Control: "0001", Body: body}},
		}},
	}
	return ack, nil
}

// ak9Code is the AK9 group result: R when every transaction is rejected, P when
// accepted and rejected transactions are mixed, E when all are accepted and at
// least one carries errors, A when all are accepted cleanly.
func ak9Code(outcomes []TxOutcome) string {
	rejected, withErrors := 0, 0
	for _, o := range outcomes {
		switch o.Code {
		case AckRejected:
			rejected++
		case AckAcceptedWithError:
			withErrors++
		}
	}
	switch {
	case rejected == len(outcomes):
		return AckRejected
	case rejected > 0:
		return AckPartial
	case withErrors > 0:
		return AckAcceptedWithError
	default:
		return AckAccepted
	}
}

// AckedSet is one AK2/AK5 pair of a received 997: the transaction set and ST02
// it answers and the normalized AK5 code (A, E or R).
type AckedSet struct {
	SetID   string `json:"setId"`
	Control string `json:"control"`
	Code    string `json:"code"`
}

// FunctionalAck is a partner's 997 answering one of our functional groups.
// GroupControl is AK102, our GS06; Code is the AK9 group result (A, E, P or R).
type FunctionalAck struct {
	FunctionalID string     `json:"functionalId"`
	GroupControl string     `json:"groupControl"`
	Code         string     `json:"code"`
	Sets         []AckedSet `json:"sets"`
}

// normalizeAckCode folds the X12 rejection variants M, W and X (authentication,
// assurance and decryption failures) into R; allowGroup admits AK9 P.
func normalizeAckCode(code string, allowGroup bool) (string, bool) {
	switch code {
	case AckAccepted, AckAcceptedWithError, AckRejected:
		return code, true
	case "M", "W", "X":
		return AckRejected, true
	case AckPartial:
		return code, allowGroup
	}
	return "", false
}

// Parse997 reads a received 997. AK3/AK4 error detail is skipped; AK2 loops are
// optional (a partner may acknowledge a whole group with AK1 and AK9 alone).
// A 997 is never itself acknowledged.
func Parse997(t Transaction) (FunctionalAck, error) {
	if t.SetID != "997" {
		return FunctionalAck{}, syntaxf("transaction set %s is not a 997", t.SetID)
	}
	var (
		ack     FunctionalAck
		sawAK1  bool
		sawAK9  bool
		pending *AckedSet
	)
	ack.Sets = []AckedSet{}
	for _, s := range t.Body {
		switch s.ID {
		case "AK1":
			if sawAK1 {
				return FunctionalAck{}, syntaxf("997 carries more than one AK1")
			}
			sawAK1 = true
			ack.FunctionalID, ack.GroupControl = element(s, 0), element(s, 1)
			if ack.FunctionalID == "" || ack.GroupControl == "" || len(ack.GroupControl) > 9 || !isDigits(ack.GroupControl, len(ack.GroupControl)) {
				return FunctionalAck{}, syntaxf("AK1 needs a functional id and a numeric group control number of 1 to 9 digits")
			}
		case "AK2":
			if pending != nil {
				return FunctionalAck{}, syntaxf("AK2 %s %s has no AK5", pending.SetID, pending.Control)
			}
			pending = &AckedSet{SetID: element(s, 0), Control: element(s, 1)}
			if pending.SetID == "" || pending.Control == "" {
				return FunctionalAck{}, syntaxf("AK2 needs a transaction set id and control number")
			}
		case "AK5":
			if pending == nil {
				return FunctionalAck{}, syntaxf("AK5 without AK2")
			}
			code, ok := normalizeAckCode(element(s, 0), false)
			if !ok {
				return FunctionalAck{}, syntaxf("AK5 code %q is not A, E, R, M, W or X", element(s, 0))
			}
			pending.Code = code
			ack.Sets = append(ack.Sets, *pending)
			pending = nil
		case "AK9":
			code, ok := normalizeAckCode(element(s, 0), true)
			if !ok {
				return FunctionalAck{}, syntaxf("AK9 code %q is not A, E, P, R, M, W or X", element(s, 0))
			}
			sawAK9 = true
			ack.Code = code
		}
	}
	switch {
	case !sawAK1:
		return FunctionalAck{}, syntaxf("997 has no AK1")
	case pending != nil:
		return FunctionalAck{}, syntaxf("AK2 %s %s has no AK5", pending.SetID, pending.Control)
	case !sawAK9:
		return FunctionalAck{}, syntaxf("997 has no AK9")
	}
	return ack, nil
}

// InterchangeAckStatus maps an AK9 group code to the interchange ack_status of
// the outbound interchange it answers.
func InterchangeAckStatus(ak9 string) AckStatus {
	switch ak9 {
	case AckAccepted:
		return StatusAccepted
	case AckRejected:
		return StatusRejected
	default:
		return StatusAcceptedWithErrors
	}
}
