package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// TestSettleCosts_AC248: the variance equals material + conversion − output,
// in the period currency, with nothing rounded away.
func TestSettleCosts_AC248(t *testing.T) {
	c, err := SettleCosts("USD", dec("900.10"), dec("300"), dec("1150.25"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Variance.Value.Equal(dec("49.85")) || c.Variance.Currency != "USD" || c.Material.Currency != "USD" {
		t.Fatalf("variance = %s", c.Variance)
	}
	// A favourable period has a negative variance.
	if c, err := SettleCosts("EUR", dec("100"), dec("50"), dec("200")); err != nil || !c.Variance.Value.Equal(dec("-50")) {
		t.Fatalf("favourable variance = %v %v", c.Variance, err)
	}
	// Three decimals are fine for a three-decimal currency, and trailing zeros
	// beyond the minor unit are not a rounding.
	if _, err := SettleCosts("KWD", dec("1.125"), dec("0"), dec("1.000")); err != nil {
		t.Fatalf("KWD: %v", err)
	}
	if _, err := SettleCosts("USD", dec("1.2500"), dec("0"), dec("0")); err != nil {
		t.Fatalf("trailing zeros refused: %v", err)
	}
}

func TestSettleCosts_Refusals(t *testing.T) {
	cases := map[string]struct {
		currency            string
		material, conv, out string
		want                string
	}{
		"lower-case currency": {"usd", "1", "1", "1", "currency"},
		"short currency":      {"US", "1", "1", "1", "currency"},
		"negative material":   {"USD", "-1", "1", "1", "materialCost"},
		"too many decimals":   {"USD", "1", "0.005", "1", "conversionCost"},
		"decimals on yen":     {"JPY", "1", "1", "1.5", "outputValue"},
		"too large":           {"USD", "100000000000000", "1", "1", "materialCost"},
		"negative output":     {"USD", "1", "1", "-0.01", "outputValue"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := SettleCosts(c.currency, dec(c.material), dec(c.conv), dec(c.out))
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeInvalid || !strings.Contains(de.Detail, c.want) {
				t.Fatalf("err = %v, want COM-0001 naming %s", err, c.want)
			}
		})
	}
}

// Property: for any amounts at the currency's minor unit, the variance is
// exactly material + conversion − output, so the settlement balances.
func TestSettleCosts_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := decimal.New(rapid.Int64Range(0, 1e12).Draw(t, "material"), -2)
		c := decimal.New(rapid.Int64Range(0, 1e12).Draw(t, "conversion"), -2)
		o := decimal.New(rapid.Int64Range(0, 1e12).Draw(t, "output"), -2)
		got, err := SettleCosts("USD", m, c, o)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Variance.Value.Equal(m.Add(c).Sub(o)) {
			t.Fatalf("variance %s != %s", got.Variance.Value, m.Add(c).Sub(o))
		}
		if !got.Output.Value.Add(got.Variance.Value).Equal(got.Material.Value.Add(got.Conversion.Value)) {
			t.Fatal("output + variance does not equal material + conversion")
		}
	})
}

func TestCheckSettleable(t *testing.T) {
	ok := []Kanban{{ID: "a", State: Completed}, {ID: "b", State: Emptied}}
	if err := CheckSettleable(ok); err != nil {
		t.Fatalf("completed and emptied cards refused: %v", err)
	}
	open := append(ok, Kanban{ID: "c", State: InProgress}, Kanban{ID: "d", State: Planned})
	err := CheckSettleable(open)
	if err == nil || err.(*Error).Code != CodeOpenKanbans {
		t.Fatalf("open cards: %v", err)
	}
	if !strings.Contains(err.Error(), "c, d") {
		t.Fatalf("open cards not listed: %v", err)
	}
	if err := CheckSettleable(nil); err != nil {
		t.Fatalf("empty period refused: %v", err)
	}
	// Many open cards: the message names the first 20 and counts the rest.
	var many []Kanban
	for i := 1; i <= 25; i++ {
		many = append(many, Kanban{ID: fmt.Sprintf("K-%02d", i), State: Planned})
	}
	err = CheckSettleable(many)
	if err == nil || !strings.Contains(err.Error(), "25 kanbans open") || !strings.Contains(err.Error(), "K-20 and 5 more") || strings.Contains(err.Error(), "K-21") {
		t.Fatalf("many open cards: %v", err)
	}
}

func TestValidateFlow(t *testing.T) {
	good := []Activity{
		{ID: "1", Name: "cut", Kind: "process", OutputLocationID: "L1"},
		{ID: "2", Name: "move", Kind: "transfer", PredecessorIDs: []string{"1"}},
		{ID: "3", Name: "vendor", Kind: "service", PredecessorIDs: []string{"2"}},
	}
	if err := ValidateFlow(good); err != nil {
		t.Fatalf("connected flow refused: %v", err)
	}
	noOutput := []Activity{{ID: "1", Name: "cut", Kind: "process"}}
	if err := ValidateFlow(noOutput); err == nil || err.(*Error).Code != CodeNoOutput {
		t.Fatalf("process without output: %v", err)
	}
	dangling := []Activity{{ID: "1", Name: "cut", Kind: "transfer", PredecessorIDs: []string{"9"}}}
	if err := ValidateFlow(dangling); err == nil || err.(*Error).Code != CodeInvalid {
		t.Fatalf("unknown predecessor: %v", err)
	}
	badKind := []Activity{{ID: "1", Name: "cut", Kind: "assembly"}}
	if err := ValidateFlow(badKind); err == nil || err.(*Error).Code != CodeInvalid {
		t.Fatalf("unknown kind: %v", err)
	}
}

func TestValidateFlow_RejectsCyclesDuplicatesAndEmpty(t *testing.T) {
	loop := []Activity{
		{ID: "1", Name: "cut", Kind: "transfer", PredecessorIDs: []string{"2"}},
		{ID: "2", Name: "bend", Kind: "transfer", PredecessorIDs: []string{"1"}},
	}
	if err := ValidateFlow(loop); err == nil || err.(*Error).Code != CodeInvalid || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle: %v", err)
	}
	self := []Activity{{ID: "1", Name: "cut", Kind: "transfer", PredecessorIDs: []string{"1"}}}
	if err := ValidateFlow(self); err == nil {
		t.Fatal("self predecessor accepted")
	}
	dup := []Activity{{ID: "1", Name: "a", Kind: "transfer"}, {ID: "1", Name: "b", Kind: "transfer"}}
	if err := ValidateFlow(dup); err == nil {
		t.Fatal("duplicate id accepted")
	}
	if err := ValidateFlow(nil); err == nil {
		t.Fatal("empty flow accepted")
	}
}
