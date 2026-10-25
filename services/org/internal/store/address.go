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
	CodeAddressValidFrom        = "ORG-ADDR-VALID-FROM-INVALID"
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

func scanAddress(row pgx.Row) (Address, error) {
	var a Address
	err := row.Scan(&a.ID, &a.VersionID, &a.OwnerType, &a.OwnerID, &a.LegalEntityID, &a.CountryCode, &a.RegionCode, &a.City, &a.PostalCode, &a.Street1, &a.Street2, &a.Street3,
		&a.Building, &a.POBox, &a.Latitude, &a.Longitude, &a.GeoStatus, &a.Formatted, &a.ValidFrom, &a.ValidTo, &a.CreatedAt)
	return a, err
}

func loadPurposes(ctx context.Context, tx pgx.Tx, tenantID string, addressIDs []string) (map[string][]AddressPurpose, error) {
	rows, err := tx.Query(ctx, `select address_id::text, purpose, is_primary from address_purpose where tenant_id = $1 and address_id = any($2::text[]::uuid[]) order by purpose`, tenantID, addressIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	purposes := map[string][]AddressPurpose{}
	for rows.Next() {
		var addressID string
		var purpose AddressPurpose
		if err := rows.Scan(&addressID, &purpose.Purpose, &purpose.Primary); err != nil {
			return nil, err
		}
		purposes[addressID] = append(purposes[addressID], purpose)
	}
	return purposes, rows.Err()
}

func loadAddress(ctx context.Context, tx pgx.Tx, condition string, tenantID, arg string) (Address, error) {
	a, err := scanAddress(tx.QueryRow(ctx, `select `+addressColumns+` from postal_address where tenant_id = $1 and `+condition, tenantID, arg))
	if err != nil {
		return Address{}, err
	}
	purposes, err := loadPurposes(ctx, tx, tenantID, []string{a.ID})
	if err != nil {
		return Address{}, err
	}
	a.Purposes = append([]AddressPurpose{}, purposes[a.ID]...)
	return a, nil
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

func prepareAddress(ctx context.Context, tx pgx.Tx, tenantID string, in Address) (Address, error) {
	if err := checkAddressRules(ctx, tx, tenantID, in); err != nil {
		return Address{}, err
	}
	in.PostalCode = nullIfEmpty(domain.NormalizePostalCode(orEmpty(in.PostalCode)))
	in.Formatted = domain.Format(in.postal())
	return in, nil
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

func (a Address) SamePostal(b Address) bool { return a.postal() == b.postal() }

type AddressPatch struct {
	CountryCode *string          `json:"countryCode"`
	RegionCode  *string          `json:"regionCode"`
	City        *string          `json:"city"`
	PostalCode  *string          `json:"postalCode"`
	Street1     *string          `json:"street1"`
	Street2     *string          `json:"street2"`
	Street3     *string          `json:"street3"`
	Building    *string          `json:"building"`
	POBox       *string          `json:"poBox"`
	Purposes    []AddressPurpose `json:"purposes"`
}

func (in AddressPatch) Apply(a Address) (Address, bool) {
	changed := false
	required := func(target *string, value *string) {
		if value != nil && *value != *target {
			*target, changed = *value, true
		}
	}
	optional := func(target **string, value *string) {
		if value != nil && *value != orEmpty(*target) {
			*target, changed = nullIfEmpty(*value), true
		}
	}
	required(&a.CountryCode, in.CountryCode)
	required(&a.City, in.City)
	required(&a.Street1, in.Street1)
	optional(&a.RegionCode, in.RegionCode)
	optional(&a.PostalCode, in.PostalCode)
	optional(&a.Street2, in.Street2)
	optional(&a.Street3, in.Street3)
	optional(&a.Building, in.Building)
	optional(&a.POBox, in.POBox)
	if in.Purposes != nil {
		if !samePurposes(a.Purposes, in.Purposes) {
			changed = true
		}
		a.Purposes = in.Purposes
	}
	return a, changed
}

func samePurposes(a, b []AddressPurpose) bool {
	if len(a) != len(b) {
		return false
	}
	primary := make(map[string]bool, len(a))
	for _, p := range a {
		primary[p.Purpose] = p.Primary
	}
	for _, p := range b {
		if flag, ok := primary[p.Purpose]; !ok || flag != p.Primary {
			return false
		}
	}
	return true
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
			replay, err := loadAddress(ctx, tx, `idempotency_key = $2 and valid_to is null`, p.TenantID, *key)
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
		if in, err = prepareAddress(ctx, tx, p.TenantID, in); err != nil {
			return err
		}
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
			address, err = loadAddress(ctx, tx, `idempotency_key = $2 and valid_to is null`, p.TenantID, *key)
			return err
		}
		in.LegalEntityID = legalEntityID
		purposes, err := insertPurposes(ctx, tx, p.TenantID, addressID, in, in.Purposes)
		if err != nil {
			return err
		}
		if address, err = loadAddress(ctx, tx, `address_id = $2 and valid_to is null`, p.TenantID, addressID); err != nil {
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
		address, err = loadAddress(ctx, tx, `address_id = $2 and valid_to is null`, p.TenantID, id)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Address{}, addressNotFound()
	}
	return address, err
}

func insertPurposes(ctx context.Context, tx pgx.Tx, tenantID, addressID string, owner Address, purposes []AddressPurpose) ([]string, error) {
	names := make([]string, 0, len(purposes))
	for _, purpose := range purposes {
		names = append(names, purpose.Purpose)
		if _, err := tx.Exec(ctx, `insert into address_purpose (tenant_id, address_id, owner_type, owner_id, legal_entity_id, purpose, is_primary) values ($1, $2, $3, $4, $5, $6, $7)`,
			tenantID, addressID, owner.OwnerType, owner.OwnerID, owner.LegalEntityID, purpose.Purpose, purpose.Primary); err != nil {
			return nil, err
		}
	}
	return names, nil
}

func (s *Postgres) ReviseAddress(ctx context.Context, id, versionID string, next Address) (Address, error) {
	p, _ := tenant.FromContext(ctx)
	var revised Address
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var current Address
		err := tx.QueryRow(ctx, `select id, owner_type, owner_id, legal_entity_id, to_char(valid_from, 'YYYY-MM-DD') from postal_address
			where tenant_id = $1 and address_id = $2 and valid_to is null for update`, p.TenantID, id).Scan(&current.VersionID, &current.OwnerType, &current.OwnerID, &current.LegalEntityID, &current.ValidFrom)
		if errors.Is(err, pgx.ErrNoRows) {
			return addressNotFound()
		}
		if err != nil {
			return err
		}
		if current.VersionID != versionID {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, "current version is "+current.VersionID)
		}
		var validFrom string
		var inRange bool
		if err := tx.QueryRow(ctx, `select to_char(d, 'YYYY-MM-DD'), d between $2::date and current_date from (select coalesce($1::date, current_date) as d) resolved`,
			nullIfEmpty(next.ValidFrom), current.ValidFrom).Scan(&validFrom, &inRange); err != nil {
			return err
		}
		if !inRange {
			return addressProblem(http.StatusUnprocessableEntity, CodeAddressValidFrom, "validFrom must be between the current version's validFrom and today", "validFrom", "must not precede the current version or be in the future")
		}
		if next, err = prepareAddress(ctx, tx, p.TenantID, next); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update postal_address set valid_to = $3::date where tenant_id = $1 and id = $2`, p.TenantID, current.VersionID, validFrom); err != nil {
			return err
		}
		newVersionID := ids.New()
		if _, err := tx.Exec(ctx, `insert into postal_address (tenant_id, id, address_id, owner_type, owner_id, legal_entity_id, country_code, region_code, city, postal_code,
			street_1, street_2, street_3, building, po_box, latitude, longitude, geo_status, formatted, valid_from)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16::numeric, $17::numeric, $18, $19, $20::date)`,
			p.TenantID, newVersionID, id, current.OwnerType, current.OwnerID, current.LegalEntityID, next.CountryCode, next.RegionCode, next.City, next.PostalCode,
			next.Street1, next.Street2, next.Street3, next.Building, next.POBox, decimalText(next.Latitude), decimalText(next.Longitude), next.GeoStatus, next.Formatted, validFrom); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from address_purpose where tenant_id = $1 and address_id = $2`, p.TenantID, id); err != nil {
			return err
		}
		purposes, err := insertPurposes(ctx, tx, p.TenantID, id, current, next.Purposes)
		if err != nil {
			return err
		}
		if revised, err = loadAddress(ctx, tx, `address_id = $2 and valid_to is null`, p.TenantID, id); err != nil {
			return err
		}
		return enqueue(ctx, tx, "address", id, orgevents.AddressChanged, p.TenantID,
			orgevents.AddressChangedData{AddressID: id, VersionID: revised.VersionID, OwnerType: current.OwnerType, OwnerID: current.OwnerID, Purposes: purposes, ValidFrom: revised.ValidFrom})
	})
	if err != nil {
		return Address{}, mapAddressConstraint(err)
	}
	return revised, nil
}

func (s *Postgres) GetAddressVersion(ctx context.Context, versionID string) (Address, error) {
	p, _ := tenant.FromContext(ctx)
	var address Address
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		address, err = loadAddress(ctx, tx, `id = $2`, p.TenantID, versionID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Address{}, addressNotFound()
	}
	return address, err
}

type AddressFilter struct{ OwnerType, OwnerID, Purpose string }

type AddressPage struct {
	Items  []Address
	NextID string
}

func (s *Postgres) ListAddresses(ctx context.Context, f AddressFilter, afterID string, limit int) (AddressPage, error) {
	p, _ := tenant.FromContext(ctx)
	if afterID == "" {
		afterID = "00000000-0000-0000-0000-000000000000"
	}
	page := AddressPage{Items: []Address{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+addressColumns+` from postal_address where tenant_id = $1 and owner_type = $2 and owner_id = $3 and valid_to is null and address_id > $4::uuid
			and ($5::text = '' or exists (select 1 from address_purpose ap where ap.tenant_id = postal_address.tenant_id and ap.address_id = postal_address.address_id and ap.purpose = $5))
			order by address_id limit $6`, p.TenantID, f.OwnerType, f.OwnerID, afterID, f.Purpose, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			address, err := scanAddress(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, address)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Items) > limit {
			page.Items = page.Items[:limit]
			page.NextID = page.Items[limit-1].ID
		}
		addressIDs := make([]string, 0, len(page.Items))
		for _, address := range page.Items {
			addressIDs = append(addressIDs, address.ID)
		}
		purposes, err := loadPurposes(ctx, tx, p.TenantID, addressIDs)
		for i := range page.Items {
			page.Items[i].Purposes = append([]AddressPurpose{}, purposes[page.Items[i].ID]...)
		}
		return err
	})
	return page, err
}
