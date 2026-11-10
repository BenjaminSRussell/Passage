package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestAckInput_Validate(t *testing.T) {
	qty, zero, price, negative := decimal.NewFromInt(5), decimal.Zero, decimal.RequireFromString("1.25"), decimal.NewFromInt(-1)
	date, _ := ParseDay("2027-02-01")
	tests := []struct {
		name string
		in   AckInput
		want string
	}{
		{"accept", AckInput{VersionNo: 1, Outcome: AckAccepted}, ""},
		{"accept with a reason", AckInput{VersionNo: 1, Outcome: AckAccepted, Reason: "ok"}, ""},
		{"reject with a reason", AckInput{VersionNo: 2, Outcome: AckRejected, Reason: "cannot supply"}, ""},
		{"propose all three", AckInput{VersionNo: 1, Outcome: AckChangesProposed, Changes: []LineChange{{LineNo: 1, Qty: &qty, UnitPrice: &price, DeliveryDate: &date}}}, ""},
		{"no version", AckInput{Outcome: AckAccepted}, "versionNo"},
		{"unknown outcome", AckInput{VersionNo: 1, Outcome: "MAYBE"}, "outcome"},
		{"reject without reason", AckInput{VersionNo: 1, Outcome: AckRejected, Reason: "  "}, "reason is required"},
		{"reason too long", AckInput{VersionNo: 1, Outcome: AckRejected, Reason: strings.Repeat("x", MaxAckReasonLen+1)}, "at most"},
		{"accept with changes", AckInput{VersionNo: 1, Outcome: AckAccepted, Changes: []LineChange{{LineNo: 1, Qty: &qty}}}, "only be sent with a proposal"},
		{"reject with changes", AckInput{VersionNo: 1, Outcome: AckRejected, Reason: "r", Changes: []LineChange{{LineNo: 1, Qty: &qty}}}, "only be sent with a proposal"},
		{"proposal without changes", AckInput{VersionNo: 1, Outcome: AckChangesProposed}, "between 1 and"},
		{"line zero", AckInput{VersionNo: 1, Outcome: AckChangesProposed, Changes: []LineChange{{Qty: &qty}}}, "line number"},
		{"same line twice", AckInput{VersionNo: 1, Outcome: AckChangesProposed, Changes: []LineChange{{LineNo: 1, Qty: &qty}, {LineNo: 1, UnitPrice: &price}}}, "more than once"},
		{"proposes nothing", AckInput{VersionNo: 1, Outcome: AckChangesProposed, Changes: []LineChange{{LineNo: 1}}}, "proposes nothing"},
		{"zero quantity", AckInput{VersionNo: 1, Outcome: AckChangesProposed, Changes: []LineChange{{LineNo: 1, Qty: &zero}}}, "more than zero"},
		{"negative price", AckInput{VersionNo: 1, Outcome: AckChangesProposed, Changes: []LineChange{{LineNo: 1, UnitPrice: &negative}}}, "cannot be negative"},
		{"zero price is allowed", AckInput{VersionNo: 1, Outcome: AckChangesProposed, Changes: []LineChange{{LineNo: 1, UnitPrice: &zero}}}, ""},
		{"missing date", AckInput{VersionNo: 1, Outcome: AckChangesProposed, Changes: []LineChange{{LineNo: 1, DeliveryDate: &Day{}}}}, "delivery date"},
	}
	for _, tc := range tests {
		err := tc.in.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: got %v, want an error containing %q", tc.name, err, tc.want)
		}
	}
}
