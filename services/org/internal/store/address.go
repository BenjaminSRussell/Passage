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
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	orgevents "github.com/BenjaminSRussell/passage/services/org/internal/events"
)

const (
	CodeAddressNotFound         = "ORG-ADDR-NOT-FOUND"
	CodeAddressOwnerInvalid     = "ORG-ADDR-OWNER-INVALID"
	CodeAddressCountryInvalid   = "ORG-ADDR-COUNTRY-INVALID"
	CodeAddressRegionUnknown    = "ORG-ADDR-REGION-UNKNOWN"
	CodeAddressPrimaryDuplicate = "ORG-ADDR-PRIMARY-DUPLICATE"
	addressPrimaryIndex         = "address_purpose_primary"
	addressColumns              = `address_id, id, owner_type, owner_id, legal_entity_id, country_code, region_code, city, postal_code, street_1, street_2, street_3,
		building, po_box, latitude, longitude, geo_status, formatted, to_char(valid_from, 'YYYY-MM-DD'), to_char(valid_to, 'YYYY-MM-DD'), created_at`
)

var ownerLookups = map[string]string{
	"legal_entity":   `select id::text from legal_entity where tenant_id = $1 and id = $2`,
	"operating_unit": `select null::text from operating_unit where tenant_id = $1 and id = $2`,
	"site":           `select legal_entity_id::text from site where tenant_id = $1 and id = $2`,
	"warehouse":      `select legal_entity_id::text from warehouse where tenant_id = $1 and id = $2`,
	"location":       `select legal_entity_id::text from location where tenant_id = $1 and id = $2`,
}

type AddressPurpose struct {
	Purpose string `json:"purpose"`
	Primary bool   `json:"primary"`
}

type Address struct {
	ID            string           `json:"id"`
	VersionID     string           `json:"versionId"`
	OwnerType     string           `json:"ownerType"`
	OwnerID       string           `json:"ownerId"`
	LegalEntityID *string          `json:"legalEntityId,omitempty"`
	CountryCode   string           `json:"countryCode"`
	RegionCode    *string          `json:"regionCode,omitempty"`
	City          string           `json:"city"`
	PostalCode    *string          `json:"postalCode,omitempty"`
	Street1       string           `json:"street1"`
	Street2       *string          `json:"street2,omitempty"`
	Street3       *string          `json:"street3,omitempty"`
	Building      *string          `json:"building,omitempty"`
	POBox         *string          `json:"poBox,omitempty"`
	Latitude      *decimal.Decimal `json:"latitude,omitempty"`
	Longitude     *decimal.Decimal `json:"longitude,omitempty"`
	GeoStatus     string           `json:"geoStatus"`
	Formatted     string           `json:"formatted"`
	Purposes      []AddressPurpose `json:"purposes"`
	ValidFrom     string           `json:"validFrom"`
	ValidTo       *string          `json:"validTo,omitempty"`
	CreatedAt     time.Time        `json:"createdAt"`
}

func addressNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeAddressNotFound, "address not found")
}

func addressProblem(status int, code, detail, field, rule string) error {
	p := httpx.NewProblem(status, code, detail)
	p.Errors = map[string]string{field: rule}
	return p
}

func orEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (a Address) postal() domain.PostalAddress {
	return domain.PostalAddress{CountryCode: a.CountryCode, RegionCode: orEmpty(a.RegionCode), City: a.City, PostalCode: orEmpty(a.PostalCode), Street1: a.Street1,
		Street2: orEmpty(a.Street2), Street3: orEmpty(a.Street3), Building: orEmpty(a.Building), POBox: orEmpty(a.POBox)}
}

func decimalText(d *decimal.Decimal) *string {
	if d == nil {
		return nil
	}
	text := d.String()
	return &text
}

func loadAddress(ctx context.Context, tx pgx.Tx, condition string, tenantID, arg string) (Address, error) {
	var a Address
	err := tx.QueryRow(ctx, `select `+addressColumns+` from postal_address where tenant_id = $1 and `+condition+` and valid_to is null`, tenantID, arg).Scan(
		&a.ID, &a.VersionID, &a.OwnerType, &a.OwnerID, &a.LegalEntityID, &a.CountryCode, &a.RegionCode, &a.City, &a.PostalCode, &a.Street1, &a.Street2, &a.Street3,
		&a.Building, &a.POBox, &a.Latitude, &a.Longitude, &a.GeoStatus, &a.Formatted, &a.ValidFrom, &a.ValidTo, &a.CreatedAt)
	if err != nil {
		return Address{}, err
	}
	rows, err := tx.Query(ctx, `select purpose, is_primary from address_purpose where tenant_id = $1 and address_id = $2 order by purpose`, tenantID, a.ID)
	if err != nil {
		return Address{}, err
	}
	defer rows.Close()
	a.Purposes = []AddressPurpose{}
	for rows.Next() {
		var purpose AddressPurpose
		if err := rows.Scan(&purpose.Purpose, &purpose.Primary); err != nil {
			return Address{}, err
		}
		a.Purposes = append(a.Purposes, purpose)
	}
	return a, rows.Err()
}

func resolveOwner(ctx context.Context, tx pgx.Tx, tenantID string, in Address) (*string, error) {
	if in.OwnerType == "party" {
		return nil, nil
	}
	var legalEntityID *string
	err := tx.QueryRow(ctx, ownerLookups[in.OwnerType], tenantID, in.OwnerID).Scan(&legalEntityID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, addressProblem(http.StatusUnprocessableEntity, CodeAddressOwnerInvalid, "owner is unknown or outside the caller's scope", "ownerId", "must be an existing owner visible to the caller")
	}
	return legalEntityID, err
}

func checkAddressRules(ctx context.Context, tx pgx.Tx, tenantID string, in Address) error {
	countries, err := queryReference(ctx, tx, KindCountry, tenantID, in.CountryCode, ReferenceFilter{})
	if err != nil {
		return err
	}
	if len(countries) == 0 || countries[0].Status != "active" {
		return addressProblem(http.StatusUnprocessableEntity, CodeAddressCountryInvalid, "country is unknown or inactive", "countryCode", "must be an active country")
	}
	if region := orEmpty(in.RegionCode); region != "" {
		regions, err := queryReference(ctx, tx, KindRegion, tenantID, in.CountryCode+"-"+region, ReferenceFilter{})
		if err != nil {
			return err
		}
		if len(regions) == 0 || regions[0].Status != "active" {
			return addressProblem(http.StatusUnprocessableEntity, CodeAddressRegionUnknown, "region is unknown or inactive for the country", "regionCode", "must be an active region of the country")
		}
	}
	rule := domain.CountryRule{RegionRequired: countries[0].RegionRequired != nil && *countries[0].RegionRequired, PostalRegex: orEmpty(countries[0].PostalRegex)}
	problems := domain.CheckCountryRules(in.postal(), rule)
	for _, field := range []string{"regionCode", "postalCode"} {
		if code, failed := problems[field]; failed {
			p := httpx.NewProblem(http.StatusUnprocessableEntity, code, "address does not satisfy the country's rules")
			p.Errors = problems
			return p
		}
	}
	return nil
}

func mapAddressConstraint(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == addressPrimaryIndex {
		return addressProblem(http.StatusConflict, CodeAddressPrimaryDuplicate, "the owner already has a primary address for that purpose", "purposes", "a primary address already exists for the owner and purpose")
	}
	return err
}

func sameAddressRequest(stored, in Address) bool {
	return stored.OwnerType == in.OwnerType && stored.OwnerID == in.OwnerID && stored.CountryCode == in.CountryCode && stored.City == in.City &&
		stored.Street1 == in.Street1 && orEmpty(stored.PostalCode) == domain.NormalizePostalCode(orEmpty(in.PostalCode))
}

func (s *Postgres) CreateAddress(ctx context.Context, in Address, idempotencyKey string) (Address, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var address Address
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := loadAddress(ctx, tx, `idempotency_key = $2`, p.TenantID, *key)
			if err == nil {
				address = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		legalEntityID, err := resolveOwner(ctx, tx, p.TenantID, in)
		if err != nil {
			return err
		}
		if err := checkAddressRules(ctx, tx, p.TenantID, in); err != nil {
			return err
		}
		if postal := domain.NormalizePostalCode(orEmpty(in.PostalCode)); postal != "" {
			in.PostalCode = &postal
		} else {
			in.PostalCode = nil
		}
		in.Formatted = domain.Format(in.postal())
		addressID := ids.New()
		tag, err := tx.Exec(ctx, `insert into postal_address (tenant_id, id, address_id, owner_type, owner_id, legal_entity_id, country_code, region_code, city, postal_code,
			street_1, street_2, street_3, building, po_box, latitude, longitude, geo_status, formatted, valid_from, idempotency_key)
			values ($1, $2, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::numeric, $16::numeric, $17, $18, coalesce($19::date, current_date), $20)
			on conflict (tenant_id, idempotency_key) do nothing`,
			p.TenantID, addressID, in.OwnerType, in.OwnerID, legalEntityID, in.CountryCode, in.RegionCode, in.City, in.PostalCode,
			in.Street1, in.Street2, in.Street3, in.Building, in.POBox, decimalText(in.Latitude), decimalText(in.Longitude), in.GeoStatus, in.Formatted, nullIfEmpty(in.ValidFrom), key)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			address, err = loadAddress(ctx, tx, `idempotency_key = $2`, p.TenantID, *key)
			return err
		}
		purposes := make([]string, 0, len(in.Purposes))
		for _, purpose := range in.Purposes {
			purposes = append(purposes, purpose.Purpose)
			if _, err := tx.Exec(ctx, `insert into address_purpose (tenant_id, address_id, owner_type, owner_id, legal_entity_id, purpose, is_primary) values ($1, $2, $3, $4, $5, $6, $7)`,
				p.TenantID, addressID, in.OwnerType, in.OwnerID, legalEntityID, purpose.Purpose, purpose.Primary); err != nil {
				return err
			}
		}
		if address, err = loadAddress(ctx, tx, `address_id = $2`, p.TenantID, addressID); err != nil {
			return err
		}
		return enqueue(ctx, tx, "address", addressID, orgevents.AddressChanged, p.TenantID,
			orgevents.AddressChangedData{AddressID: addressID, VersionID: address.VersionID, OwnerType: in.OwnerType, OwnerID: in.OwnerID, Purposes: purposes, ValidFrom: address.ValidFrom})
	})
	if err != nil {
		return Address{}, mapAddressConstraint(err)
	}
	if key != nil && !sameAddressRequest(address, in) {
		return Address{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return address, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Postgres) GetAddress(ctx context.Context, id string) (Address, error) {
	p, _ := tenant.FromContext(ctx)
	var address Address
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		address, err = loadAddress(ctx, tx, `address_id = $2`, p.TenantID, id)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Address{}, addressNotFound()
	}
	return address, err
}
