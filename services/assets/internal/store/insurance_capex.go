package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
)

// Codes raised by insurance and capex requests.
const (
	CodeCapexNotFound        = "AST-0021"
	CodeCapexVariantNotFound = "AST-0022"
	CodeCapexNumberDup       = "AST-0023"
	CodeIndexSeriesNotFound  = "AST-0024"
	CodePolicyNumberDup      = "AST-0025"
)

// IndexPoint is one year's value of an insurance index series.
type IndexPoint struct {
	Year  int             `json:"year"`
	Value decimal.Decimal `json:"value"`
}

// IndexSeries is a tenant-wide insured-value index per asset class and country
// (FR-37.15, insurance.indexSeries).
type IndexSeries struct {
	Code      string       `json:"code"`
	Country   string       `json:"country"`
	ClassCode string       `json:"classCode"`
	Values    []IndexPoint `json:"values"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

// InsurancePolicy is a policy on one asset (FR-37.15). With an index series,
// IndexedValue is BaseValue × index(ValuationYear) ÷ index(IndexBaseYear)
// rounded to the currency minor unit (BR-37.10, AC-37.9); IndexYearUsed is the
// latest loaded year when the valuation year has no value (AST-0531).
// UnderInsured is set when SumInsured is below 90% of IndexedValue (AST-0532).
type InsurancePolicy struct {
	ID              string           `json:"id"`
	AssetID         string           `json:"assetId"`
	LegalEntityID   string           `json:"legalEntityId"`
	InsurerPartyID  string           `json:"insurerPartyId"`
	PolicyNumber    string           `json:"policyNumber"`
	InsuranceType   string           `json:"insuranceType"`
	CoverageFrom    Date             `json:"coverageFrom"`
	CoverageTo      Date             `json:"coverageTo"`
	Premium         decimal.Decimal  `json:"premium"`
	Currency        string           `json:"currency"`
	Basis           string           `json:"basis"`
	BaseValue       decimal.Decimal  `json:"baseValue"`
	SumInsured      decimal.Decimal  `json:"sumInsured"`
	IndexSeriesCode *string          `json:"indexSeriesCode"`
	IndexBaseYear   *int             `json:"indexBaseYear"`
	ValuationYear   *int             `json:"valuationYear"`
	IndexYearUsed   *int             `json:"indexYearUsed"`
	IndexedValue    *decimal.Decimal `json:"indexedValue"`
	UnderInsured    bool             `json:"underInsured"`
	Warnings        []string         `json:"warnings"`
	CreatedAt       time.Time        `json:"createdAt"`
}

// CapexVariant is one scenario of a capital appropriation request: scope,
// amounts per year, expected in-service date and the depreciation profile
// assumption. TotalAmount is the sum of AmountsByYear.
type CapexVariant struct {
	ID                string                     `json:"id"`
	Position          int                        `json:"position"`
	Description       string                     `json:"description"`
	AmountsByYear     map[string]decimal.Decimal `json:"amountsByYear"`
	TotalAmount       decimal.Decimal            `json:"totalAmount"`
	InServiceDate     Date                       `json:"inServiceDate"`
	ProfileAssumption string                     `json:"profileAssumption"`
}

// CapexRequest is a capital appropriation request (FR-37.17). Submitting it
// selects a variant, records its total as RequestedAmount and routes it to an
// approval level by that amount (BR-37.12, AC-37.11).
type CapexRequest struct {
	ID                string           `json:"id"`
	LegalEntityID     string           `json:"legalEntityId"`
	Number            string           `json:"number"`
	Title             string           `json:"title"`
	RequesterID       string           `json:"requesterId"`
	Status            string           `json:"status"`
	Currency          string           `json:"currency"`
	SelectedVariantID *string          `json:"selectedVariantId"`
	ApprovalLevel     *string          `json:"approvalLevel"`
	RequestedAmount   *decimal.Decimal `json:"requestedAmount"`
	ApprovedAmount    *decimal.Decimal `json:"approvedAmount"`
	// CreatedBy is the user that created the request (null before slice 4);
	// that user may not approve, reject or postpone it (7.37.17).
	CreatedBy *string        `json:"createdBy"`
	Version   int            `json:"version"`
	CreatedAt time.Time      `json:"createdAt"`
	Variants  []CapexVariant `json:"variants,omitempty"`
	// Decisions is the approver trail, oldest first; only the single-request
	// read carries it.
	Decisions []CapexDecisionRecord `json:"decisions,omitempty"`
}

// CapexFilter narrows ListCapexRequests. After is the id of the last request
// of the previous page; Limit is 1..200.
type CapexFilter struct {
	LegalEntityID string
	Status        string
	After         string
	Limit         int
}

// CapexPage is one page of requests ordered by number, without variants.
type CapexPage struct {
	Items      []CapexRequest `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

// SubmitInput submits a Draft request with the variant to approve at the
// version the caller read (If-Match).
type SubmitInput struct {
	VariantID string
	Version   int
}

// UpsertIndexSeries creates or replaces a series and upserts its yearly values;
// years not in the request keep their value. It is naturally idempotent.
func (s *Postgres) UpsertIndexSeries(ctx context.Context, in IndexSeries) (IndexSeries, error) {
	p := principal(ctx)
	var out IndexSeries
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into asset_index_series (tenant_id, code, country, class_code) values ($1, $2, $3, $4)
			on conflict (tenant_id, code) do update set country = excluded.country, class_code = excluded.class_code, updated_at = now()`,
			p.TenantID, in.Code, in.Country, in.ClassCode); err != nil {
			return err
		}
		for _, v := range in.Values {
			if _, err := tx.Exec(ctx, `insert into asset_index_value (tenant_id, series_code, year, value) values ($1, $2, $3, $4)
				on conflict (tenant_id, series_code, year) do update set value = excluded.value`, p.TenantID, in.Code, v.Year, v.Value); err != nil {
				return err
			}
		}
		series, err := loadIndexSeries(ctx, tx, p.TenantID, in.Code)
		if err != nil {
			return err
		}
		if len(series) == 0 {
			return notFound(CodeIndexSeriesNotFound, "no index series "+in.Code)
		}
		out = series[0]
		return nil
	})
	return out, err
}

// ListIndexSeries returns every series of the tenant with its values.
func (s *Postgres) ListIndexSeries(ctx context.Context) ([]IndexSeries, error) {
	p := principal(ctx)
	var out []IndexSeries
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadIndexSeries(ctx, tx, p.TenantID, "")
		return err
	})
	return out, err
}

// loadIndexSeries reads the series (only code when set) ordered by code, each
// with its values ordered by year.
func loadIndexSeries(ctx context.Context, tx pgx.Tx, tenantID, code string) ([]IndexSeries, error) {
	rows, err := tx.Query(ctx, `select code, country, class_code, updated_at from asset_index_series
		where tenant_id = $1 and ($2::text = '' or code = $2) order by code`, tenantID, code)
	if err != nil {
		return nil, err
	}
	out := []IndexSeries{}
	index := map[string]int{}
	for rows.Next() {
		var s IndexSeries
		if err := rows.Scan(&s.Code, &s.Country, &s.ClassCode, &s.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		s.Values = []IndexPoint{}
		index[s.Code] = len(out)
		out = append(out, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	vals, err := tx.Query(ctx, `select series_code, year, value from asset_index_value
		where tenant_id = $1 and ($2::text = '' or series_code = $2) order by series_code, year`, tenantID, code)
	if err != nil {
		return nil, err
	}
	defer vals.Close()
	for vals.Next() {
		var series string
		var pt IndexPoint
		if err := vals.Scan(&series, &pt.Year, &pt.Value); err != nil {
			return nil, err
		}
		if i, ok := index[series]; ok {
			out[i].Values = append(out[i].Values, pt)
		}
	}
	return out, vals.Err()
}

// decorate sets the derived flags and warnings of a stored policy.
func (p *InsurancePolicy) decorate() {
	p.Warnings = []string{}
	if p.IndexedValue == nil {
		return
	}
	if p.ValuationYear != nil && p.IndexYearUsed != nil && *p.ValuationYear != *p.IndexYearUsed {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s: index %s has no value for %d; using %d",
			domain.CodeIndexMissing, deref(p.IndexSeriesCode), *p.ValuationYear, *p.IndexYearUsed))
	}
	p.UnderInsured = domain.UnderInsured(p.SumInsured, *p.IndexedValue, p.Currency)
	if p.UnderInsured {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s: the asset is under-insured: %s vs %s indexed",
			domain.CodeUnderInsured, p.SumInsured.StringFixed(2), p.IndexedValue.StringFixed(2)))
	}
}

// AddInsurancePolicy records a policy on an asset that is not yet acquired or
// Open. With an index series it computes the indexed value for the valuation
// year (BR-37.10); a missing year falls back to the latest loaded year with an
// AST-0531 warning.
func (s *Postgres) AddInsurancePolicy(ctx context.Context, assetID string, in InsurancePolicy, idempotencyKey string) (InsurancePolicy, error) {
	p := principal(ctx)
	var out InsurancePolicy
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		request := struct {
			AssetID string
			Input   InsurancePolicy
		}{assetID, in}
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, idempotencyKey, "asset.insurance-policy.add", request, func() (InsurancePolicy, error) {
			if err := lockOpenAsset(ctx, tx, p.TenantID, assetID, true); err != nil {
				return InsurancePolicy{}, err
			}
			if err := domain.CheckCoverage(in.CoverageFrom.Time, in.CoverageTo.Time); err != nil {
				return InsurancePolicy{}, err
			}
			var indexed *decimal.Decimal
			var used *int
			if in.IndexSeriesCode != nil {
				series, err := loadIndexSeries(ctx, tx, p.TenantID, *in.IndexSeriesCode)
				if err != nil {
					return InsurancePolicy{}, err
				}
				if len(series) == 0 {
					return InsurancePolicy{}, notFound(CodeIndexSeriesNotFound, "no index series "+*in.IndexSeriesCode)
				}
				values := map[int]decimal.Decimal{}
				for _, pt := range series[0].Values {
					values[pt.Year] = pt.Value
				}
				base, ok := values[*in.IndexBaseYear]
				if !ok {
					return InsurancePolicy{}, &domain.Error{Code: domain.CodeInvalidInsurance,
						Detail: "index " + *in.IndexSeriesCode + " has no value for the base year " + strconv.Itoa(*in.IndexBaseYear)}
				}
				iv, _ := domain.IndexFor(values, *in.ValuationYear)
				v, err := domain.IndexedValue(in.BaseValue, iv.Value, base, in.Currency)
				if err != nil {
					return InsurancePolicy{}, err
				}
				indexed, used = &v, &iv.Year
			}
			id := ids.New()
			if _, err := tx.Exec(ctx, `insert into asset_insurance_policy (tenant_id, id, asset_id, legal_entity_id, insurer_party_id, policy_number,
				insurance_type, coverage_from, coverage_to, premium_amount, currency, basis, base_value, sum_insured, index_series_code, index_base_year,
				valuation_year, index_year_used, indexed_value)
				select $1, $2, a.id, a.legal_entity_id, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18
				from asset a where a.tenant_id = $1 and a.id = $3`,
				p.TenantID, id, assetID, in.InsurerPartyID, in.PolicyNumber, in.InsuranceType, in.CoverageFrom, in.CoverageTo, in.Premium,
				in.Currency, in.Basis, in.BaseValue, in.SumInsured, in.IndexSeriesCode, in.IndexBaseYear, in.ValuationYear, used, indexed); err != nil {
				if uniqueConstraint(err) != "" {
					return InsurancePolicy{}, httpx.NewProblem(http.StatusConflict, CodePolicyNumberDup, "the asset already has a policy with this number")
				}
				return InsurancePolicy{}, err
			}
			policies, err := loadPolicies(ctx, tx, p.TenantID, assetID, id)
			if err != nil {
				return InsurancePolicy{}, err
			}
			return policies[0], nil
		})
		return err
	})
	return out, err
}

// ListInsurancePolicies returns an asset's policies ordered by coverage start.
func (s *Postgres) ListInsurancePolicies(ctx context.Context, assetID string) ([]InsurancePolicy, error) {
	p := principal(ctx)
	var out []InsurancePolicy
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadAsset(ctx, tx, p.TenantID, assetID); err != nil {
			return err
		}
		var err error
		out, err = loadPolicies(ctx, tx, p.TenantID, assetID, "")
		return err
	})
	return out, err
}

func loadPolicies(ctx context.Context, tx pgx.Tx, tenantID, assetID, id string) ([]InsurancePolicy, error) {
	rows, err := tx.Query(ctx, `select id, asset_id, legal_entity_id, insurer_party_id, policy_number, insurance_type, coverage_from, coverage_to,
		premium_amount, currency, basis, base_value, sum_insured, index_series_code, index_base_year, valuation_year, index_year_used, indexed_value, created_at
		from asset_insurance_policy where tenant_id = $1 and asset_id = $2 and ($3::text = '' or id::text = $3)
		order by coverage_from, policy_number`, tenantID, assetID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InsurancePolicy{}
	for rows.Next() {
		var p InsurancePolicy
		if err := rows.Scan(&p.ID, &p.AssetID, &p.LegalEntityID, &p.InsurerPartyID, &p.PolicyNumber, &p.InsuranceType, &p.CoverageFrom, &p.CoverageTo,
			&p.Premium, &p.Currency, &p.Basis, &p.BaseValue, &p.SumInsured, &p.IndexSeriesCode, &p.IndexBaseYear, &p.ValuationYear, &p.IndexYearUsed,
			&p.IndexedValue, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.decorate()
		out = append(out, p)
	}
	return out, rows.Err()
}

const capexColumns = `id, legal_entity_id, number, title, requester_id, status, currency, selected_variant_id, approval_level, requested_amount,
	approved_amount, created_by, version, created_at`

func scanCapex(row pgx.Row) (CapexRequest, error) {
	var c CapexRequest
	err := row.Scan(&c.ID, &c.LegalEntityID, &c.Number, &c.Title, &c.RequesterID, &c.Status, &c.Currency, &c.SelectedVariantID,
		&c.ApprovalLevel, &c.RequestedAmount, &c.ApprovedAmount, &c.CreatedBy, &c.Version, &c.CreatedAt)
	return c, err
}

// CreateCapexRequest stores a request in Draft with its variants; numbers are
// unique per legal entity (AST-0023).
func (s *Postgres) CreateCapexRequest(ctx context.Context, in CapexRequest, idempotencyKey string) (CapexRequest, error) {
	p := principal(ctx)
	var out CapexRequest
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, idempotencyKey, "capex-request.create", in, func() (CapexRequest, error) {
			id := ids.New()
			if _, err := tx.Exec(ctx, `insert into capex_request (tenant_id, id, legal_entity_id, number, title, requester_id, currency, created_by)
				values ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''))`, p.TenantID, id, in.LegalEntityID, in.Number, in.Title, in.RequesterID, in.Currency, p.UserID); err != nil {
				return CapexRequest{}, err
			}
			for i, v := range in.Variants {
				amounts, err := json.Marshal(v.AmountsByYear)
				if err != nil {
					return CapexRequest{}, err
				}
				total := decimal.Zero
				for _, a := range v.AmountsByYear {
					total = total.Add(a)
				}
				if _, err := tx.Exec(ctx, `insert into capex_request_variant (tenant_id, id, request_id, legal_entity_id, position, description,
					amounts_by_year, total_amount, in_service_date, profile_assumption) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
					p.TenantID, ids.New(), id, in.LegalEntityID, i+1, v.Description, amounts, total, v.InServiceDate, v.ProfileAssumption); err != nil {
					return CapexRequest{}, err
				}
			}
			return loadCapex(ctx, tx, p.TenantID, id)
		})
		return err
	})
	if uniqueConstraint(err) == "capex_request_number_unique" {
		return CapexRequest{}, httpx.NewProblem(http.StatusConflict, CodeCapexNumberDup, "a capex request with this number already exists in the legal entity")
	}
	return out, err
}

// ListCapexRequests returns one page of requests ordered by number.
func (s *Postgres) ListCapexRequests(ctx context.Context, f CapexFilter) (CapexPage, error) {
	p := principal(ctx)
	page := CapexPage{Items: []CapexRequest{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+capexColumns+` from capex_request r
			where r.tenant_id = $1
			  and ($2::text = '' or r.legal_entity_id::text = $2)
			  and ($3::text = '' or r.status = $3)
			  and ($4::text = '' or (r.number, r.id) > (select c.number, c.id from capex_request c where c.tenant_id = $1 and c.id::text = $4))
			order by r.number, r.id limit $5`, p.TenantID, f.LegalEntityID, f.Status, f.After, f.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCapex(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, c)
		}
		return rows.Err()
	})
	if len(page.Items) > f.Limit {
		page.Items = page.Items[:f.Limit]
		page.NextCursor = page.Items[f.Limit-1].ID
	}
	return page, err
}

// GetCapexRequest reads a request with its variants.
func (s *Postgres) GetCapexRequest(ctx context.Context, id string) (CapexRequest, error) {
	p := principal(ctx)
	var out CapexRequest
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadCapex(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// SubmitCapexRequest moves a Draft request through Submitted to InApproval with
// the selected variant and routes it to an approval level by the variant's
// total (BR-37.12, AC-37.11). The approval itself runs in the wave-4 workflow.
func (s *Postgres) SubmitCapexRequest(ctx context.Context, id string, in SubmitInput) (CapexRequest, error) {
	p := principal(ctx)
	var out CapexRequest
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		var version int
		err := tx.QueryRow(ctx, `select status, version from capex_request where tenant_id = $1 and id = $2 for update`, p.TenantID, id).Scan(&status, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeCapexNotFound, "no capex request with id "+id)
		}
		if err != nil {
			return err
		}
		if version != in.Version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
				fmt.Sprintf("the request changed since you read it (version %d, current %d); reload and try again", in.Version, version))
		}
		if err := domain.TransitionCapex(domain.CapexStatus(status), domain.CapexSubmitted); err != nil {
			return err
		}
		var total decimal.Decimal
		err = tx.QueryRow(ctx, `select total_amount from capex_request_variant where tenant_id = $1 and request_id = $2 and id = $3`,
			p.TenantID, id, in.VariantID).Scan(&total)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeCapexVariantNotFound, "no variant "+in.VariantID+" on capex request "+id)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update capex_request set status = $3, selected_variant_id = $4, approval_level = $5, requested_amount = $6,
			version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, id, string(domain.CapexInApproval), in.VariantID, domain.ApprovalLevel(total), total); err != nil {
			return err
		}
		out, err = loadCapex(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

func loadCapex(ctx context.Context, tx pgx.Tx, tenantID, id string) (CapexRequest, error) {
	out, err := scanCapex(tx.QueryRow(ctx, `select `+capexColumns+` from capex_request where tenant_id = $1 and id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return CapexRequest{}, notFound(CodeCapexNotFound, "no capex request with id "+id)
	}
	if err != nil {
		return CapexRequest{}, err
	}
	rows, err := tx.Query(ctx, `select id, position, description, amounts_by_year, total_amount, in_service_date, profile_assumption
		from capex_request_variant where tenant_id = $1 and request_id = $2 order by position`, tenantID, id)
	if err != nil {
		return CapexRequest{}, err
	}
	defer rows.Close()
	out.Variants = []CapexVariant{}
	for rows.Next() {
		var v CapexVariant
		var amounts []byte
		if err := rows.Scan(&v.ID, &v.Position, &v.Description, &amounts, &v.TotalAmount, &v.InServiceDate, &v.ProfileAssumption); err != nil {
			return CapexRequest{}, err
		}
		if err := json.Unmarshal(amounts, &v.AmountsByYear); err != nil {
			return CapexRequest{}, err
		}
		out.Variants = append(out.Variants, v)
	}
	if err := rows.Err(); err != nil {
		return CapexRequest{}, err
	}
	out.Decisions, err = loadCapexDecisions(ctx, tx, tenantID, id)
	return out, err
}
