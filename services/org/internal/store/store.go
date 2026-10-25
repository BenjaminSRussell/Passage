package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	orgevents "github.com/BenjaminSRussell/passage/services/org/internal/events"
)

const (
	MaxPageSize          = 100
	CodeLegalEntityCode  = "ORG-LE-CODE-DUPLICATE"
	CodeIdempotencyReuse = "idempotency_key_reuse"
	uniqueViolation      = "23505"
	eventSource          = "org"
	DefaultLanguage      = "en-US"
)

type LegalEntity struct {
	ID                 string    `json:"id"`
	Code               string    `json:"code"`
	Name               string    `json:"name"`
	Country            string    `json:"country"`
	Currency           string    `json:"currency"`
	TimeZone           string    `json:"timeZone"`
	DefaultLanguage    string    `json:"defaultLanguage"`
	RegistrationNumber *string   `json:"registrationNumber,omitempty"`
	PrimaryAddressID   *string   `json:"primaryAddressId,omitempty"`
	Status             string    `json:"status"`
	Version            int       `json:"version"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type LegalEntityPage struct {
	Items    []LegalEntity `json:"items"`
	NextCode string        `json:"-"`
}

type Store interface {
	CreateLegalEntity(ctx context.Context, in LegalEntity, idempotencyKey string) (LegalEntity, error)
	GetLegalEntity(ctx context.Context, id string) (LegalEntity, error)
	UpdateLegalEntity(ctx context.Context, id string, version int, in LegalEntityPatch) (LegalEntity, error)
	ListLegalEntityNames(ctx context.Context, id string) ([]LegalEntityName, error)
	ListLegalEntities(ctx context.Context, f LegalEntityFilter, afterCode string, limit int) (LegalEntityPage, error)
	CreateOperatingUnit(ctx context.Context, in OperatingUnit, idempotencyKey string) (OperatingUnit, error)
	GetOperatingUnit(ctx context.Context, id string) (OperatingUnit, error)
	ListOperatingUnits(ctx context.Context, f OperatingUnitFilter, after OperatingUnitKey, limit int) (OperatingUnitPage, error)
	UpdateOperatingUnit(ctx context.Context, id string, version int, in OperatingUnitPatch) (OperatingUnit, error)
	CreateSite(ctx context.Context, in Site, idempotencyKey string) (Site, error)
	GetSite(ctx context.Context, id string) (Site, error)
	ListSites(ctx context.Context, f SiteFilter, after SiteKey, limit int) (SitePage, error)
	UpdateSite(ctx context.Context, id string, version int, in SitePatch) (Site, error)
	CreateWarehouse(ctx context.Context, in Warehouse, idempotencyKey string) (Warehouse, error)
	GetWarehouse(ctx context.Context, id string) (Warehouse, error)
	ListWarehouses(ctx context.Context, f WarehouseFilter, after SiteKey, limit int) (WarehousePage, error)
	UpdateWarehouse(ctx context.Context, id string, version int, in WarehousePatch) (Warehouse, error)
	CreateLocation(ctx context.Context, in Location, idempotencyKey string) (Location, error)
	GetLocation(ctx context.Context, id string) (Location, error)
	ListLocations(ctx context.Context, f LocationFilter, after SiteKey, limit int) (LocationPage, error)
	UpdateLocation(ctx context.Context, id string, version int, in LocationPatch) (Location, error)
	CreateAddress(ctx context.Context, in Address, idempotencyKey string) (Address, error)
	GetAddress(ctx context.Context, id string) (Address, error)
	ReviseAddress(ctx context.Context, id, versionID string, next Address) (Address, error)
	GetAddressVersion(ctx context.Context, versionID string) (Address, error)
	ListAddresses(ctx context.Context, f AddressFilter, afterID string, limit int) (AddressPage, error)
	CreateFiscalCalendar(ctx context.Context, in FiscalCalendar, idempotencyKey string) (FiscalCalendar, error)
	GetFiscalCalendar(ctx context.Context, id string) (FiscalCalendar, error)
	GenerateFiscalYear(ctx context.Context, id string, year int, custom []domain.DateRange, force bool) (FiscalYear, error)
	ListFiscalPeriods(ctx context.Context, id string, year int) ([]FiscalPeriod, error)
	CreateHierarchyType(ctx context.Context, in HierarchyType, idempotencyKey string) (HierarchyType, error)
	GetHierarchyType(ctx context.Context, id string) (HierarchyType, error)
	ListHierarchyTypes(ctx context.Context, f HierarchyTypeFilter, afterCode string, limit int) (HierarchyTypePage, error)
	GetHierarchyVersion(ctx context.Context, id string) (HierarchyVersion, error)
	DeleteHierarchyDraft(ctx context.Context, id string, version int) error
	CreateHierarchyDraft(ctx context.Context, typeID, idempotencyKey string) (HierarchyVersion, error)
	ReplaceHierarchyNodes(ctx context.Context, id string, version int, nodes []HierarchyNode) (HierarchyVersion, error)
	PublishHierarchyVersion(ctx context.Context, id string, version int, effectiveFrom time.Time, reason, idempotencyKey, publishedBy string, backdated bool) (HierarchyVersion, error)
	HierarchyAsOf(ctx context.Context, typeID string, date time.Time, orgUnitID string) (HierarchyAsOf, error)
	ListReference(ctx context.Context, kind ReferenceKind, f ReferenceFilter) ([]ReferenceItem, error)
	GetReference(ctx context.Context, kind ReferenceKind, code string) (ReferenceItem, error)
	SetReferenceStatus(ctx context.Context, kind ReferenceKind, code, status string) (ReferenceItem, error)
	Ping(ctx context.Context) error
}

type Postgres struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

const legalEntityColumns = "id, code, name, country, currency, time_zone, default_language, registration_number, primary_address_id, status, version, created_at, updated_at"

func scanLegalEntity(row pgx.Row, extra ...any) (LegalEntity, error) {
	var e LegalEntity
	err := row.Scan(append([]any{&e.ID, &e.Code, &e.Name, &e.Country, &e.Currency, &e.TimeZone, &e.DefaultLanguage, &e.RegistrationNumber, &e.PrimaryAddressID, &e.Status, &e.Version, &e.CreatedAt, &e.UpdatedAt}, extra...)...)
	return e, err
}

func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType, tenantID string, data any) error {
	evt, err := events.New(eventType, eventSource, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

func (s *Postgres) CreateLegalEntity(ctx context.Context, in LegalEntity, idempotencyKey string) (LegalEntity, error) {
	p, _ := tenant.FromContext(ctx)
	if in.DefaultLanguage == "" {
		in.DefaultLanguage = DefaultLanguage
	}
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var e LegalEntity
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanLegalEntity(tx.QueryRow(ctx, `select `+legalEntityColumns+` from legal_entity where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				e = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if err := checkLegalEntityLimit(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if err := checkActiveReferences(ctx, tx, p.TenantID, in.Country, in.Currency, in.DefaultLanguage); err != nil {
			return err
		}
		var inserted bool
		var err error
		e, err = scanLegalEntity(tx.QueryRow(ctx, `insert into legal_entity (tenant_id, id, code, name, country, currency, time_zone, default_language, registration_number, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			on conflict (tenant_id, idempotency_key) do update set version = legal_entity.version
			returning `+legalEntityColumns+`, (xmax = 0)`, p.TenantID, ids.New(), in.Code, in.Name, in.Country, in.Currency, in.TimeZone, in.DefaultLanguage, in.RegistrationNumber, key), &inserted)
		if err != nil || !inserted {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into legal_entity_name (tenant_id, legal_entity_id, name) values ($1, $2, $3)`, p.TenantID, e.ID, e.Name); err != nil {
			return err
		}
		return enqueue(ctx, tx, "legalentity", e.ID, orgevents.LegalEntityCreated, p.TenantID, orgevents.LegalEntityCreatedData{
			ID: e.ID, Code: e.Code, Name: e.Name, CountryCode: e.Country, AccountingCurrency: e.Currency, TimeZone: e.TimeZone,
		})
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return LegalEntity{}, httpx.NewProblem(http.StatusConflict, CodeLegalEntityCode, "a legal entity with this code already exists")
	}
	if err == nil && key != nil && (e.Code != in.Code || e.Name != in.Name || e.Country != in.Country || e.Currency != in.Currency || e.TimeZone != in.TimeZone ||
		e.DefaultLanguage != in.DefaultLanguage || deref(e.RegistrationNumber) != deref(in.RegistrationNumber)) {
		return LegalEntity{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return e, err
}

func (s *Postgres) ListLegalEntities(ctx context.Context, f LegalEntityFilter, afterCode string, limit int) (LegalEntityPage, error) {
	p, _ := tenant.FromContext(ctx)
	page := LegalEntityPage{Items: []LegalEntity{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+legalEntityColumns+` from legal_entity where tenant_id = $1 and code > $2
			and ($3::text = '' or status = $3) and ($4::text = '' or country = $4) and ($5::text = '' or currency = $5) order by code limit $6`,
			p.TenantID, afterCode, f.Status, f.Country, f.Currency, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanLegalEntity(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, e)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCode = page.Items[limit-1].Code
	}
	return page, err
}
