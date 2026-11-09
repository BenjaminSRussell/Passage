package domain

import "testing"

func TestMessageActionable(t *testing.T) {
	for _, a := range []string{ActionPostpone, ActionAdvance, ActionCancel, ActionDecrease, ActionIncrease, ActionDerived} {
		if !MessageActionable(a) {
			t.Errorf("%s changes a source order and must be actionable", a)
		}
	}
	for _, a := range []string{ActionDelay, ActionShortage, ActionRedZone, ActionSalesOnTop, ActionNoVendor, ""} {
		if MessageActionable(a) {
			t.Errorf("%q is information or a planning decision and must not be actionable", a)
		}
	}
}
