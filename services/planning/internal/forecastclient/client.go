// Package forecastclient calls the statistical forecast service
// (services/forecast-py, POST /v1/forecast/forecasts:generate, FR-22.9).
package forecastclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// Timeout bounds one call of up to 1,000 series; a forecast service that
	// has not answered by then is unavailable (502 to the planner).
	Timeout = 60 * time.Second
	// maxResponseBytes bounds what a misbehaving forecast service can make us
	// read: 1,000 series of 24 periods with their errors fit in about 1 MiB.
	maxResponseBytes = 8 << 20
	generatePath     = "/v1/forecast/forecasts:generate"
)

// Series is one history to forecast; History holds one value per calendar month,
// oldest first.
type Series struct {
	ItemID         string    `json:"itemId"`
	SiteID         string    `json:"siteId"`
	History        []float64 `json:"history"`
	Model          string    `json:"model"`
	HorizonPeriods int       `json:"horizonPeriods"`
	SeasonLength   int       `json:"seasonLength,omitempty"`
	AutoSeason     bool      `json:"autoSeason"`
	HistoryMonths  int       `json:"historyMonths,omitempty"`
}

// Result is the forecast of one series.
type Result struct {
	ItemID       string    `json:"itemId"`
	SiteID       string    `json:"siteId"`
	Model        string    `json:"model"`
	ShortHistory bool      `json:"shortHistory"`
	SeasonLength int       `json:"seasonLength"`
	Forecast     []float64 `json:"forecast"`
	MAPE         *float64  `json:"mape"`
	WMAPE        *float64  `json:"wmape"`
	Warnings     []string  `json:"warnings"`
}

// ErrUnavailable is returned when the forecast service cannot be reached or
// answers anything but a forecast for every series.
var ErrUnavailable = errors.New("forecast service unavailable")

// Client is an HTTP client for forecast-py.
type Client struct {
	base string
	http *http.Client
}

// New returns a client for the service at baseURL, for example
// http://forecast:8080. The URL comes from configuration only (FORECAST_URL);
// it must be an absolute http or https URL. forecast-py is a stateless compute
// service inside the cluster: like scheduler-opt and the TMS optimizer it takes
// no service token, and the call carries no tenant, only the series.
func New(baseURL string) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("forecast service URL %q must be an absolute http or https URL", baseURL)
	}
	return &Client{base: base, http: &http.Client{Timeout: Timeout}}, nil
}

// Generate forecasts the series and returns one result per series, in order.
func (c *Client) Generate(ctx context.Context, series []Series) ([]Result, error) {
	body, err := json.Marshal(map[string]any{"series": series})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+generatePath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	var out struct {
		Results []Result `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if len(out.Results) != len(series) {
		return nil, fmt.Errorf("%w: %d results for %d series", ErrUnavailable, len(out.Results), len(series))
	}
	for i, r := range out.Results {
		if r.ItemID != series[i].ItemID || r.SiteID != series[i].SiteID {
			return nil, fmt.Errorf("%w: result %d is for another item or site than its series", ErrUnavailable, i)
		}
		if len(r.Forecast) != series[i].HorizonPeriods {
			return nil, fmt.Errorf("%w: series %d has %d periods, want %d", ErrUnavailable, i, len(r.Forecast), series[i].HorizonPeriods)
		}
	}
	return out.Results, nil
}
