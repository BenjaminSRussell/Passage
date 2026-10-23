package vies_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/vies"
)

const recordedPath = "testdata/vies-recorded.json"

type recordedResult struct {
	Country string `json:"country"`
	Number  string `json:"number"`
	Valid   bool   `json:"valid"`
}

func loadRecorded(t *testing.T) []recordedResult {
	t.Helper()
	raw, err := os.ReadFile(recordedPath)
	if err != nil {
		t.Fatal(err)
	}
	var records []recordedResult
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	return records
}

func TestFixtureMatchesRecorded(t *testing.T) {
	fixture, err := vies.Recorded()
	if err != nil {
		t.Fatal(err)
	}
	records := loadRecorded(t)
	if len(records) == 0 {
		t.Fatal("recorded set is empty")
	}
	for _, rec := range records {
		want := domain.StatusInvalid
		if rec.Valid {
			want = domain.StatusValid
		}
		got, err := fixture.Check(context.Background(), rec.Country, rec.Number)
		if err != nil || got != want {
			t.Errorf("%s%s: got %q %v, want %q", rec.Country, rec.Number, got, err, want)
		}
	}
}

func TestRecordedNumbersPassDomainFormatAndValidate(t *testing.T) {
	fixture, err := vies.Recorded()
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range loadRecorded(t) {
		want := domain.StatusInvalid
		if rec.Valid {
			want = domain.StatusValid
		}
		got, err := domain.Validate(context.Background(), fixture, domain.TaxVAT, rec.Country, rec.Country+rec.Number)
		if err != nil || got != want {
			t.Errorf("%s%s: got %q %v, want %q", rec.Country, rec.Number, got, err, want)
		}
	}
}

func TestUnknownNumberIsUnverifiedNotError(t *testing.T) {
	fixture, err := vies.Recorded()
	if err != nil {
		t.Fatal(err)
	}
	got, err := fixture.Check(context.Background(), "DE", "555555555")
	if err != nil || got != domain.StatusUnverified {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestNewFixtureRejectsMalformedJSON(t *testing.T) {
	if _, err := vies.NewFixture(strings.NewReader(`{`)); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestUnavailableReportsUnverified(t *testing.T) {
	status, err := vies.Unavailable{}.Check(context.Background(), "DE", "123456789")
	if err != nil || status != domain.StatusUnverified {
		t.Fatalf("got %s, %v", status, err)
	}
}
