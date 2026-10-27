package domain

import "testing"

func TestBucketFor(t *testing.T) {
	asOf := day("2027-03-31")
	tests := []struct {
		due  string
		want Bucket
	}{
		{"2027-04-05", BucketCurrent},
		{"2027-03-31", BucketCurrent},
		{"2027-03-30", Bucket1to30},
		{"2027-03-01", Bucket1to30},
		{"2027-02-28", Bucket31to60},
		{"2027-01-29", Bucket61to90},
		{"2026-12-30", Bucket91to180},
		{"2026-10-05", Bucket91to180},
		{"2026-09-30", BucketOver180},
	}
	for _, tc := range tests {
		if got := BucketFor(asOf, day(tc.due)); got != tc.want {
			t.Errorf("BucketFor(due %s) = %s, want %s", tc.due, got, tc.want)
		}
	}
}

func TestAge_SumsPerCurrencyAndBucket(t *testing.T) {
	asOf := day("2027-03-31")
	rows := []AgingRow{
		{Currency: "USD", DueDate: day("2027-04-10"), OpenAmount: amt("100.00")},
		{Currency: "USD", DueDate: day("2027-03-15"), OpenAmount: amt("40.00")},
		{Currency: "USD", DueDate: day("2027-03-20"), OpenAmount: amt("60.00")},
		{Currency: "EUR", DueDate: day("2027-03-20"), OpenAmount: amt("7.50")},
		{Currency: "EUR", DueDate: day("2027-03-20"), OpenAmount: amt("0")},
	}
	got := Age(asOf, rows)
	want := []AgingTotal{
		{Currency: "EUR", Bucket: Bucket1to30, Amount: amt("7.50")},
		{Currency: "USD", Bucket: BucketCurrent, Amount: amt("100.00")},
		{Currency: "USD", Bucket: Bucket1to30, Amount: amt("100.00")},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d totals, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Currency != want[i].Currency || got[i].Bucket != want[i].Bucket || !got[i].Amount.Equal(want[i].Amount) {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
