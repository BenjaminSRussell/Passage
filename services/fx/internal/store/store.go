package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fx/internal/domain"
)

const (
	CodeConflict    = "FX_RATE_CONFLICT"
	RatePublished   = "dev.passage.fx.rate.published.v1"
	uniqueViolation = "23505"
	maxDecimals     = 10
)

var priorities = map[string]int{"ecb": 10, "openexchangerates": 20, "csv": 30, "manual": 40, "migration": 40}

type RateType struct {
	ID           string `json:"id"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	AllowInverse bool   `json:"allowInverse"`
	Provider     string `json:"provider,omitempty"`
}

type RateInput struct {
	RateType          string
	From              string
	To                string
	ValidFrom         time.Time
	Rate              decimal.Decimal
	Source            string
	ProviderTimestamp *time.Time
	PayloadHash       []byte
	DeviationPct      string
}

type Posted struct {
	ID         string
	Version    int
	Superseded bool
	Unchanged  bool
}

type HistoryEntry struct {
	ID                string
	Version           int
	ValidFrom         time.Time
	Rate              decimal.Decimal
	Source            string
	ProviderTimestamp *time.Time
	PayloadHash       string
	SupersededBy      string
	CreatedAt         time.Time
	CreatedBy         string
}

type Store interface {
	PutRateType(ctx context.Context, in RateType) (RateType, error)
	GetRateType(ctx context.Context, code string) (RateType, error)
	ListRateTypes(ctx context.Context) ([]RateType, error)
	PostRates(ctx context.Context, in []RateInput) ([]Posted, error)
	CurrentBook(ctx context.Context, asOf time.Time) (*domain.Book, error)
	Previous(ctx context.Context, rateType, from, to string, date time.Time) (domain.Rate, bool, error)
	History(ctx context.Context, rateType, from, to string) ([]HistoryEntry, error)
	Ping(ctx context.Context) error
	StartRun(ctx context.Context, provider, rateType string, runDate time.Time) (Run, bool, error)
	RecordAttempt(ctx context.Context, runID string, attempts int) error
	FinishRunSucceeded(ctx context.Context, runID string, imported, unchanged int) error
	FinishRunFailed(ctx context.Context, runID string, attempts int, cause error, retryable bool) error
	FinishRunDegraded(ctx context.Context, runID string, attempts int, cause error, retryable bool) error
	FinishRunAborted(ctx context.Context, runID string, attempts int, cause error) error
	ListRuns(ctx context.Context, provider string) ([]Run, error)
	LastSuccess(ctx context.Context, provider, rateType string) (*time.Time, error)
	PublishIdempotent(ctx context.Context, key, requestHash string, in []RateInput, render func([]Posted) (json.RawMessage, error)) (rec IdempotencyRecord, replayed bool, err error)
}

type Postgres struct {
	pool   *pgxpool.Pool
	now    func() time.Time
	owners sync.Map
}

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool, now: time.Now} }

func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func Priority(source string) (int, bool) {
	p, ok := priorities[source]
	return p, ok
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", domain.ErrInvalidRate, fmt.Sprintf(format, args...))
}

func validate(in RateInput, now time.Time) error {
	if _, ok := Priority(in.Source); !ok {
		return invalid("unknown source %q", in.Source)
	}
	if in.PayloadHash != nil && len(in.PayloadHash) != 32 {
		return invalid("payload hash must be 32 bytes, got %d", len(in.PayloadHash))
	}
	if in.ValidFrom.IsZero() {
		return invalid("valid_from is required")
	}
	if in.Rate.Exponent() < -maxDecimals {
		return invalid("rate has more than %d decimals", maxDecimals)
	}
	return domain.NewBook(now, domain.RateType{Code: in.RateType}).Add(domain.Rate{
		ID: "validate", Type: in.RateType, From: in.From, To: in.To, ValidFrom: in.ValidFrom, Value: in.Rate,
	})
}

func DeviationPct(previous, next decimal.Decimal) string {
	if previous.Sign() <= 0 {
		return ""
	}
	return next.Sub(previous).Abs().Div(previous).Mul(decimal.NewFromInt(100)).Round(2).StringFixed(2)
}

func conflict(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return httpx.NewProblem(http.StatusConflict, CodeConflict, "a concurrent write already created this rate version")
	}
	return err
}

func scanType(row pgx.Row) (RateType, error) {
	var t RateType
	var provider *string
	err := row.Scan(&t.ID, &t.Code, &t.Name, &t.AllowInverse, &provider)
	if provider != nil {
		t.Provider = *provider
	}
	return t, err
}

const typeColumns = `id, code, name, allow_inverse, provider`

func (s *Postgres) PutRateType(ctx context.Context, in RateType) (RateType, error) {
	if in.Code == "" || in.Name == "" {
		return RateType{}, invalid("rate type code and name are required")
	}
	p, _ := tenant.FromContext(ctx)
	var out RateType
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanType(tx.QueryRow(ctx, `insert into exchange_rate_type (tenant_id, id, code, name, allow_inverse, provider, created_by)
			values ($1,$2,$3,$4,$5,nullif($6,''),$7)
			on conflict (tenant_id, code) do update set name = excluded.name, allow_inverse = excluded.allow_inverse, provider = excluded.provider
			returning `+typeColumns, p.TenantID, ids.New(), in.Code, in.Name, in.AllowInverse, in.Provider, p.UserID))
		return err
	})
	return out, err
}

func (s *Postgres) GetRateType(ctx context.Context, code string) (RateType, error) {
	p, _ := tenant.FromContext(ctx)
	var out RateType
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanType(tx.QueryRow(ctx, `select `+typeColumns+` from exchange_rate_type where tenant_id=$1 and code=$2`, p.TenantID, code))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return RateType{}, httpx.NewProblem(http.StatusNotFound, "FX_RATE_TYPE_UNKNOWN", "rate type not found")
	}
	return out, err
}

func (s *Postgres) ListRateTypes(ctx context.Context) ([]RateType, error) {
	p, _ := tenant.FromContext(ctx)
	out := []RateType{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+typeColumns+` from exchange_rate_type where tenant_id=$1 order by code`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanType(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

type keyRow struct {
	id      string
	version int
	source  string
	current bool
	rate    decimal.Decimal
}

func (s *Postgres) validateAll(in []RateInput) error {
	if len(in) == 0 {
		return invalid("at least one rate is required")
	}
	now := s.now()
	for i, r := range in {
		if err := validate(r, now); err != nil {
			return fmt.Errorf("item %d: %w", i, err)
		}
	}
	return nil
}

func (s *Postgres) PostRates(ctx context.Context, in []RateInput) ([]Posted, error) {
	if err := s.validateAll(in); err != nil {
		return nil, err
	}
	var posted []Posted
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		posted, err = postRatesTx(ctx, tx, in)
		return err
	})
	if err != nil {
		return nil, conflict(err)
	}
	return posted, nil
}

func postRatesTx(ctx context.Context, tx pgx.Tx, in []RateInput) ([]Posted, error) {
	p, _ := tenant.FromContext(ctx)
	posted := make([]Posted, 0, len(in))
	typeIDs := map[string]string{}
	for i, r := range in {
		typeID, ok := typeIDs[r.RateType]
		if !ok {
			err := tx.QueryRow(ctx, `select id from exchange_rate_type where tenant_id=$1 and code=$2`, p.TenantID, r.RateType).Scan(&typeID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("item %d: %w", i, invalid("unknown rate type %q", r.RateType))
			}
			if err != nil {
				return nil, err
			}
			typeIDs[r.RateType] = typeID
		}
		out, err := postOne(ctx, tx, p, typeID, r)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		posted = append(posted, out)
	}
	return posted, nil
}

func postOne(ctx context.Context, tx pgx.Tx, p tenant.Principal, typeID string, r RateInput) (Posted, error) {
	v := r.ValidFrom.UTC()
	date := time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, time.UTC)
	rows, err := tx.Query(ctx, `select id, version, source, superseded_by is null, rate::text from exchange_rate
		where tenant_id=$1 and rate_type_id=$2 and from_currency=$3 and to_currency=$4 and valid_from=$5
		order by version for update`, p.TenantID, typeID, r.From, r.To, date)
	if err != nil {
		return Posted{}, err
	}
	existing, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (keyRow, error) {
		var k keyRow
		var rate string
		if err := row.Scan(&k.id, &k.version, &k.source, &k.current, &rate); err != nil {
			return k, err
		}
		var err error
		k.rate, err = decimal.NewFromString(rate)
		return k, err
	})
	if err != nil {
		return Posted{}, err
	}
	out := Posted{ID: ids.New(), Version: 1}
	var current *keyRow
	for i := range existing {
		out.Version = existing[i].version + 1
		if existing[i].current {
			current = &existing[i]
		}
	}
	if current != nil && current.source == r.Source && current.rate.Equal(r.Rate) {
		return Posted{ID: current.id, Version: current.version, Unchanged: true}, nil
	}
	previous, hasPrevious, err := previousRate(ctx, tx, p.TenantID, typeID, r, date)
	if err != nil {
		return Posted{}, err
	}
	newPriority, _ := Priority(r.Source)
	supersededBy := (*string)(nil)
	if current != nil {
		if currentPriority, _ := Priority(current.source); currentPriority > newPriority {
			supersededBy = &current.id
			out.Superseded = true
		}
	}
	if _, err := tx.Exec(ctx, `insert into exchange_rate
		(tenant_id, id, rate_type_id, from_currency, to_currency, valid_from, version, rate, source, provider_timestamp, payload_hash, superseded_by, created_by)
		values ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9,$10,$11,$12,$13)`,
		p.TenantID, out.ID, typeID, r.From, r.To, date, out.Version, r.Rate.String(), r.Source, r.ProviderTimestamp, r.PayloadHash, supersededBy, p.UserID); err != nil {
		return Posted{}, err
	}
	if current != nil && supersededBy == nil {
		if _, err := tx.Exec(ctx, `update exchange_rate set superseded_by=$3 where tenant_id=$1 and id=$2 and superseded_by is null`, p.TenantID, current.id, out.ID); err != nil {
			return Posted{}, err
		}
	}
	data := map[string]any{
		"rate_type": r.RateType, "from_currency": r.From, "to_currency": r.To,
		"valid_from": date.Format(time.DateOnly), "rate": r.Rate.String(), "source": r.Source,
	}
	data["previous_rate"] = nil
	if hasPrevious {
		data["previous_rate"] = previous.String()
	}
	deviation := r.DeviationPct
	if deviation == "" && hasPrevious {
		deviation = DeviationPct(previous, r.Rate)
	}
	if deviation != "" {
		data["deviation_pct"] = deviation
	}
	evt, err := events.New(RatePublished, "passage/fx", out.ID, p.TenantID, data)
	if err != nil {
		return Posted{}, err
	}
	return out, outbox.Enqueue(ctx, tx, "exchange_rate", out.ID, evt)
}

func previousRate(ctx context.Context, tx pgx.Tx, tenantID, typeID string, r RateInput, date time.Time) (decimal.Decimal, bool, error) {
	var value string
	err := tx.QueryRow(ctx, `select rate::text from exchange_rate
		where tenant_id=$1 and rate_type_id=$2 and from_currency=$3 and to_currency=$4 and superseded_by is null and valid_from <= $5
		order by valid_from desc, version desc limit 1`, tenantID, typeID, r.From, r.To, date).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return decimal.Decimal{}, false, nil
	}
	if err != nil {
		return decimal.Decimal{}, false, err
	}
	previous, err := decimal.NewFromString(value)
	return previous, err == nil, err
}

func (s *Postgres) CurrentBook(ctx context.Context, asOf time.Time) (*domain.Book, error) {
	p, _ := tenant.FromContext(ctx)
	var book *domain.Book
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		types := map[string]string{}
		var defs []domain.RateType
		typeRows, err := tx.Query(ctx, `select id, code, allow_inverse from exchange_rate_type where tenant_id=$1`, p.TenantID)
		if err != nil {
			return err
		}
		for typeRows.Next() {
			var id string
			var t domain.RateType
			if err := typeRows.Scan(&id, &t.Code, &t.AllowInverse); err != nil {
				typeRows.Close()
				return err
			}
			types[id] = t.Code
			defs = append(defs, t)
		}
		typeRows.Close()
		if err := typeRows.Err(); err != nil {
			return err
		}
		book = domain.NewBook(asOf, defs...)
		rows, err := tx.Query(ctx, `select id, rate_type_id, from_currency, to_currency, valid_from, rate::text from exchange_rate
			where tenant_id=$1 and superseded_by is null and valid_from <= $2 order by valid_from, version`, p.TenantID, asOf)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r domain.Rate
			var typeID, value string
			if err := rows.Scan(&r.ID, &typeID, &r.From, &r.To, &r.ValidFrom, &value); err != nil {
				return err
			}
			if r.Value, err = decimal.NewFromString(value); err != nil {
				return err
			}
			r.Type = types[typeID]
			if err := book.Add(r); errors.Is(err, domain.ErrInvalidRate) {
				slog.Warn("skipping stored rate the book rejects", "rateId", r.ID, "err", err)
			} else if err != nil {
				return fmt.Errorf("load rate %s: %w", r.ID, err)
			}
		}
		return rows.Err()
	})
	return book, err
}

func (s *Postgres) Previous(ctx context.Context, rateType, from, to string, date time.Time) (domain.Rate, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var r domain.Rate
	var value string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select e.id, e.valid_from, e.rate::text from exchange_rate e
			join exchange_rate_type t on t.tenant_id = e.tenant_id and t.id = e.rate_type_id
			where e.tenant_id=$1 and t.code=$2 and e.from_currency=$3 and e.to_currency=$4
			  and e.superseded_by is null and e.valid_from <= $5
			order by e.valid_from desc limit 1`, p.TenantID, rateType, from, to, date).Scan(&r.ID, &r.ValidFrom, &value)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Rate{}, false, nil
	}
	if err != nil {
		return domain.Rate{}, false, err
	}
	r.Type, r.From, r.To = rateType, from, to
	r.Value, err = decimal.NewFromString(value)
	return r, err == nil, err
}

func (s *Postgres) History(ctx context.Context, rateType, from, to string) ([]HistoryEntry, error) {
	p, _ := tenant.FromContext(ctx)
	out := []HistoryEntry{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select e.id, e.version, e.valid_from, e.rate::text, e.source, e.provider_timestamp,
				e.payload_hash, e.superseded_by::text, e.created_at, e.created_by
			from exchange_rate e
			join exchange_rate_type t on t.tenant_id = e.tenant_id and t.id = e.rate_type_id
			where e.tenant_id=$1 and t.code=$2 and e.from_currency=$3 and e.to_currency=$4
			order by e.valid_from, e.version`, p.TenantID, rateType, from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h HistoryEntry
			var value string
			var hash []byte
			var superseded, createdBy *string
			if err := rows.Scan(&h.ID, &h.Version, &h.ValidFrom, &value, &h.Source, &h.ProviderTimestamp, &hash, &superseded, &h.CreatedAt, &createdBy); err != nil {
				return err
			}
			if h.Rate, err = decimal.NewFromString(value); err != nil {
				return err
			}
			h.PayloadHash = hex.EncodeToString(hash)
			if superseded != nil {
				h.SupersededBy = *superseded
			}
			if createdBy != nil {
				h.CreatedBy = *createdBy
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	return out, err
}
