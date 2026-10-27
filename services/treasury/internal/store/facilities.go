package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
)

const facilityColumns = `f.id, f.legal_entity_id, f.facility_code, f.bank_name, f.currency, f.limit_amount::text,
	coalesce((select sum(i.amount) from treasury_facility_instrument i
		where i.tenant_id = f.tenant_id and i.facility_id = f.id and i.status = 'open'), 0)::text,
	f.expiry_date::text, f.status, f.version, f.created_at`

const instrumentColumns = `id, facility_id, legal_entity_id, kind, reference, beneficiary, currency, amount::text,
	issue_date::text, expiry_date::text, linked_order_ref, status, limit_override, version, created_at, updated_at`

// Facility is a bank credit line with a limit (FR-36.7). Utilized sums the open instruments.
type Facility struct {
	ID            string    `json:"id"`
	LegalEntityID string    `json:"legalEntityId"`
	Code          string    `json:"code"`
	BankName      string    `json:"bankName"`
	Currency      string    `json:"currency"`
	Limit         string    `json:"limit"`
	Utilized      string    `json:"utilized"`
	Headroom      string    `json:"headroom"`
	ExpiryDate    string    `json:"expiryDate"`
	Status        string    `json:"status"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"createdAt"`
}

// NewFacility is a validated facility create request.
type NewFacility struct {
	LegalEntityID string
	Code          string
	BankName      string
	Currency      string
	Limit         string
	ExpiryDate    string
}

// Instrument is a letter of credit or guarantee drawn on a facility.
type Instrument struct {
	ID             string    `json:"id"`
	FacilityID     string    `json:"facilityId"`
	LegalEntityID  string    `json:"legalEntityId"`
	Kind           string    `json:"kind"`
	Reference      string    `json:"reference"`
	Beneficiary    string    `json:"beneficiary"`
	Currency       string    `json:"currency"`
	Amount         string    `json:"amount"`
	IssueDate      string    `json:"issueDate"`
	ExpiryDate     string    `json:"expiryDate"`
	LinkedOrderRef *string   `json:"linkedOrderRef,omitempty"`
	Status         string    `json:"status"`
	LimitOverride  bool      `json:"limitOverride"`
	Version        int       `json:"version"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// NewInstrument is a validated instrument create request.
type NewInstrument struct {
	FacilityID     string
	Kind           string
	Reference      string
	Beneficiary    string
	Currency       string
	Amount         string
	IssueDate      string
	ExpiryDate     string
	LinkedOrderRef *string
}

func scanFacility(row pgx.Row) (Facility, error) {
	var f Facility
	var limit, utilized string
	if err := row.Scan(&f.ID, &f.LegalEntityID, &f.Code, &f.BankName, &f.Currency, &limit, &utilized, &f.ExpiryDate, &f.Status, &f.Version, &f.CreatedAt); err != nil {
		return f, err
	}
	l, err := decimal.NewFromString(limit)
	if err != nil {
		return f, err
	}
	u, err := decimal.NewFromString(utilized)
	if err != nil {
		return f, err
	}
	// Shown in the currency's minor unit: 1000000.00 for EUR, 150000000 for JPY.
	places := money.MinorUnits(f.Currency)
	f.Limit = l.StringFixed(places)
	f.Utilized = u.StringFixed(places)
	f.Headroom = domain.Headroom(l, u).StringFixed(places)
	return f, nil
}

func scanInstrument(row pgx.Row) (Instrument, error) {
	var i Instrument
	err := row.Scan(&i.ID, &i.FacilityID, &i.LegalEntityID, &i.Kind, &i.Reference, &i.Beneficiary, &i.Currency, &i.Amount,
		&i.IssueDate, &i.ExpiryDate, &i.LinkedOrderRef, &i.Status, &i.LimitOverride, &i.Version, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

// getFacilityTx loads one facility with utilization inside an open transaction.
func getFacilityTx(ctx context.Context, tx pgx.Tx, tenantID, id string) (Facility, error) {
	f, err := scanFacility(tx.QueryRow(ctx, `select `+facilityColumns+` from treasury_facility f where f.tenant_id = $1 and f.id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return f, notFound(CodeFacilityNotFound, "facility not found")
	}
	return f, err
}

// CreateFacility stores an active facility; a repeated Idempotency-Key replays it.
func (s *Postgres) CreateFacility(ctx context.Context, in NewFacility, key string) (Facility, error) {
	p, _ := tenant.FromContext(ctx)
	reqHash, err := requestHash("create-facility", in)
	if err != nil {
		return Facility{}, err
	}
	var out Facility
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, key, reqHash, func() (Facility, error) {
			id := ids.New()
			if _, err := tx.Exec(ctx, `insert into treasury_facility (tenant_id, id, legal_entity_id, facility_code, bank_name, currency, limit_amount, expiry_date)
				values ($1, $2, $3, $4, $5, $6, $7, $8)`,
				p.TenantID, id, in.LegalEntityID, in.Code, in.BankName, in.Currency, in.Limit, in.ExpiryDate); err != nil {
				return Facility{}, err
			}
			return getFacilityTx(ctx, tx, p.TenantID, id)
		})
		return err
	})
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == uniqueViolation {
		return Facility{}, httpx.NewProblem(http.StatusConflict, CodeFacilityDuplicate, "a facility with this code already exists for the legal entity")
	}
	return out, err
}

// ListFacilities returns the facilities of one legal entity with utilization.
func (s *Postgres) ListFacilities(ctx context.Context, legalEntityID string) ([]Facility, error) {
	p, _ := tenant.FromContext(ctx)
	items := []Facility{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+facilityColumns+` from treasury_facility f
			where f.tenant_id = $1 and f.legal_entity_id = $2 order by f.facility_code`, p.TenantID, legalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanFacility(rows)
			if err != nil {
				return err
			}
			items = append(items, f)
		}
		return rows.Err()
	})
	return items, err
}

// GetFacility returns one facility with utilization.
func (s *Postgres) GetFacility(ctx context.Context, id string) (Facility, error) {
	p, _ := tenant.FromContext(ctx)
	var out Facility
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = getFacilityTx(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// CreateInstrument books an LC or guarantee against a facility (FR-36.7). The
// facility row is locked so concurrent instruments see each other's
// utilization. A breach of the limit is refused with TRS-0415 unless override
// is set (the caller has checked the TreasuryFacilityOverride duty); an
// overriding instrument records that in limit_override.
func (s *Postgres) CreateInstrument(ctx context.Context, in NewInstrument, override bool, key string) (Instrument, error) {
	p, _ := tenant.FromContext(ctx)
	reqHash, err := requestHash("create-facility-instrument", struct {
		NewInstrument
		Override bool
	}{in, override})
	if err != nil {
		return Instrument{}, err
	}
	var out Instrument
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, key, reqHash, func() (Instrument, error) {
			var legalEntityID, status, currency, limitText, expiry string
			err := tx.QueryRow(ctx, `select legal_entity_id, status, currency, limit_amount::text, expiry_date::text from treasury_facility
				where tenant_id = $1 and id = $2 for update`, p.TenantID, in.FacilityID).Scan(&legalEntityID, &status, &currency, &limitText, &expiry)
			if errors.Is(err, pgx.ErrNoRows) {
				return Instrument{}, notFound(CodeFacilityNotFound, "facility not found")
			}
			if err != nil {
				return Instrument{}, err
			}
			if status != "active" {
				return Instrument{}, unprocessable(CodeFacilityState, "instruments can only be booked on an active facility")
			}
			// Dates are YYYY-MM-DD, so string order is date order.
			if in.IssueDate > expiry {
				return Instrument{}, unprocessable(CodeFacilityState, "the facility expired on "+expiry+"; an instrument issued on "+in.IssueDate+" cannot draw on it")
			}
			if currency != in.Currency {
				return Instrument{}, unprocessable(CodeInstrumentCurrency, "instrument currency "+in.Currency+" must equal the facility currency "+currency)
			}
			var utilizedText string
			if err := tx.QueryRow(ctx, `select coalesce(sum(amount), 0)::text from treasury_facility_instrument
				where tenant_id = $1 and facility_id = $2 and status = 'open'`, p.TenantID, in.FacilityID).Scan(&utilizedText); err != nil {
				return Instrument{}, err
			}
			limit, err := decimal.NewFromString(limitText)
			if err != nil {
				return Instrument{}, err
			}
			utilized, err := decimal.NewFromString(utilizedText)
			if err != nil {
				return Instrument{}, err
			}
			amount, err := decimal.NewFromString(in.Amount)
			if err != nil {
				return Instrument{}, err
			}
			if err := domain.CheckFacilityLimit(limit, utilized, amount, override); err != nil {
				return Instrument{}, err
			}
			// limit_override marks only an instrument that actually needed the override.
			breach := utilized.Add(amount).GreaterThan(limit)
			return scanInstrument(tx.QueryRow(ctx, `insert into treasury_facility_instrument (tenant_id, id, legal_entity_id, facility_id, kind,
				reference, beneficiary, currency, amount, issue_date, expiry_date, linked_order_ref, limit_override)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) returning `+instrumentColumns,
				p.TenantID, ids.New(), legalEntityID, in.FacilityID, in.Kind, in.Reference, in.Beneficiary, in.Currency, in.Amount,
				in.IssueDate, in.ExpiryDate, in.LinkedOrderRef, breach))
		})
		return err
	})
	return out, err
}

// ListInstruments returns the instruments booked on one facility.
func (s *Postgres) ListInstruments(ctx context.Context, facilityID string) ([]Instrument, error) {
	p, _ := tenant.FromContext(ctx)
	items := []Instrument{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+instrumentColumns+` from treasury_facility_instrument
			where tenant_id = $1 and facility_id = $2 order by created_at, id`, p.TenantID, facilityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			i, err := scanInstrument(rows)
			if err != nil {
				return err
			}
			items = append(items, i)
		}
		return rows.Err()
	})
	return items, err
}

// CloseInstrument releases an open instrument from utilization (FR-36.7), for
// example when an LC is drawn in full or a guarantee is returned.
func (s *Postgres) CloseInstrument(ctx context.Context, id string, version int) (Instrument, error) {
	p, _ := tenant.FromContext(ctx)
	var out Instrument
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanInstrument(tx.QueryRow(ctx, `select `+instrumentColumns+` from treasury_facility_instrument
			where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeInstrumentNotFound, "instrument not found")
		}
		if err != nil {
			return err
		}
		if cur.Version != version {
			return versionMismatch()
		}
		if cur.Status != "open" {
			return unprocessable(CodeInstrumentState, "only an open instrument can be closed")
		}
		out, err = scanInstrument(tx.QueryRow(ctx, `update treasury_facility_instrument set status = 'closed', version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+instrumentColumns, p.TenantID, id))
		return err
	})
	return out, err
}
