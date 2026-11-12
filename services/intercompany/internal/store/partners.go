package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	icevents "github.com/BenjaminSRussell/passage/services/intercompany/internal/events"
)

// activePairingIndex is the partial unique index of migrations/0001_partners.sql.
const activePairingIndex = "intercompany_partner_one_active"

// partnerColumns lists the selected columns in the order scanPartner reads
// them. Numerics are read as text so they reach shopspring decimals exactly.
const partnerColumns = `id::text, version, created_at, updated_at, seller_entity, buyer_entity, customer_account, vendor_account, active,
	auto_create, auto_confirm, auto_post_packing_slip, auto_post_invoice, pricing, markup_pct::text, tolerance_price_pct::text, tolerance_date_days`

// PartnerFilter narrows a partner listing. Entity matches either side;
// Active nil lists active and inactive pairings.
type PartnerFilter struct {
	Entity string
	Active *bool
}

// PartnerPage is one page of pairings ordered by id; Next is the cursor of
// the following page, empty on the last one.
type PartnerPage struct {
	Items []domain.PartnerRecord
	Next  string
}

func errPairingExists(p domain.Partner) error {
	return &domain.Error{Code: domain.CodePairingExists, Detail: fmt.Sprintf("an active pairing from %s to %s already exists; deactivate it first", p.SellerEntity, p.BuyerEntity)}
}

func errPartnerNotFound(id string) error {
	return &domain.Error{Code: domain.CodePartnerNotFound, Detail: "no intercompany partner with id " + id}
}

// isActivePairingConflict reports a violation of one active pairing per direction.
func isActivePairingConflict(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == uniqueViolation && pg.ConstraintName == activePairingIndex
}

// CreatePartner stores a pairing and publishes PartnerCreated in one
// transaction. With an Idempotency-Key a retry returns the stored record and
// replayed=true. A second active pairing for the direction fails with IC-0840.
func (s *Postgres) CreatePartner(ctx context.Context, p domain.Partner, key string) (domain.PartnerRecord, bool, error) {
	if err := domain.ValidatePairing(p); err != nil {
		return domain.PartnerRecord{}, false, err
	}
	pr, ok := tenant.FromContext(ctx)
	if !ok {
		return domain.PartnerRecord{}, false, errors.New("store: no principal in context")
	}
	var (
		rec      domain.PartnerRecord
		replayed bool
	)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		create := func() (domain.PartnerRecord, error) {
			created, err := scanPartner(tx.QueryRow(ctx, `insert into intercompany_partner (tenant_id, id, seller_entity, buyer_entity,
					customer_account, vendor_account, active, auto_create, auto_confirm, auto_post_packing_slip, auto_post_invoice,
					pricing, markup_pct, tolerance_price_pct, tolerance_date_days)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::numeric, $14::numeric, $15)
				returning `+partnerColumns,
				pr.TenantID, ids.New(), p.SellerEntity, p.BuyerEntity, p.CustomerAccount, p.VendorAccount, p.Active,
				p.Actions.AutoCreate, p.Actions.AutoConfirm, p.Actions.AutoPostPackingSlip, p.Actions.AutoPostInvoice,
				string(p.Pricing), p.MarkupPct.String(), p.Tolerance.PricePct.String(), p.Tolerance.DateDays))
			if isActivePairingConflict(err) {
				return domain.PartnerRecord{}, errPairingExists(p)
			}
			if err != nil {
				return domain.PartnerRecord{}, err
			}
			return created, enqueue(ctx, tx, "partner", created.ID, icevents.PartnerCreated, pr.TenantID, icevents.PartnerCreatedData{
				ID: created.ID, SellerEntity: created.SellerEntity, BuyerEntity: created.BuyerEntity,
				CustomerAccount: created.CustomerAccount, VendorAccount: created.VendorAccount,
				Active: created.Active, Pricing: string(created.Pricing), Version: created.Version,
			})
		}
		var err error
		if key == "" {
			rec, err = create()
			return err
		}
		rec, replayed, err = idempotent(ctx, tx, pr.TenantID, key, "partner.create", p, create)
		return err
	})
	if err != nil {
		return domain.PartnerRecord{}, false, wrap("create intercompany partner", err)
	}
	return rec, replayed, nil
}

// ListPartners returns one page of the tenant's pairings ordered by id, after
// the cursor. limit must be at least 1.
func (s *Postgres) ListPartners(ctx context.Context, f PartnerFilter, after string, limit int) (PartnerPage, error) {
	pr, _ := tenant.FromContext(ctx)
	page := PartnerPage{Items: []domain.PartnerRecord{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+partnerColumns+` from intercompany_partner
			where tenant_id = $1
			  and ($2::text = '' or id > $2::uuid)
			  and ($3::text = '' or seller_entity = $3 or buyer_entity = $3)
			  and ($4::boolean is null or active = $4)
			order by id limit $5`, pr.TenantID, after, f.Entity, f.Active, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			rec, err := scanPartner(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, rec)
		}
		return rows.Err()
	})
	if err != nil {
		return PartnerPage{}, wrap("list intercompany partners", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.Next = page.Items[limit-1].ID
	}
	return page, nil
}

// GetPartner returns one pairing, or IC-0842 when the tenant has none with id.
func (s *Postgres) GetPartner(ctx context.Context, id string) (domain.PartnerRecord, error) {
	pr, _ := tenant.FromContext(ctx)
	var rec domain.PartnerRecord
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		rec, err = scanPartner(tx.QueryRow(ctx, `select `+partnerColumns+` from intercompany_partner where tenant_id = $1 and id = $2::uuid`, pr.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return errPartnerNotFound(id)
		}
		return err
	})
	if err != nil {
		return domain.PartnerRecord{}, wrap("get intercompany partner", err)
	}
	return rec, nil
}

// UpdatePartner applies a policy change when version is the stored version
// (else IC-0843), bumps the version and publishes PartnerUpdated in the same
// transaction. A patch that changes nothing returns the stored record without
// a new version or event. Activating a second pairing for the direction fails
// with IC-0840.
func (s *Postgres) UpdatePartner(ctx context.Context, id string, version int, patch domain.PartnerPatch) (domain.PartnerRecord, error) {
	pr, ok := tenant.FromContext(ctx)
	if !ok {
		return domain.PartnerRecord{}, errors.New("store: no principal in context")
	}
	var rec domain.PartnerRecord
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := scanPartner(tx.QueryRow(ctx, `select `+partnerColumns+` from intercompany_partner where tenant_id = $1 and id = $2::uuid for update`, pr.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return errPartnerNotFound(id)
		}
		if err != nil {
			return err
		}
		if current.Version != version {
			return &domain.Error{Code: domain.CodeVersionConflict, Detail: fmt.Sprintf("the pairing is at version %d, not %d; reload it and try again", current.Version, version)}
		}
		next, changed := patch.Apply(current.Partner)
		if len(changed) == 0 {
			rec = current
			return nil
		}
		if err := domain.ValidatePairing(next); err != nil {
			return err
		}
		rec, err = scanPartner(tx.QueryRow(ctx, `update intercompany_partner set customer_account = $2, vendor_account = $3, active = $4,
				auto_create = $5, auto_confirm = $6, auto_post_packing_slip = $7, auto_post_invoice = $8, pricing = $9,
				markup_pct = $10::numeric, tolerance_price_pct = $11::numeric, tolerance_date_days = $12,
				version = version + 1, updated_at = now()
			where tenant_id = $13 and id = $1::uuid returning `+partnerColumns,
			id, next.CustomerAccount, next.VendorAccount, next.Active,
			next.Actions.AutoCreate, next.Actions.AutoConfirm, next.Actions.AutoPostPackingSlip, next.Actions.AutoPostInvoice,
			string(next.Pricing), next.MarkupPct.String(), next.Tolerance.PricePct.String(), next.Tolerance.DateDays, pr.TenantID))
		if isActivePairingConflict(err) {
			return errPairingExists(next)
		}
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "partner", rec.ID, icevents.PartnerUpdated, pr.TenantID, icevents.PartnerUpdatedData{
			ID: rec.ID, SellerEntity: rec.SellerEntity, BuyerEntity: rec.BuyerEntity, Active: rec.Active, ChangedFields: changed, Version: rec.Version,
		})
	})
	if err != nil {
		return domain.PartnerRecord{}, wrap("update intercompany partner", err)
	}
	return rec, nil
}

// scanPartner reads one row selected with partnerColumns.
func scanPartner(row pgx.Row) (domain.PartnerRecord, error) {
	var (
		rec               domain.PartnerRecord
		pricing           string
		markup, tolerance string
	)
	err := row.Scan(&rec.ID, &rec.Version, &rec.CreatedAt, &rec.UpdatedAt, &rec.SellerEntity, &rec.BuyerEntity,
		&rec.CustomerAccount, &rec.VendorAccount, &rec.Active,
		&rec.Actions.AutoCreate, &rec.Actions.AutoConfirm, &rec.Actions.AutoPostPackingSlip, &rec.Actions.AutoPostInvoice,
		&pricing, &markup, &tolerance, &rec.Tolerance.DateDays)
	if err != nil {
		return domain.PartnerRecord{}, err
	}
	rec.Pricing = domain.PricingPolicy(pricing)
	if rec.MarkupPct, err = decimal.NewFromString(markup); err != nil {
		return domain.PartnerRecord{}, fmt.Errorf("store: markup %q: %w", markup, err)
	}
	if rec.Tolerance.PricePct, err = decimal.NewFromString(tolerance); err != nil {
		return domain.PartnerRecord{}, fmt.Errorf("store: price tolerance %q: %w", tolerance, err)
	}
	return rec, nil
}

// wrap adds context to infrastructure errors and passes rule violations through.
func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return de
	}
	return fmt.Errorf("%s: %w", op, err)
}
