package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// eventsOf returns the data of every outbox event of type for aggregate, oldest first.
func eventsOf(t *testing.T, pool *pgxpool.Pool, typ, aggregate string) []json.RawMessage {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select payload->'data' from outbox where type = $1 and aggregate_id = $2 order by created_at, id`, typ, aggregate)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var d json.RawMessage
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

// order.confirmed carries the catch-weight spec of a line whose quantity is
// cases, and orderprogress.recorded the weight each delivery moved (#480), so
// a consumer that books inventory in weight never reads cases as weight.
func TestPostgres_CatchWeightEventsCarryTheWeight(t *testing.T) {
	s, pool, ctx := migrated(t)
	o := confirmed(t, s, ctx, catchWeightOrder(t, ids.New(), ids.New(), 10))
	conf := eventsOf(t, pool, salesevents.OrderConfirmed, o.ID)
	var c salesevents.OrderConfirmedData
	if len(conf) != 1 || json.Unmarshal(conf[0], &c) != nil || len(c.Lines) != 1 || c.Lines[0].CatchWeight == nil ||
		c.Lines[0].Quantity != "10" || c.Lines[0].CatchWeight.Nominal != "10" || c.Lines[0].CatchWeight.Min != "9.8" || c.Lines[0].CatchWeight.Max != "11" {
		t.Fatalf("order.confirmed %s", conf)
	}

	if _, err := report(t, s, ctx, o.ID, domain.ProgressDelivered, "PS-W", 5, weigh("51")); err != nil {
		t.Fatal(err)
	}
	if _, err := report(t, s, ctx, o.ID, domain.ProgressDelivered, "PS-N", 3, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := report(t, s, ctx, o.ID, domain.ProgressInvoiced, "INV-1", 8, nil); err != nil {
		t.Fatal(err)
	}
	prog := eventsOf(t, pool, salesevents.OrderProgressRecorded, o.ID)
	if len(prog) != 3 {
		t.Fatalf("%d progress events", len(prog))
	}
	want := []struct{ reported, delivered string }{{"51", "51"}, {"30", "81"}, {"", ""}}
	for i, raw := range prog {
		var p salesevents.OrderProgressData
		if err := json.Unmarshal(raw, &p); err != nil || len(p.Lines) != 1 {
			t.Fatalf("progress %d %s", i, raw)
		}
		if l := p.Lines[0]; l.ReportedWeight != want[i].reported || l.DeliveredWeight != want[i].delivered {
			t.Fatalf("progress %d (%s): reported %q delivered %q, want %+v", i, p.Kind, l.ReportedWeight, l.DeliveredWeight, want[i])
		}
	}
}
