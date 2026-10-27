package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 15, 30, 0, 0, time.UTC) }

func TestShelfDates(t *testing.T) {
	bb, exp, err := ShelfDates(day(2026, time.January, 31), 30, 90)
	if err != nil {
		t.Fatal(err)
	}
	if bb.Format("2006-01-02") != "2026-03-02" || exp.Format("2006-01-02") != "2026-05-01" {
		t.Fatalf("best-before %s expiry %s", bb.Format("2006-01-02"), exp.Format("2006-01-02"))
	}
	if _, _, err := ShelfDates(day(2026, time.January, 1), -1, 1); err == nil {
		t.Fatal("negative days accepted")
	}
}

func TestShortestExpiry(t *testing.T) {
	own := day(2027, time.June, 1)
	got := ShortestExpiry(own, []time.Time{day(2027, time.March, 1), day(2027, time.July, 1)})
	if got.Format("2006-01-02") != "2027-03-01" {
		t.Fatalf("shortest = %s", got.Format("2006-01-02"))
	}
	if got := ShortestExpiry(own, nil); !got.Equal(civil(own)) {
		t.Fatalf("no senders changed expiry to %s", got)
	}
}

// TestWeightedAverage_BR2411 is BR-24.11: 200 kg at 40% and 300 kg at 45% -> 43.0000%.
func TestWeightedAverage_BR2411(t *testing.T) {
	got, err := WeightedAverage([]decimal.Decimal{dec("40"), dec("45")}, []decimal.Decimal{dec("200"), dec("300")}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got.StringFixed(4) != "43.0000" {
		t.Fatalf("got %s", got.StringFixed(4))
	}
}

func TestWeightedAverage_Rejects(t *testing.T) {
	one := []decimal.Decimal{dec("1")}
	if _, err := WeightedAverage(nil, nil, 4); err == nil {
		t.Fatal("empty input accepted")
	}
	if _, err := WeightedAverage(one, []decimal.Decimal{dec("0")}, 4); err == nil {
		t.Fatal("zero total weight accepted")
	}
	if _, err := WeightedAverage(one, []decimal.Decimal{dec("-1")}, 4); err == nil {
		t.Fatal("negative weight accepted")
	}
}
