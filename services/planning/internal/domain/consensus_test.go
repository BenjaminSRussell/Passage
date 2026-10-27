package domain

import (
	"errors"
	"testing"
)

func TestSubmitConsensus(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		user    string
		cells   int
		want    string
		wantErr error
	}{
		{"draft with cells submits", ConsensusDraft, "alice", 3, ConsensusSubmitted, nil},
		{"empty draft cannot be submitted", ConsensusDraft, "alice", 0, "", ErrConsensusEmpty},
		{"anonymous submit is refused", ConsensusDraft, "", 3, "", ErrConsensusSoD},
		{"submitted cannot submit again", ConsensusSubmitted, "alice", 3, "", ErrConsensusState},
		{"published cannot submit", ConsensusPublished, "alice", 3, "", ErrConsensusState},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SubmitConsensus(tc.status, tc.user, tc.cells)
			if !errors.Is(err, tc.wantErr) || got != tc.want {
				t.Fatalf("SubmitConsensus(%q, %q, %d) = %q, %v; want %q, %v", tc.status, tc.user, tc.cells, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

// 4.22.17: Approve forecast consensus conflicts with Maintain forecasts on the
// same version, so neither the submitter nor any cell writer may decide.
func TestDecideConsensus(t *testing.T) {
	editors := []string{"alice", "carol"}
	tests := []struct {
		name     string
		status   string
		approver string
		approve  bool
		want     string
		wantErr  error
	}{
		{"another user publishes a submitted model", ConsensusSubmitted, "bob", true, ConsensusPublished, nil},
		{"another user rejects back to draft", ConsensusSubmitted, "bob", false, ConsensusDraft, nil},
		{"the submitter cannot publish", ConsensusSubmitted, "dave", true, "", ErrConsensusSoD},
		{"a cell writer of the version cannot publish", ConsensusSubmitted, "carol", true, "", ErrConsensusSoD},
		{"a cell writer cannot reject either", ConsensusSubmitted, "alice", false, "", ErrConsensusSoD},
		{"a missing approver identity is refused", ConsensusSubmitted, "", true, "", ErrConsensusSoD},
		{"a draft cannot be published", ConsensusDraft, "bob", true, "", ErrConsensusState},
		{"a published model cannot be published again", ConsensusPublished, "bob", true, "", ErrConsensusState},
		{"a published model cannot be rejected", ConsensusPublished, "bob", false, "", ErrConsensusState},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecideConsensus(tc.status, "dave", editors, tc.approver, tc.approve)
			if !errors.Is(err, tc.wantErr) || got != tc.want {
				t.Fatalf("DecideConsensus = %q, %v; want %q, %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestConsensusWritable(t *testing.T) {
	if !ConsensusWritable(ConsensusDraft) {
		t.Fatal("draft must accept cell writes")
	}
	for _, s := range []string{ConsensusSubmitted, ConsensusPublished} {
		if ConsensusWritable(s) {
			t.Fatalf("%s must lock cell writes", s)
		}
	}
}
