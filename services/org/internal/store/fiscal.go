package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	orgevents "github.com/BenjaminSRussell/passage/services/org/internal/events"
)

const (
	CodeFiscalCalendarNotFound = "ORG-CAL-NOT-FOUND"
	CodeFiscalCalendarCode     = "ORG-CAL-CODE-DUPLICATE"
	CodeFiscalYearLocked       = "ORG-CAL-LOCKED"
	CodeFiscalCalendarInUse    = "ORG-CAL-IN-USE"
	CodeFiscalCalendarInactive = "ORG-CAL-INACTIVE"
	fiscalDateLayout           = "2006-01-02"
	fiscalCalendarColumns      = "id, code, name, pattern, start_month, week_start, opening_closing, special_periods, status, version, created_at, updated_at, array(select distinct fiscal_year::int from fiscal_period p where p.tenant_id = fiscal_calendar.tenant_id and p.calendar_id = fiscal_calendar.id order by 1)"
	fiscalPeriodColumns        = "fiscal_year, period_no, name, start_date, end_date, type, week53"
)

var weekdayNames = []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

func WeekdayNumber(name string) (int, bool) {
	n := slices.Index(weekdayNames, name)
	return n, n >= 0
}

type FiscalCalendar struct {
	ID             string    `json:"id"`
	Code           string    `json:"code"`
	Name           string    `json:"name"`
	Pattern        string    `json:"pattern"`
	StartMonth     int       `json:"startMonth"`
	WeekStart      string    `json:"weekStart"`
	OpeningClosing bool      `json:"openingClosing"`
	SpecialPeriods int       `json:"specialPeriods"`
	Status         string    `json:"status"`
	Version        int       `json:"version"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	GeneratedYears []int     `json:"generatedYears"`
}

type FiscalCalendarFilter struct {
	Status  string
	Pattern string
}

type FiscalCalendarPage struct {
	Items    []FiscalCalendar `json:"items"`
	NextCode string           `json:"-"`
}

type FiscalCalendarPatch struct {
	Name   *string
	Status *string
}

type FiscalPeriod struct {
	FiscalYear int    `json:"fiscalYear"`
	PeriodNo   int    `json:"periodNo"`
	Name       string `json:"name"`
	StartDate  string `json:"startDate"`
	EndDate    string `json:"endDate"`
	Type       string `json:"type"`
	Week53     bool   `json:"week53"`
}

type FiscalYear struct {
	CalendarID string         `json:"calendarId"`
	FiscalYear int            `json:"fiscalYear"`
	Generated  bool           `json:"generated"`
	Periods    []FiscalPeriod `json:"periods"`
}

func fiscalCalendarNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeFiscalCalendarNotFound, "fiscal calendar not found")
}

func scanFiscalCalendar(row pgx.Row, extra ...any) (FiscalCalendar, error) {
	var c FiscalCalendar
	var weekStart int
	var years []int32
	err := row.Scan(append([]any{&c.ID, &c.Code, &c.Name, &c.Pattern, &c.StartMonth, &weekStart, &c.OpeningClosing, &c.SpecialPeriods, &c.Status, &c.Version, &c.CreatedAt, &c.UpdatedAt, &years}, extra...)...)
	if err == nil {
		c.WeekStart = weekdayNames[weekStart]
		c.GeneratedYears = make([]int, len(years))
		for i, y := range years {
			c.GeneratedYears[i] = int(y)
		}
	}
	return c, err
}

func scanFiscalPeriod(row pgx.Row) (FiscalPeriod, error) {
	var p FiscalPeriod
	var start, end time.Time
	err := row.Scan(&p.FiscalYear, &p.PeriodNo, &p.Name, &start, &end, &p.Type, &p.Week53)
	p.StartDate, p.EndDate = start.Format(fiscalDateLayout), end.Format(fiscalDateLayout)
	return p, err
}

func toFiscalPeriod(p domain.FiscalPeriod) FiscalPeriod {
	return FiscalPeriod{FiscalYear: p.FiscalYear, PeriodNo: p.PeriodNo, Name: p.Name, StartDate: p.Start.Format(fiscalDateLayout), EndDate: p.End.Format(fiscalDateLayout), Type: string(p.Type), Week53: p.Week53}
}

func periodData(p FiscalPeriod) orgevents.FiscalPeriodData {
	return orgevents.FiscalPeriodData{PeriodNo: p.PeriodNo, Name: p.Name, StartDate: p.StartDate, EndDate: p.EndDate, Type: p.Type, Week53: p.Week53}
}

func loadFiscalCalendar(ctx context.Context, tx pgx.Tx, tenantID, id string, lock string) (FiscalCalendar, error) {
	c, err := scanFiscalCalendar(tx.QueryRow(ctx, `select `+fiscalCalendarColumns+` from fiscal_calendar where tenant_id = $1 and id = $2`+lock, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, fiscalCalendarNotFound()
	}
	return c, err
}

func readFiscalPeriods(ctx context.Context, tx pgx.Tx, tenantID, id string, year int) ([]FiscalPeriod, error) {
	rows, err := tx.Query(ctx, `select `+fiscalPeriodColumns+` from fiscal_period where tenant_id = $1 and calendar_id = $2 and ($3 = 0 or fiscal_year = $3) order by fiscal_year, period_no`, tenantID, id, year)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	periods := []FiscalPeriod{}
	for rows.Next() {
		p, err := scanFiscalPeriod(rows)
		if err != nil {
			return nil, err
		}
		periods = append(periods, p)
	}
	return periods, rows.Err()
}

func (s *Postgres) CreateFiscalCalendar(ctx context.Context, in FiscalCalendar, idempotencyKey string) (FiscalCalendar, error) {
	p, _ := tenant.FromContext(ctx)
	weekStart, _ := WeekdayNumber(in.WeekStart)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var c FiscalCalendar
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanFiscalCalendar(tx.QueryRow(ctx, `select `+fiscalCalendarColumns+` from fiscal_calendar where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				c = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var inserted bool
		var err error
		c, err = scanFiscalCalendar(tx.QueryRow(ctx, `insert into fiscal_calendar (tenant_id, id, code, name, pattern, start_month, week_start, opening_closing, special_periods, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			on conflict (tenant_id, idempotency_key) do update set version = fiscal_calendar.version
			returning `+fiscalCalendarColumns+`, (xmax = 0)`, p.TenantID, ids.New(), in.Code, in.Name, in.Pattern, in.StartMonth, weekStart, in.OpeningClosing, in.SpecialPeriods, key), &inserted)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return FiscalCalendar{}, httpx.NewProblem(http.StatusConflict, CodeFiscalCalendarCode, "a fiscal calendar with this code already exists")
	}
	if err == nil && key != nil && (c.Code != in.Code || c.Name != in.Name || c.Pattern != in.Pattern || c.StartMonth != in.StartMonth || c.WeekStart != in.WeekStart ||
		c.OpeningClosing != in.OpeningClosing || c.SpecialPeriods != in.SpecialPeriods) {
		return FiscalCalendar{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return c, err
}

func (s *Postgres) GetFiscalCalendar(ctx context.Context, id string) (FiscalCalendar, error) {
	p, _ := tenant.FromContext(ctx)
	var c FiscalCalendar
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		c, err = loadFiscalCalendar(ctx, tx, p.TenantID, id, "")
		return err
	})
	return c, err
}

func (s *Postgres) GenerateFiscalYear(ctx context.Context, id string, year int, custom []domain.DateRange, force bool) (FiscalYear, error) {
	p, _ := tenant.FromContext(ctx)
	out := FiscalYear{CalendarID: id, FiscalYear: year}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := loadFiscalCalendar(ctx, tx, p.TenantID, id, " for update")
		if err != nil {
			return err
		}
		isCustom := c.Pattern == string(domain.PatternCustom)
		if isCustom != (len(custom) > 0) {
			return &domain.Error{Code: domain.CodeCalPattern, Detail: "periods are required for a custom calendar and accepted for no other pattern"}
		}
		weekStart, _ := WeekdayNumber(c.WeekStart)
		generated, err := domain.GenerateYear(domain.CalendarSpec{Pattern: domain.Pattern(c.Pattern), StartMonth: time.Month(c.StartMonth), WeekStart: time.Weekday(weekStart),
			OpeningClosing: c.OpeningClosing, SpecialPeriods: c.SpecialPeriods, Custom: custom}, year)
		if err != nil {
			return err
		}
		var existing int
		if err := tx.QueryRow(ctx, `select count(*) from fiscal_period where tenant_id = $1 and calendar_id = $2 and fiscal_year = $3`, p.TenantID, id, year).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 && !force {
			out.Periods, err = readFiscalPeriods(ctx, tx, p.TenantID, id, year)
			return err
		}
		if c.Status == "inactive" {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeFiscalCalendarInactive, fmt.Sprintf("Fiscal calendar %s is inactive.", c.Code))
		}
		if _, err := tx.Exec(ctx, `delete from fiscal_period where tenant_id = $1 and calendar_id = $2 and fiscal_year = $3`, p.TenantID, id, year); err != nil {
			return err
		}
		out.Generated = true
		out.Periods = make([]FiscalPeriod, len(generated))
		data := make([]orgevents.FiscalPeriodData, len(generated))
		for i, g := range generated {
			out.Periods[i] = toFiscalPeriod(g)
			data[i] = periodData(out.Periods[i])
			if _, err := tx.Exec(ctx, `insert into fiscal_period (tenant_id, calendar_id, fiscal_year, period_no, name, start_date, end_date, type, week53)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, p.TenantID, id, year, g.PeriodNo, g.Name, g.Start, g.End, string(g.Type), g.Week53); err != nil {
				return err
			}
		}
		return enqueue(ctx, tx, "fiscalcalendar", id, orgevents.FiscalCalendarGenerated, p.TenantID,
			orgevents.FiscalCalendarGeneratedData{CalendarID: id, FiscalYear: year, Periods: data})
	})
	return out, err
}

func (s *Postgres) ListFiscalPeriods(ctx context.Context, id string, year int) ([]FiscalPeriod, error) {
	p, _ := tenant.FromContext(ctx)
	var periods []FiscalPeriod
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadFiscalCalendar(ctx, tx, p.TenantID, id, ""); err != nil {
			return err
		}
		var err error
		periods, err = readFiscalPeriods(ctx, tx, p.TenantID, id, year)
		return err
	})
	return periods, err
}

func (s *Postgres) ListFiscalCalendars(ctx context.Context, f FiscalCalendarFilter, afterCode string, limit int) (FiscalCalendarPage, error) {
	p, _ := tenant.FromContext(ctx)
	page := FiscalCalendarPage{Items: []FiscalCalendar{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+fiscalCalendarColumns+` from fiscal_calendar where tenant_id = $1 and code > $2
			and ($3::text = '' or status = $3) and ($4::text = '' or pattern = $4) order by code limit $5`, p.TenantID, afterCode, f.Status, f.Pattern, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanFiscalCalendar(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, c)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCode = page.Items[limit-1].Code
	}
	return page, err
}

func (s *Postgres) UpdateFiscalCalendar(ctx context.Context, id string, version int, in FiscalCalendarPatch) (FiscalCalendar, error) {
	p, _ := tenant.FromContext(ctx)
	var c FiscalCalendar
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := loadFiscalCalendar(ctx, tx, p.TenantID, id, " for update")
		if err != nil {
			return err
		}
		c = current
		if current.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, fmt.Sprintf("current version is %d", current.Version))
		}
		next := current
		if in.Name != nil {
			next.Name = *in.Name
		}
		if in.Status != nil {
			next.Status = *in.Status
		}
		if next.Name == current.Name && next.Status == current.Status {
			return nil
		}
		if next.Status == "inactive" && current.Status != "inactive" {
			var assigned int
			if err := tx.QueryRow(ctx, `select count(*) from legal_entity where tenant_id = $1 and fiscal_calendar_id = $2 and status = 'active'`, p.TenantID, id).Scan(&assigned); err != nil {
				return err
			}
			if assigned > 0 {
				return httpx.NewProblem(http.StatusConflict, CodeFiscalCalendarInUse, fmt.Sprintf("Fiscal calendar %s is assigned to %d active legal entities.", current.Code, assigned))
			}
		}
		c, err = scanFiscalCalendar(tx.QueryRow(ctx, `update fiscal_calendar set name = $3, status = $4, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+fiscalCalendarColumns, p.TenantID, id, next.Name, next.Status))
		return err
	})
	if err != nil {
		return FiscalCalendar{}, err
	}
	return c, nil
}
