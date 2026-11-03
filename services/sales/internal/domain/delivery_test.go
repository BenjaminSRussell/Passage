package domain

import (
	"strings"
	"testing"
)

func TestShipDate(t *testing.T) {
	today := day("2027-03-01")
	cases := []struct {
		name      string
		control   string
		requested string
		lead      int
		want      string
		past      bool
		leadMoved bool
	}{
		{"none keeps the requested date", DeliveryControlNone, "2027-03-04", 5, "2027-03-04", false, false},
		{"past date becomes today", DeliveryControlNone, "2027-02-20", 0, "2027-03-01", true, false},
		{"lead time pushes an early request", DeliveryControlSalesLeadTime, "2027-03-03", 5, "2027-03-06", false, true},
		{"lead time keeps a late request", DeliveryControlSalesLeadTime, "2027-03-10", 5, "2027-03-10", false, false},
		{"past date and lead time", DeliveryControlSalesLeadTime, "2027-02-01", 2, "2027-03-03", true, true},
		{"lead time zero", DeliveryControlSalesLeadTime, "2027-03-01", 0, "2027-03-01", false, false},
	}
	for _, c := range cases {
		got := ShipDate(c.control, day(c.requested), today, c.lead)
		if got.Date.Format("2006-01-02") != c.want || got.PastMoved != c.past || got.LeadMoved != c.leadMoved {
			t.Errorf("%s: got %s past=%v lead=%v, want %s past=%v lead=%v", c.name, got.Date.Format("2006-01-02"), got.PastMoved, got.LeadMoved, c.want, c.past, c.leadMoved)
		}
	}
}

func TestReceiptDate(t *testing.T) {
	// 2027-03-05 is a Friday.
	weekdays := DeliveryTerms{TransportDays: 2, ReceivingDays: []int{1, 2, 3, 4, 5}}
	cases := []struct {
		name  string
		terms DeliveryTerms
		ship  string
		want  string
	}{
		{"default terms receive on the ship date", DeliveryTerms{}, "2027-03-05", "2027-03-05"},
		{"transport days only", DeliveryTerms{TransportDays: 3}, "2027-03-01", "2027-03-04"},
		{"Friday ship, two days, rolls from Sunday to Monday", weekdays, "2027-03-05", "2027-03-08"},
		{"Monday ship lands on Wednesday", weekdays, "2027-03-01", "2027-03-03"},
		{"single receiving day Thursday", DeliveryTerms{ReceivingDays: []int{4}}, "2027-03-01", "2027-03-04"},
		{"receiving day today is kept", DeliveryTerms{ReceivingDays: []int{1}}, "2027-03-01", "2027-03-01"},
		{"Sunday is 7", DeliveryTerms{ReceivingDays: []int{7}}, "2027-03-01", "2027-03-07"},
	}
	for _, c := range cases {
		if got := c.terms.ReceiptDate(day(c.ship)).Format("2006-01-02"); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestDeliveryTermsValidate(t *testing.T) {
	cases := []struct {
		name  string
		terms DeliveryTerms
		field string
		want  string
	}{
		{"ok", DeliveryTerms{TransportDays: 2, ReceivingDays: []int{1, 3}}, "", ""},
		{"negative transport", DeliveryTerms{TransportDays: -1, ReceivingDays: []int{1}}, "transportDays", "0 to 60"},
		{"too long transport", DeliveryTerms{TransportDays: 61, ReceivingDays: []int{1}}, "transportDays", "0 to 60"},
		{"no days", DeliveryTerms{}, "receivingDays", "at least one"},
		{"weekday 8", DeliveryTerms{ReceivingDays: []int{8}}, "receivingDays", "1 (Monday)"},
		{"repeat", DeliveryTerms{ReceivingDays: []int{2, 2}}, "receivingDays", "repeat"},
	}
	for _, c := range cases {
		f := c.terms.Validate()
		if c.field == "" {
			if len(f) != 0 {
				t.Errorf("%s: unexpected %v", c.name, f)
			}
			continue
		}
		if !strings.Contains(f[c.field], c.want) {
			t.Errorf("%s: %s = %q, want it to contain %q", c.name, c.field, f[c.field], c.want)
		}
	}
}

func TestValidateDeliveryControl(t *testing.T) {
	for _, ok := range []string{DeliveryControlNone, DeliveryControlSalesLeadTime} {
		if m := ValidateDeliveryControl(ok); m != "" {
			t.Errorf("%s refused: %s", ok, m)
		}
	}
	for _, c := range []string{DeliveryControlATP, DeliveryControlCTP} {
		if m := ValidateDeliveryControl(c); !strings.Contains(m, "not active yet") {
			t.Errorf("%s: %q", c, m)
		}
	}
	if m := ValidateDeliveryControl("FAST"); !strings.Contains(m, "must be") {
		t.Errorf("FAST: %q", m)
	}
}
