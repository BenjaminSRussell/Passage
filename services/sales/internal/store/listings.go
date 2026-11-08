package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

// ProductListing is one ALLOW or DENY entry for a product, scoped to a customer
// or customer group (FR-27.15). SAP VB01 listing and OV04 exclusion map here.
type ProductListing struct {
	ID            string    `json:"id"`
	LegalEntityID string    `json:"legalEntityId"`
	ScopeType     string    `json:"scopeType"`
	ScopeKey      string    `json:"scopeKey"`
	ProductID     string    `json:"productId"`
	ListingType   string    `json:"listingType"`
	ValidFrom     *string   `json:"validFrom,omitempty"`
	ValidTo       *string   `json:"validTo,omitempty"`
	CreatedBy     string    `json:"createdBy"`
	CreatedAt     time.Time `json:"createdAt"`
}

// ListingFilter narrows the listing inquiry. Empty fields match everything;
// an empty LegalEntityID means every legal entity in the caller's scope.
type ListingFilter struct {
	LegalEntityID string
	ProductID     string
	ScopeKey      string
	Limit         int
}

// ListingOverride records that a user with SalesOrderApprove accepted a line
// the product listing denied, and why (FR-27.15, AC-27.11).
type ListingOverride struct {
	LineNo    int    `json:"lineNo"`
	ProductID string `json:"productId"`
	Decision  string `json:"decision"`
	Reason    string `json:"reason"`
	Actor     string `json:"actor"`
}

const listingColumns = `id::text, legal_entity_id::text, scope_type, scope_key, product_id::text, listing_type,
	valid_from::text, valid_to::text, created_by, created_at`

func scanListing(row pgx.Row) (ProductListing, error) {
	var l ProductListing
	err := row.Scan(&l.ID, &l.LegalEntityID, &l.ScopeType, &l.ScopeKey, &l.ProductID, &l.ListingType, &l.ValidFrom, &l.ValidTo,
		&l.CreatedBy, &l.CreatedAt)
	return l, err
}

// checkListings evaluates every line of a new order against the product
// listings of its legal entity on the order date (BR-27.13). A denied line is
// refused with 422 PRODUCT_NOT_LISTED (SLS-0011) unless the caller holds the
// override duty and gave a reason; the accepted overrides are returned so the
// order logs them.
func checkListings(ctx context.Context, tx pgx.Tx, tenantID string, in NewOrder) ([]ListingOverride, error) {
	overrides := []ListingOverride{}
	productIDs := make([]string, 0, len(in.Lines))
	for _, l := range in.Lines {
		productIDs = append(productIDs, l.ProductID)
	}
	rows, err := tx.Query(ctx, `select scope_type, scope_key, product_id::text, listing_type, valid_from::text, valid_to::text
		from product_listing where tenant_id = $1 and legal_entity_id = $2 and product_id = any($3::uuid[])`,
		tenantID, in.LegalEntityID, productIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []domain.ListingEntry
	for rows.Next() {
		var e domain.ListingEntry
		var from, to *string
		if err := rows.Scan(&e.ScopeType, &e.ScopeKey, &e.ProductID, &e.ListingType, &from, &to); err != nil {
			return nil, err
		}
		if e.ValidFrom, err = parseDay(from); err != nil {
			return nil, err
		}
		if e.ValidTo, err = parseDay(to); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return overrides, nil
	}
	group := ""
	if in.CustomerGroup != nil {
		group = *in.CustomerGroup
	}
	orderDate := in.OrderDate
	if orderDate.IsZero() {
		orderDate = time.Now()
	}
	day := orderDate.UTC().Truncate(24 * time.Hour)
	var denied []string
	for _, l := range in.Lines {
		d := domain.EvaluateListing(entries, in.CustomerID, group, l.ProductID, day)
		if d.Allowed {
			continue
		}
		if !in.CanOverrideListing || strings.TrimSpace(in.ListingOverrideReason) == "" {
			denied = append(denied, fmt.Sprintf("line %d (%s)", l.LineNo, d.Reason))
			continue
		}
		overrides = append(overrides, ListingOverride{LineNo: l.LineNo, ProductID: l.ProductID, Decision: d.Reason,
			Reason: in.ListingOverrideReason, Actor: in.Actor})
	}
	if len(denied) > 0 {
		hint := "; a user with sales.order.approve may accept it with listingOverrideReason"
		if !in.CanOverrideListing && strings.TrimSpace(in.ListingOverrideReason) != "" {
			// The reason was given but the caller cannot override: say so
			// rather than leave the reason looking ignored.
			hint = "; listingOverrideReason was given, but accepting a denied line needs sales.order.approve, which the caller does not hold"
		}
		return nil, problem(http.StatusUnprocessableEntity, CodeProductNotListed,
			"PRODUCT_NOT_LISTED: the product is not listed for this customer on "+strings.Join(denied, ", ")+hint)
	}
	return overrides, nil
}

// listingCheckInput shapes a quotation's lines as the order the listing check
// reads: the same rule applies at quotation entry as at order entry.
func listingCheckInput(legalEntityID, customerID string, group *string, lines []Line, day time.Time, actor string, canOverride bool, reason string) NewOrder {
	return NewOrder{LegalEntityID: legalEntityID, CustomerID: customerID, CustomerGroup: group, Lines: lines, OrderDate: day,
		Actor: actor, CanOverrideListing: canOverride, ListingOverrideReason: reason}
}

// CreateListing stores one listing entry.
func (s *Postgres) CreateListing(ctx context.Context, in ProductListing) (ProductListing, error) {
	p, _ := tenant.FromContext(ctx)
	var out ProductListing
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanListing(tx.QueryRow(ctx, `insert into product_listing (tenant_id, id, legal_entity_id, scope_type, scope_key, product_id,
				listing_type, valid_from, valid_to, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8::date, $9::date, $10) returning `+listingColumns,
			p.TenantID, ids.New(), in.LegalEntityID, in.ScopeType, in.ScopeKey, in.ProductID, in.ListingType, in.ValidFrom, in.ValidTo, in.CreatedBy))
		return err
	})
	return out, err
}

// ListListings returns listing entries in the caller's legal-entity scope,
// newest first.
func (s *Postgres) ListListings(ctx context.Context, f ListingFilter) ([]ProductListing, error) {
	p, _ := tenant.FromContext(ctx)
	out := []ProductListing{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+listingColumns+` from product_listing
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or product_id::text = $3) and ($4 = '' or scope_key = $4)
			order by created_at desc, id desc limit $5`, p.TenantID, f.LegalEntityID, f.ProductID, f.ScopeKey, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanListing(rows)
			if err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	return out, err
}

// DeleteListing removes one listing entry. Orders already taken keep their
// logged overrides.
func (s *Postgres) DeleteListing(ctx context.Context, id string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var gone string
		err := tx.QueryRow(ctx, `delete from product_listing where tenant_id = $1 and id = $2 returning id::text`, p.TenantID, id).Scan(&gone)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeListingNotFound, "product listing not found")
		}
		return err
	})
}
