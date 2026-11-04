// Package store persists condition records. Every write runs in a tenant-scoped
// transaction (RLS) and enqueues its event in the same transaction.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// Problem codes raised by the store (message catalog PRC-*).
const (
	CodeRecordNotFound  = "PRC-0030"
	CodeStateInvalid    = "PRC-0031"
	CodeVersionConflict = "conflict_version"
	CodeIdempotencyUse  = "idempotency_key_reuse"
	eventSource         = "pricing"
	MaxListLimit        = 100
	// MaxExportRows bounds one trade agreements CSV export (ExportRecords).
	MaxExportRows = 50000
)

// Entry is a stored condition record with its owning legal entity.
type Entry struct {
	domain.Record
	LegalEntityID string
}

// Filter narrows a record list. AsOf (when set) keeps records valid on that day.
// Customer and Product match the record's customer or product key exactly.
type Filter struct {
	LegalEntityID string
	Status        string
	Type          string
	Customer      string
	Product       string
	// CampaignID keeps the records of one campaign (FR-26.10).
	CampaignID string
	AsOf       *time.Time
	// ValidToFrom and ValidToTo keep records whose last valid day falls in the range
	// (both inclusive). With either set the list is ordered by valid_to, then id, so
	// the first page is the records that end soonest (the workspace's expiring list).
	ValidToFrom *time.Time
	ValidToTo   *time.Time
	Limit       int
}

// ActiveQuery selects the records one resolve can use: ACTIVE in the legal entity and
// currency on AsOf. CustomerGroupID is the document customer's group; a record of a
// campaign that targets another group is left out (FR-26.10).
type ActiveQuery struct {
	LegalEntityID   string
	Currency        string
	CustomerGroupID string
	AsOf            time.Time
}

// Store is what the API needs. Postgres implements it; tests use an in-memory fake.
type Store interface {
	CreateRecord(ctx context.Context, in Entry, idempotencyKey string) (Entry, error)
	GetRecord(ctx context.Context, id string) (Entry, error)
	ListRecords(ctx context.Context, f Filter) ([]Entry, error)
	ExportRecords(ctx context.Context, f Filter, maxRows int, start func(truncated bool) error, row func(Entry) error) error
	ActiveRecords(ctx context.Context, q ActiveQuery) ([]domain.Record, error)
	ActivateRecord(ctx context.Context, id string, version int, idempotencyKey string) (Entry, error)
	WithdrawRecord(ctx context.Context, id string, version int, reason, idempotencyKey string) (Entry, error)
	ExpireRecords(ctx context.Context, legalEntityID string, asOf time.Time) ([]Entry, error)
	WorkspaceSummary(ctx context.Context, legalEntityID string, asOf time.Time) (WorkspaceSummary, error)
	CreateJournal(ctx context.Context, in JournalInput, idempotencyKey string) (Journal, error)
	ListJournals(ctx context.Context, f JournalFilter) ([]Journal, error)
	GetJournal(ctx context.Context, id string) (Journal, error)
	StreamJournal(ctx context.Context, id string, start func(Journal) error, line func(domain.JournalLine) error) error
	ValidateJournal(ctx context.Context, id string) (Journal, error)
	PostJournal(ctx context.Context, id, idempotencyKey string) (Journal, error)
	ReverseJournal(ctx context.Context, id, idempotencyKey string) (Journal, error)
	GetParameters(ctx context.Context, legalEntityID string) (Parameters, error)
	SetParameters(ctx context.Context, in Parameters, version int, idempotencyKey string) (Parameters, error)
	GetTenantParameters(ctx context.Context) (TenantParameters, error)
	SetTenantParameters(ctx context.Context, in TenantParameters, version int, idempotencyKey string) (TenantParameters, error)
	CreateAgreement(ctx context.Context, a domain.RebateAgreement, idempotencyKey string) (domain.RebateAgreement, error)
	GetAgreement(ctx context.Context, id string) (domain.RebateAgreement, error)
	ListAgreements(ctx context.Context, f AgreementFilter) ([]domain.RebateAgreement, error)
	AccrueInvoiceLine(ctx context.Context, in AccrualInput) (Accrual, bool, error)
	ListAccruals(ctx context.Context, f AccrualFilter) ([]Accrual, error)
	RebateStatementInput(ctx context.Context, agreementID, periodKey string) (domain.RebateAgreement, domain.StatementInput, error)
	PreviewVendorClaim(ctx context.Context, agreementID, periodKey string) (VendorClaimPreview, error)
	CreateVendorClaim(ctx context.Context, in VendorClaimInput, idempotencyKey string) (VendorClaim, error)
	GetVendorClaim(ctx context.Context, id string) (VendorClaim, error)
	ListVendorClaims(ctx context.Context, f ClaimFilter) ([]VendorClaim, error)
	ApproveVendorClaim(ctx context.Context, id string, version int, idempotencyKey string) (VendorClaim, error)
	RecomputeRetro(ctx context.Context, in RetroInput) (RetroResult, error)
	CreateSalesGroup(ctx context.Context, g SalesGroup, idempotencyKey string) (SalesGroup, error)
	GetSalesGroup(ctx context.Context, id string) (SalesGroup, error)
	ListSalesGroups(ctx context.Context, legalEntityID string, limit int) ([]SalesGroup, error)
	CreateCommissionRule(ctx context.Context, r domain.CommissionRule, idempotencyKey string) (CommissionRuleRow, error)
	ListCommissionRules(ctx context.Context, salesGroupID string, limit int) ([]CommissionRuleRow, error)
	PostCommission(ctx context.Context, c CommissionPosting) ([]CommissionTxn, bool, error)
	ListCommissionTransactions(ctx context.Context, f CommissionFilter) ([]CommissionTxn, error)
	SettlementTotals(ctx context.Context, legalEntityID string, from, to time.Time) ([]domain.SettlementRow, error)
	CreateFund(ctx context.Context, f domain.Fund, idempotencyKey string) (domain.Fund, error)
	GetFund(ctx context.Context, id string) (domain.Fund, error)
	ListFunds(ctx context.Context, legalEntityID string, limit int) ([]domain.Fund, error)
	ReviseFund(ctx context.Context, rev FundRevision, idempotencyKey string) (domain.Fund, error)
	AttributeFund(ctx context.Context, a FundAttribution, idempotencyKey string) (FundPosition, error)
	CloseFund(ctx context.Context, id string, version int, idempotencyKey string) (domain.Fund, error)
	ReopenFund(ctx context.Context, id string, version int, idempotencyKey string) (domain.Fund, error)
	FundUsage(ctx context.Context, id string) (domain.Fund, []FundUsageLine, error)
	CreatePromotion(ctx context.Context, p domain.PromotionPlan, idempotencyKey string) (domain.PromotionPlan, error)
	GetPromotion(ctx context.Context, id string) (domain.PromotionPlan, error)
	ListPromotions(ctx context.Context, legalEntityID string, limit int) ([]domain.PromotionPlan, error)
	RecordPromotionActuals(ctx context.Context, id string, a domain.PromotionActuals) (domain.PromotionPlan, error)
	CreateCampaign(ctx context.Context, c domain.Campaign, idempotencyKey string) (domain.Campaign, error)
	GetCampaign(ctx context.Context, id string) (domain.Campaign, error)
	ListCampaigns(ctx context.Context, legalEntityID string, limit int) ([]domain.Campaign, error)
	SetCampaignKilled(ctx context.Context, id string, version int, killed bool, idempotencyKey string) (domain.Campaign, error)
	ReviseCampaign(ctx context.Context, rev domain.CampaignRevision, idempotencyKey string) (domain.Campaign, error)
	CreateProcedure(ctx context.Context, p domain.Procedure, idempotencyKey string) (domain.Procedure, error)
	GetProcedure(ctx context.Context, id string) (domain.Procedure, error)
	ListProcedures(ctx context.Context, legalEntityID string, limit int) ([]domain.Procedure, error)
	ReviseProcedure(ctx context.Context, id string, revision int, steps []domain.ProcedureStep, idempotencyKey string) (domain.Procedure, error)
	ActivateProcedure(ctx context.Context, id string, idempotencyKey string) (domain.Procedure, error)
	SetProcedureAssignment(ctx context.Context, in ProcedureAssignment, version int, idempotencyKey string) (ProcedureAssignment, error)
	ListProcedureAssignments(ctx context.Context, legalEntityID string) ([]ProcedureAssignment, error)
	ClearProcedureAssignment(ctx context.Context, legalEntityID, salesChannel string, version int, idempotencyKey string) error
	DetermineProcedure(ctx context.Context, legalEntityID, salesChannel, name string) (*domain.Procedure, error)
	CreateFreeGoodsRule(ctx context.Context, r domain.FreeGoodsDef, idempotencyKey string) (domain.FreeGoodsDef, error)
	GetFreeGoodsRule(ctx context.Context, id string) (domain.FreeGoodsDef, error)
	ListFreeGoodsRules(ctx context.Context, legalEntityID, productID string, limit int) ([]domain.FreeGoodsDef, error)
	ActiveFreeGoodsRules(ctx context.Context, legalEntityID string, productIDs []string, asOf time.Time) ([]domain.FreeGoodsDef, error)
	WithdrawFreeGoodsRule(ctx context.Context, id string, version int, idempotencyKey string) (domain.FreeGoodsDef, error)
	ReviseAgreement(ctx context.Context, rev AgreementRevision, idempotencyKey string) (domain.RebateAgreement, error)
	CloseAgreement(ctx context.Context, id string, version int, closeDate time.Time, idempotencyKey string) (domain.RebateAgreement, error)
	ReopenAgreement(ctx context.Context, id string, version int, validTo time.Time, idempotencyKey string) (domain.RebateAgreement, error)
	CreateBrokerContract(ctx context.Context, c domain.BrokerContract, idempotencyKey string) (domain.BrokerContract, error)
	GetBrokerContract(ctx context.Context, id string) (domain.BrokerContract, error)
	ListBrokerContracts(ctx context.Context, f ContractFilter) ([]domain.BrokerContract, error)
	CloseBrokerContract(ctx context.Context, id string, version int, idempotencyKey string) (domain.BrokerContract, error)
	CreateRoyaltyAgreement(ctx context.Context, a domain.RoyaltyAgreement, idempotencyKey string) (domain.RoyaltyAgreement, error)
	GetRoyaltyAgreement(ctx context.Context, id string) (domain.RoyaltyAgreement, error)
	ListRoyaltyAgreements(ctx context.Context, f ContractFilter) ([]domain.RoyaltyAgreement, error)
	CloseRoyaltyAgreement(ctx context.Context, id string, version int, idempotencyKey string) (domain.RoyaltyAgreement, error)
	AccrueContractLine(ctx context.Context, in ContractAccrualInput) (ContractAccrual, bool, error)
	ListContractAccruals(ctx context.Context, f ContractAccrualFilter) ([]ContractAccrual, error)
	CloseRoyaltyPeriod(ctx context.Context, agreementID, periodKey string) (RoyaltyClose, error)
	PreviewContractClaim(ctx context.Context, kind, contractID, periodKey string) (ContractClaimPreview, error)
	CreateContractClaim(ctx context.Context, kind, contractID, periodKey, idempotencyKey string) (ContractClaim, error)
	GetContractClaim(ctx context.Context, id string) (ContractClaim, error)
	ListContractClaims(ctx context.Context, f ContractClaimFilter) ([]ContractClaim, error)
	ApproveContractClaim(ctx context.Context, id string, version int, idempotencyKey string) (ContractClaim, error)
	CreateCommodityBasis(ctx context.Context, b domain.CommodityBasis, idempotencyKey string) (domain.CommodityBasis, error)
	GetCommodityBasis(ctx context.Context, id string) (domain.CommodityBasis, error)
	ListCommodityBases(ctx context.Context, legalEntityID string, limit int) ([]domain.CommodityBasis, error)
	RecordIndexValue(ctx context.Context, v domain.IndexValue, idempotencyKey string) (domain.IndexValue, error)
	ListIndexValues(ctx context.Context, basisID string, limit int) ([]domain.IndexValue, error)
	CreateCommodityCalc(ctx context.Context, c domain.CommodityCalc, idempotencyKey string) (domain.CommodityCalc, error)
	GetCommodityCalc(ctx context.Context, id string) (domain.CommodityCalc, error)
	ListCommodityCalcs(ctx context.Context, legalEntityID, basisID string, limit int) ([]domain.CommodityCalc, error)
	WithdrawCommodityCalc(ctx context.Context, id string, version int, idempotencyKey string) (domain.CommodityCalc, error)
	PreviewCommodityRun(ctx context.Context, calcID string, asOf time.Time) (domain.CommodityCalc, domain.RunPlan, error)
	RunCommodityCalc(ctx context.Context, calcID string, asOf time.Time, idempotencyKey string) (CommodityRun, error)
	ListCommodityRuns(ctx context.Context, calcID string, limit int) ([]CommodityRun, error)
	Ping(ctx context.Context) error
}

// Postgres is the production store.
type Postgres struct{ pool *pgxpool.Pool }

// New wraps a pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database is reachable.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

const recordColumns = `id, legal_entity_id, condition_type, customer_kind, customer_key, product_kind, product_key, variant,
	currency, unit, from_quantity::text, amount::text, disc_pct_1::text, disc_pct_2::text, valid_from, valid_to,
	search_next, exclusive, status, version, coalesce(campaign_id::text, '')`

// KeyHash is the lookup key for overlap checks; it mirrors the domain key tuple. The
// campaign joins the tuple only when set, so records without one keep their hash.
func KeyHash(r domain.Record) string {
	parts := []string{string(r.Type), string(r.CustomerKind), r.CustomerKey, string(r.ProductKind), r.ProductKey, r.Variant, r.Currency, r.Unit, r.FromQuantity.String()}
	if r.CampaignID != "" {
		parts = append(parts, "campaign="+r.CampaignID)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func scanEntry(row pgx.Row) (Entry, error) {
	var e Entry
	var ctype, ckind, pkind string
	var from, amount, p1, p2 string
	err := row.Scan(&e.ID, &e.LegalEntityID, &ctype, &ckind, &e.CustomerKey, &pkind, &e.ProductKey, &e.Variant,
		&e.Currency, &e.Unit, &from, &amount, &p1, &p2, &e.ValidFrom, &e.ValidTo, &e.SearchNext, &e.Exclusive, &e.Status, &e.Version,
		&e.CampaignID)
	if err != nil {
		return Entry{}, err
	}
	e.Type, e.CustomerKind, e.ProductKind = domain.ConditionType(ctype), domain.CustomerKind(ckind), domain.ProductKind(pkind)
	e.FromQuantity, e.Amount = decimal.RequireFromString(from), decimal.RequireFromString(amount)
	e.DiscPct1, e.DiscPct2 = decimal.RequireFromString(p1), decimal.RequireFromString(p2)
	return e, nil
}

func notFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, CodeRecordNotFound, "no condition record "+id)
}

// CreateRecord inserts a DRAFT record. A repeated Idempotency-Key returns the
// original row; the insert uses ON CONFLICT so two concurrent requests with the
// same key cannot both create a record or fail with a unique violation.
func (s *Postgres) CreateRecord(ctx context.Context, in Entry, idempotencyKey string) (Entry, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out Entry
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if in.CampaignID != "" {
			if err := checkRecordCampaign(ctx, tx, in); err != nil {
				return err
			}
		}
		created, err := insertDraft(ctx, tx, p.TenantID, in, key)
		if err == nil {
			out = created
			return nil
		}
		if key == nil || !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		replay, err := scanEntry(tx.QueryRow(ctx, `select `+recordColumns+` from condition_record where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
		if err != nil {
			return err
		}
		if !sameBody(replay, in) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request body")
		}
		out = replay
		return nil
	})
	return out, err
}

func insertDraft(ctx context.Context, tx pgx.Tx, tenantID string, in Entry, key *string) (Entry, error) {
	return scanEntry(tx.QueryRow(ctx, `insert into condition_record (tenant_id, id, legal_entity_id, condition_type, customer_kind, customer_key,
		product_kind, product_key, variant, currency, unit, from_quantity, amount, disc_pct_1, disc_pct_2, valid_from, valid_to,
		search_next, exclusive, status, version, key_hash, idempotency_key, campaign_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::numeric, $13::numeric, $14::numeric, $15::numeric, $16, $17, $18, $19, 'DRAFT', 1, $20, $21,
			nullif($22, '')::uuid)
		on conflict (tenant_id, idempotency_key) do nothing
		returning `+recordColumns,
		tenantID, ids.New(), in.LegalEntityID, string(in.Type), string(in.CustomerKind), in.CustomerKey, string(in.ProductKind), in.ProductKey,
		in.Variant, in.Currency, in.Unit, in.FromQuantity.String(), in.Amount.String(), in.DiscPct1.String(), in.DiscPct2.String(),
		in.ValidFrom, in.ValidTo, in.SearchNext, in.Exclusive, KeyHash(in.Record), key, in.CampaignID))
}

// sameBody reports whether a replayed key carries the same economic content.
func sameBody(a, b Entry) bool {
	return KeyHash(a.Record) == KeyHash(b.Record) && a.Amount.Equal(b.Amount) && a.DiscPct1.Equal(b.DiscPct1) &&
		a.DiscPct2.Equal(b.DiscPct2) && a.LegalEntityID == b.LegalEntityID && a.SearchNext == b.SearchNext && a.Exclusive == b.Exclusive &&
		dayStr(a.ValidFrom) == dayStr(b.ValidFrom) && dayStr(a.ValidTo) == dayStr(b.ValidTo)
}

func dayStr(t time.Time) string { return t.Format("2006-01-02") }

// GetRecord reads one record.
func (s *Postgres) GetRecord(ctx context.Context, id string) (Entry, error) {
	var e Entry
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		e, err = scanEntry(tx.QueryRow(ctx, `select `+recordColumns+` from condition_record where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(id)
		}
		return err
	})
	return e, err
}

// recordQuery is the filtered select shared by ListRecords and ExportRecords: records ordered
// by id (by valid_to, then id, when a valid-to range is given), $4 being the row limit.
const recordQuery = ` from condition_record
	where ($1::text = '' or legal_entity_id::text = $1) and ($2::text = '' or status = $2)
	and ($3::date is null or (valid_from <= $3::date and valid_to >= $3::date))
	and ($5::text = '' or condition_type = $5) and ($6::text = '' or customer_key = $6) and ($7::text = '' or product_key = $7)
	and ($8::text = '' or campaign_id::text = $8)
	and ($9::date is null or valid_to >= $9::date) and ($10::date is null or valid_to <= $10::date)
	order by case when $9::date is not null or $10::date is not null then valid_to end, id limit $4`

func recordArgs(f Filter, limit int) []any {
	optDay := func(t *time.Time) *string {
		if t == nil {
			return nil
		}
		day := dayStr(*t)
		return &day
	}
	return []any{f.LegalEntityID, f.Status, optDay(f.AsOf), limit, f.Type, f.Customer, f.Product, f.CampaignID,
		optDay(f.ValidToFrom), optDay(f.ValidToTo)}
}

// eachRecord runs the record query in tx and calls fn per row.
func eachRecord(ctx context.Context, tx pgx.Tx, f Filter, limit int, fn func(Entry) error) error {
	rows, err := tx.Query(ctx, `select `+recordColumns+recordQuery, recordArgs(f, limit)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ListRecords returns records ordered by id (by valid_to, then id, when a valid-to range
// is given), optionally filtered by status, validity and the last valid day.
func (s *Postgres) ListRecords(ctx context.Context, f Filter) ([]Entry, error) {
	if f.Limit <= 0 || f.Limit > MaxListLimit {
		f.Limit = MaxListLimit
	}
	out := []Entry{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return eachRecord(ctx, tx, f, f.Limit, func(e Entry) error {
			out = append(out, e)
			return nil
		})
	})
	return out, err
}

// ExportRecords streams the records that match f's filters (f.Limit is ignored) in list
// order, at most maxRows of them, without holding them in memory (the trade agreements CSV
// export). start runs once before the first row, with truncated=true when more than maxRows
// records match, so the caller can set its headers; row runs per record. A start or row
// error stops the read and is returned.
func (s *Postgres) ExportRecords(ctx context.Context, f Filter, maxRows int, start func(truncated bool) error, row func(Entry) error) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var matched int
		if err := tx.QueryRow(ctx, `select count(*) from (select 1`+recordQuery+`) t`, recordArgs(f, maxRows+1)...).Scan(&matched); err != nil {
			return err
		}
		if err := start(matched > maxRows); err != nil {
			return err
		}
		return eachRecord(ctx, tx, f, maxRows, row)
	})
}

// ActiveRecords loads the ACTIVE records a resolve needs: one legal entity, one
// currency, valid on AsOf. A record of a campaign stays only while the campaign is live
// on AsOf and targets the customer's group (FR-26.10): the kill switch takes effect on
// the next resolve (campaign_kill_switch_effect NEXT_RESOLVE). The engine applies the key
// and quantity filters.
func (s *Postgres) ActiveRecords(ctx context.Context, q ActiveQuery) ([]domain.Record, error) {
	out := []domain.Record{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+recordColumns+` from condition_record
			where legal_entity_id::text = $1 and currency = $2 and status = 'ACTIVE' and valid_from <= $3::date and valid_to >= $3::date`,
			q.LegalEntityID, q.Currency, dayStr(q.AsOf))
		if err != nil {
			return err
		}
		var recs []domain.Record
		campaignIDs := map[string]bool{}
		for rows.Next() {
			e, err := scanEntry(rows)
			if err != nil {
				rows.Close()
				return err
			}
			recs = append(recs, e.Record)
			if e.CampaignID != "" {
				campaignIDs[e.CampaignID] = true
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		usable := map[string]bool{}
		if len(campaignIDs) > 0 {
			ids := make([]string, 0, len(campaignIDs))
			for id := range campaignIDs {
				ids = append(ids, id)
			}
			campaigns, err := campaignsByID(ctx, tx, ids)
			if err != nil {
				return err
			}
			for _, c := range campaigns {
				usable[c.ID] = domain.CampaignLive(c, q.AsOf) && domain.CampaignTargets(c, q.CustomerGroupID)
			}
		}
		for _, r := range recs {
			if r.CampaignID == "" || usable[r.CampaignID] {
				out = append(out, r)
			}
		}
		return nil
	})
	return out, err
}

// lockRecord reads a record FOR UPDATE, so lifecycle commands on one record run one at a time.
func lockRecord(ctx context.Context, tx pgx.Tx, id string) (Entry, error) {
	cur, err := scanEntry(tx.QueryRow(ctx, `select `+recordColumns+` from condition_record where id = $1 for update`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, notFound(id)
	}
	return cur, err
}

// commandHash identifies a lifecycle command for Idempotency-Key replay.
func commandHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// replayCommand returns the stored response when key was already used for the same
// command, and idempotency_key_reuse when it was used for a different one. It runs
// after lockRecord, so a concurrent retry waits for the first call and then replays it.
func replayCommand(ctx context.Context, tx pgx.Tx, tenantID, key, hash string) (Entry, bool, error) {
	var e Entry
	ok, err := replayJSON(ctx, tx, tenantID, key, hash, &e)
	return e, ok, err
}

// rememberCommand stores the response of a lifecycle command under its key.
func rememberCommand(ctx context.Context, tx pgx.Tx, tenantID, key, hash string, e Entry) error {
	return rememberJSON(ctx, tx, tenantID, key, hash, e)
}

// replayJSON decodes the response stored under key into out and reports whether one
// existed. A key stored for a different command hash is idempotency_key_reuse.
func replayJSON(ctx context.Context, tx pgx.Tx, tenantID, key, hash string, out any) (bool, error) {
	if key == "" {
		return false, nil
	}
	var stored string
	var body []byte
	err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where tenant_id = $1 and key = $2`, tenantID, key).Scan(&stored, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if stored != hash {
		return false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request")
	}
	return true, json.Unmarshal(body, out)
}

// rememberJSON stores a command's response under its key, in the command's transaction.
func rememberJSON(ctx context.Context, tx pgx.Tx, tenantID, key, hash string, v any) error {
	if key == "" {
		return nil
	}
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body) values ($1, $2, $3, 200, $4)`,
		tenantID, key, hash, body)
	return err
}

// ActivateRecord moves DRAFT to ACTIVE (BR-26.6). Under a transaction-scoped advisory
// lock on the tenant and key hash it rejects any ACTIVE record with the same key and
// overlapping validity (409 PRC-0002, naming the conflict). A repeated Idempotency-Key
// replays the first response.
func (s *Postgres) ActivateRecord(ctx context.Context, id string, version int, idempotencyKey string) (Entry, error) {
	p, _ := tenant.FromContext(ctx)
	var out Entry
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		hash := commandHash("activate", id, strconv.Itoa(version))
		if replay, ok, err := replayCommand(ctx, tx, p.TenantID, idempotencyKey, hash); err != nil || ok {
			out = replay
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "record version does not match")
		}
		if cur.Status != domain.StatusDraft {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "only DRAFT records can be activated")
		}
		// A price journal posting for this legal entity holds the entity lock exclusively and
		// checks overlaps against every ACTIVE record; activations share it, so the two never
		// validate against each other's uncommitted state.
		if err := lockEntityShared(ctx, tx, p.TenantID, cur.LegalEntityID); err != nil {
			return err
		}
		key := KeyHash(cur.Record)
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, p.TenantID+"|"+key); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+recordColumns+` from condition_record
			where legal_entity_id = $1 and key_hash = $2 and status = 'ACTIVE' and id <> $3`, cur.LegalEntityID, key, id)
		if err != nil {
			return err
		}
		var active []domain.Record
		for rows.Next() {
			e, err := scanEntry(rows)
			if err != nil {
				rows.Close()
				return err
			}
			active = append(active, e.Record)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if c, clash := domain.Conflict(cur.Record, active); clash {
			return httpx.NewProblem(http.StatusConflict, domain.CodeOverlap, fmt.Sprintf("overlapping active record %s for the same key, currency and unit", c.ID))
		}
		var retroactive bool
		out, err = scanEntry(tx.QueryRow(ctx, `update condition_record set status = 'ACTIVE', version = version + 1
			where id = $1 and version = $2 returning `+recordColumns, id, version))
		if err != nil {
			return err
		}
		// BR-26.7: a record whose validity starts before today is announced as retroactive.
		if err := tx.QueryRow(ctx, `select $1::date < current_date`, dayStr(out.ValidFrom)).Scan(&retroactive); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, out, pevents.ConditionRecordActivated, pevents.ConditionRecordData{ID: out.ID, LegalEntityID: out.LegalEntityID,
			Type: string(out.Type), CustomerKind: string(out.CustomerKind), CustomerKey: out.CustomerKey, ProductKind: string(out.ProductKind),
			ProductKey: out.ProductKey, Variant: out.Variant, Currency: out.Currency, Unit: out.Unit, FromQuantity: out.FromQuantity.String(),
			ValidFrom: dayStr(out.ValidFrom), ValidTo: dayStr(out.ValidTo), Version: out.Version, Retroactive: retroactive,
			CampaignID: out.CampaignID}); err != nil {
			return err
		}
		return rememberCommand(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// WithdrawRecord moves ACTIVE to WITHDRAWN (BR-26.9) and announces it. A repeated
// Idempotency-Key replays the first response.
func (s *Postgres) WithdrawRecord(ctx context.Context, id string, version int, reason, idempotencyKey string) (Entry, error) {
	p, _ := tenant.FromContext(ctx)
	var out Entry
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		hash := commandHash("withdraw", id, strconv.Itoa(version), reason)
		if replay, ok, err := replayCommand(ctx, tx, p.TenantID, idempotencyKey, hash); err != nil || ok {
			out = replay
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "record version does not match")
		}
		if cur.Status != domain.StatusActive {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "only ACTIVE records can be withdrawn")
		}
		out, err = scanEntry(tx.QueryRow(ctx, `update condition_record set status = 'WITHDRAWN', version = version + 1
			where id = $1 and version = $2 returning `+recordColumns, id, version))
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, out, pevents.ConditionRecordWithdrawn, pevents.ConditionRecordWithdrawnData{ID: out.ID, Version: out.Version, Reason: reason}); err != nil {
			return err
		}
		return rememberCommand(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// enqueue writes the event into the outbox inside tx (same transaction as the change).
func enqueue(ctx context.Context, tx pgx.Tx, e Entry, typ string, data any) error {
	p, _ := tenant.FromContext(ctx)
	evt, err := events.New(typ, eventSource, e.ID, p.TenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "conditionrecord", e.ID, evt)
}
