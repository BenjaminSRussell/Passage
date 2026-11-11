package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestTruckLevelReplenishQty(t *testing.T) {
	l := TruckLevel{Min: decimal.NewFromInt(2), Max: decimal.NewFromInt(10)}
	for _, c := range []struct{ onHand, inbound, want string }{
		{"5", "0", "0"},   // above the minimum
		{"2", "0", "0"},   // at the minimum is not below it
		{"1", "0", "9"},   // below: up to the maximum
		{"-1", "0", "11"}, // a negative truck is brought back to the maximum
		{"1", "9", "0"},   // what is on its way counts
		{"0", "1", "9"},   // on the way but still short
	} {
		if got := l.ReplenishQty(decimal.RequireFromString(c.onHand), decimal.RequireFromString(c.inbound)); got.String() != c.want {
			t.Errorf("on hand %s inbound %s: %s, want %s", c.onHand, c.inbound, got, c.want)
		}
	}
}

func TestTruckLevelValidate(t *testing.T) {
	d := decimal.RequireFromString
	for name, c := range map[string]struct {
		l  TruckLevel
		ok bool
	}{
		"plain":         {TruckLevel{Min: d("1"), Max: d("5")}, true},
		"zero min":      {TruckLevel{Min: d("0"), Max: d("5")}, true},
		"equal":         {TruckLevel{Min: d("5"), Max: d("5")}, true},
		"negative min":  {TruckLevel{Min: d("-1"), Max: d("5")}, false},
		"max below min": {TruckLevel{Min: d("5"), Max: d("4")}, false},
		"zero max":      {TruckLevel{Min: d("0"), Max: d("0")}, false},
		"too precise":   {TruckLevel{Min: d("1"), Max: d("5.1234567")}, false},
	} {
		if err := c.l.Validate(6); (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}
