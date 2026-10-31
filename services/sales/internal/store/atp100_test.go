package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

// TestPostgres_ATP100PromisedAndStored runs the 100 scenarios of
// testdata/atp-100.json (expectations from the independent reference
// testdata/gen_atp100.py) through the stored promise: each is a CONFIRMED
// order line of the scenario's quantity and requested date, promised with
// PromiseLine against the scenario's buckets. The stored schedule must be the
// reference's lines, in order, with their bucket dates, CONFIRMED up to the
// requested date and BACKORDER after it (FR-27.6), plus one UNPROMISED line
// for the reference's backorder; the line's confirmed ship date is its first
// delivery, and none when nothing is promised.
func TestPostgres_ATP100PromisedAndStored(t *testing.T) {
	s, _, ctx := migrated(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "atp-100.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		ID           string `json:"id"`
		Note         string `json:"note"`
		Requested    string `json:"requested"`
		Quantity     string `json:"quantity"`
		CompleteOnly bool   `json:"completeOnly"`
		Periods      []struct {
			Date   string `json:"date"`
			Supply string `json:"supply"`
			Demand string `json:"demand"`
		} `json:"periods"`
		Expect struct {
			Backorder string `json:"backorder"`
			Schedule  []struct {
				Date     string `json:"date"`
				Quantity string `json:"quantity"`
				Period   int    `json:"period"`
			} `json:"schedule"`
		} `json:"expect"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 100 {
		t.Fatalf("want 100 scenarios, got %d", len(fixtures))
	}
	le, customer := ids.New(), ids.New()
	for _, fx := range fixtures {
		qty := decimal.RequireFromString(fx.Quantity)
		in := orderOf(le, customer, ids.New(), fx.Requested, 1)
		in.Lines[0].Quantity, in.Lines[0].LineNet = qty, qty.Mul(d(10))
		o := confirmed(t, s, ctx, in)
		periods := make([]domain.Period, len(fx.Periods))
		for i, p := range fx.Periods {
			periods[i] = domain.Period{Date: day(p.Date), Supply: decimal.RequireFromString(p.Supply), Demand: decimal.RequireFromString(p.Demand)}
		}
		res, err := s.PromiseLine(ctx, o.ID, o.Version, PromiseRequest{LineNo: 1, Periods: periods, CompleteOnly: fx.CompleteOnly}, "planner", time.Now())
		if err != nil {
			t.Fatalf("%s: promise: %v", fx.ID, err)
		}
		type want struct{ status, date, bucket, qty string }
		var exp []want
		for _, l := range fx.Expect.Schedule {
			status := domain.SchedConfirmed
			if l.Date > fx.Requested {
				status = domain.SchedBackorder
			}
			exp = append(exp, want{status, l.Date, fx.Periods[l.Period].Date, l.Quantity})
		}
		if bo := decimal.RequireFromString(fx.Expect.Backorder); bo.IsPositive() {
			exp = append(exp, want{domain.SchedUnpromised, "", "", fx.Expect.Backorder})
		}
		stored, err := s.ListScheduleLines(ctx, o.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(stored) != len(exp) || len(res.Schedule) != len(exp) {
			t.Fatalf("%s (%s): stored %+v, want %+v", fx.ID, fx.Note, stored, exp)
		}
		for i, w := range exp {
			g := stored[i]
			if g.Status != w.status || dayOrEmpty(g.DeliveryDate) != w.date || dayOrEmpty(g.BucketDate) != w.bucket ||
				!g.Quantity.Equal(decimal.RequireFromString(w.qty)) || g.Source != domain.SchedSourceATP {
				t.Fatalf("%s (%s): schedule line %d = %+v (date %s, bucket %s), want %+v", fx.ID, fx.Note, i, g, dayOrEmpty(g.DeliveryDate), dayOrEmpty(g.BucketDate), w)
			}
		}
		first := ""
		if len(fx.Expect.Schedule) > 0 {
			first = fx.Expect.Schedule[0].Date
		}
		if got := dayOrEmpty(res.Order.Lines[0].ConfirmedShipDate); got != first {
			t.Fatalf("%s: confirmed ship date %q, want the first delivery %q", fx.ID, got, first)
		}
	}
}
