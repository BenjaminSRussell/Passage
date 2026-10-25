package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

func TestTransitionPayment_ExactlyDocumentedEdges(t *testing.T) {
	states := []string{
		domain.PaymentCreated, domain.PaymentPosted, domain.PaymentSent, domain.PaymentAccepted,
		domain.PaymentSettledAtBank, domain.PaymentRejected, domain.PaymentReversed, domain.PaymentVoid,
	}
	allowed := map[[2]string]bool{
		{domain.PaymentCreated, domain.PaymentPosted}:         true,
		{domain.PaymentPosted, domain.PaymentSent}:            true,
		{domain.PaymentSent, domain.PaymentAccepted}:          true,
		{domain.PaymentAccepted, domain.PaymentSettledAtBank}: true,
		{domain.PaymentSent, domain.PaymentRejected}:          true,
		{domain.PaymentAccepted, domain.PaymentRejected}:      true,
		{domain.PaymentPosted, domain.PaymentReversed}:        true,
		{domain.PaymentSent, domain.PaymentReversed}:          true,
	}
	for _, method := range []string{domain.MethodCheck, domain.MethodTransfer} {
		for _, from := range states {
			for _, to := range states {
				want := allowed[[2]string{from, to}] || (from == domain.PaymentSent && to == domain.PaymentVoid && method == domain.MethodCheck)
				err := domain.TransitionPayment(from, to, method)
				if want && err != nil {
					t.Errorf("%s>%s (%s): unexpected %v", from, to, method, err)
				}
				if !want {
					if err == nil {
						t.Errorf("%s>%s (%s): want error", from, to, method)
					} else if codeOf(t, err) != domain.CodeValidation {
						t.Errorf("%s>%s: wrong code", from, to)
					}
				}
			}
		}
	}
}

func TestTransitionPayment_UnknownStates(t *testing.T) {
	for _, pair := range [][2]string{{"", domain.PaymentPosted}, {domain.PaymentCreated, "BOGUS"}, {"BOGUS", "BOGUS"}} {
		if err := domain.TransitionPayment(pair[0], pair[1], domain.MethodCheck); err == nil {
			t.Errorf("%v: want error", pair)
		}
	}
}

func TestTransitionPrenote_ExactlyDocumentedEdges(t *testing.T) {
	states := []string{domain.PrenoteNone, domain.PrenotePending, domain.PrenoteConfirmed, domain.PrenoteRejected}
	allowed := map[[2]string]bool{
		{domain.PrenoteNone, domain.PrenotePending}:      true,
		{domain.PrenotePending, domain.PrenoteConfirmed}: true,
		{domain.PrenotePending, domain.PrenoteRejected}:  true,
	}
	for _, changed := range []bool{false, true} {
		for _, from := range states {
			for _, to := range states {
				want := allowed[[2]string{from, to}] || (from == domain.PrenoteConfirmed && to == domain.PrenoteNone && changed)
				err := domain.TransitionPrenote(from, to, changed)
				if want != (err == nil) {
					t.Errorf("%s>%s changed=%v: want ok=%v, got %v", from, to, changed, want, err)
				}
			}
		}
	}
	if err := domain.TransitionPrenote("BOGUS", domain.PrenotePending, true); err == nil {
		t.Error("unknown prenote state: want error")
	}
}
