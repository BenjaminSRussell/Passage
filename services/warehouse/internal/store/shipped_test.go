package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	evts "github.com/BenjaminSRussell/passage/services/warehouse/internal/events"
)

func TestStore_ConfirmShippedPublishesShipmentShippedOnce(t *testing.T) {
	st, ctx, wh := setup(t)
	le := "00000000-0000-0000-0000-0000000000e1"
	sh := ids.New()
	at := time.Date(2026, 11, 10, 8, 30, 0, 0, time.UTC)
	c := ShipConfirmation{ShipmentID: sh, LegalEntityID: le, WarehouseID: wh, CarrierCode: "UPSN", ShippedAt: at,
		SSCCs: []string{"376104250000000017"}, ConfirmedBy: "u1"}
	out, replayed, err := st.ConfirmShipped(ctx, key("ship-"+sh), c)
	if err != nil || replayed || out.CreatedAt.IsZero() {
		t.Fatalf("ConfirmShipped = %+v, %v, %v", out, replayed, err)
	}
	if _, replayed, err := st.ConfirmShipped(ctx, key("ship-"+sh), c); err != nil || !replayed {
		t.Fatalf("repeat of the key = %v, %v", replayed, err)
	}
	if _, _, err := st.ConfirmShipped(ctx, key("other-"+sh), c); !errors.Is(err, ErrShipmentShipped) {
		t.Fatalf("second confirmation of the shipment = %v, want ErrShipmentShipped", err)
	}
	var raw []byte
	var n int
	if err := adminOf(st).QueryRow(context.Background(), `select count(*), max(payload::text) from outbox where type = $1 and aggregate_id = $2`,
		evts.ShipmentShipped, sh).Scan(&n, &raw); err != nil || n != 1 {
		t.Fatalf("shipment.shipped rows = %d, %v", n, err)
	}
	var env struct {
		LegalEntityID string                   `json:"legalentityid"`
		Data          evts.ShipmentShippedData `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.LegalEntityID != le || env.Data.ShipmentID != sh || env.Data.ShippedAt != "2026-11-10T08:30:00Z" ||
		len(env.Data.Containers) != 1 || env.Data.Containers[0].SSCC != "376104250000000017" || env.Data.CarrierCode != "UPSN" {
		t.Fatalf("event %+v", env)
	}
	got, err := st.GetShipConfirmation(ctx, sh)
	if err != nil || got.WarehouseID != wh || !got.ShippedAt.Equal(at) {
		t.Fatalf("GetShipConfirmation = %+v, %v", got, err)
	}
}
