package provider

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"time"
)

type ECB struct {
	client  *http.Client
	baseURL string
}

func NewECB(client *http.Client, baseURL string) *ECB { return &ECB{client: client, baseURL: baseURL} }

func (*ECB) Name() string { return "ecb" }

func (*ECB) SupportsHistory() bool { return false }

type ecbDocument struct {
	Days []struct {
		Time  string `xml:"time,attr"`
		Cubes []struct {
			Currency string `xml:"currency,attr"`
			Rate     string `xml:"rate,attr"`
		} `xml:"Cube"`
	} `xml:"Cube>Cube"`
}

func (e *ECB) Fetch(ctx context.Context, _ string, _ time.Time) ([]RatePoint, error) {
	body, err := fetchBody(ctx, e.client, e.Name(), e.baseURL)
	if err != nil {
		return nil, err
	}
	var doc ecbDocument
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, malformed("ecb xml: %v", err)
	}
	if len(doc.Days) != 1 {
		return nil, malformed("ecb xml has %d dated cubes, want 1", len(doc.Days))
	}
	day := doc.Days[0]
	validFrom, err := time.Parse(time.DateOnly, day.Time)
	if err != nil {
		return nil, malformed("ecb cube time %q", day.Time)
	}
	if len(day.Cubes) == 0 {
		return nil, malformed("ecb xml has no rates")
	}
	points := make([]RatePoint, 0, len(day.Cubes))
	for _, c := range day.Cubes {
		if !validCurrency(c.Currency) {
			return nil, malformed("ecb currency %q", c.Currency)
		}
		rate, err := parseRate(c.Rate, false)
		if err != nil {
			return nil, malformed("ecb currency %s: %v", c.Currency, err)
		}
		points = append(points, RatePoint{
			From: "EUR", To: c.Currency, ValidFrom: validFrom, Rate: rate,
			PayloadHash: hashOf(fmt.Sprintf(`<Cube currency=%q rate=%q/>`, c.Currency, c.Rate)),
		})
	}
	return points, nil
}
