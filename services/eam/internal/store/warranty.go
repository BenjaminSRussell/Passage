package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	eamevents "github.com/BenjaminSRussell/passage/services/eam/internal/events"
)

// Warranty is a vendor or manufacturer warranty on an asset (FR-44.17).
// StartsOn is a calendar date (YYYY-MM-DD); EndsOn is the first day after it.
type Warranty struct {
	ID            string    `json:"id"`
	AssetID       string    `json:"assetId"`
	WarrantyType  string    `json:"warrantyType"`
	VendorPartyID string    `json:"vendorPartyId"`
	StartBasis    string    `json:"startBasis"`
	StartsOn      string    `json:"startsOn"`
	EndsOn        string    `json:"endsOn"`
	Months        int       `json:"months"`
	PartsCovered  bool      `json:"partsCovered"`
	LaborCovered  bool      `json:"laborCovered"`
	Exclusions    []string  `json:"exclusions"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
}

const dateLayout = "2006-01-02"

const warrantyColumns = `id::text, asset_id::text, warranty_type, vendor_party_id::text, start_basis,
  to_char(starts_on, 'YYYY-MM-DD'), months, parts_covered, labor_covered, exclusions, status, created_at`

func scanWarranty(row pgx.Row) (Warranty, error) {
	var w Warranty
	err := row.Scan(&w.ID, &w.AssetID, &w.WarrantyType, &w.VendorPartyID, &w.StartBasis, &w.StartsOn, &w.Months,
		&w.PartsCovered, &w.LaborCovered, &w.Exclusions, &w.Status, &w.CreatedAt)
	if err == nil {
		if start, perr := time.Parse(dateLayout, w.StartsOn); perr == nil {
			w.EndsOn = domain.WarrantyEnds(start, w.Months).Format(dateLayout)
		}
	}
	return w, err
}

// window converts a stored warranty to the domain coverage window. Dates are
// UTC days.
func (w Warranty) window() (domain.WarrantyWindow, error) {
	start, err := time.Parse(dateLayout, w.StartsOn)
	if err != nil {
		return domain.WarrantyWindow{}, err
	}
	return domain.WarrantyWindow{Status: w.Status, StartsOn: start, Months: w.Months,
		PartsCovered: w.PartsCovered, LaborCovered: w.LaborCovered, Exclusions: w.Exclusions}, nil
}

// CreateWarranty records a warranty on an existing asset. A repeated key
// returns the warranty created with it.
func (s *Postgres) CreateWarranty(ctx context.Context, assetID string, in Warranty, key string) (Warranty, error) {
	var out Warranty
	start, err := time.Parse(dateLayout, in.StartsOn)
	if err != nil {
		return out, problem(400, CodeValidation, "startsOn must be a date (YYYY-MM-DD)")
	}
	if err := domain.ValidateWarranty(domain.WarrantyWindow{StartsOn: start, Months: in.Months,
		PartsCovered: in.PartsCovered, LaborCovered: in.LaborCovered}); err != nil {
		return out, problem(400, CodeValidation, err.Error())
	}
	if in.Exclusions == nil {
		in.Exclusions = []string{}
	}
	err = s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if id, err := existingID(ctx, tx, "asset_warranty", t, key); err != nil || id != "" {
			if err != nil {
				return err
			}
			var gerr error
			out, gerr = scanWarranty(tx.QueryRow(ctx, `select `+warrantyColumns+` from asset_warranty
			  where tenant_id = $1 and id = $2`, t, id))
			if gerr == nil && out.AssetID != assetID {
				return problem(422, CodeKeyReuse, "Idempotency-Key was already used for another asset's warranty")
			}
			return gerr
		}
		if _, err := getAsset(ctx, tx, t, assetID, false); err != nil {
			return err
		}
		var err error
		out, err = scanWarranty(tx.QueryRow(ctx, `insert into asset_warranty (tenant_id, id, asset_id, warranty_type,
		    vendor_party_id, start_basis, starts_on, months, parts_covered, labor_covered, exclusions, idempotency_key)
		  values ($1, $2, $3, $4, $5, $6, $7::date, $8, $9, $10, $11, $12)
		  returning `+warrantyColumns,
			t, ids.New(), assetID, in.WarrantyType, in.VendorPartyID, in.StartBasis, in.StartsOn, in.Months,
			in.PartsCovered, in.LaborCovered, in.Exclusions, optStr(key)))
		return err
	})
	return out, err
}

// ListWarranties returns the warranties of an asset, newest start first.
func (s *Postgres) ListWarranties(ctx context.Context, assetID string) ([]Warranty, error) {
	out := []Warranty{}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if _, err := getAsset(ctx, tx, t, assetID, false); err != nil {
			return err
		}
		var err error
		out, err = warranties(ctx, tx, t, assetID, false)
		return err
	})
	return out, err
}

func warranties(ctx context.Context, tx pgx.Tx, t, assetID string, activeOnly bool) ([]Warranty, error) {
	rows, err := tx.Query(ctx, `select `+warrantyColumns+` from asset_warranty
	  where tenant_id = $1 and asset_id = $2 and (not $3 or status = 'ACTIVE') order by starts_on desc, id`, t, assetID, activeOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Warranty{}
	for rows.Next() {
		w, err := scanWarranty(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// coveringWarranty is the warranty check at work order creation (BR-44.14):
// the newest ACTIVE warranty of the asset that is in force at `at` and does
// not exclude the job type, or nil.
func coveringWarranty(ctx context.Context, tx pgx.Tx, t, assetID string, at time.Time, jobType string) (*Warranty, error) {
	all, err := warranties(ctx, tx, t, assetID, true)
	if err != nil {
		return nil, err
	}
	for i := range all {
		win, err := all[i].window()
		if err != nil {
			return nil, err
		}
		if domain.Covers(win, at, jobType) {
			return &all[i], nil
		}
	}
	return nil, nil
}

// claimWarranty emits warranty.claimable for a covered work order at completion
// (BR-44.14) when the covered parts or labor are above zero.
func claimWarranty(ctx context.Context, tx pgx.Tx, t string, wo WorkOrder) error {
	w, err := scanWarranty(tx.QueryRow(ctx, `select `+warrantyColumns+` from asset_warranty where tenant_id = $1 and id = $2`,
		t, *wo.WarrantyID))
	if err != nil {
		return notFound(err, "warranty")
	}
	parts, err := decimal.NewFromString(wo.PartsCost)
	if err != nil {
		return err
	}
	labor, err := decimal.NewFromString(wo.LaborCost)
	if err != nil {
		return err
	}
	claim := domain.ClaimFor(w.PartsCovered, w.LaborCovered, parts, labor)
	if !claim.Total().IsPositive() {
		return nil
	}
	return enqueue(ctx, tx, t, "workorder", wo.ID, eamevents.WarrantyClaimable, eamevents.WarrantyClaimableData{
		WorkOrderID: wo.ID, AssetID: wo.AssetID, WarrantyID: w.ID, VendorPartyID: w.VendorPartyID, Currency: wo.Currency,
		CoveredParts: claim.Parts.StringFixed(2), CoveredLabor: claim.Labor.StringFixed(2)})
}
