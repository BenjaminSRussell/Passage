package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	orgevents "github.com/BenjaminSRussell/passage/services/org/internal/events"
)

const (
	MaxLegalEntitiesPerTenant      = 500
	CodeLegalEntityNotFound        = "ORG-LE-NOT-FOUND"
	CodeVersionConflict            = "ORG-VERSION-CONFLICT"
	CodeReferenceInactive          = "ORG-LE-REFERENCE-INACTIVE"
	CodeLegalEntityLimit           = "ORG-LE-LIMIT"
	CodeLegalEntityCalendarInvalid = "ORG-LE-CALENDAR-INVALID"
)

type LegalEntityPatch struct {
	Name               *string `json:"name"`
	TimeZone           *string `json:"timeZone"`
	DefaultLanguage    *string `json:"defaultLanguage"`
	RegistrationNumber *string `json:"registrationNumber"`
	PrimaryAddressID   *string `json:"primaryAddressId"`
	FiscalCalendarID   *string `json:"fiscalCalendarId"`
	Status             *string `json:"status"`
}

type LegalEntityName struct {
	Name      string     `json:"name"`
	ValidFrom time.Time  `json:"validFrom"`
	ValidTo   *time.Time `json:"validTo,omitempty"`
}

type LegalEntityFilter struct {
	Status   string
	Country  string
	Currency string
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func legalEntityNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeLegalEntityNotFound, "legal entity not found")
}

func checkLegalEntityLimit(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('org-legal-entity-limit:' || $1::text, 0))`, tenantID); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(ctx, `select count(*) from legal_entity where tenant_id = $1`, tenantID).Scan(&n); err != nil {
		return err
	}
	if n >= MaxLegalEntitiesPerTenant {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeLegalEntityLimit, fmt.Sprintf("a tenant can have at most %d legal entities", MaxLegalEntitiesPerTenant))
	}
	return nil
}

func checkActiveReferences(ctx context.Context, tx pgx.Tx, tenantID, country, currency, language string) error {
	primaryLanguage, _, _ := strings.Cut(language, "-")
	for _, c := range []struct {
		kind        ReferenceKind
		code, field string
	}{{KindCountry, country, "country"}, {KindCurrency, currency, "currency"}, {KindLanguage, primaryLanguage, "defaultLanguage"}} {
		if c.code == "" {
			continue
		}
		items, err := queryReference(ctx, tx, c.kind, tenantID, c.code, ReferenceFilter{})
		if err != nil {
			return err
		}
		if len(items) == 0 || items[0].Status != "active" {
			p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeReferenceInactive, fmt.Sprintf("%s %s is unknown or inactive", c.kind, c.code))
			p.Errors = map[string]string{c.field: "must be an active " + string(c.kind)}
			return p
		}
	}
	return nil
}

func checkAssignableCalendar(ctx context.Context, tx pgx.Tx, tenantID, calendarID string) error {
	var status string
	var generated bool
	err := tx.QueryRow(ctx, `select status, exists(select 1 from fiscal_period where tenant_id = $1 and calendar_id = $2) from fiscal_calendar where tenant_id = $1 and id = $2 for share of fiscal_calendar`,
		tenantID, calendarID).Scan(&status, &generated)
	if err == nil && status == "active" && generated {
		return nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeLegalEntityCalendarInvalid, "fiscal calendar is unknown, inactive or has no generated year")
	p.Errors = map[string]string{"fiscalCalendarId": "must be an active fiscal calendar with at least one generated year"}
	return p
}

func (s *Postgres) GetLegalEntity(ctx context.Context, id string) (LegalEntity, error) {
	p, _ := tenant.FromContext(ctx)
	var e LegalEntity
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		e, err = scanLegalEntity(tx.QueryRow(ctx, `select `+legalEntityColumns+` from legal_entity where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return LegalEntity{}, legalEntityNotFound()
	}
	return e, err
}

func (in LegalEntityPatch) apply(e LegalEntity) (LegalEntity, []string) {
	var changed []string
	set := func(field string, current, next *string) {
		if next != nil && *next != *current {
			*current = *next
			changed = append(changed, field)
		}
	}
	set("default_language", &e.DefaultLanguage, in.DefaultLanguage)
	set("name", &e.Name, in.Name)
	set("status", &e.Status, in.Status)
	set("time_zone", &e.TimeZone, in.TimeZone)
	if in.RegistrationNumber != nil && *in.RegistrationNumber != deref(e.RegistrationNumber) {
		registration := *in.RegistrationNumber
		e.RegistrationNumber = &registration
		changed = append(changed, "registration_number")
	}
	var addressChanged bool
	if e.PrimaryAddressID, addressChanged = applyID(e.PrimaryAddressID, in.PrimaryAddressID); addressChanged {
		changed = append(changed, "primary_address_id")
	}
	var calendarChanged bool
	if e.FiscalCalendarID, calendarChanged = applyID(e.FiscalCalendarID, in.FiscalCalendarID); calendarChanged {
		changed = append(changed, "fiscal_calendar_id")
	}
	slices.Sort(changed)
	return e, changed
}

func (s *Postgres) UpdateLegalEntity(ctx context.Context, id string, version int, in LegalEntityPatch) (LegalEntity, error) {
	p, _ := tenant.FromContext(ctx)
	var e LegalEntity
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := scanLegalEntity(tx.QueryRow(ctx, `select `+legalEntityColumns+` from legal_entity where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return legalEntityNotFound()
		}
		if err != nil {
			return err
		}
		e = current
		if current.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, fmt.Sprintf("current version is %d", current.Version))
		}
		next, changed := in.apply(current)
		if len(changed) == 0 {
			return nil
		}
		if slices.Contains(changed, "default_language") {
			if err := checkActiveReferences(ctx, tx, p.TenantID, "", "", next.DefaultLanguage); err != nil {
				return err
			}
		}
		if slices.Contains(changed, "primary_address_id") && next.PrimaryAddressID != nil {
			if err := checkOwnedAddress(ctx, tx, p.TenantID, "legal_entity", id, *next.PrimaryAddressID, "primaryAddressId"); err != nil {
				return err
			}
		}
		if slices.Contains(changed, "fiscal_calendar_id") && next.FiscalCalendarID != nil {
			if err := checkAssignableCalendar(ctx, tx, p.TenantID, *next.FiscalCalendarID); err != nil {
				return err
			}
		}
		if slices.Contains(changed, "name") {
			if _, err := tx.Exec(ctx, `update legal_entity_name set valid_to = now() where tenant_id = $1 and legal_entity_id = $2 and valid_to is null`, p.TenantID, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into legal_entity_name (tenant_id, legal_entity_id, name) values ($1, $2, $3)`, p.TenantID, id, next.Name); err != nil {
				return err
			}
		}
		e, err = scanLegalEntity(tx.QueryRow(ctx, `update legal_entity set name = $3, time_zone = $4, default_language = $5, registration_number = $6, status = $7, primary_address_id = $8, fiscal_calendar_id = $9, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+legalEntityColumns, p.TenantID, id, next.Name, next.TimeZone, next.DefaultLanguage, next.RegistrationNumber, next.Status, next.PrimaryAddressID, next.FiscalCalendarID))
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "legalentity", id, orgevents.LegalEntityUpdated, p.TenantID, orgevents.LegalEntityUpdatedData{ID: id, ChangedFields: changed, Version: e.Version})
	})
	if err != nil {
		return LegalEntity{}, err
	}
	return e, nil
}

func (s *Postgres) ListLegalEntityNames(ctx context.Context, id string) ([]LegalEntityName, error) {
	p, _ := tenant.FromContext(ctx)
	var names []LegalEntityName
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select name, valid_from, valid_to from legal_entity_name where tenant_id = $1 and legal_entity_id = $2 order by valid_from desc`, p.TenantID, id)
		if err != nil {
			return err
		}
		names, err = pgx.CollectRows(rows, pgx.RowToStructByPos[LegalEntityName])
		return err
	})
	if err == nil && len(names) == 0 {
		return nil, legalEntityNotFound()
	}
	return names, err
}
