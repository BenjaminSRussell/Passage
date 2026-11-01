package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	pmfevents "github.com/BenjaminSRussell/passage/services/processmfg/internal/events"
)

// CodeRequirementNotFound is a customer batch requirement outside the caller's
// scope or that does not exist; CodeRequirementRetired a retire of a rule that
// is already retired.
const (
	CodeRequirementNotFound = "PMF-0113"
	CodeRequirementRetired  = "PMF-0114"
)

// CustomerRequirement is one stored customer batch requirement (FR-24.19). Level
// is the scope the rule applies at: 3 for a customer, 2 for a customer group, 1
// for the item default.
type CustomerRequirement struct {
	ID              string           `json:"id"`
	LegalEntityID   string           `json:"legalEntityId"`
	ItemID          string           `json:"itemId"`
	CustomerID      *string          `json:"customerId,omitempty"`
	CustomerGroupID *string          `json:"customerGroupId,omitempty"`
	Level           int              `json:"level"`
	SellableDays    *int             `json:"sellableDays,omitempty"`
	Attribute       string           `json:"attribute,omitempty"`
	Min             *decimal.Decimal `json:"min,omitempty"`
	Max             *decimal.Decimal `json:"max,omitempty"`
	SameBatch       *bool            `json:"sameBatch,omitempty"`
	ValidFrom       *string          `json:"validFrom,omitempty"`
	ValidTo         *string          `json:"validTo,omitempty"`
	RetiredAt       *time.Time       `json:"retiredAt,omitempty"`
	RetiredBy       *string          `json:"retiredBy,omitempty"`
	Version         int              `json:"version"`
	CreatedBy       string           `json:"createdBy"`
	CreatedAt       time.Time        `json:"createdAt"`
	UpdatedAt       time.Time        `json:"updatedAt"`
}

// RequirementFilter narrows the requirement list. ActiveOn (yyyy-mm-dd) keeps
// the rules in force that day and not retired; without it retired rules are
// listed too. Ids are in canonical (lower-case) form.
type RequirementFilter struct {
	LegalEntityID   string
	ItemID          string
	CustomerID      string
	CustomerGroupID string
	ActiveOn        string
	Limit           int
}

// ReqScope names the customer a line is for; either may be empty. Both are
// references to the party and sales master data, which this service does not
// read: the caller names the customer's group.
type ReqScope struct{ CustomerID, CustomerGroupID string }

// DomainReq turns a stored rule into the row domain.Resolve works on.
func (r CustomerRequirement) DomainReq() domain.Requirement {
	return domain.Requirement{Level: r.Level, SellableDays: r.SellableDays, Attribute: r.Attribute, Min: r.Min, Max: r.Max, SameBatch: r.SameBatch}
}

func levelOf(customerID, groupID *string) int {
	switch {
	case customerID != nil:
		return domain.ScopeCustomer
	case groupID != nil:
		return domain.ScopeGroup
	}
	return domain.ScopeItemDefault
}

const reqCols = `id, legal_entity_id, item_id, customer_id, customer_group_id, sellable_days, coalesce(attribute, ''), min_value, max_value,
	same_batch, to_char(valid_from, 'YYYY-MM-DD'), to_char(valid_to, 'YYYY-MM-DD'), retired_at, retired_by, version, created_by, created_at, updated_at`

func scanReq(row pgx.Row) (CustomerRequirement, error) {
	var r CustomerRequirement
	err := row.Scan(&r.ID, &r.LegalEntityID, &r.ItemID, &r.CustomerID, &r.CustomerGroupID, &r.SellableDays, &r.Attribute, &r.Min, &r.Max,
		&r.SameBatch, &r.ValidFrom, &r.ValidTo, &r.RetiredAt, &r.RetiredBy, &r.Version, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt)
	r.Level = levelOf(r.CustomerID, r.CustomerGroupID)
	return r, err
}

// CreateCustomerRequirement stores a rule (FR-24.19, BR-24.13) after the
// domain checks, and writes BatchRequirementDefined in the same transaction.
func (s *Postgres) CreateCustomerRequirement(ctx context.Context, in CustomerRequirement, idem Idem) (CustomerRequirement, bool, error) {
	in.Level = levelOf(in.CustomerID, in.CustomerGroupID)
	if err := domain.ValidateRequirement(in.DomainReq()); err != nil {
		return CustomerRequirement{}, false, err
	}
	var out CustomerRequirement
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		p := principal(ctx)
		in.ID = ids.New()
		row, err := scanReq(tx.QueryRow(ctx, `insert into customer_batch_requirement (tenant_id, id, legal_entity_id, item_id, customer_id, customer_group_id,
			sellable_days, attribute, min_value, max_value, same_batch, valid_from, valid_to, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), $9, $10, $11, $12::date, $13::date, $14) returning `+reqCols,
			p.TenantID, in.ID, in.LegalEntityID, in.ItemID, in.CustomerID, in.CustomerGroupID, in.SellableDays, in.Attribute, in.Min, in.Max,
			in.SameBatch, in.ValidFrom, in.ValidTo, p.UserID))
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "batchrequirement", row.ID, pmfevents.BatchRequirementDefined, requirementEvent(row)); err != nil {
			return err
		}
		out, created = row, true
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, created, err
}

func requirementEvent(r CustomerRequirement) pmfevents.BatchRequirementData {
	d := pmfevents.BatchRequirementData{RequirementID: r.ID, LegalEntityID: r.LegalEntityID, ItemID: r.ItemID, Level: r.Level, Attribute: r.Attribute, Version: r.Version}
	if r.CustomerID != nil {
		d.CustomerID = *r.CustomerID
	}
	if r.CustomerGroupID != nil {
		d.CustomerGroupID = *r.CustomerGroupID
	}
	d.SellableDays, d.SameBatch = r.SellableDays, r.SameBatch
	if r.Min != nil {
		d.Min = r.Min.String()
	}
	if r.Max != nil {
		d.Max = r.Max.String()
	}
	if r.ValidFrom != nil {
		d.ValidFrom = *r.ValidFrom
	}
	if r.ValidTo != nil {
		d.ValidTo = *r.ValidTo
	}
	return d
}

// ListCustomerRequirements returns rules ordered by item, then oldest first.
func (s *Postgres) ListCustomerRequirements(ctx context.Context, f RequirementFilter) ([]CustomerRequirement, error) {
	out := []CustomerRequirement{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+reqCols+` from customer_batch_requirement
			where ($1 = '' or item_id::text = $1) and ($2 = '' or customer_id::text = $2) and ($3 = '' or customer_group_id::text = $3)
			  and ($4 = '' or (retired_at is null and (valid_from is null or valid_from <= $4::date) and (valid_to is null or valid_to >= $4::date)))
			  and ($6 = '' or legal_entity_id::text = $6)
			order by item_id, created_at, id limit $5`, f.ItemID, f.CustomerID, f.CustomerGroupID, f.ActiveOn, f.Limit, f.LegalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanReq(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetCustomerRequirement returns one rule.
func (s *Postgres) GetCustomerRequirement(ctx context.Context, id string) (CustomerRequirement, error) {
	var out CustomerRequirement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadReq(ctx, tx, id, "")
		return err
	})
	return out, err
}

func loadReq(ctx context.Context, tx pgx.Tx, id, lock string) (CustomerRequirement, error) {
	r, err := scanReq(tx.QueryRow(ctx, `select `+reqCols+` from customer_batch_requirement where id = $1`+lock, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, notFound(CodeRequirementNotFound, "customer batch requirement", id)
	}
	return r, err
}

// RetireCustomerRequirement ends a rule: it stops applying at once and is kept
// for the record. version is the version the caller read.
func (s *Postgres) RetireCustomerRequirement(ctx context.Context, id string, version int, idem Idem) (CustomerRequirement, bool, error) {
	var out CustomerRequirement
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := loadReq(ctx, tx, id, " for update")
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if r.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
				fmt.Sprintf("customer batch requirement %s is at version %d, not %d; reload and try again", r.ID, r.Version, version))
		}
		if r.RetiredAt != nil {
			return httpx.NewProblem(http.StatusConflict, CodeRequirementRetired, fmt.Sprintf("customer batch requirement %s is already retired", r.ID))
		}
		p := principal(ctx)
		if _, err := tx.Exec(ctx, `update customer_batch_requirement set retired_at = now(), retired_by = $2, version = version + 1, updated_at = now() where id = $1`, id, p.UserID); err != nil {
			return err
		}
		if out, err = loadReq(ctx, tx, id, ""); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "batchrequirement", id, pmfevents.BatchRequirementRetired, requirementEvent(out)); err != nil {
			return err
		}
		changed = true
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, changed, err
}

// ApplicableRequirements returns the rules of one legal entity that apply to a
// line for an item shipped on shipDate (a calendar day, yyyy-mm-dd): the
// item's defaults, those of the customer's group and the customer's own, in
// force that day and not retired (BR-24.13). They come in precedence order, the
// order domain.Resolve expects: the most specific scope first and, within a
// scope, the rule that started last (no start counts as earliest), then the
// one defined last. So a rule defined to take over from a date supersedes the
// open-ended rule it replaces from that day, and two overlapping rules of one
// scope never leave the answer to chance.
func (s *Postgres) ApplicableRequirements(ctx context.Context, legalEntityID, itemID string, who ReqScope, shipDate string) ([]CustomerRequirement, error) {
	out := []CustomerRequirement{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+reqCols+` from customer_batch_requirement
			where legal_entity_id = $6 and item_id = $1 and retired_at is null
			  and (valid_from is null or valid_from <= $4::date) and (valid_to is null or valid_to >= $4::date)
			  and ((customer_id is null and customer_group_id is null)
			       or ($2 <> '' and customer_id::text = $2) or ($3 <> '' and customer_group_id::text = $3))
			order by (customer_id is not null) desc, (customer_group_id is not null) desc,
			         valid_from desc nulls last, created_at desc, id desc
			limit $5`, itemID, who.CustomerID, who.CustomerGroupID, shipDate, MaxList, legalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanReq(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
