package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func intPtr(n int) *int { return &n }

func TestValidateSample(t *testing.T) {
	order, primary := ids.New(), ids.New()
	base := SampleInput{QualityOrderID: order, Kind: SampleReserve, Quantity: decimal.NewFromInt(1), BatchExpiresOn: "2027-06-30"}
	if f := ValidateSample(base); len(f) != 0 {
		t.Fatalf("valid reserve sample: %v", f)
	}
	cases := []struct {
		name  string
		edit  func(*SampleInput)
		field string
	}{
		{"order id", func(in *SampleInput) { in.QualityOrderID = "qo-1" }, "qualityOrderId"},
		{"kind", func(in *SampleInput) { in.Kind = "RETAINED" }, "kind"},
		{"zero quantity", func(in *SampleInput) { in.Quantity = decimal.Zero }, "quantity"},
		{"seven decimals", func(in *SampleInput) { in.Quantity = decimal.RequireFromString("0.0000001") }, "quantity"},
		{"reserve without batch expiry", func(in *SampleInput) { in.BatchExpiresOn = "" }, "batchExpiresOn"},
		{"bad date", func(in *SampleInput) { in.BatchExpiresOn = "30.06.2027" }, "batchExpiresOn"},
		{"retention over 10 years", func(in *SampleInput) { in.RetentionYears = intPtr(11) }, "retentionYears"},
		{"negative retention", func(in *SampleInput) { in.RetentionYears = intPtr(-1) }, "retentionYears"},
		{"reserve naming primaries", func(in *SampleInput) { in.PrimaryIDs = []string{primary} }, "primaryIds"},
		{"pooled without primaries", func(in *SampleInput) { in.Kind = SamplePooled }, "primaryIds"},
		{"pooled repeating a primary", func(in *SampleInput) { in.Kind, in.PrimaryIDs = SamplePooled, []string{primary, primary} }, "primaryIds"},
		{"container over 60", func(in *SampleInput) { in.Container = string(make([]rune, 61)) }, "container"},
	}
	for _, c := range cases {
		in := base
		c.edit(&in)
		if f := ValidateSample(in); f[c.field] == "" {
			t.Errorf("%s: want an error on %s, got %v", c.name, c.field, f)
		}
	}
	if f := ValidateSample(SampleInput{QualityOrderID: order, Kind: SamplePrimary, Quantity: decimal.NewFromInt(2)}); len(f) != 0 {
		t.Errorf("a primary sample needs no batch expiry: %v", f)
	}
}

func TestNewSample_RetentionExpiry(t *testing.T) {
	now := time.Date(2026, 10, 27, 15, 0, 0, 0, time.UTC)
	in := SampleInput{QualityOrderID: ids.New(), Kind: SampleReserve, Quantity: decimal.NewFromInt(1), BatchExpiresOn: "2027-06-30", Container: "  vial  "}
	s, err := NewSample("s-1", in, "EA", "qa-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if s.RetentionYears != DefaultRetentionYears || s.RetentionExpiresOn != "2028-06-30" || s.Status != SampleActive || s.UOM != "EA" ||
		s.Container != "vial" || s.RecordedBy != "qa-1" || s.PrimaryIDs == nil {
		t.Fatalf("default retention is one year past batch expiry (21 CFR 211.170): %+v", s)
	}
	in.RetentionYears, in.BatchExpiresOn = intPtr(3), "2024-02-29"
	if s, _ := NewSample("s-2", in, "EA", "qa-1", now); s.RetentionExpiresOn != "2027-03-01" || s.Status != SampleActive {
		t.Fatalf("29 February moves to 1 March, never earlier: %+v", s)
	}
	in.RetentionYears, in.BatchExpiresOn = intPtr(0), "2026-10-27"
	if s, _ := NewSample("s-3", in, "EA", "qa-1", now); s.Status != SampleDueForDisposal {
		t.Fatalf("zero retention and a batch expiring today is due now: %+v", s)
	}
	if _, err := NewSample("s-4", SampleInput{Kind: SampleReserve}, "EA", "qa-1", now); err == nil {
		t.Fatal("an invalid request must not build a sample")
	}
}

// TestSampleStatus is AC-33.18: a retention sample whose expiry is today is due
// for disposal; the day before it is still active.
func TestSampleStatus(t *testing.T) {
	expiry := "2027-06-30"
	for _, c := range []struct {
		asOf time.Time
		want string
	}{
		{time.Date(2027, 6, 29, 23, 59, 0, 0, time.UTC), SampleActive},
		{time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC), SampleDueForDisposal},
		{time.Date(2027, 7, 1, 8, 0, 0, 0, time.UTC), SampleDueForDisposal},
		{time.Date(2027, 6, 30, 1, 0, 0, 0, time.FixedZone("UTC+2", 2*3600)), SampleActive}, // still 29 June in UTC
	} {
		if got := SampleStatus(expiry, c.asOf); got != c.want {
			t.Errorf("as of %s: %s, want %s", c.asOf, got, c.want)
		}
	}
	if SampleStatus("", time.Now()) != SampleActive {
		t.Error("a sample without an expiry stays active")
	}
}

func TestCheckSampleOrder(t *testing.T) {
	order := Order{Status: StatusOpen, LotQty: decimal.NewFromInt(10)}
	draw := func(kind string, q int64) Sample { return Sample{Kind: kind, Quantity: decimal.NewFromInt(q)} }
	if err := CheckSampleOrder(order, draw(SamplePrimary, 4), decimal.NewFromInt(6)); err != nil {
		t.Fatalf("exactly the lot: %v", err)
	}
	if e, ok := CheckSampleOrder(order, draw(SampleReserve, 5), decimal.NewFromInt(6)).(*Error); !ok || e.Code != CodeSampleOverLot {
		t.Fatalf("over the lot: %v", e)
	}
	if err := CheckSampleOrder(order, draw(SamplePooled, 5), decimal.NewFromInt(10)); err != nil {
		t.Fatalf("a pooled sample is mixed from primaries, not drawn from the lot: %v", err)
	}
	order.Status = StatusCancelled
	if e, ok := CheckSampleOrder(order, draw(SamplePrimary, 1), decimal.Zero).(*Error); !ok || e.Code != CodeStatus {
		t.Fatalf("cancelled order: %v", e)
	}
}

// TestRetentionExpiryProperty: the retention expiry is never before the batch
// expiry plus the retention in whole years, and at most one day after it.
func TestRetentionExpiryProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		batch := time.Date(rapid.IntRange(2000, 2100).Draw(t, "year"), time.Month(rapid.IntRange(1, 12).Draw(t, "month")),
			rapid.IntRange(1, 28).Draw(t, "day"), 0, 0, 0, 0, time.UTC).AddDate(0, 0, rapid.IntRange(0, 3).Draw(t, "extra"))
		years := rapid.IntRange(0, MaxRetentionYears).Draw(t, "years")
		got := RetentionExpiry(batch, years)
		floor := time.Date(batch.Year()+years, batch.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, -1)
		if batch.Day() <= floor.Day() {
			floor = time.Date(batch.Year()+years, batch.Month(), batch.Day(), 0, 0, 0, 0, time.UTC)
		}
		if got.Before(floor) || got.Sub(floor) > 24*time.Hour {
			t.Fatalf("batch %s + %d years = %s, want %s or the next day", batch.Format(DateLayout), years, got.Format(DateLayout), floor.Format(DateLayout))
		}
	})
}
