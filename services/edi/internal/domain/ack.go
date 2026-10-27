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
