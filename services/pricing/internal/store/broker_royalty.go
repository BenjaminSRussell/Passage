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
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// CodeContractPeriodOpen: a royalty period is claimed before it is closed, so its minimum
// royalty shortfall is not yet known.
const CodeContractPeriodOpen = "PRC-0062"

const brokerColumns = `id::text, legal_entity_id::text, name, broker_vendor_id::text, coalesce(customer_id::text, ''),
	coalesce(customer_group_id::text, ''), product_id, product_group_id, rate_kind, rate::text, currency::text, valid_from, valid_to,
	payable_account, expense_account, status, version`

func scanBroker(row pgx.Row) (domain.BrokerContract, error) {
	var c domain.BrokerContract
	var rate string
	if err := row.Scan(&c.ID, &c.LegalEntityID, &c.Name, &c.BrokerVendorID, &c.CustomerID, &c.CustomerGroupID, &c.ProductID,
		&c.ProductGroupID, &c.RateKind, &rate, &c.Currency, &c.ValidFrom, &c.ValidTo, &c.PayableAccount, &c.ExpenseAccount,
		&c.Status, &c.Version); err != nil {
		return c, err
	}
	c.Rate = decimal.RequireFromString(rate)
	return c, nil
}

const royaltyColumns = `id::text, legal_entity_id::text, name, licensor_vendor_id::text, product_id, product_group_id, basis, period,
	tiers, minimum_royalty::text, currency::text, valid_from, valid_to, payable_account, expense_account, status, version`

func scanRoyalty(row pgx.Row) (domain.RoyaltyAgreement, error) {
	var a domain.RoyaltyAgreement
	var tiers []byte
	var minimum string
	if err := row.Scan(&a.ID, &a.LegalEntityID, &a.Name, &a.LicensorVendorID, &a.ProductID, &a.ProductGroupID, &a.Basis, &a.Period,
		&tiers, &minimum, &a.Currency, &a.ValidFrom, &a.ValidTo, &a.PayableAccount, &a.ExpenseAccount, &a.Status, &a.Version); err != nil {
		return a, err
	}
	a.MinimumRoyalty = decimal.RequireFromString(minimum)
	if err := json.Unmarshal(tiers, &a.Tiers); err != nil {
		return a, fmt.Errorf("decode royalty tiers: %w", err)
	}
	return a, nil
}

func contractNotFound(kind, id string) error {
	what := "broker contract"
	if kind == domain.ContractRoyalty {
		what = "royalty agreement"
	}
	return httpx.NewProblem(http.StatusNotFound, domain.CodeContractNotFound, "no "+what+" "+id)
}

// idemLock serializes retries of one Idempotency-Key, so the second call waits for the first
// and replays its response instead of failing on the key's unique index.
func idemLock(ctx context.Context, tx pgx.Tx, tenantID, scope, key string) error {
	if key == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, tenantID+"|"+scope+"|"+key)
	return err
}

func brokerEventData(c domain.BrokerContract) pevents.BrokerContractData {
	return pevents.BrokerContractData{ContractID: c.ID, LegalEntityID: c.LegalEntityID, BrokerVendorID: c.BrokerVendorID,
		RateKind: c.RateKind, Rate: c.Rate.String(), Currency: c.Currency, ValidFrom: dayStr(c.ValidFrom), ValidTo: dayStr(c.ValidTo),
		Status: c.Status, Version: c.Version}
}

func royaltyEventData(a domain.RoyaltyAgreement) pevents.RoyaltyAgreementData {
	return pevents.RoyaltyAgreementData{AgreementID: a.ID, LegalEntityID: a.LegalEntityID, LicensorVendorID: a.LicensorVendorID,
		Basis: a.Basis, Period: a.Period, MinimumRoyalty: a.MinimumRoyalty.String(), Currency: a.Currency, ValidFrom: dayStr(a.ValidFrom),
		ValidTo: dayStr(a.ValidTo), Status: a.Status, Version: a.Version}
}

// CreateBrokerContract stores an ACTIVE broker contract at version 1 and announces it through
// the outbox in the same transaction (FR-26.16). A repeated Idempotency-Key replays the first
// response, or is idempotency_key_reuse when the body differs.
func (s *Postgres) CreateBrokerContract(ctx context.Context, in domain.BrokerContract, idempotencyKey string) (domain.BrokerContract, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.BrokerContract
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := idemLock(ctx, tx, p.TenantID, "bc", idempotencyKey); err != nil {
			return err
		}
		body, _ := json.Marshal(in)
		hash := commandHash("broker-create", string(body))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		var err error
		out, err = scanBroker(tx.QueryRow(ctx, `insert into broker_contract (tenant_id, id, legal_entity_id, name, broker_vendor_id, customer_id,
			customer_group_id, product_id, product_group_id, rate_kind, rate, currency, valid_from, valid_to, payable_account, expense_account, created_by)
			values ($1, $2, $3, $4, $5, nullif($6, '')::uuid, nullif($7, '')::uuid, $8, $9, $10, $11::numeric, $12, $13, $14, $15, $16, $17)
			returning `+brokerColumns, p.TenantID, ids.New(), in.LegalEntityID, in.Name, in.BrokerVendorID, in.CustomerID, in.CustomerGroupID,
			in.ProductID, in.ProductGroupID, in.RateKind, in.Rate.String(), in.Currency, dayStr(in.ValidFrom), dayStr(in.ValidTo),
			in.PayableAccount, in.ExpenseAccount, p.UserID))
		if err != nil {
			return err
		}
		if err := enqueueEvent(ctx, tx, "brokercontract", out.ID, pevents.BrokerContractCreated, brokerEventData(out)); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// GetBrokerContract reads one contract (PRC-0058 when it does not exist or lies outside the
// caller's legal entities).
func (s *Postgres) GetBrokerContract(ctx context.Context, id string) (domain.BrokerContract, error) {
	var out domain.BrokerContract
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = scanBroker(tx.QueryRow(ctx, `select `+brokerColumns+` from broker_contract where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return contractNotFound(domain.ContractBroker, id)
		}
		return err
	})
	return out, err
}

// ContractFilter selects broker contracts or royalty agreements.
type ContractFilter struct {
	LegalEntityID string
	VendorID      string
	Status        string
	Limit         int
}

// ListBrokerContracts returns contracts, latest start first.
func (s *Postgres) ListBrokerContracts(ctx context.Context, f ContractFilter) ([]domain.BrokerContract, error) {
	if f.Limit <= 0 || f.Limit > MaxListLimit {
		f.Limit = MaxListLimit
	}
	out := []domain.BrokerContract{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+brokerColumns+` from broker_contract
			where ($1::text = '' or legal_entity_id::text = $1) and ($2::text = '' or broker_vendor_id::text = $2) and ($3::text = '' or status = $3)
			order by valid_from desc, id limit $4`, f.LegalEntityID, f.VendorID, f.Status, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanBroker(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// CloseBrokerContract closes a contract at the expected version (412 conflict_version
// otherwise, 409 PRC-0031 when already closed). A closed contract takes no further accruals;
// claims of its earlier periods stay open.
func (s *Postgres) CloseBrokerContract(ctx context.Context, id string, version int, idempotencyKey string) (domain.BrokerContract, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.BrokerContract
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanBroker(tx.QueryRow(ctx, `select `+brokerColumns+` from broker_contract where id = $1 for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return contractNotFound(domain.ContractBroker, id)
		}
		if err != nil {
			return err
		}
		hash := commandHash("broker-close", id, strconv.Itoa(version))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "broker contract version does not match")
		}
		if cur.Status != domain.AgreementActive {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "broker contract is already closed")
		}
		out, err = scanBroker(tx.QueryRow(ctx, `update broker_contract set status = 'CLOSED', version = version + 1 where id = $1 returning `+brokerColumns, id))
		if err != nil {
			return err
		}
		if err := enqueueEvent(ctx, tx, "brokercontract", out.ID, pevents.BrokerContractClosed, brokerEventData(out)); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// CreateRoyaltyAgreement stores an ACTIVE royalty agreement at version 1 and announces it
// through the outbox in the same transaction (FR-26.17). Idempotency-Key as for broker contracts.
func (s *Postgres) CreateRoyaltyAgreement(ctx context.Context, in domain.RoyaltyAgreement, idempotencyKey string) (domain.RoyaltyAgreement, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RoyaltyAgreement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := idemLock(ctx, tx, p.TenantID, "ry", idempotencyKey); err != nil {
			return err
		}
		body, _ := json.Marshal(in)
		hash := commandHash("royalty-create", string(body))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		tiers, err := marshalTiers(in.Tiers)
		if err != nil {
			return err
		}
		out, err = scanRoyalty(tx.QueryRow(ctx, `insert into royalty_agreement (tenant_id, id, legal_entity_id, name, licensor_vendor_id, product_id,
			product_group_id, basis, period, tiers, minimum_royalty, currency, valid_from, valid_to, payable_account, expense_account, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::numeric, $12, $13, $14, $15, $16, $17)
			returning `+royaltyColumns, p.TenantID, ids.New(), in.LegalEntityID, in.Name, in.LicensorVendorID, in.ProductID, in.ProductGroupID,
			in.Basis, in.Period, tiers, in.MinimumRoyalty.String(), in.Currency, dayStr(in.ValidFrom), dayStr(in.ValidTo), in.PayableAccount,
			in.ExpenseAccount, p.UserID))
		if err != nil {
			return err
		}
		if err := enqueueEvent(ctx, tx, "royaltyagreement", out.ID, pevents.RoyaltyAgreementCreated, royaltyEventData(out)); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// GetRoyaltyAgreement reads one agreement (PRC-0058 when it does not exist or is out of scope).
func (s *Postgres) GetRoyaltyAgreement(ctx context.Context, id string) (domain.RoyaltyAgreement, error) {
	var out domain.RoyaltyAgreement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = scanRoyalty(tx.QueryRow(ctx, `select `+royaltyColumns+` from royalty_agreement where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return contractNotFound(domain.ContractRoyalty, id)
		}
		return err
	})
	return out, err
}

// ListRoyaltyAgreements returns agreements, latest start first.
func (s *Postgres) ListRoyaltyAgreements(ctx context.Context, f ContractFilter) ([]domain.RoyaltyAgreement, error) {
	if f.Limit <= 0 || f.Limit > MaxListLimit {
		f.Limit = MaxListLimit
	}
	out := []domain.RoyaltyAgreement{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+royaltyColumns+` from royalty_agreement
			where ($1::text = '' or legal_entity_id::text = $1) and ($2::text = '' or licensor_vendor_id::text = $2) and ($3::text = '' or status = $3)
			order by valid_from desc, id limit $4`, f.LegalEntityID, f.VendorID, f.Status, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanRoyalty(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// CloseRoyaltyAgreement closes an agreement at the expected version, as CloseBrokerContract.
func (s *Postgres) CloseRoyaltyAgreement(ctx context.Context, id string, version int, idempotencyKey string) (domain.RoyaltyAgreement, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RoyaltyAgreement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanRoyalty(tx.QueryRow(ctx, `select `+royaltyColumns+` from royalty_agreement where id = $1 for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return contractNotFound(domain.ContractRoyalty, id)
		}
		if err != nil {
			return err
		}
		hash := commandHash("royalty-close", id, strconv.Itoa(version))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "royalty agreement version does not match")
		}
		if cur.Status != domain.AgreementActive {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "royalty agreement is already closed")
		}
		out, err = scanRoyalty(tx.QueryRow(ctx, `update royalty_agreement set status = 'CLOSED', version = version + 1 where id = $1 returning `+royaltyColumns, id))
		if err != nil {
			return err
		}
		if err := enqueueEvent(ctx, tx, "royaltyagreement", out.ID, pevents.RoyaltyAgreementClosed, royaltyEventData(out)); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// contractRef is what accruals and claims need of either contract kind.
type contractRef struct {
	Kind, ID, LegalEntityID, VendorID, Currency, Period, Status, Payable, Expense string
	broker                                                                        domain.BrokerContract
	royalty                                                                       domain.RoyaltyAgreement
}

// loadContract reads a contract of the kind; lock is "" (plain read) or "for share".
func loadContract(ctx context.Context, tx pgx.Tx, kind, id, lock string) (contractRef, error) {
	ref := contractRef{Kind: kind, ID: id}
	switch kind {
	case domain.ContractBroker:
		c, err := scanBroker(tx.QueryRow(ctx, `select `+brokerColumns+` from broker_contract where id = $1 `+lock, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ref, contractNotFound(kind, id)
		}
		if err != nil {
			return ref, err
		}
		ref.broker = c
		ref.LegalEntityID, ref.VendorID, ref.Currency, ref.Period, ref.Status = c.LegalEntityID, c.BrokerVendorID, c.Currency, domain.PeriodMonth, c.Status
		ref.Payable, ref.Expense = c.PayableAccount, c.ExpenseAccount
	case domain.ContractRoyalty:
		a, err := scanRoyalty(tx.QueryRow(ctx, `select `+royaltyColumns+` from royalty_agreement where id = $1 `+lock, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ref, contractNotFound(kind, id)
		}
		if err != nil {
			return ref, err
		}
		ref.royalty = a
		ref.LegalEntityID, ref.VendorID, ref.Currency, ref.Period, ref.Status = a.LegalEntityID, a.LicensorVendorID, a.Currency, a.Period, a.Status
		ref.Payable, ref.Expense = a.PayableAccount, a.ExpenseAccount
	default:
		return ref, fmt.Errorf("unknown contract kind %q", kind)
	}
	return ref, nil
}

// ContractAccrual is one broker or royalty accrual row (FR-26.16, FR-26.17).
type ContractAccrual struct {
	ID              string          `json:"id"`
	ContractKind    string          `json:"contractKind"`
	ContractID      string          `json:"contractId"`
	PeriodKey       string          `json:"periodKey"`
	LineKey         string          `json:"lineKey"`
	Kind            string          `json:"kind"`
	InvoiceDate     time.Time       `json:"invoiceDate"`
	BasisValue      decimal.Decimal `json:"basisValue"`
	BasisQty        decimal.Decimal `json:"basisQty"`
	Amount          decimal.Decimal `json:"amount"`
	Currency        string          `json:"currency"`
	ReversesLineKey string          `json:"reversesLineKey"`
}

const accrualFields = `id::text, contract_kind, contract_id::text, period_key, line_key, kind, invoice_date, basis_value::text, basis_qty::text,
	amount::text, currency::text, coalesce(reverses_line_key, '')`

func scanContractAccrual(row pgx.Row) (ContractAccrual, error) {
	var a ContractAccrual
	var bv, bq, amt string
	if err := row.Scan(&a.ID, &a.ContractKind, &a.ContractID, &a.PeriodKey, &a.LineKey, &a.Kind, &a.InvoiceDate, &bv, &bq, &amt,
		&a.Currency, &a.ReversesLineKey); err != nil {
		return a, err
	}
	a.BasisValue, a.BasisQty, a.Amount = decimal.RequireFromString(bv), decimal.RequireFromString(bq), decimal.RequireFromString(amt)
	return a, nil
}

// ContractAccrualInput hands one posted invoice line (or credit note line) to a contract.
type ContractAccrualInput struct {
	Kind       string
	ContractID string
	Line       domain.ContractLine
}

// AccrueContractLine accrues one posted invoice line against a broker contract or royalty
// agreement (FR-26.16, FR-26.17). It locks the contract period, so lines of one period
// serialize. A broker line accrues rate x basis; a credit note line reverses the original
// accrual in proportion to the credited share (PRC-0059 when the line was never accrued or the
// credit exceeds it). A royalty line moves the period to its tier target at the new
// cumulative basis (BR-26.8) and books the difference; a credit note line subtracts from the
// basis, never beyond what the original line accrued (PRC-0059). A redelivered line returns
// its accrual with created=false, even after the contract closed; a closed period is PRC-0060
// and a new invoice line on a closed contract PRC-0061 (a credit note line is still taken, see
// contractTakesLine). The accrual leaves through the outbox in the same transaction.
func (s *Postgres) AccrueContractLine(ctx context.Context, in ContractAccrualInput) (ContractAccrual, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out ContractAccrual
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ref, err := loadContract(ctx, tx, in.Kind, in.ContractID, "for share")
		if err != nil {
			return err
		}
		l := in.Line
		key := domain.PeriodKey(ref.Period, l.InvoiceDate)
		if _, err := tx.Exec(ctx, `insert into contract_period (tenant_id, legal_entity_id, contract_kind, contract_id, period_key)
			values ($1, $2, $3, $4, $5) on conflict do nothing`, p.TenantID, ref.LegalEntityID, ref.Kind, ref.ID, key); err != nil {
			return err
		}
		var bv, bq, accrued string
		var closed bool
		if err := tx.QueryRow(ctx, `select basis_value::text, basis_qty::text, accrued_total::text, closed from contract_period
			where contract_kind = $1 and contract_id = $2 and period_key = $3 for update`, ref.Kind, ref.ID, key).Scan(&bv, &bq, &accrued, &closed); err != nil {
			return err
		}
		existing, err := scanContractAccrual(tx.QueryRow(ctx, `select `+accrualFields+` from contract_accrual
			where contract_kind = $1 and contract_id = $2 and line_key = $3`, ref.Kind, ref.ID, l.InvoiceLineID))
		if err == nil {
			wantValue, wantQty := l.NetAmount, l.Quantity
			if l.IsCredit() {
				wantValue, wantQty = wantValue.Neg(), wantQty.Neg()
			}
			if dayStr(existing.InvoiceDate) != dayStr(l.InvoiceDate) || !existing.BasisValue.Equal(wantValue) || !existing.BasisQty.Equal(wantQty) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse,
					"invoice line "+l.InvoiceLineID+" was already accrued with a different date, value or quantity")
			}
			out = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := contractTakesLine(ref, l); err != nil {
			return err
		}
		if closed {
			return httpx.NewProblem(http.StatusConflict, domain.CodePeriodClosed, "period "+key+" of the contract is closed")
		}
		rowKind := domain.AccrualLine
		var amount, rowValue, rowQty decimal.Decimal
		rowValue, rowQty = l.NetAmount, l.Quantity
		if l.IsCredit() {
			rowKind, rowValue, rowQty = domain.AccrualCredit, l.NetAmount.Neg(), l.Quantity.Neg()
		}
		prior := domain.PeriodState{Accrued: decimal.RequireFromString(accrued), Value: decimal.RequireFromString(bv), Qty: decimal.RequireFromString(bq)}
		next := domain.PeriodState{Value: prior.Value.Add(rowValue), Qty: prior.Qty.Add(rowQty)}
		if in.Kind == domain.ContractRoyalty {
			if l.IsCredit() {
				if err := royaltyCreditFits(ctx, tx, ref, l); err != nil {
					return err
				}
			}
			next, amount = domain.Accrue(domain.RoyaltyRebateView(ref.royalty), prior, rowValue, rowQty)
		} else {
			if l.IsCredit() {
				if amount, err = brokerCreditAmount(ctx, tx, ref, l); err != nil {
					return err
				}
			} else {
				amount = domain.BrokerAmount(ref.broker, l)
			}
			next.Accrued = prior.Accrued.Add(amount)
		}
		if _, err := tx.Exec(ctx, `update contract_period set basis_value = $4::numeric, basis_qty = $5::numeric, accrued_total = $6::numeric
			where contract_kind = $1 and contract_id = $2 and period_key = $3`, ref.Kind, ref.ID, key, next.Value.String(), next.Qty.String(), next.Accrued.String()); err != nil {
			return err
		}
		out, err = insertContractAccrual(ctx, tx, ref, key, l.InvoiceLineID, rowKind, l.InvoiceDate, rowValue, rowQty, amount, l.ReversesInvoiceLineID)
		if err != nil {
			return err
		}
		created = true
		return postContractAccrual(ctx, tx, ref, out)
	})
	return out, created, err
}

// contractTakesLine decides whether a new line may accrue on the contract. An invoice line
// needs an ACTIVE contract (PRC-0061) whose customer and product scope, validity and currency
// it matches (422). A credit note line reverses an accrual the contract already took, so it
// needs only the contract's currency: it is taken after the validity ended or the contract
// closed, otherwise the broker or licensor would keep the commission on a credited sale.
func contractTakesLine(ref contractRef, l domain.ContractLine) error {
	if l.IsCredit() {
		if l.Currency != ref.Currency {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "the credit note line is not in the contract's currency")
		}
		return nil
	}
	if ref.Status != domain.AgreementActive {
		return httpx.NewProblem(http.StatusConflict, domain.CodeContractClosed, "contract "+ref.ID+" is closed and takes no further accruals")
	}
	applies := domain.BrokerApplies(ref.broker, l)
	if ref.Kind == domain.ContractRoyalty {
		applies = domain.RoyaltyApplies(ref.royalty, l)
	}
	if !applies {
		return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation,
			"the invoice line is outside the contract's customer or product scope, validity or currency")
	}
	return nil
}

// creditedLine is an original invoice line accrual and what earlier credit note lines already
// reversed of it, as positive magnitudes.
type creditedLine struct {
	orig                                       ContractAccrual
	creditedValue, creditedQty, creditedAmount decimal.Decimal
}

// lineCredits reads the original accrual a credit note line reverses (PRC-0059 when the line
// was never accrued on this contract) and the sums of the earlier credits of it.
func lineCredits(ctx context.Context, tx pgx.Tx, ref contractRef, l domain.ContractLine) (creditedLine, error) {
	var out creditedLine
	orig, err := scanContractAccrual(tx.QueryRow(ctx, `select `+accrualFields+` from contract_accrual
		where contract_kind = $1 and contract_id = $2 and line_key = $3 and kind = 'ACCRUAL'`, ref.Kind, ref.ID, l.ReversesInvoiceLineID))
	if errors.Is(err, pgx.ErrNoRows) {
		return out, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCreditExceedsOriginal,
			"invoice line "+l.ReversesInvoiceLineID+" was never accrued on this contract")
	}
	if err != nil {
		return out, err
	}
	var sumValue, sumQty, sumAmount string
	if err := tx.QueryRow(ctx, `select coalesce(sum(basis_value), 0)::text, coalesce(sum(basis_qty), 0)::text, coalesce(sum(amount), 0)::text
		from contract_accrual where contract_kind = $1 and contract_id = $2 and reverses_line_key = $3`, ref.Kind, ref.ID, l.ReversesInvoiceLineID).
		Scan(&sumValue, &sumQty, &sumAmount); err != nil {
		return out, err
	}
	out.orig = orig
	out.creditedValue = decimal.RequireFromString(sumValue).Neg()
	out.creditedQty = decimal.RequireFromString(sumQty).Neg()
	out.creditedAmount = decimal.RequireFromString(sumAmount).Neg()
	return out, nil
}

// royaltyCreditFits refuses a royalty credit note line that credits more of the agreement's
// basis (net sales or quantity) than the original line accrued and earlier credits left
// (PRC-0059), as for broker contracts.
func royaltyCreditFits(ctx context.Context, tx pgx.Tx, ref contractRef, l domain.ContractLine) error {
	c, err := lineCredits(ctx, tx, ref, l)
	if err != nil {
		return err
	}
	remaining, credit := c.orig.BasisValue.Sub(c.creditedValue), l.NetAmount
	if ref.royalty.Basis == domain.RoyaltyQuantity {
		remaining, credit = c.orig.BasisQty.Sub(c.creditedQty), l.Quantity
	}
	if credit.GreaterThan(remaining) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCreditExceedsOriginal,
			fmt.Sprintf("credit of %s exceeds the %s still creditable on invoice line %s", credit, remaining, l.ReversesInvoiceLineID))
	}
	return nil
}

// brokerCreditAmount reverses the original accrual in proportion to the credited share (BR
// FR-26.16), capped at what the original accrued and earlier credits left.
func brokerCreditAmount(ctx context.Context, tx pgx.Tx, ref contractRef, l domain.ContractLine) (decimal.Decimal, error) {
	c, err := lineCredits(ctx, tx, ref, l)
	if err != nil {
		return decimal.Zero, err
	}
	origBasis, creditedBasis := c.orig.BasisValue, c.creditedValue
	if ref.broker.RateKind == domain.RatePerUnit {
		origBasis, creditedBasis = c.orig.BasisQty, c.creditedQty
	}
	amount, derr := domain.BrokerCredit(origBasis, c.orig.Amount, creditedBasis, c.creditedAmount,
		domain.BrokerBasis(ref.broker, l), ref.Currency)
	if derr != nil {
		return decimal.Zero, httpx.NewProblem(http.StatusUnprocessableEntity, derr.Code, derr.Detail)
	}
	return amount, nil
}

func insertContractAccrual(ctx context.Context, tx pgx.Tx, ref contractRef, periodKey, lineKey, kind string, date time.Time,
	value, qty, amount decimal.Decimal, reverses string) (ContractAccrual, error) {
	p, _ := tenant.FromContext(ctx)
	return scanContractAccrual(tx.QueryRow(ctx, `insert into contract_accrual (tenant_id, id, legal_entity_id, contract_kind, contract_id,
		period_key, line_key, kind, invoice_date, basis_value, basis_qty, amount, currency, reverses_line_key)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::numeric, $11::numeric, $12::numeric, $13, nullif($14, ''))
		returning `+accrualFields, p.TenantID, ids.New(), ref.LegalEntityID, ref.Kind, ref.ID, periodKey, lineKey, kind, dayStr(date),
		value.String(), qty.String(), amount.String(), ref.Currency, reverses))
}

// postContractAccrual announces a non-zero accrual: Dr expense / Cr payable.
func postContractAccrual(ctx context.Context, tx pgx.Tx, ref contractRef, a ContractAccrual) error {
	if a.Amount.IsZero() {
		return nil
	}
	return enqueueEvent(ctx, tx, "contractaccrual", a.ID, pevents.ContractAccrualPosted, pevents.ContractAccrualPostedData{
		AccrualID: a.ID, LegalEntityID: ref.LegalEntityID, ContractKind: ref.Kind, ContractID: ref.ID, VendorID: ref.VendorID, PeriodID: a.PeriodKey,
		LineKey: a.LineKey, Kind: a.Kind, PostingDate: dayStr(a.InvoiceDate), Amount: a.Amount.String(), Currency: a.Currency, DebitAccount: ref.Expense, CreditAccount: ref.Payable})
}

// ContractAccrualFilter selects accrual rows of one contract, optionally for one period.
type ContractAccrualFilter struct {
	Kind       string
	ContractID string
	PeriodKey  string
	Limit      int
}

// ListContractAccruals returns the accrual rows of a contract in posting order.
func (s *Postgres) ListContractAccruals(ctx context.Context, f ContractAccrualFilter) ([]ContractAccrual, error) {
	if f.Limit <= 0 || f.Limit > MaxListLimit {
		f.Limit = MaxListLimit
	}
	out := []ContractAccrual{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+accrualFields+` from contract_accrual
			where contract_kind = $1 and contract_id = $2 and ($3::text = '' or period_key = $3)
			order by invoice_date, created_at, id limit $4`, f.Kind, f.ContractID, f.PeriodKey, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanContractAccrual(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// RoyaltyClose is the result of closing a royalty period.
type RoyaltyClose struct {
	AgreementID string
	PeriodKey   string
	Accrued     decimal.Decimal
	Minimum     decimal.Decimal
	// Shortfall is the minimum royalty top-up posted at close (PRC-0017 when above zero).
	Shortfall decimal.Decimal
	Currency  string
	// AlreadyClosed is true when the period was closed before; nothing is posted again.
	AlreadyClosed bool
	Accrual       *ContractAccrual
}

// CloseRoyaltyPeriod closes one period of a royalty agreement (FR-26.17): when the period's
// royalty is below the agreement's minimum, the shortfall posts as one SHORTFALL accrual
// (Dr expense / Cr payable, PRC-0017 as an informational notice) and the period takes no
// further accruals. Closing again returns the same result without posting. The period must
// be of the agreement's granularity and overlap its validity.
func (s *Postgres) CloseRoyaltyPeriod(ctx context.Context, agreementID, periodKey string) (RoyaltyClose, error) {
	p, _ := tenant.FromContext(ctx)
	var out RoyaltyClose
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ref, err := loadContract(ctx, tx, domain.ContractRoyalty, agreementID, "for share")
		if err != nil {
			return err
		}
		a := ref.royalty
		if !domain.PeriodMatches(a.Period, periodKey) || !domain.PeriodOverlapsValidity(periodKey, a.ValidFrom, a.ValidTo) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation,
				"period "+periodKey+" is not a "+a.Period+" period inside the agreement's validity")
		}
		if _, err := tx.Exec(ctx, `insert into contract_period (tenant_id, legal_entity_id, contract_kind, contract_id, period_key)
			values ($1, $2, 'ROYALTY', $3, $4) on conflict do nothing`, p.TenantID, ref.LegalEntityID, ref.ID, periodKey); err != nil {
			return err
		}
		var accrued, shortfall string
		var closed bool
		if err := tx.QueryRow(ctx, `select accrued_total::text, shortfall::text, closed from contract_period
			where contract_kind = 'ROYALTY' and contract_id = $1 and period_key = $2 for update`, ref.ID, periodKey).Scan(&accrued, &shortfall, &closed); err != nil {
			return err
		}
		out = RoyaltyClose{AgreementID: a.ID, PeriodKey: periodKey, Accrued: decimal.RequireFromString(accrued), Minimum: a.MinimumRoyalty,
			Shortfall: decimal.RequireFromString(shortfall), Currency: a.Currency, AlreadyClosed: closed}
		if closed {
			return nil
		}
		out.Shortfall = domain.MinimumShortfall(a.MinimumRoyalty, out.Accrued)
		if _, err := tx.Exec(ctx, `update contract_period set closed = true, shortfall = $3::numeric
			where contract_kind = 'ROYALTY' and contract_id = $1 and period_key = $2`, ref.ID, periodKey, out.Shortfall.String()); err != nil {
			return err
		}
		if out.Shortfall.IsPositive() {
			_, last, _ := domain.PeriodBounds(periodKey)
			row, err := insertContractAccrual(ctx, tx, ref, periodKey, "MIN:"+periodKey, domain.AccrualShortfall, last, decimal.Zero, decimal.Zero, out.Shortfall, "")
			if err != nil {
				return err
			}
			out.Accrual = &row
			return postContractAccrual(ctx, tx, ref, row)
		}
		return nil
	})
	return out, err
}

// ContractClaim is a period claim on a broker contract or royalty agreement (FR-26.16, FR-26.17).
type ContractClaim struct {
	ID            string          `json:"id"`
	Number        string          `json:"number"`
	LegalEntityID string          `json:"legalEntityId"`
	ContractKind  string          `json:"contractKind"`
	ContractID    string          `json:"contractId"`
	VendorID      string          `json:"vendorId"`
	PeriodKey     string          `json:"periodKey"`
	AccruedTotal  decimal.Decimal `json:"accruedTotal"`
	PriorClaimed  decimal.Decimal `json:"priorClaimed"`
	Amount        decimal.Decimal `json:"amount"`
	Currency      string          `json:"currency"`
	Status        string          `json:"status"`
	Version       int             `json:"version"`
	CreatedBy     string          `json:"createdBy"`
	ApprovedBy    string          `json:"approvedBy"`
	CreatedAt     time.Time       `json:"createdAt"`
	ApprovedAt    *time.Time      `json:"approvedAt"`
}

// ContractClaimPreview is what a claim for a period would be, before it is created.
type ContractClaimPreview struct {
	ContractKind string
	ContractID   string
	VendorID     string
	PeriodKey    string
	Accrued      decimal.Decimal
	Shortfall    decimal.Decimal
	PriorClaimed decimal.Decimal
	Amount       decimal.Decimal
	Currency     string
	Claims       int
	// Closed is true once the period was closed; a royalty period is claimed only when closed.
	Closed bool
}

// ContractClaimFilter selects claims (the Broker and royalty claims inquiry, §5.26.14).
type ContractClaimFilter struct {
	Kind       string
	ContractID string
	VendorID   string
	PeriodKey  string
	Status     string
	Limit      int
}

const contractClaimFields = `id::text, number, legal_entity_id::text, contract_kind, contract_id::text, vendor_id::text, period_key,
	accrued_total::text, prior_claimed::text, amount::text, currency::text, status, version, created_by, coalesce(approved_by, ''),
	created_at, approved_at`

func scanContractClaim(row pgx.Row) (ContractClaim, error) {
	var c ContractClaim
	var accrued, prior, amount string
	if err := row.Scan(&c.ID, &c.Number, &c.LegalEntityID, &c.ContractKind, &c.ContractID, &c.VendorID, &c.PeriodKey, &accrued, &prior, &amount,
		&c.Currency, &c.Status, &c.Version, &c.CreatedBy, &c.ApprovedBy, &c.CreatedAt, &c.ApprovedAt); err != nil {
		return c, err
	}
	c.AccruedTotal, c.PriorClaimed, c.Amount = decimal.RequireFromString(accrued), decimal.RequireFromString(prior), decimal.RequireFromString(amount)
	return c, nil
}

func contractClaimNotFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, CodeClaimNotFound, "no broker or royalty claim "+id)
}

// contractPosition reads a period's accrued total (including a posted shortfall) and the
// claims already made; lock=true takes the period row lock the accruals take.
func contractPosition(ctx context.Context, tx pgx.Tx, ref contractRef, periodKey string, lock bool) (pos ContractClaimPreview, found bool, err error) {
	pos = ContractClaimPreview{ContractKind: ref.Kind, ContractID: ref.ID, VendorID: ref.VendorID, PeriodKey: periodKey, Currency: ref.Currency}
	q := `select accrued_total::text, shortfall::text, closed from contract_period where contract_kind = $1 and contract_id = $2 and period_key = $3`
	if lock {
		q += ` for update`
	}
	var accrued, shortfall string
	err = tx.QueryRow(ctx, q, ref.Kind, ref.ID, periodKey).Scan(&accrued, &shortfall, &pos.Closed)
	if errors.Is(err, pgx.ErrNoRows) {
		return pos, false, nil
	}
	if err != nil {
		return pos, false, err
	}
	var claimed string
	if err = tx.QueryRow(ctx, `select coalesce(sum(amount), 0)::text, count(*)::int from contract_claim
		where contract_kind = $1 and contract_id = $2 and period_key = $3`, ref.Kind, ref.ID, periodKey).Scan(&claimed, &pos.Claims); err != nil {
		return pos, false, err
	}
	pos.Accrued, pos.Shortfall, pos.PriorClaimed = decimal.RequireFromString(accrued), decimal.RequireFromString(shortfall), decimal.RequireFromString(claimed)
	pos.Amount = domain.ClaimAmount(pos.Accrued.Add(pos.Shortfall), pos.PriorClaimed)
	return pos, true, nil
}

// PreviewContractClaim computes the claim a period would produce without writing anything.
func (s *Postgres) PreviewContractClaim(ctx context.Context, kind, contractID, periodKey string) (ContractClaimPreview, error) {
	var out ContractClaimPreview
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ref, err := loadContract(ctx, tx, kind, contractID, "")
		if err != nil {
			return err
		}
		out, _, err = contractPosition(ctx, tx, ref, periodKey, false)
		return err
	})
	return out, err
}

// CreateContractClaim creates the claim for one contract period: the period's accruals (and
// minimum royalty shortfall) less its earlier claims, numbered BC-n (broker) or RY-n (royalty)
// per legal entity, OPEN, with contractclaim.created in the same transaction. A period with
// nothing accrued is PRC-0043; one whose accruals are fully claimed is 409 PRC-0015; a royalty
// period that is not closed yet is 409 PRC-0062. A repeated Idempotency-Key replays the first
// response.
func (s *Postgres) CreateContractClaim(ctx context.Context, kind, contractID, periodKey, idempotencyKey string) (ContractClaim, error) {
	p, _ := tenant.FromContext(ctx)
	var out ContractClaim
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ref, err := loadContract(ctx, tx, kind, contractID, "")
		if err != nil {
			return err
		}
		pos, found, err := contractPosition(ctx, tx, ref, periodKey, true)
		if err != nil {
			return err
		}
		hash := commandHash("contract-claim", kind, contractID, periodKey)
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if !found || (pos.Amount.IsZero() && pos.Claims == 0) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeNothingToClaim, "contract "+ref.ID+" has nothing accrued in period "+periodKey)
		}
		if kind == domain.ContractRoyalty && !pos.Closed {
			return httpx.NewProblem(http.StatusConflict, CodeContractPeriodOpen, "close royalty period "+periodKey+" before claiming it")
		}
		if pos.Amount.IsZero() {
			return httpx.NewProblem(http.StatusConflict, CodeClaimAlreadyProcessed, "the accruals of "+periodKey+" are fully claimed")
		}
		prefix := "BC"
		if kind == domain.ContractRoyalty {
			prefix = "RY"
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "cc|"+p.TenantID+"|"+ref.LegalEntityID+"|"+prefix); err != nil {
			return err
		}
		var next int64
		if err := tx.QueryRow(ctx, `select coalesce(max(substr(number, 4)::bigint), 0) + 1 from contract_claim
			where legal_entity_id = $1 and contract_kind = $2`, ref.LegalEntityID, kind).Scan(&next); err != nil {
			return err
		}
		out, err = scanContractClaim(tx.QueryRow(ctx, `insert into contract_claim (tenant_id, id, legal_entity_id, contract_kind, contract_id, vendor_id,
			period_key, number, accrued_total, prior_claimed, amount, currency, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10::numeric, $11::numeric, $12, $13)
			returning `+contractClaimFields, p.TenantID, ids.New(), ref.LegalEntityID, kind, ref.ID, ref.VendorID, periodKey,
			fmt.Sprintf("%s-%06d", prefix, next), pos.Accrued.Add(pos.Shortfall).String(), pos.PriorClaimed.String(), pos.Amount.String(),
			ref.Currency, p.UserID))
		if err != nil {
			return err
		}
		if err := enqueueContractClaim(ctx, tx, out, pevents.ContractClaimCreated); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// ApproveContractClaim moves an OPEN claim to APPROVED at the body version (412 otherwise)
// and writes contractclaim.approved for w19 AP. The user who created the claim cannot
// approve it, nor can a caller without a user identity, since the approval releases a payment
// to AP and must name its approver (PRC-0046).
func (s *Postgres) ApproveContractClaim(ctx context.Context, id string, version int, idempotencyKey string) (ContractClaim, error) {
	p, _ := tenant.FromContext(ctx)
	var out ContractClaim
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanContractClaim(tx.QueryRow(ctx, `select `+contractClaimFields+` from contract_claim where id = $1 for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return contractClaimNotFound(id)
		}
		if err != nil {
			return err
		}
		hash := commandHash("approve-contract-claim", id, strconv.Itoa(version))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "claim version does not match")
		}
		if cur.Status != domain.ClaimOpen {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "only OPEN claims can be approved")
		}
		if p.UserID == "" {
			return httpx.NewProblem(http.StatusForbidden, CodeClaimFourEyes, "approving claim "+cur.Number+" needs a user identity")
		}
		if p.UserID == cur.CreatedBy {
			return httpx.NewProblem(http.StatusForbidden, CodeClaimFourEyes, "the user who created claim "+cur.Number+" cannot approve it")
		}
		out, err = scanContractClaim(tx.QueryRow(ctx, `update contract_claim set status = 'APPROVED', version = version + 1, approved_by = $3, approved_at = now()
			where id = $1 and version = $2 returning `+contractClaimFields, id, version, p.UserID))
		if err != nil {
			return err
		}
		if err := enqueueContractClaim(ctx, tx, out, pevents.ContractClaimApproved); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// GetContractClaim reads one claim.
func (s *Postgres) GetContractClaim(ctx context.Context, id string) (ContractClaim, error) {
	var out ContractClaim
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = scanContractClaim(tx.QueryRow(ctx, `select `+contractClaimFields+` from contract_claim where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return contractClaimNotFound(id)
		}
		return err
	})
	return out, err
}

// ListContractClaims returns claims, newest first (the Broker and royalty claims inquiry).
func (s *Postgres) ListContractClaims(ctx context.Context, f ContractClaimFilter) ([]ContractClaim, error) {
	if f.Limit <= 0 || f.Limit > MaxListLimit {
		f.Limit = MaxListLimit
	}
	out := []ContractClaim{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+contractClaimFields+` from contract_claim
			where ($1::text = '' or contract_kind = $1) and ($2::text = '' or contract_id::text = $2) and ($3::text = '' or vendor_id::text = $3)
			and ($4::text = '' or period_key = $4) and ($5::text = '' or status = $5)
			order by created_at desc, number desc limit $6`, f.Kind, f.ContractID, f.VendorID, f.PeriodKey, f.Status, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanContractClaim(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func enqueueContractClaim(ctx context.Context, tx pgx.Tx, c ContractClaim, typ string) error {
	return enqueueEvent(ctx, tx, "contractclaim", c.ID, typ, pevents.ContractClaimData{ClaimID: c.ID, Number: c.Number, LegalEntityID: c.LegalEntityID,
		ContractKind: c.ContractKind, ContractID: c.ContractID, VendorID: c.VendorID, PeriodID: c.PeriodKey, AccruedTotal: c.AccruedTotal.String(),
		PriorClaimed: c.PriorClaimed.String(), Amount: c.Amount.String(), Currency: c.Currency, Status: c.Status, Version: c.Version})
}
