package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

// CodeNoPackingRule is the FUL-0012 warning auto-pack returns for a line that
// no packing rule covers; the line stays unpacked (BR-28.11).
const CodeNoPackingRule = "FUL-0012"

// PackingStore is the persistence of packing configuration and auto-pack
// (FR-28.15), packaging materials and fees (FR-28.14) and returnable transport
// packaging (FR-28.16).
type PackingStore interface {
	ListContainerTypes(ctx context.Context, limit int) ([]ContainerType, error)
	SaveContainerType(ctx context.Context, ct ContainerType, idem Idem) (ContainerType, error)
	ListPackingRules(ctx context.Context, productID string, limit int) ([]PackingRule, error)
	SavePackingRule(ctx context.Context, rule PackingRule, idem Idem) (PackingRule, error)
	ListProductPackaging(ctx context.Context, productID string, limit int) ([]ProductPackaging, error)
	SaveProductPackaging(ctx context.Context, pp ProductPackaging, idem Idem) (ProductPackaging, error)
	ListFeeRates(ctx context.Context, countryCode string, limit int) ([]FeeRate, error)
	SaveFeeRate(ctx context.Context, rate FeeRate, idem Idem) (FeeRate, error)
	PackagingReport(ctx context.Context, f ReportFilter) ([]PackagingReportRow, error)
	AutoPack(ctx context.Context, id string, version int, unitWeights map[string]decimal.Decimal, idem Idem) (AutoPackResult, error)
	ListReturnableBalances(ctx context.Context, customerID string, limit int) ([]ReturnableBalance, error)
	ReturnPackaging(ctx context.Context, in ReturnInput, idem Idem) (ReturnableBalance, error)
	AdjustReturnable(ctx context.Context, in AdjustInput, idem Idem) (ReturnableMovement, error)
	ListReturnableMovements(ctx context.Context, f MovementFilter) ([]ReturnableMovement, error)
	ReturnableAging(ctx context.Context, customerID string) ([]ReturnableAgingRow, error)
}

// ContainerType is a carton, crate or pallet type: its tare weight, whether it
// is returnable transport packaging, and the packaging material it is made of.
type ContainerType struct {
	Code              string          `json:"code"`
	TareKg            decimal.Decimal `json:"tareKg"`
	Returnable        bool            `json:"returnable"`
	PackagingMaterial *string         `json:"packagingMaterial"`
}

// PackingRule maps a product, for every customer or for one, to a container
// type with a quantity per container and a max gross weight (FR-28.15).
type PackingRule struct {
	ID              string          `json:"id"`
	ProductID       string          `json:"productId"`
	CustomerID      *string         `json:"customerId"`
	ContainerType   string          `json:"containerType"`
	QtyPerContainer decimal.Decimal `json:"qtyPerContainer"`
	MaxWeightKg     decimal.Decimal `json:"maxWeightKg"`
}

// ProductPackaging is the weight of one packaging material per sold unit of a product (FR-28.14).
type ProductPackaging struct {
	ProductID       string          `json:"productId"`
	MaterialCode    string          `json:"materialCode"`
	WeightPerUnitKg decimal.Decimal `json:"weightPerUnitKg"`
}

// FeeRate is a country packaging fee per kilogram of a material, valid from a date.
type FeeRate struct {
	CountryCode  string          `json:"countryCode"`
	MaterialCode string          `json:"materialCode"`
	Currency     string          `json:"currency"`
	RatePerKg    decimal.Decimal `json:"ratePerKg"`
	ValidFrom    string          `json:"validFrom"`
}

// PackagingFeeLine is one statistical packaging fee line of a packing slip (BR-28.12).
type PackagingFeeLine struct {
	CountryCode  string          `json:"countryCode"`
	Period       string          `json:"period"`
	MaterialCode string          `json:"materialCode"`
	WeightKg     decimal.Decimal `json:"weightKg"`
	RatePerKg    decimal.Decimal `json:"ratePerKg"`
	FeeAmount    decimal.Decimal `json:"feeAmount"`
	Currency     string          `json:"currency"`
}

// ReportFilter selects the packaging report: one country and month, optionally one legal entity.
type ReportFilter struct {
	CountryCode   string
	Period        string
	LegalEntityID string
}

// PackagingReportRow totals one material for a country and period; reversals net out.
type PackagingReportRow struct {
	MaterialCode string          `json:"materialCode"`
	WeightKg     decimal.Decimal `json:"weightKg"`
	FeeAmount    decimal.Decimal `json:"feeAmount"`
	Currency     string          `json:"currency"`
}

// AutoPackWarning flags a line auto-pack left unpacked (FUL-0012).
type AutoPackWarning struct {
	Code           string `json:"code"`
	ShipmentLineID string `json:"shipmentLineId"`
	ProductID      string `json:"productId"`
	Message        string `json:"message"`
}

// AutoPackResult is the outcome of auto-pack: the containers created, the
// shipment after packing, and the lines no packing rule covers.
type AutoPackResult struct {
	Shipment   Shipment          `json:"shipment"`
	Containers []Container       `json:"containers"`
	Unpacked   []string          `json:"unpackedLineIds"`
	Warnings   []AutoPackWarning `json:"warnings"`
}

// ReturnableBalance is the number of returnable containers of one type a customer holds (FR-28.16).
type ReturnableBalance struct {
	CustomerID     string          `json:"customerId"`
	ContainerType  string          `json:"containerType"`
	Balance        decimal.Decimal `json:"balance"`
	LastShippedAt  *time.Time      `json:"lastShippedAt"`
	LastReturnedAt *time.Time      `json:"lastReturnedAt"`
}

// ReturnableAgingRow is an outstanding returnable balance with the shipments
// that can still make it up, newest first, for first-in-first-out aging.
type ReturnableAgingRow struct {
	ReturnableBalance
	Shipped []domain.RTPShipped
}

// ReturnInput is a standalone return receipt of returnable containers.
type ReturnInput struct {
	CustomerID    string
	ContainerType string
	Qty           decimal.Decimal
}

// ---- container types --------------------------------------------------------

// ListContainerTypes returns the container types by code.
func (s *Postgres) ListContainerTypes(ctx context.Context, limit int) ([]ContainerType, error) {
	out := []ContainerType{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select code, tare_weight_kg::text, returnable, packaging_material_code
			from container_type order by code limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ct ContainerType
			var tare string
			if err := rows.Scan(&ct.Code, &tare, &ct.Returnable, &ct.PackagingMaterial); err != nil {
				return err
			}
			ct.TareKg = dec(tare)
			out = append(out, ct)
		}
		return rows.Err()
	})
	return out, err
}

// SaveContainerType creates or replaces a container type. Containers already
// packed keep the tare and flags they were packed with.
func (s *Postgres) SaveContainerType(ctx context.Context, ct ContainerType, idem Idem) (ContainerType, error) {
	p, _ := tenant.FromContext(ctx)
	var out ContainerType
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (ContainerType, error) {
			_, err := tx.Exec(ctx, `insert into container_type (tenant_id, code, tare_weight_kg, returnable, packaging_material_code)
				values ($1, $2, $3, $4, $5)
				on conflict (tenant_id, code) do update set tare_weight_kg = excluded.tare_weight_kg,
					returnable = excluded.returnable, packaging_material_code = excluded.packaging_material_code, updated_at = now()`,
				p.TenantID, ct.Code, ct.TareKg, ct.Returnable, ct.PackagingMaterial)
			return ct, err
		})
		out = res
		return err
	})
	return out, err
}

func loadContainerType(ctx context.Context, tx pgx.Tx, code string) (ContainerType, bool, error) {
	ct := ContainerType{Code: code}
	var tare string
	err := tx.QueryRow(ctx, `select tare_weight_kg::text, returnable, packaging_material_code from container_type where code = $1`, code).
		Scan(&tare, &ct.Returnable, &ct.PackagingMaterial)
	if errors.Is(err, pgx.ErrNoRows) {
		return ContainerType{}, false, nil
	}
	if err != nil {
		return ContainerType{}, false, err
	}
	ct.TareKg = dec(tare)
	return ct, true, nil
}

func unknownContainerType() error {
	p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeValidation, "request failed validation")
	p.Errors = map[string]string{"containerType": "is not a known container type"}
	return p
}

// ---- packing rules ----------------------------------------------------------

// ListPackingRules returns the packing rules, optionally of one product.
func (s *Postgres) ListPackingRules(ctx context.Context, productID string, limit int) ([]PackingRule, error) {
	out := []PackingRule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id, product_id, customer_id::text, container_type, qty_per_container::text, max_weight_kg::text
			from packing_rule where ($1 = '' or product_id::text = $1)
			order by product_id, customer_id nulls first limit $2`, productID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r PackingRule
			var qty, maxKg string
			if err := rows.Scan(&r.ID, &r.ProductID, &r.CustomerID, &r.ContainerType, &qty, &maxKg); err != nil {
				return err
			}
			r.QtyPerContainer, r.MaxWeightKg = dec(qty), dec(maxKg)
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// SavePackingRule creates the rule for a product and customer scope, or
// replaces the existing rule of that scope and keeps its id.
func (s *Postgres) SavePackingRule(ctx context.Context, rule PackingRule, idem Idem) (PackingRule, error) {
	p, _ := tenant.FromContext(ctx)
	var out PackingRule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (PackingRule, error) {
			if _, known, err := loadContainerType(ctx, tx, rule.ContainerType); err != nil {
				return PackingRule{}, err
			} else if !known {
				return PackingRule{}, unknownContainerType()
			}
			err := tx.QueryRow(ctx, `insert into packing_rule (tenant_id, id, product_id, customer_id, container_type, qty_per_container, max_weight_kg)
				values ($1, $2, $3, $4, $5, $6, $7)
				on conflict (tenant_id, product_id, coalesce(customer_id, '00000000-0000-0000-0000-000000000000'::uuid))
				do update set container_type = excluded.container_type, qty_per_container = excluded.qty_per_container,
					max_weight_kg = excluded.max_weight_kg, updated_at = now()
				returning id`,
				p.TenantID, ids.New(), rule.ProductID, rule.CustomerID, rule.ContainerType, rule.QtyPerContainer, rule.MaxWeightKg).Scan(&rule.ID)
			return rule, err
		})
		out = res
		return err
	})
	return out, err
}

// loadPackingRules returns the rules for the products with the tare of their container type.
func loadPackingRules(ctx context.Context, tx pgx.Tx, productIDs []string) ([]domain.PackRule, error) {
	rules := []domain.PackRule{}
	if len(productIDs) == 0 {
		return rules, nil
	}
	rows, err := tx.Query(ctx, `select r.product_id, coalesce(r.customer_id::text, ''), r.container_type,
			r.qty_per_container::text, r.max_weight_kg::text, t.tare_weight_kg::text
		from packing_rule r
		join container_type t on t.tenant_id = r.tenant_id and t.code = r.container_type
		where r.product_id = any($1::uuid[])`, productIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r domain.PackRule
		var qty, maxKg, tare string
		if err := rows.Scan(&r.ProductID, &r.CustomerID, &r.ContainerType, &qty, &maxKg, &tare); err != nil {
			return nil, err
		}
		r.QtyPerContainer, r.MaxWeightKg, r.TareKg = dec(qty), dec(maxKg), dec(tare)
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

// ---- auto-pack --------------------------------------------------------------

// AutoPack places the picked, not yet packed quantities into containers by
// packing rule (FR-28.15, BR-28.11) and closes each with an SSCC. unitWeights
// gives the unit weight in kg per shipment line; a line without one counts as
// weightless, so only the quantity per container limits it. Lines without a
// rule stay unpacked with a FUL-0012 warning. When nothing is packed the
// shipment and its version are left as they are.
func (s *Postgres) AutoPack(ctx context.Context, id string, version int, unitWeights map[string]decimal.Decimal, idem Idem) (AutoPackResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out AutoPackResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (AutoPackResult, error) {
			sh, err := loadShipment(ctx, tx, id)
			if err != nil {
				return AutoPackResult{}, err
			}
			if err := checkVersion(sh, version); err != nil {
				return AutoPackResult{}, err
			}
			if err := checkWorkable(ctx, tx, sh); err != nil {
				return AutoPackResult{}, err
			}
			if sh.Status != domain.StatusPicked && sh.Status != domain.StatusPacked {
				return AutoPackResult{}, statusProblem("auto-pack", sh.Status)
			}
			// auto_pack_enabled (section 5.28.13): a warehouse may keep packing manual.
			rule, err := shipmentOperationRule(ctx, tx, sh)
			if err != nil {
				return AutoPackResult{}, err
			}
			if !rule.AutoPackEnabled {
				return AutoPackResult{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeAutoPackDisabled, "auto-pack is turned off for this warehouse (auto_pack_enabled); pack the containers by hand")
			}
			for lineID := range unitWeights {
				if lineIndex(sh, lineID) < 0 {
					return AutoPackResult{}, httpx.NewProblem(http.StatusNotFound, CodeLineNotFound, "unit weight names a line that is not on this shipment: "+lineID)
				}
			}
			lines := []domain.PackLine{}
			productIDs := []string{}
			for _, l := range sh.Lines {
				open := l.Picked.Sub(l.Packed)
				if !open.IsPositive() {
					continue
				}
				lines = append(lines, domain.PackLine{ShipmentLineID: l.ID, ProductID: l.ProductID, Qty: open, UnitWeightKg: unitWeights[l.ID]})
				productIDs = append(productIDs, l.ProductID)
			}
			rules, err := loadPackingRules(ctx, tx, productIDs)
			if err != nil {
				return AutoPackResult{}, err
			}
			plan, err := domain.AutoPack(lines, rules, sh.CustomerID)
			if err != nil {
				return AutoPackResult{}, mapDomain(err)
			}
			result := AutoPackResult{Containers: []Container{}, Unpacked: []string{}, Warnings: []AutoPackWarning{}}
			for _, lineID := range plan.Unpacked {
				productID := sh.Lines[lineIndex(sh, lineID)].ProductID
				result.Unpacked = append(result.Unpacked, lineID)
				result.Warnings = append(result.Warnings, AutoPackWarning{Code: CodeNoPackingRule, ShipmentLineID: lineID, ProductID: productID,
					Message: fmt.Sprintf("no packing rule for product %s; line left unpacked", productID)})
			}
			types := map[string]ContainerType{}
			for _, pc := range plan.Containers {
				ct, ok := types[pc.ContainerType]
				if !ok {
					if ct, _, err = loadContainerType(ctx, tx, pc.ContainerType); err != nil {
						return AutoPackResult{}, err
					}
					types[pc.ContainerType] = ct
				}
				cont := Container{ID: ids.New(), ContainerType: pc.ContainerType, Status: "CLOSED", TareKg: pc.TareKg, GrossKg: pc.GrossKg,
					Returnable: ct.Returnable, PackagingMaterial: ct.PackagingMaterial, Lines: []ContainerLine{}}
				for _, pl := range pc.Lines {
					cont.Lines = append(cont.Lines, ContainerLine{ShipmentLineID: pl.ShipmentLineID, Qty: pl.Qty})
				}
				if err := s.insertContainer(ctx, tx, p.TenantID, &sh, &cont); err != nil {
					return AutoPackResult{}, err
				}
				result.Containers = append(result.Containers, cont)
			}
			if len(result.Containers) > 0 {
				target := sh.Status
				if domain.AllPacked(lineQtys(sh)) {
					if target, err = domain.Advance(sh.Status, domain.StatusPacked); err != nil {
						return AutoPackResult{}, mapDomain(err)
					}
				}
				if err := bumpShipment(ctx, tx, p.TenantID, &sh, target); err != nil {
					return AutoPackResult{}, err
				}
			}
			result.Shipment = sh
			return result, nil
		})
		out = res
		return err
	})
	return out, err
}

// insertContainer issues the SSCC, stores the container with its weights and
// lines, adds the packed quantities to the shipment lines in the database and
// in sh, and appends the container to sh. The caller has checked the quantities.
func (s *Postgres) insertContainer(ctx context.Context, tx pgx.Tx, tenantID string, sh *Shipment, cont *Container) error {
	// The legal entity's own GS1 company prefix and extension digit win over the
	// process default (BR-28.5); the serial counter is per prefix, so two
	// entities that share a prefix never issue the same SSCC.
	prefix, extension := s.gs1Prefix, s.extension
	le, err := loadLegalEntitySetting(ctx, tx, sh.LegalEntityID, false)
	if err != nil {
		return err
	}
	if le.GS1CompanyPrefix != nil {
		prefix = *le.GS1CompanyPrefix
	}
	if le.SSCCExtensionDigit != nil {
		extension = *le.SSCCExtensionDigit
	}
	serial, err := nextNumber(ctx, tx, tenantID, "sscc:"+prefix)
	if err != nil {
		return err
	}
	if cont.SSCC, err = domain.SSCC(extension, prefix, uint64(serial)); errors.Is(err, domain.ErrSerialRange) {
		// A long prefix leaves few serial references (a 12-digit prefix 9,999).
		p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeValidation, "request failed validation")
		p.Errors = map[string]string{"gs1CompanyPrefix": fmt.Sprintf("the serial references of GS1 company prefix %s are used up; give the legal entity another prefix", prefix)}
		return p
	} else if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into container (tenant_id, id, shipment_id, container_type, sscc, tare_weight_kg, gross_weight_kg, returnable, packaging_material_code)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		tenantID, cont.ID, sh.ID, cont.ContainerType, cont.SSCC, cont.TareKg, cont.GrossKg, cont.Returnable, cont.PackagingMaterial)
	// The SSCC was issued before: under a prefix that nests with this one
	// (another legal entity outside the caller's scope, see checkPrefixNesting).
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "container_tenant_id_sscc_key" {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeSSCCInvalid, fmt.Sprintf("SSCC %s is already used; check the GS1 company prefixes of the legal entities", cont.SSCC))
	}
	if err != nil {
		return err
	}
	for _, cl := range cont.Lines {
		if _, err := tx.Exec(ctx, `insert into container_line (tenant_id, container_id, shipment_line_id, qty) values ($1, $2, $3, $4)`,
			tenantID, cont.ID, cl.ShipmentLineID, cl.Qty); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update shipment_line set qty_packed = qty_packed + $3 where tenant_id = $1 and id = $2`,
			tenantID, cl.ShipmentLineID, cl.Qty); err != nil {
			return err
		}
		idx := lineIndex(*sh, cl.ShipmentLineID)
		sh.Lines[idx].Packed = sh.Lines[idx].Packed.Add(cl.Qty)
	}
	sh.Containers = append(sh.Containers, *cont)
	return nil
}

// ---- packaging materials and fees -------------------------------------------

// ListProductPackaging returns the packaging per product unit, optionally of one product.
func (s *Postgres) ListProductPackaging(ctx context.Context, productID string, limit int) ([]ProductPackaging, error) {
	out := []ProductPackaging{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select product_id, material_code, weight_per_unit_kg::text from product_packaging
			where ($1 = '' or product_id::text = $1) order by product_id, material_code limit $2`, productID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pp ProductPackaging
			var w string
			if err := rows.Scan(&pp.ProductID, &pp.MaterialCode, &w); err != nil {
				return err
			}
			pp.WeightPerUnitKg = dec(w)
			out = append(out, pp)
		}
		return rows.Err()
	})
	return out, err
}

// SaveProductPackaging sets the weight of a material per unit of a product. A
// weight of 0 stops the material counting toward the packaging fee.
func (s *Postgres) SaveProductPackaging(ctx context.Context, pp ProductPackaging, idem Idem) (ProductPackaging, error) {
	p, _ := tenant.FromContext(ctx)
	var out ProductPackaging
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (ProductPackaging, error) {
			_, err := tx.Exec(ctx, `insert into product_packaging (tenant_id, product_id, material_code, weight_per_unit_kg)
				values ($1, $2, $3, $4)
				on conflict (tenant_id, product_id, material_code) do update set weight_per_unit_kg = excluded.weight_per_unit_kg, updated_at = now()`,
				p.TenantID, pp.ProductID, pp.MaterialCode, pp.WeightPerUnitKg)
			return pp, err
		})
		out = res
		return err
	})
	return out, err
}

// ListFeeRates returns the packaging fee rates, optionally of one country, newest first per material.
func (s *Postgres) ListFeeRates(ctx context.Context, countryCode string, limit int) ([]FeeRate, error) {
	out := []FeeRate{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select country_code, material_code, currency, rate_per_kg::text, valid_from
			from packaging_fee_rate where ($1 = '' or country_code = $1)
			order by country_code, material_code, valid_from desc limit $2`, countryCode, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r FeeRate
			var rate string
			var from time.Time
			if err := rows.Scan(&r.CountryCode, &r.MaterialCode, &r.Currency, &rate, &from); err != nil {
				return err
			}
			r.RatePerKg, r.ValidFrom = dec(rate), from.Format("2006-01-02")
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// SaveFeeRate creates or replaces the rate of a country and material valid from a date.
func (s *Postgres) SaveFeeRate(ctx context.Context, rate FeeRate, idem Idem) (FeeRate, error) {
	p, _ := tenant.FromContext(ctx)
	var out FeeRate
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (FeeRate, error) {
			_, err := tx.Exec(ctx, `insert into packaging_fee_rate (tenant_id, country_code, material_code, currency, rate_per_kg, valid_from)
				values ($1, $2, $3, $4, $5, $6::date)
				on conflict (tenant_id, country_code, material_code, valid_from) do update set rate_per_kg = excluded.rate_per_kg, currency = excluded.currency`,
				p.TenantID, rate.CountryCode, rate.MaterialCode, rate.Currency, rate.RatePerKg, rate.ValidFrom)
			return rate, err
		})
		out = res
		return err
	})
	return out, err
}

// recordPackagingFees rates the packaging weight that ships with the slip and
// stores the statistical fee lines for the posting month (BR-28.12): each
// product's packaging per unit × the shipped quantity, plus the tare of the
// non-returnable containers of a known packaging material that ship now. No
// journal line is written. An empty country posts no fee.
func recordPackagingFees(ctx context.Context, tx pgx.Tx, tenantID string, sh Shipment, slip PackingSlip, countryCode string) ([]PackagingFeeLine, error) {
	lines := []PackagingFeeLine{}
	if countryCode == "" {
		return lines, nil
	}
	shipped := map[string]decimal.Decimal{} // product id -> shipped qty
	var productIDs []string
	for _, sl := range slip.Lines {
		productID := sh.Lines[lineIndex(sh, sl.ShipmentLineID)].ProductID
		if _, ok := shipped[productID]; !ok {
			productIDs = append(productIDs, productID)
		}
		shipped[productID] = shipped[productID].Add(sl.Qty)
	}
	var uses []domain.PackagingUse
	rows, err := tx.Query(ctx, `select product_id::text, material_code, weight_per_unit_kg::text from product_packaging where product_id = any($1::uuid[])`, productIDs)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var productID, material, perUnit string
		if err := rows.Scan(&productID, &material, &perUnit); err != nil {
			rows.Close()
			return nil, err
		}
		uses = append(uses, domain.PackagingUse{Material: material, WeightKg: dec(perUnit).Mul(shipped[productID])})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, c := range sh.Containers {
		if c.Status == "CLOSED" && !c.Returnable && c.PackagingMaterial != nil {
			uses = append(uses, domain.PackagingUse{Material: *c.PackagingMaterial, WeightKg: c.TareKg})
		}
	}
	rates, err := loadFeeRates(ctx, tx, countryCode, slip.PostingDate)
	if err != nil {
		return nil, err
	}
	fees, err := domain.PackagingFees(uses, rates, slip.Currency)
	if err != nil {
		if errors.Is(err, domain.ErrRateMissing) || errors.Is(err, domain.ErrRateCurrency) {
			return nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeRateMissing,
				fmt.Sprintf("%v for %s in %s on %s", err, slip.Currency, countryCode, slip.PostingDate.Format("2006-01-02")))
		}
		return nil, err
	}
	period := slip.PostingDate.Format("2006-01")
	for _, f := range fees {
		line := PackagingFeeLine{CountryCode: countryCode, Period: period, MaterialCode: f.Material, WeightKg: f.WeightKg,
			RatePerKg: f.RatePerKg, FeeAmount: f.Amount, Currency: f.Currency}
		if err := insertFeeLine(ctx, tx, tenantID, sh.LegalEntityID, slip.ID, line); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, nil
}

func insertFeeLine(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, slipID string, l PackagingFeeLine) error {
	_, err := tx.Exec(ctx, `insert into packaging_fee_line (tenant_id, id, legal_entity_id, packing_slip_id, country_code, period, material_code, weight_kg, rate_per_kg, fee_amount, currency)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		tenantID, ids.New(), legalEntityID, slipID, l.CountryCode, l.Period, l.MaterialCode, l.WeightKg, l.RatePerKg, l.FeeAmount, l.Currency)
	return err
}

// loadFeeRates returns the latest rate per material valid on the date.
func loadFeeRates(ctx context.Context, tx pgx.Tx, countryCode string, asOf time.Time) (map[string]domain.FeeRate, error) {
	rows, err := tx.Query(ctx, `select distinct on (material_code) material_code, currency, rate_per_kg::text
		from packaging_fee_rate where country_code = $1 and valid_from <= $2::date
		order by material_code, valid_from desc`, countryCode, asOf.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rates := map[string]domain.FeeRate{}
	for rows.Next() {
		var material, currency, rate string
		if err := rows.Scan(&material, &currency, &rate); err != nil {
			return nil, err
		}
		rates[material] = domain.FeeRate{RatePerKg: dec(rate), Currency: currency}
	}
	return rates, rows.Err()
}

func loadFeeLines(ctx context.Context, tx pgx.Tx, slipID string) ([]PackagingFeeLine, error) {
	rows, err := tx.Query(ctx, `select country_code, period, material_code, weight_kg::text, rate_per_kg::text, fee_amount::text, currency
		from packaging_fee_line where packing_slip_id = $1 order by material_code`, slipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PackagingFeeLine{}
	for rows.Next() {
		var l PackagingFeeLine
		var w, rate, fee string
		if err := rows.Scan(&l.CountryCode, &l.Period, &l.MaterialCode, &w, &rate, &fee, &l.Currency); err != nil {
			return nil, err
		}
		l.WeightKg, l.RatePerKg, l.FeeAmount = dec(w), dec(rate), dec(fee)
		out = append(out, l)
	}
	return out, rows.Err()
}

// mirrorFeeLines writes the reversing slip's fee lines: the original lines
// with negative weight and fee, in the month of the reversal, so the
// packaging report of that month corrects the original.
func mirrorFeeLines(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, orig PackingSlip, rev PackingSlip) ([]PackagingFeeLine, error) {
	out := []PackagingFeeLine{}
	for _, l := range orig.PackagingFees {
		m := l
		m.Period = rev.PostingDate.Format("2006-01")
		m.WeightKg, m.FeeAmount = l.WeightKg.Neg(), l.FeeAmount.Neg()
		if err := insertFeeLine(ctx, tx, tenantID, legalEntityID, rev.ID, m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// PackagingReport totals the packaging weight and fee per material for one
// country and month (section 5.28.14), net of reversals.
func (s *Postgres) PackagingReport(ctx context.Context, f ReportFilter) ([]PackagingReportRow, error) {
	out := []PackagingReportRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select material_code, sum(weight_kg)::text, sum(fee_amount)::text, currency
			from packaging_fee_line where country_code = $1 and period = $2 and ($3 = '' or legal_entity_id::text = $3)
			group by material_code, currency order by material_code, currency`, f.CountryCode, f.Period, f.LegalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r PackagingReportRow
			var w, fee string
			if err := rows.Scan(&r.MaterialCode, &w, &fee, &r.Currency); err != nil {
				return err
			}
			r.WeightKg, r.FeeAmount = dec(w), dec(fee)
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// ---- returnable transport packaging -----------------------------------------

type returnableCount struct {
	ContainerType string
	Qty           int64
}

// countReturnables counts the returnable containers of a shipment in a status, per type.
func countReturnables(ctx context.Context, tx pgx.Tx, shipmentID, status string) ([]returnableCount, error) {
	rows, err := tx.Query(ctx, `select container_type, count(*) from container
		where shipment_id = $1 and status = $2 and returnable group by container_type order by container_type`, shipmentID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []returnableCount{}
	for rows.Next() {
		var c returnableCount
		if err := rows.Scan(&c.ContainerType, &c.Qty); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// moveBalances adds (sign 1, packing slip post) or removes (sign -1, slip
// cancel) the returnable containers on the customer's balances (BR-28.13) and
// writes one movement per container type that names the packing slip.
func moveBalances(ctx context.Context, tx pgx.Tx, tenantID, customerID string, counts []returnableCount, sign int64, slipID string) error {
	kind := MovementShipped
	if sign < 0 {
		kind = MovementShipmentCancelled
	}
	for _, c := range counts {
		qty := decimal.NewFromInt(sign * c.Qty)
		var after decimal.Decimal
		if sign > 0 {
			var bal string
			if err := tx.QueryRow(ctx, `insert into returnable_packaging_balance (tenant_id, customer_id, container_type, balance, last_shipped_at)
				values ($1, $2, $3, $4, now())
				on conflict (tenant_id, customer_id, container_type) do update
				set balance = returnable_packaging_balance.balance + excluded.balance, last_shipped_at = now()
				returning balance::text`,
				tenantID, customerID, c.ContainerType, qty).Scan(&bal); err != nil {
				return err
			}
			after = dec(bal)
		} else {
			b, err := applyReturn(ctx, tx, customerID, c.ContainerType, qty, false)
			if err != nil {
				return err
			}
			after = b.Balance
		}
		if _, err := recordMovement(ctx, tx, tenantID, customerID, c.ContainerType, kind, qty, after, slipID, "", actorOf(ctx)); err != nil {
			return err
		}
	}
	return nil
}

// applyReturn moves one balance by delta (negative) under a row lock and
// refuses a negative result with FUL-0015. received stamps last_returned_at.
func applyReturn(ctx context.Context, tx pgx.Tx, customerID, containerType string, delta decimal.Decimal, received bool) (ReturnableBalance, error) {
	b := ReturnableBalance{CustomerID: customerID, ContainerType: containerType, Balance: decimal.Zero}
	var bal string
	err := tx.QueryRow(ctx, `select balance::text, last_shipped_at, last_returned_at from returnable_packaging_balance
		where customer_id = $1 and container_type = $2 for update`, customerID, containerType).Scan(&bal, &b.LastShippedAt, &b.LastReturnedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return b, err
	}
	if err == nil {
		b.Balance = dec(bal)
	}
	next, aerr := domain.ApplyBalance(b.Balance, delta)
	if aerr != nil {
		return b, httpx.NewProblem(http.StatusUnprocessableEntity, CodeRTPNegative,
			fmt.Sprintf("returnable packaging balance would become negative for customer %s: %s %s held, %s returned",
				customerID, b.Balance.String(), containerType, delta.Neg().String()))
	}
	if err := tx.QueryRow(ctx, `update returnable_packaging_balance
		set balance = $3, last_returned_at = case when $4::boolean then now() else last_returned_at end
		where customer_id = $1 and container_type = $2 returning last_returned_at`, customerID, containerType, next, received).Scan(&b.LastReturnedAt); err != nil {
		return b, err
	}
	b.Balance = next
	return b, nil
}

// ListReturnableBalances returns the returnable packaging balances, optionally of one customer.
func (s *Postgres) ListReturnableBalances(ctx context.Context, customerID string, limit int) ([]ReturnableBalance, error) {
	out := []ReturnableBalance{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select customer_id, container_type, balance::text, last_shipped_at, last_returned_at
			from returnable_packaging_balance where ($1 = '' or customer_id::text = $1)
			order by customer_id, container_type limit $2`, customerID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b ReturnableBalance
			var bal string
			if err := rows.Scan(&b.CustomerID, &b.ContainerType, &bal, &b.LastShippedAt, &b.LastReturnedAt); err != nil {
				return err
			}
			b.Balance = dec(bal)
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// ReturnPackaging records a standalone return receipt of returnable containers
// from a customer (BR-28.13). A return above the balance is refused with FUL-0015.
func (s *Postgres) ReturnPackaging(ctx context.Context, in ReturnInput, idem Idem) (ReturnableBalance, error) {
	p, _ := tenant.FromContext(ctx)
	var out ReturnableBalance
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (ReturnableBalance, error) {
			b, err := applyReturn(ctx, tx, in.CustomerID, in.ContainerType, in.Qty.Neg(), true)
			if err != nil {
				return b, err
			}
			m, err := recordMovement(ctx, tx, p.TenantID, in.CustomerID, in.ContainerType, MovementReturned, in.Qty.Neg(), b.Balance, "", "", actorOf(ctx))
			if err != nil {
				return b, err
			}
			return b, enqueueFor(ctx, tx, "returnable_packaging", in.CustomerID, fulevents.ReturnableReturned, p.TenantID, movementData(in.CustomerID, m))
		})
		out = res
		return err
	})
	return out, err
}

// ---- event payloads ---------------------------------------------------------

func feeData(lines []PackagingFeeLine) []fulevents.PackagingFee {
	out := []fulevents.PackagingFee{}
	for _, l := range lines {
		out = append(out, fulevents.PackagingFee{CountryCode: l.CountryCode, MaterialCode: l.MaterialCode, WeightKg: l.WeightKg.String(),
			RatePerKg: l.RatePerKg.String(), FeeAmount: l.FeeAmount.String(), Currency: l.Currency})
	}
	return out
}

func returnableData(counts []returnableCount, sign int64) []fulevents.ReturnableMove {
	out := []fulevents.ReturnableMove{}
	for _, c := range counts {
		out = append(out, fulevents.ReturnableMove{ContainerType: c.ContainerType, Qty: decimal.NewFromInt(sign * c.Qty).String()})
	}
	return out
}

// ReturnableAging returns every balance above zero, optionally of one
// customer, with the posting dates and counts of the returnable containers
// that shipped to that customer (FR-28.16 aging inquiry). The balances are
// per tenant; the shipped containers are read through their packing slip and
// shipment, so a caller scoped to some legal entities ages only from the
// shipments it can see. Cancelled slips do not count: their containers are
// back to CLOSED with no slip.
func (s *Postgres) ReturnableAging(ctx context.Context, customerID string) ([]ReturnableAgingRow, error) {
	out := []ReturnableAgingRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select customer_id::text, container_type, balance::text, last_shipped_at, last_returned_at
			from returnable_packaging_balance
			where balance > 0 and ($1::text = '' or customer_id = nullif($1::text, '')::uuid)
			order by customer_id, container_type`, customerID)
		if err != nil {
			return err
		}
		index := map[string]int{} // customer/container type -> row
		seen := map[string]bool{}
		customers := []string{}
		for rows.Next() {
			var r ReturnableAgingRow
			var bal string
			if err := rows.Scan(&r.CustomerID, &r.ContainerType, &bal, &r.LastShippedAt, &r.LastReturnedAt); err != nil {
				rows.Close()
				return err
			}
			r.Balance = dec(bal)
			if !seen[r.CustomerID] {
				seen[r.CustomerID] = true
				customers = append(customers, r.CustomerID)
			}
			index[r.CustomerID+"/"+r.ContainerType] = len(out)
			out = append(out, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(out) == 0 {
			return err
		}
		// Only slips that raised the balance (rtp_tracked) age it; each carries
		// the rtp_charge_days of its legal entity (90 when none is saved).
		shipped, err := tx.Query(ctx, `select sh.customer_id::text, c.container_type, ps.posting_date, coalesce(les.rtp_charge_days, 90), count(*)
			from container c
			join packing_slip ps on ps.tenant_id = c.tenant_id and ps.id = c.packing_slip_id
			join shipment sh on sh.tenant_id = c.tenant_id and sh.id = c.shipment_id
			left join legal_entity_setting les on les.tenant_id = sh.tenant_id and les.legal_entity_id = sh.legal_entity_id
			where c.returnable and c.packing_slip_id is not null and ps.rtp_tracked and sh.customer_id = any($1::uuid[])
			group by sh.customer_id, c.container_type, ps.posting_date, coalesce(les.rtp_charge_days, 90)`, customers)
		if err != nil {
			return err
		}
		defer shipped.Close()
		for shipped.Next() {
			var customer, containerType string
			var day time.Time
			var n int64
			var chargeDays int
			if err := shipped.Scan(&customer, &containerType, &day, &chargeDays, &n); err != nil {
				return err
			}
			if i, ok := index[customer+"/"+containerType]; ok {
				out[i].Shipped = append(out[i].Shipped, domain.RTPShipped{Date: day, Qty: decimal.NewFromInt(n), ChargeDays: chargeDays})
			}
		}
		return shipped.Err()
	})
	return out, err
}
