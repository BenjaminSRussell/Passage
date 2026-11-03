package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestValidateRule(t *testing.T) {
	one := decimal.NewFromInt(1)
	text := "A"
	day := time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		rule Rule
		kind string
		ok   bool
	}{
		{"min numeric", Rule{Attribute: "p", Aggregation: AggMin, Decimals: -1}, AttrNumeric, true},
		{"min text", Rule{Attribute: "p", Aggregation: AggMin, Decimals: -1}, AttrText, false},
		{"weighted avg decimals", Rule{Attribute: "p", Aggregation: AggWeightedAverage, Decimals: 2}, AttrNumeric, true},
		{"weighted avg too precise", Rule{Attribute: "p", Aggregation: AggWeightedAverage, Decimals: 7}, AttrNumeric, false},
		{"decimals on max", Rule{Attribute: "p", Aggregation: AggMax, Decimals: 2}, AttrNumeric, false},
		{"earliest date", Rule{Attribute: "expiry", Aggregation: AggEarliestDate, Decimals: -1}, AttrDate, true},
		{"latest on numeric", Rule{Attribute: "p", Aggregation: AggLatestDate, Decimals: -1}, AttrNumeric, false},
		{"copy enum", Rule{Attribute: "grade", Aggregation: AggCopy, Decimals: -1}, AttrEnum, true},
		{"copy with fixed value", Rule{Attribute: "grade", Aggregation: AggCopy, Decimals: -1, Fixed: Value{Text: &text}}, AttrText, false},
		{"fixed text", Rule{Attribute: "grade", Aggregation: AggFixed, Decimals: -1, Fixed: Value{Text: &text}}, AttrEnum, true},
		{"fixed number on text", Rule{Attribute: "grade", Aggregation: AggFixed, Decimals: -1, Fixed: Value{Num: &one}}, AttrText, false},
		{"fixed date", Rule{Attribute: "expiry", Aggregation: AggFixed, Decimals: -1, Fixed: Value{Date: &day}}, AttrDate, true},
		{"fixed empty", Rule{Attribute: "grade", Aggregation: AggFixed, Decimals: -1}, AttrText, false},
		{"fixed two values", Rule{Attribute: "grade", Aggregation: AggFixed, Decimals: -1, Fixed: Value{Text: &text, Num: &one}}, AttrText, false},
		{"unknown", Rule{Attribute: "p", Aggregation: "median", Decimals: -1}, AttrNumeric, false},
		{"no attribute", Rule{Aggregation: AggMin, Decimals: -1}, AttrNumeric, false},
	}
	for _, tc := range tests {
		err := ValidateRule(tc.rule, tc.kind)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}
