package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/edi/internal/autosend"
	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
)

// packedShipment is the fake shipment as fulfillment shows it when it left the dock without a packing slip: PACKED, no actual
// ship date, its container CLOSED and on no slip.
func packedShipment(f *fakeSources) {
	f.shipment.Status, f.shipment.ActualShipDate = "PACKED", nil
	f.shipment.Containers[0].Status = "CLOSED"
}

// #35 (35-1): a shipment the warehouse ships without a packing slip still gets its 856, once, from shipment.shipped.
func TestAutoOutbound_WarehouseShippedSendsTheWholeShipment856Once(t *testing.T) {
	f := newFake()
	packedShipment(f)
	_, h, pool, partner, handlers := autoSetup(t, f)
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/auto-outbound", `{"transactionSets":["856"]}`, "edi.admin", srcScope)
	handle, ok := handlers[autosend.ShipmentShippedType]
	if !ok {
		t.Fatal("the consumer does not handle shipment.shipped")
	}
	evt := event(t, autosend.ShipmentShippedType, autosend.WarehouseSource, map[string]any{"shipment_id": srcShipment, "legal_entity_id": srcLE,
		"carrier_code": "UPSN", "shipped_at": "2026-11-10T08:30:00Z", "containers": []map[string]string{{"sscc": "003456789012340019"}}})
	for i := 0; i < 2; i++ { // the second is a redelivery
		if err := handle(consumerCtx(t), evt); err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
	}
	if n := countInterchanges(t, h, "OUT"); n != 1 {
		t.Fatalf("one 856 expected, got %d", n)
	}
	var raw string
	if err := pool.QueryRow(context.Background(), `select raw_payload from interchange where direction = 'OUT'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "003456789012340019") || !strings.Contains(raw, "20261110") || !strings.Contains(raw, "UPSN") {
		t.Fatalf("856 must carry the SSCC, the warehouse's ship date and carrier: %s", raw)
	}
	var rows int
	if err := pool.QueryRow(context.Background(), `select count(*) from self_billing_shipment`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("self-billing shipments: %d %v", rows, err)
	}
}

// A shipment fulfillment already shipped by a packing slip gets its notices from asn.issued; shipment.shipped sends nothing.
func TestAutoOutbound_WarehouseShippedSkipsAShipmentShippedByASlip(t *testing.T) {
	f := newFake() // SHIPPED, by a slip
	_, h, _, partner, handlers := autoSetup(t, f)
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/auto-outbound", `{"transactionSets":["856"]}`, "edi.admin", srcScope)
	evt := event(t, autosend.ShipmentShippedType, autosend.WarehouseSource, map[string]any{"shipment_id": srcShipment, "legal_entity_id": srcLE,
		"shipped_at": "2026-11-10T08:30:00Z", "containers": []map[string]string{}})
	if err := handlers[autosend.ShipmentShippedType](consumerCtx(t), evt); err != nil {
		t.Fatalf("a skip is not a retry: %v", err)
	}
	if n := countInterchanges(t, h, "OUT"); n != 0 {
		t.Fatalf("no 856 expected, got %d", n)
	}
	bad := event(t, autosend.ShipmentShippedType, autosend.WarehouseSource, map[string]any{"shipment_id": srcShipment, "legal_entity_id": srcLE, "shipped_at": "yesterday"})
	if err := handlers[autosend.ShipmentShippedType](consumerCtx(t), bad); err == nil {
		t.Fatal("an unreadable ship time is a permanent failure")
	}
	wrong := event(t, autosend.ShipmentShippedType, autosend.FulfillmentSource, map[string]any{"shipment_id": srcShipment, "legal_entity_id": srcLE, "shipped_at": "2026-11-10T08:30:00Z"})
	if err := handlers[autosend.ShipmentShippedType](consumerCtx(t), wrong); err == nil {
		t.Fatal("shipment.shipped from another source is refused")
	}
}

func TestShippedByWarehouse(t *testing.T) {
	p := domain.TradeProfile{LegalEntityID: srcLE, CustomerID: srcCustomer}
	ws := domain.WarehouseShip{ShippedAt: newFake().shipment.ActualShipDate.Add(48 * 3600e9)}
	pallet := "c-pallet"
	base := func() domain.SourceShipment {
		f := newFake()
		packedShipment(f)
		sh := f.shipment
		sh.Containers = []domain.SourceContainer{
			{ID: pallet, SSCC: "003456789012340019", Status: "CLOSED", GrossKg: decimal.NewFromInt(100)},
			{ID: "c-carton", SSCC: "003456789012340026", Status: "CLOSED", ParentContainerID: &pallet,
				Lines: []domain.SourceContainerLine{{ShipmentLineID: "sl-1", Qty: decimal.NewFromInt(10)}}},
			{ID: "c-open", SSCC: "003456789012340033", Status: "OPEN"},
		}
		return sh
	}
	got, err := domain.ShippedByWarehouse(p, base(), domain.WarehouseShip{ShippedAt: ws.ShippedAt, SSCCs: []string{"003456789012340019"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "SHIPPED" || got.ActualShipDate == nil || !got.ActualShipDate.Equal(ws.ShippedAt) || len(got.Containers) != 2 {
		t.Fatalf("a named pallet takes its carton along: %+v", got)
	}
	for _, c := range got.Containers {
		if c.Status != "SHIPPED" {
			t.Fatalf("container %s is %s", c.SSCC, c.Status)
		}
	}
	if got, err := domain.ShippedByWarehouse(p, base(), ws); err != nil || len(got.Containers) != 2 {
		t.Fatalf("none named: every closed container left, %d %v", len(got.Containers), err)
	}
	refusals := map[string]func() (domain.SourceShipment, domain.TradeProfile, domain.WarehouseShip){
		"shipped by a slip": func() (domain.SourceShipment, domain.TradeProfile, domain.WarehouseShip) {
			return newFake().shipment, p, ws
		},
		"not packed yet": func() (domain.SourceShipment, domain.TradeProfile, domain.WarehouseShip) {
			sh := base()
			sh.Status = "RELEASED"
			return sh, p, ws
		},
		"another customer's": func() (domain.SourceShipment, domain.TradeProfile, domain.WarehouseShip) {
			return base(), domain.TradeProfile{LegalEntityID: srcLE, CustomerID: srcOrder}, ws
		},
		"an SSCC the shipment does not hold": func() (domain.SourceShipment, domain.TradeProfile, domain.WarehouseShip) {
			return base(), p, domain.WarehouseShip{ShippedAt: ws.ShippedAt, SSCCs: []string{"003456789012340040"}}
		},
		"an open container named": func() (domain.SourceShipment, domain.TradeProfile, domain.WarehouseShip) {
			return base(), p, domain.WarehouseShip{ShippedAt: ws.ShippedAt, SSCCs: []string{"003456789012340033"}}
		},
		"a container already on a slip": func() (domain.SourceShipment, domain.TradeProfile, domain.WarehouseShip) {
			sh := base()
			slip := "slip-1"
			sh.Containers[0].PackingSlipID = &slip
			return sh, p, ws
		},
		"no ship time": func() (domain.SourceShipment, domain.TradeProfile, domain.WarehouseShip) {
			return base(), p, domain.WarehouseShip{}
		},
	}
	for name, make := range refusals {
		t.Run(name, func(t *testing.T) {
			sh, prof, w := make()
			if _, err := domain.ShippedByWarehouse(prof, sh, w); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
