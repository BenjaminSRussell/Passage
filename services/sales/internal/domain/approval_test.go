package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func decp(s string) *decimal.Decimal { d := decimal.RequireFromString(s); return &d }

const (
	mgrA = "0190a3c4-0000-7000-8000-00000000000a"
	mgrB = "0190a3c4-0000-7000-8000-00000000000b"
)

func TestNeedsApproval(t *testing.T) {
	usd500 := ApprovalPolicy{Required: true, Threshold: decp("500"), ThresholdCurrency: "USD"}
	cases := []struct {
		name          string
		p             ApprovalPolicy
		moved, change string
		currency      string
		want          bool
	}{
		{"switch off", ApprovalPolicy{}, "0", "1000000", "USD", false},
		{"no threshold needs every change", ApprovalPolicy{Required: true}, "0", "0", "USD", true},
		{"below threshold applies at once", usd500, "0", "499.9999", "USD", false},
		{"at threshold needs approval", usd500, "0", "500", "USD", true},
		{"a fall counts by its size", usd500, "0", "-800", "USD", true},
		{"a small fall applies at once", usd500, "0", "-20", "USD", false},
		{"earlier unapproved changes count: split change reaches it", usd500, "450", "60", "USD", true},
		{"earlier unapproved changes below it leave room", usd500, "400", "99.99", "USD", false},
		{"a change that moves nothing applies even when the budget is nearly used", usd500, "499.99", "0", "USD", false},
		{"other currency has no rate so needs approval", usd500, "0", "1", "EUR", true},
		{"threshold ignored when the switch is off", ApprovalPolicy{Threshold: decp("500"), ThresholdCurrency: "USD"}, "0", "9999", "USD", false},
	}
	for _, c := range cases {
		if got := c.p.NeedsApproval(decimal.RequireFromString(c.moved), decimal.RequireFromString(c.change), c.currency); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if !usd500.UsesThreshold("USD") || usd500.UsesThreshold("EUR") || (ApprovalPolicy{Required: true}).UsesThreshold("USD") {
		t.Error("UsesThreshold: only a switched-on policy with a threshold in the order's currency")
	}
}

func TestMayDecide(t *testing.T) {
	if !(ApprovalPolicy{}).MayDecide("anyone") {
		t.Error("no group named: anyone with the duty may decide")
	}
	p := ApprovalPolicy{Approvers: []string{mgrA, mgrB}}
	if !p.MayDecide(mgrB) || p.MayDecide("0190a3c4-0000-7000-8000-00000000000c") || p.MayDecide("mgr-a") || p.MayDecide("") {
		t.Error("only group members may decide")
	}
	if !p.MayDecide(strings.ToUpper(mgrA)) {
		t.Error("a member is matched in canonical form whatever the case of the caller's id")
	}
}

func TestNormalizeApprovers(t *testing.T) {
	got, invalid := NormalizeApprovers([]string{mgrB, strings.ToUpper(mgrA), mgrA, mgrB})
	if strings.Join(got, ",") != mgrA+","+mgrB || len(invalid) != 0 {
		t.Errorf("got %v invalid %v", got, invalid)
	}
	if got, invalid := NormalizeApprovers([]string{"mgr-a", "", mgrA, "00000000-0000-0000-0000-000000000000"}); len(got) != 1 || len(invalid) != 3 {
		t.Errorf("non-UUID, blank and nil ids must be reported: got %v invalid %v", got, invalid)
	}
	if got, _ := NormalizeApprovers(nil); got == nil || len(got) != 0 {
		t.Errorf("nil input must give an empty, non-nil group, got %#v", got)
	}
}

func TestValidateApprovalPolicy(t *testing.T) {
	if f := ValidateApprovalPolicy(ApprovalPolicy{Required: true}); len(f) != 0 {
		t.Errorf("plain switch is valid: %v", f)
	}
	if f := ValidateApprovalPolicy(ApprovalPolicy{Threshold: decp("10.5"), ThresholdCurrency: "USD", Approvers: []string{mgrA}}); len(f) != 0 {
		t.Errorf("valid: %v", f)
	}
	bad := map[string]ApprovalPolicy{
		"zero threshold":             {Threshold: decp("0"), ThresholdCurrency: "USD"},
		"negative threshold":         {Threshold: decp("-1"), ThresholdCurrency: "USD"},
		"5 decimal places":           {Threshold: decp("1.00001"), ThresholdCurrency: "USD"},
		"beyond numeric(19,4)":       {Threshold: decp("1000000000000000"), ThresholdCurrency: "USD"},
		"currency without threshold": {ThresholdCurrency: "USD"},
	}
	for name, p := range bad {
		if ValidateApprovalPolicy(p)["approvalThreshold"] == "" {
			t.Errorf("%s not rejected", name)
		}
	}
	if ValidateApprovalPolicy(ApprovalPolicy{Threshold: decp("5")})["thresholdCurrency"] == "" {
		t.Error("threshold without currency not rejected")
	}
	if ValidateApprovalPolicy(ApprovalPolicy{Threshold: decp("5"), ThresholdCurrency: "usd"})["thresholdCurrency"] == "" {
		t.Error("lower-case currency not rejected")
	}
	if ValidateApprovalPolicy(ApprovalPolicy{Approvers: make([]string, 21)})["approverIds"] == "" {
		t.Error("21 approvers not rejected")
	}
}
