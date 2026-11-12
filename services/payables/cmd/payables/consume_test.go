package main

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func TestICChargeConsumer_IsSeeded(t *testing.T) {
	if !icChargeSeeded(tenant.ServicePrincipalTable()) {
		t.Fatal("service:payables-iccharge must be seeded with payables.iccharge.from-event")
	}
}
