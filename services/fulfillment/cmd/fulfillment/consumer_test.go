package main

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/api"
)

// The consumer starts only when the service principal table gives it the sync duty.
func TestOrderSeeded(t *testing.T) {
	if orderSeeded(map[string][]string{}) || orderSeeded(map[string][]string{api.OrderConsumerName: {"other.duty"}}) {
		t.Fatal("unseeded consumer reported seeded")
	}
	if !orderSeeded(map[string][]string{api.OrderConsumerName: {api.DutyOrderSync}}) {
		t.Fatal("seeded consumer reported unseeded")
	}
}
