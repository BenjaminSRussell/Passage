package domain

import (
	"testing"
	"time"
)

func TestAck997_SwapsEnvelopeAndCarriesCodes(t *testing.T) {
	recv := sampleInterchange()
	at := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	outcomes := []TxOutcome{{SetID: "850", Control: "0001", Code: AckAccepted}}
	ack, err := Ack997(recv, recv.Groups[0], outcomes, "000000777", "43", at)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Serialize(ack)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(raw)
	if err != nil {
		t.Fatalf("997 does not parse back: %v\n%s", err, raw)
	}
	if parsed.Sender.ID != "RECEIVER" || parsed.Receiver.ID != "SENDER" || parsed.Date != "260102" || parsed.Time != "0304" {
		t.Fatalf("envelope not swapped or dated: %+v", parsed)
	}
	g := parsed.Groups[0]
	if g.FunctionalID != "FA" || g.Control != "43" {
		t.Fatalf("group = %+v", g)
	}
	body := g.Transactions[0].Body
	if len(body) != 4 || body[0].ID != "AK1" || body[1].ID != "AK2" || body[2].ID != "AK5" || body[3].ID != "AK9" {
		t.Fatalf("997 body segments = %+v", body)
	}
	if body[1].Elements[0] != "850" || body[1].Elements[1] != "0001" || body[2].Elements[0] != AckAccepted {
		t.Fatalf("AK2/AK5 = %+v %+v", body[1], body[2])
	}
	if body[3].Elements[0] != AckAccepted || body[3].Elements[1] != "1" || body[3].Elements[3] != "1" {
		t.Fatalf("AK9 = %+v", body[3])
	}
}

func TestAck997_RefusesEmptyOutcomes(t *testing.T) {
	recv := sampleInterchange()
	if _, err := Ack997(recv, recv.Groups[0], nil, "1", "1", time.Now()); err == nil {
		t.Fatal("want an error for a group with no transactions")
	}
}

func TestAK9Code(t *testing.T) {
	tests := []struct {
		name     string
		outcomes []string
		want     string
	}{
		{"all accepted", []string{AckAccepted, AckAccepted}, AckAccepted},
		{"accepted with errors", []string{AckAccepted, AckAcceptedWithError}, AckAcceptedWithError},
		{"all rejected", []string{AckRejected}, AckRejected},
		{"mixed accepted and rejected", []string{AckAccepted, AckRejected}, AckPartial},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var outcomes []TxOutcome
			for _, c := range tc.outcomes {
				outcomes = append(outcomes, TxOutcome{Code: c})
			}
			if got := ak9Code(outcomes); got != tc.want {
				t.Fatalf("ak9Code = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSummarizeOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		outcomes []string
		want     AckStatus
	}{
		{"clean", []string{AckAccepted}, StatusAccepted},
		{"with errors", []string{AckAccepted, AckAcceptedWithError}, StatusAcceptedWithErrors},
		{"partial acceptance is accepted with errors (AK9 P)", []string{AckAccepted, AckRejected}, StatusAcceptedWithErrors},
		{"every transaction rejected", []string{AckRejected, AckRejected}, StatusRejected},
		{"nothing to accept", nil, StatusRejected},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var outcomes []TxOutcome
			for _, c := range tc.outcomes {
				outcomes = append(outcomes, TxOutcome{Code: c})
			}
			if got := SummarizeOutcomes(outcomes); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
