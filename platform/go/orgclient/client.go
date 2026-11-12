// Package orgclient reads fiscal calendars from the org service on behalf of a calling service. The request is signed with the
// service identity and carries only org.read.
package orgclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	// Timeout bounds one call unless the caller passes its own HTTP client.
	Timeout = 3 * time.Second
	// DutyRead is the duty org accepts on its read routes.
	DutyRead = "org.read"

	audience        = "org"
	maxResponseSize = 1 << 20
)

// ErrNoCalendar is returned when the legal entity has no fiscal calendar, or the calendar has no period for the date.
var ErrNoCalendar = errors.New("orgclient: the legal entity has no fiscal period for the date")

// Error is any other non-success answer of the org service.
type Error struct{ Status int }

func (e *Error) Error() string { return fmt.Sprintf("orgclient: unexpected status %d", e.Status) }

// Period is one fiscal period of a calendar.
type Period struct {
	FiscalYear int
	PeriodNo   int
	Name       string
	Start      time.Time
	End        time.Time
	Type       string
}

// Resolution is the period a date falls in with the calendar it came from.
type Resolution struct {
	CalendarID string
	Period     Period
}

// Calendars resolves fiscal periods. Client and Fake implement it.
type Calendars interface {
	// Resolve returns the operating period of the legal entity's calendar that contains date.
	Resolve(ctx context.Context, legalEntityID string, date time.Time) (Resolution, error)
	// YearPeriods returns the operating periods of a fiscal year of a calendar, ordered by start date.
	YearPeriods(ctx context.Context, legalEntityID, calendarID string, fiscalYear int) ([]Period, error)
}

// Client calls the org service.
type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

// New returns a client; signer is required.
func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("orgclient: a base URL is required")
	}
	if signer == nil {
		return nil, errors.New("orgclient: a service signing key is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}, nil
}

// FromEnv builds the client from ORG_URL, ORG_TIMEOUT and the service signing key variables; it returns nil, nil when ORG_URL is
// unset and an error when it is set without a signing key.
func FromEnv(getenv func(string) string) (*Client, error) {
	baseURL := getenv("ORG_URL")
	if baseURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("orgclient: ORG_URL needs the service signing key")
	}
	timeout := Timeout
	if v, err := time.ParseDuration(getenv("ORG_TIMEOUT")); err == nil && v > 0 {
		timeout = v
	}
	return New(baseURL, signer, &http.Client{Timeout: timeout})
}

type periodBody struct {
	FiscalYear int    `json:"fiscalYear"`
	PeriodNo   int    `json:"periodNo"`
	Name       string `json:"name"`
	StartDate  string `json:"startDate"`
	EndDate    string `json:"endDate"`
	Type       string `json:"type"`
}

func (b periodBody) period() (Period, error) {
	start, err1 := time.Parse(time.DateOnly, b.StartDate)
	end, err2 := time.Parse(time.DateOnly, b.EndDate)
	if err := errors.Join(err1, err2); err != nil {
		return Period{}, fmt.Errorf("orgclient: unusable period dates: %w", err)
	}
	return Period{FiscalYear: b.FiscalYear, PeriodNo: b.PeriodNo, Name: b.Name, Start: start, End: end, Type: b.Type}, nil
}

func (c *Client) get(ctx context.Context, legalEntityID, path string, q url.Values) ([]byte, int, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, 0, errors.New("orgclient: no principal in context")
	}
	target := c.baseURL + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("orgclient: %w", err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{DutyRead}); err != nil {
		return nil, 0, fmt.Errorf("orgclient: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("orgclient: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, 0, fmt.Errorf("orgclient: %w", err)
	}
	return raw, resp.StatusCode, nil
}

// Resolve implements Calendars.
func (c *Client) Resolve(ctx context.Context, legalEntityID string, date time.Time) (Resolution, error) {
	raw, status, err := c.get(ctx, legalEntityID, "/v1/org/legal-entities/"+url.PathEscape(legalEntityID)+"/fiscal-period", url.Values{"date": {date.Format(time.DateOnly)}})
	if err != nil {
		return Resolution{}, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusUnprocessableEntity:
		return Resolution{}, ErrNoCalendar
	default:
		slog.ErrorContext(ctx, "org refused fiscal period lookup", "status", status, "correlationId", middleware.GetReqID(ctx))
		return Resolution{}, &Error{Status: status}
	}
	var body struct {
		CalendarID string     `json:"calendarId"`
		Period     periodBody `json:"period"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.CalendarID == "" {
		return Resolution{}, errors.New("orgclient: response has no calendar")
	}
	p, err := body.Period.period()
	if err != nil {
		return Resolution{}, err
	}
	return Resolution{CalendarID: body.CalendarID, Period: p}, nil
}

// YearPeriods implements Calendars.
func (c *Client) YearPeriods(ctx context.Context, legalEntityID, calendarID string, fiscalYear int) ([]Period, error) {
	raw, status, err := c.get(ctx, legalEntityID, "/v1/org/fiscal-calendars/"+url.PathEscape(calendarID)+"/periods", url.Values{"fiscalYear": {fmt.Sprint(fiscalYear)}})
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, ErrNoCalendar
	}
	if status != http.StatusOK {
		slog.ErrorContext(ctx, "org refused fiscal period list", "status", status, "correlationId", middleware.GetReqID(ctx))
		return nil, &Error{Status: status}
	}
	var body struct {
		Items []periodBody `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, errors.New("orgclient: response has no periods")
	}
	var out []Period
	for _, it := range body.Items {
		p, err := it.period()
		if err != nil {
			return nil, err
		}
		if p.Type == "operating" {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// Years answers the questions consolidation asks of a calendar and keeps what a calendar year looks like for the length of one
// run, so a group of many units does not read the same year twice.
type Years struct {
	cal   Calendars
	mu    sync.Mutex
	cache map[string][]Period
}

// NewYears wraps a calendar source.
func NewYears(cal Calendars) *Years { return &Years{cal: cal, cache: map[string][]Period{}} }

func (y *Years) year(ctx context.Context, legalEntityID, calendarID string, fiscalYear int) ([]Period, error) {
	key := fmt.Sprintf("%s|%s|%d", legalEntityID, calendarID, fiscalYear)
	y.mu.Lock()
	cached, ok := y.cache[key]
	y.mu.Unlock()
	if ok {
		return cached, nil
	}
	periods, err := y.cal.YearPeriods(ctx, legalEntityID, calendarID, fiscalYear)
	if err != nil {
		return nil, err
	}
	if len(periods) == 0 {
		return nil, ErrNoCalendar
	}
	y.mu.Lock()
	y.cache[key] = periods
	y.mu.Unlock()
	return periods, nil
}

// lastDay is the last day of a YYYY-MM month.
func lastDay(period string) (time.Time, error) {
	first, err := time.Parse("2006-01", period)
	if err != nil {
		return time.Time{}, fmt.Errorf("orgclient: period %q is not YYYY-MM", period)
	}
	return first.AddDate(0, 1, -1), nil
}

// FiscalYearStart returns the first month (YYYY-MM) of the legal entity's fiscal year that the month period (YYYY-MM) ends in.
// ErrNoCalendar is returned when the legal entity has no calendar for it.
func (y *Years) FiscalYearStart(ctx context.Context, legalEntityID, period string) (string, error) {
	end, err := lastDay(period)
	if err != nil {
		return "", err
	}
	res, err := y.cal.Resolve(ctx, legalEntityID, end)
	if err != nil {
		return "", err
	}
	periods, err := y.year(ctx, legalEntityID, res.CalendarID, res.Period.FiscalYear)
	if err != nil {
		return "", err
	}
	return periods[0].Start.Format("2006-01"), nil
}

// PreviousYearEnd returns the last month (YYYY-MM) of the fiscal year before the one period falls in.
func (y *Years) PreviousYearEnd(ctx context.Context, legalEntityID, period string) (string, error) {
	start, err := y.FiscalYearStart(ctx, legalEntityID, period)
	if err != nil {
		return "", err
	}
	first, err := time.Parse("2006-01", start)
	if err != nil {
		return "", err
	}
	return first.AddDate(0, -1, 0).Format("2006-01"), nil
}

// NextPeriodStart returns the first day of the operating period after the one that contains date.
func (y *Years) NextPeriodStart(ctx context.Context, legalEntityID string, date time.Time) (time.Time, error) {
	res, err := y.cal.Resolve(ctx, legalEntityID, date)
	if err != nil {
		return time.Time{}, err
	}
	return res.Period.End.AddDate(0, 0, 1), nil
}

// Fake is an in-memory Calendars for tests: one calendar per legal entity whose year starts in StartMonth and whose periods are
// calendar months.
type Fake struct {
	StartMonth map[string]int
}

func (f Fake) start(le string) (int, bool) { m, ok := f.StartMonth[le]; return m, ok }

// Resolve implements Calendars.
func (f Fake) Resolve(_ context.Context, le string, date time.Time) (Resolution, error) {
	sm, ok := f.start(le)
	if !ok {
		return Resolution{}, ErrNoCalendar
	}
	fy := date.Year()
	if int(date.Month()) >= sm && sm != 1 {
		fy++
	}
	first := time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, time.UTC)
	return Resolution{CalendarID: le, Period: Period{FiscalYear: fy, Start: first, End: first.AddDate(0, 1, -1), Type: "operating"}}, nil
}

// YearPeriods implements Calendars.
func (f Fake) YearPeriods(_ context.Context, le, _ string, fiscalYear int) ([]Period, error) {
	sm, ok := f.start(le)
	if !ok {
		return nil, ErrNoCalendar
	}
	startYear := fiscalYear
	if sm != 1 {
		startYear--
	}
	var out []Period
	for i := 0; i < 12; i++ {
		first := time.Date(startYear, time.Month(sm)+time.Month(i), 1, 0, 0, 0, 0, time.UTC)
		out = append(out, Period{FiscalYear: fiscalYear, PeriodNo: i + 1, Start: first, End: first.AddDate(0, 1, -1), Type: "operating"})
	}
	return out, nil
}
