// Package sources reads the drivers a budget plan is generated from (FR-38.14): the depreciation schedules of the fixed assets, the
// positions of HR, the forecast models of projects and the demand forecast of planning. Each read goes through the owning service's
// API under budgeting's service identity, for the plan's legal entity, and answers per-month amounts in the plan's currency; the
// plan generation maps them to accounts and cost centers.
package sources

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
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	// Timeout bounds one call unless the caller passes its own HTTP client.
	Timeout = 5 * time.Second
	// MaxAssets bounds how many assets one generation reads a schedule for: each asset is one more call.
	MaxAssets = 300
	// MaxPositions bounds the positions one generation reads.
	MaxPositions = 5000

	maxResponseSize = 4 << 20
	pageSize        = 200

	dutyAssets   = "assets.asset.view"
	dutyHR       = "hr.org.inquire"
	dutyProjects = "projects.inquire"
	dutyPlanning = "planning.read"
)

// ErrNotConfigured is returned for a source whose service URL is not set.
var ErrNotConfigured = errors.New("sources: the service this source reads is not configured")

// Error is a source that could not be read; Detail says why, in words a planner can act on.
type Error struct{ Detail string }

func (e *Error) Error() string { return e.Detail }

// Months is the amounts of one fiscal year, indexed by period (YYYY-MM).
type Months map[string]decimal.Decimal

// Add adds an amount to a period.
func (m Months) Add(period string, v decimal.Decimal) { m[period] = m[period].Add(v) }

// Periods returns the periods that hold an amount, in order.
func (m Months) Periods() []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Reader reads the sources.
type Reader struct {
	Assets, HR, Projects, Planning string
	http                           *http.Client
	signer                         *tenant.ServiceSigner
}

// New returns a reader; signer is required.
func New(assets, hr, projects, planning string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Reader, error) {
	if signer == nil {
		return nil, errors.New("sources: a service signing key is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	trim := func(s string) string { return strings.TrimRight(s, "/") }
	return &Reader{Assets: trim(assets), HR: trim(hr), Projects: trim(projects), Planning: trim(planning), http: &noRedirect, signer: signer}, nil
}

// FromEnv builds the reader from ASSETS_URL, HR_URL, PROJECTS_URL, PLANNING_URL and SOURCES_TIMEOUT and the service signing key
// variables; nil, nil when none of the URLs is set.
func FromEnv(getenv func(string) string) (*Reader, error) {
	a, h, p, pl := getenv("ASSETS_URL"), getenv("HR_URL"), getenv("PROJECTS_URL"), getenv("PLANNING_URL")
	if a == "" && h == "" && p == "" && pl == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("sources: ASSETS_URL, HR_URL, PROJECTS_URL and PLANNING_URL need the service signing key")
	}
	timeout := Timeout
	if v, err := time.ParseDuration(getenv("SOURCES_TIMEOUT")); err == nil && v > 0 {
		timeout = v
	}
	return New(a, h, p, pl, signer, &http.Client{Timeout: timeout})
}

func (r *Reader) get(ctx context.Context, base, audience, duty, legalEntityID, path string, q url.Values, out any) error {
	if base == "" {
		return ErrNotConfigured
	}
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("sources: no principal in context")
	}
	target := base + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("sources: %w", err)
	}
	if _, err := r.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{duty}); err != nil {
		return fmt.Errorf("sources: %w", err)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "plan source unreachable", "audience", audience, "path", path, "err", err, "correlationId", middleware.GetReqID(ctx))
		return &Error{Detail: fmt.Sprintf("the %s service did not answer", audience)}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return &Error{Detail: fmt.Sprintf("the %s service answer could not be read", audience)}
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return &Error{Detail: fmt.Sprintf("the %s service has nothing at %s", audience, path)}
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		slog.ErrorContext(ctx, "plan source refused", "audience", audience, "path", path, "status", resp.StatusCode, "correlationId", middleware.GetReqID(ctx))
		return &Error{Detail: fmt.Sprintf("the %s service refused the read (status %d)", audience, resp.StatusCode)}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &Error{Detail: fmt.Sprintf("the %s service answered something unreadable", audience)}
	}
	return nil
}

func dec(raw, what string) (decimal.Decimal, error) {
	v, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, &Error{Detail: "a " + what + " is not a number: " + raw}
	}
	return v, nil
}

func inYear(period string, year int) bool {
	return strings.HasPrefix(period, fmt.Sprintf("%04d-", year))
}

// Depreciation is the planned depreciation of the legal entity's open assets for the fiscal year (calendar year), month by month,
// read from each asset's projected schedule of the book bookCode (the first open book of an asset when empty). A book in another
// currency than the plan's is refused, and so is a register larger than MaxAssets.
func (r *Reader) Depreciation(ctx context.Context, legalEntityID string, year int, currency, bookCode string) (Months, int, error) {
	out := Months{}
	var ids []string
	after := ""
	for {
		q := url.Values{"status": {"Open"}, "legalEntityId": {legalEntityID}, "limit": {fmt.Sprint(pageSize)}}
		if after != "" {
			q.Set("after", after)
		}
		var page struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
			NextCursor string `json:"nextCursor"`
		}
		if err := r.get(ctx, r.Assets, "assets", dutyAssets, legalEntityID, "/v1/assets", q, &page); err != nil {
			return nil, 0, err
		}
		for _, a := range page.Items {
			ids = append(ids, a.ID)
		}
		if len(ids) > MaxAssets {
			return nil, 0, &Error{Detail: fmt.Sprintf("the register holds more than %d open assets; generate depreciation from the ledger actuals instead", MaxAssets)}
		}
		if page.NextCursor == "" || len(page.Items) == 0 {
			break
		}
		after = page.NextCursor
	}
	used := 0
	for _, id := range ids {
		var detail struct {
			Books []struct {
				ID       string `json:"id"`
				BookCode string `json:"bookCode"`
				Currency string `json:"currency"`
				Status   string `json:"status"`
			} `json:"books"`
		}
		if err := r.get(ctx, r.Assets, "assets", dutyAssets, legalEntityID, "/v1/assets/"+url.PathEscape(id), nil, &detail); err != nil {
			return nil, 0, err
		}
		bookID := ""
		for _, b := range detail.Books {
			if b.Status == "Open" && (bookCode == "" || b.BookCode == bookCode) {
				if b.Currency != currency {
					return nil, 0, &Error{Detail: fmt.Sprintf("book %s of an asset is in %s and the plan in %s", b.BookCode, b.Currency, currency)}
				}
				bookID = b.ID
				break
			}
		}
		if bookID == "" {
			continue
		}
		var sched struct {
			Lines []struct {
				Period string `json:"period"`
				Amount string `json:"amount"`
			} `json:"lines"`
		}
		if err := r.get(ctx, r.Assets, "assets", dutyAssets, legalEntityID, "/v1/assets/"+url.PathEscape(id)+"/schedule", url.Values{"book": {bookID}}, &sched); err != nil {
			return nil, 0, err
		}
		used++
		for _, l := range sched.Lines {
			v, err := dec(l.Amount, "depreciation amount")
			if err != nil {
				return nil, 0, err
			}
			switch {
			case len(l.Period) == 7 && inYear(l.Period, year):
				out.Add(l.Period, v)
			case len(l.Period) == 4 && l.Period == fmt.Sprint(year): // an annual line is shared evenly over the year's months
				for m := 1; m <= 12; m++ {
					out.Add(fmt.Sprintf("%04d-%02d", year, m), v.Div(decimal.NewFromInt(12)))
				}
			}
		}
	}
	return out, used, nil
}

// PositionRate is the monthly cost of one full-time position of a job.
type PositionRate struct {
	JobCode     string
	MonthlyCost decimal.Decimal
}

// PositionCost is one department's planned payroll by month.
type PositionCost struct {
	DepartmentID string
	Months       Months
	Positions    int
}

// Positions plans the payroll of the legal entity's positions for the fiscal year: each position costs its FTE times the monthly cost
// of its job code, in every month its validity overlaps. A position whose job code has no rate is an error that names the job.
func (r *Reader) Positions(ctx context.Context, legalEntityID string, year int, rates []PositionRate) ([]PositionCost, error) {
	byJob := map[string]decimal.Decimal{}
	for _, rt := range rates {
		byJob[rt.JobCode] = rt.MonthlyCost
	}
	byDept := map[string]*PositionCost{}
	cursor := ""
	seen := 0
	for {
		q := url.Values{"limit": {fmt.Sprint(pageSize)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			Items []struct {
				JobCode       string `json:"jobCode"`
				DepartmentID  string `json:"departmentId"`
				LegalEntityID string `json:"legalEntityId"`
				FTE           string `json:"fte"`
				ValidFrom     string `json:"validFrom"`
				ValidTo       string `json:"validTo"`
			} `json:"items"`
			NextCursor string `json:"nextCursor"`
		}
		if err := r.get(ctx, r.HR, "hr", dutyHR, legalEntityID, "/v1/hr/positions", q, &page); err != nil {
			return nil, err
		}
		for _, p := range page.Items {
			seen++
			if p.LegalEntityID != legalEntityID {
				continue
			}
			from, to, err := validity(p.ValidFrom, p.ValidTo)
			if err != nil {
				return nil, err
			}
			rate, ok := byJob[p.JobCode]
			if !ok {
				return nil, &Error{Detail: "no monthly cost is given for job " + p.JobCode}
			}
			fte, err := dec(p.FTE, "FTE")
			if err != nil {
				return nil, err
			}
			contributed := false
			for m := 1; m <= 12; m++ {
				start := time.Date(year, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
				end := start.AddDate(0, 1, 0)
				if !from.Before(end) || (!to.IsZero() && !to.After(start)) {
					continue
				}
				pc := byDept[p.DepartmentID]
				if pc == nil {
					pc = &PositionCost{DepartmentID: p.DepartmentID, Months: Months{}}
					byDept[p.DepartmentID] = pc
				}
				pc.Months.Add(fmt.Sprintf("%04d-%02d", year, m), fte.Mul(rate))
				contributed = true
			}
			if contributed {
				byDept[p.DepartmentID].Positions++
			}
		}
		if seen > MaxPositions {
			return nil, &Error{Detail: fmt.Sprintf("HR holds more than %d positions", MaxPositions)}
		}
		if page.NextCursor == "" || len(page.Items) == 0 {
			break
		}
		cursor = page.NextCursor
	}
	out := make([]PositionCost, 0, len(byDept))
	for _, pc := range byDept {
		out = append(out, *pc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DepartmentID < out[j].DepartmentID })
	return out, nil
}

func validity(from, to string) (time.Time, time.Time, error) {
	f, err := time.Parse(time.RFC3339, from)
	if err != nil {
		return time.Time{}, time.Time{}, &Error{Detail: "a position has an unreadable start date " + from}
	}
	if to == "" {
		return f, time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, to)
	if err != nil {
		return time.Time{}, time.Time{}, &Error{Detail: "a position has an unreadable end date " + to}
	}
	return f, t, nil
}

// ProjectForecast reads the months of one project forecast model that fall in the fiscal year: its cost amounts, or its sales amounts
// when sales is true. The model's currency must be the plan's.
func (r *Reader) ProjectForecast(ctx context.Context, legalEntityID, projectID, modelID string, year int, currency string, sales bool) (Months, error) {
	var model struct {
		Currency string `json:"currency"`
		Lines    []struct {
			Period      string `json:"period"`
			CostAmount  string `json:"costAmount"`
			SalesAmount string `json:"salesAmount"`
		} `json:"lines"`
	}
	if err := r.get(ctx, r.Projects, "projects", dutyProjects, legalEntityID, "/v1/projects/"+url.PathEscape(projectID)+"/forecasts/"+url.PathEscape(modelID), nil, &model); err != nil {
		return nil, err
	}
	if model.Currency != currency {
		return nil, &Error{Detail: fmt.Sprintf("the forecast of a project is in %s and the plan in %s", model.Currency, currency)}
	}
	out := Months{}
	for _, l := range model.Lines {
		if !inYear(l.Period, year) {
			continue
		}
		raw := l.CostAmount
		if sales {
			raw = l.SalesAmount
		}
		v, err := dec(raw, "forecast amount")
		if err != nil {
			return nil, err
		}
		out.Add(l.Period, v)
	}
	return out, nil
}

// ItemPrice is the price one unit of an item is planned at.
type ItemPrice struct {
	ItemID string
	Price  decimal.Decimal
}

// Demand values the demand forecast model of planning: each priced item's forecast quantity of a period (by the month the period
// starts in) times its price, summed per month.
func (r *Reader) Demand(ctx context.Context, legalEntityID, modelCode string, year int, prices []ItemPrice) (Months, error) {
	out := Months{}
	for _, ip := range prices {
		var res struct {
			Items []struct {
				PeriodStart string `json:"periodStart"`
				Qty         string `json:"qty"`
			} `json:"items"`
		}
		q := url.Values{"itemId": {ip.ItemID}, "limit": {"500"}}
		if err := r.get(ctx, r.Planning, "planning", dutyPlanning, legalEntityID, "/v1/planning/forecasts/"+url.PathEscape(modelCode)+"/lines", q, &res); err != nil {
			return nil, err
		}
		if len(res.Items) >= 500 {
			return nil, &Error{Detail: "the forecast of one item holds 500 cells or more; use a shorter horizon"}
		}
		for _, l := range res.Items {
			if len(l.PeriodStart) < 7 || !inYear(l.PeriodStart[:7], year) {
				continue
			}
			qty, err := dec(l.Qty, "forecast quantity")
			if err != nil {
				return nil, err
			}
			out.Add(l.PeriodStart[:7], qty.Mul(ip.Price))
		}
	}
	return out, nil
}
