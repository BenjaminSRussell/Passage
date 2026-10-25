package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const oxrBase = "USD"

type OXR struct {
	client  *http.Client
	baseURL string
	appID   string
}

func NewOXR(client *http.Client, baseURL, appID string) *OXR {
	return &OXR{client: client, baseURL: strings.TrimRight(baseURL, "/"), appID: appID}
}

func (*OXR) Name() string { return "openexchangerates" }

func (o *OXR) Enabled() bool { return o.appID != "" }

type oxrDocument struct {
	Timestamp *int64                     `json:"timestamp"`
	Base      string                     `json:"base"`
	Rates     map[string]json.RawMessage `json:"rates"`
}

func (o *OXR) Fetch(ctx context.Context, _ string, date time.Time) ([]RatePoint, error) {
	if !o.Enabled() {
		return nil, ErrDisabled
	}
	path := "/latest.json"
	if !date.IsZero() {
		path = "/historical/" + date.UTC().Format(time.DateOnly) + ".json"
	}
	body, err := fetchBody(ctx, o.client, o.Name(), o.baseURL+path+"?"+url.Values{"app_id": {o.appID}}.Encode())
	if err != nil {
		return nil, err
	}
	var doc oxrDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, malformed("oxr json: %v", err)
	}
	if doc.Timestamp == nil {
		return nil, malformed("oxr payload has no timestamp")
	}
	if doc.Base != "" && doc.Base != oxrBase {
		return nil, malformed("oxr base %q, want %s", doc.Base, oxrBase)
	}
	if len(doc.Rates) == 0 {
		return nil, malformed("oxr payload has no rates")
	}
	stamp := time.Unix(*doc.Timestamp, 0).UTC()
	validFrom := time.Date(stamp.Year(), stamp.Month(), stamp.Day(), 0, 0, 0, 0, time.UTC)
	codes := make([]string, 0, len(doc.Rates))
	for code := range doc.Rates {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	points := make([]RatePoint, 0, len(codes))
	for _, code := range codes {
		if !validCurrency(code) {
			return nil, malformed("oxr currency %q", code)
		}
		if code == oxrBase {
			continue
		}
		raw := string(doc.Rates[code])
		rate, err := parseRate(raw, false)
		if err != nil {
			return nil, malformed("oxr currency %s: %v", code, err)
		}
		at := stamp
		points = append(points, RatePoint{
			From: oxrBase, To: code, ValidFrom: validFrom, Rate: rate, ProviderTimestamp: &at,
			PayloadHash: hashOf(`"` + code + `":` + raw),
		})
	}
	return points, nil
}
