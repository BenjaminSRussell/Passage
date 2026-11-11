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
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

// CodeDownPaymentNotFound answers a down payment id the caller cannot see.
const CodeDownPaymentNotFound = "AR-DOWN-PAYMENT-NOT-FOUND"

// DownPayment is a customer down payment (2190) or security deposit (2195)
// (FR-30.21). Remaining is amount less what has cleared open items. Clearings is
// filled on a single read. A request's Remaining is the amount asked for.
type DownPayment struct {
	ID            string                `json:"id"`
	LegalEntityID string                `json:"legalEntityId"`
	CustomerID    string                `json:"customerId"`
	SalesOrderID  *string               `json:"salesOrderId,omitempty"`
	Kind          string                `json:"kind"`
	Currency      string                `json:"currency"`
	Amount        decimal.Decimal       `json:"amount"`
	ClearedAmount decimal.Decimal       `json:"clearedAmount"`
	Remaining     decimal.Decimal       `json:"remaining"`
	Status        string                `json:"status"`
	ReceivedDate  *string               `json:"receivedDate,omitempty"`
	CreatedAt     time.Time             `json:"createdAt"`
	Version       int                   `json:"version"`
	Clearings     []DownPaymentClearing `json:"clearings,omitempty"`
}

// DownPaymentClearing is one clearing against an open item (BR-30.14).
type DownPaymentClearing struct {
	ID             string          `json:"id"`
	OpenItemID     string          `json:"openItemId"`
	DocumentNumber string          `json:"documentNumber"`
	Amount         decimal.Decimal `json:"amount"`
	ClearedAt      time.Time       `json:"clearedAt"`
}

// DownPaymentFilter narrows a down payment list.
type DownPaymentFilter struct {
	CustomerID string
	Status     string
	Kind       string
}

const downPaymentColumns = `id, legal_entity_id, customer_id, sales_order_id, kind, currency, amount::text,
	cleared_amount::text, status, received_date::text, created_at, version`

func scanDownPayment(row rowScanner) (DownPayment, error) {
	var d DownPayment
	var amount, cleared string
	if err := row.Scan(&d.ID, &d.LegalEntityID, &d.CustomerID, &d.SalesOrderID, &d.Kind, &d.Currency, &amount, &cleared,
		&d.Status, &d.ReceivedDate, &d.CreatedAt, &d.Version); err != nil {
		return DownPayment{}, err
	}
	var err error
	if d.Amount, err = decimal.NewFromString(amount); err != nil {
		return DownPayment{}, err
	}
	if d.ClearedAmount, err = decimal.NewFromString(cleared); err != nil {
		return DownPayment{}, err
	}
	// What remains to clear or release; a request holds no money yet, and a
	// cancelled or released one holds none any more.
	d.Remaining = decimal.Zero
	if d.Status == domain.DownReceived || d.Status == domain.DownRequested {
		d.Remaining = d.Amount.Sub(d.ClearedAmount)
	}
	return d, nil
}

func readDownPayment(ctx context.Context, tx pgx.Tx, where string, args ...any) (DownPayment, error) {
	return scanDownPayment(tx.QueryRow(ctx, `select `+downPaymentColumns+` from down_payment where `+where, args...))
}

// loadDownPayment reads one down payment with its clearings.
func loadDownPayment(ctx context.Context, tx pgx.Tx, id string) (DownPayment, error) {
	d, err := readDownPayment(ctx, tx, `id = $1`, id)
	if err != nil {
		return DownPayment{}, notFound(err, CodeDownPaymentNotFound, "down payment not found")
	}
	rows, err := tx.Query(ctx, `select cl.id, cl.open_item_id, oi.document_number, cl.amount::text, cl.cleared_at
		from down_payment_clearing cl join open_item oi on oi.tenant_id = cl.tenant_id and oi.id = cl.open_item_id
		where cl.down_payment_id = $1 order by cl.cleared_at, cl.id`, id)
	if err != nil {
		return DownPayment{}, err
	}
	defer rows.Close()
	d.Clearings = []DownPaymentClearing{}
	for rows.Next() {
		var c DownPaymentClearing
		var amount string
		if err := rows.Scan(&c.ID, &c.OpenItemID, &c.DocumentNumber, &amount, &c.ClearedAt); err != nil {
			return DownPayment{}, err
		}
		if c.Amount, err = decimal.NewFromString(amount); err != nil {
			return DownPayment{}, err
		}
		d.Clearings = append(d.Clearings, c)
	}
	return d, rows.Err()
}

// lockDownPayment reads a down payment under a row lock and checks the If-Match version.
func lockDownPayment(ctx context.Context, tx pgx.Tx, id string, version int) (DownPayment, error) {
	cur, err := readDownPayment(ctx, tx, `id = $1 for update`, id)
	if err != nil {
		return DownPayment{}, notFound(err, CodeDownPaymentNotFound, "down payment not found")
	}
	if cur.Version != version {
		return DownPayment{}, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the down payment changed since it was read")
	}
	return cur, nil
}

func downPaymentEvent(d DownPayment, amount decimal.Decimal, itemID *string, postings []domain.Posting) rcvevents.DownPaymentData {
	return rcvevents.DownPaymentData{DownPaymentID: d.ID, LegalEntityID: d.LegalEntityID, CustomerID: d.CustomerID,
		SalesOrderID: d.SalesOrderID, Kind: d.Kind, Amount: amount, Currency: d.Currency, Remaining: d.Remaining,
		OpenItemID: itemID, Postings: postings}
}

// CreateDownPayment records a down payment or deposit request: a noted item with
// no posting. A repeated key returns the stored request when the body matches.
func (s *Postgres) CreateDownPayment(ctx context.Context, in DownPayment, key string) (DownPayment, error) {
	p, _ := tenant.FromContext(ctx)
	var out DownPayment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay, err := readDownPayment(ctx, tx, `idempotency_key = $1`, key)
		if err == nil {
			if replay.LegalEntityID != in.LegalEntityID || replay.CustomerID != in.CustomerID || replay.Kind != in.Kind ||
				replay.Currency != in.Currency || !replay.Amount.Equal(in.Amount) || !eqStrPtr(replay.SalesOrderID, in.SalesOrderID) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out = replay
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out, err = scanDownPayment(tx.QueryRow(ctx, `insert into down_payment (tenant_id, id, legal_entity_id, customer_id,
			sales_order_id, kind, currency, amount, status, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, 'REQUESTED', $9)
			returning `+downPaymentColumns,
			p.TenantID, ids.New(), in.LegalEntityID, in.CustomerID, in.SalesOrderID, in.Kind, in.Currency, in.Amount.String(), key))
		return err
	})
	return out, mapWriteErr(err)
}

// ListDownPayments reads down payments and deposits, newest first, bounded by limit.
func (s *Postgres) ListDownPayments(ctx context.Context, f DownPaymentFilter, limit int) ([]DownPayment, error) {
	out := []DownPayment{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+downPaymentColumns+` from down_payment
			where ($1::text = '' or customer_id::text = $1::text) and ($2::text = '' or status = $2::text)
			and ($3::text = '' or kind = $3::text)
			order by created_at desc, id limit $4`, f.CustomerID, f.Status, f.Kind, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDownPayment(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// GetDownPayment reads one down payment or deposit with its clearings.
func (s *Postgres) GetDownPayment(ctx context.Context, id string) (DownPayment, error) {
	var out DownPayment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadDownPayment(ctx, tx, id)
		return err
	})
	return out, err
}

// ReceiveDownPayment records the money as received on receivedDate (today when
// empty) and enqueues downpayment.received.v1 with Dr bank / Cr 2190 or 2195.
func (s *Postgres) ReceiveDownPayment(ctx context.Context, id string, version int, receivedDate string) (DownPayment, error) {
	p, _ := tenant.FromContext(ctx)
	var out DownPayment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockDownPayment(ctx, tx, id, version)
		if err != nil {
			return err
		}
		if !domain.ReceiveAllowed(cur.Status) {
			return domainProblem(domain.CodeDownPaymentState, "the down payment is "+cur.Status+"; only a request can be received")
		}
		if _, err := tx.Exec(ctx, `update down_payment set status = 'RECEIVED', received_date = coalesce(nullif($3, '')::date, current_date),
			version = version + 1 where tenant_id = $1 and id = $2`, p.TenantID, id, receivedDate); err != nil {
			return err
		}
		if out, err = loadDownPayment(ctx, tx, id); err != nil {
			return err
		}
		date := today()
		if out.ReceivedDate != nil {
			date = *out.ReceivedDate
		}
		if err := downPaymentJournal(ctx, tx, p.TenantID, out, "receipt", date, domain.ReceiptPostings(out.Kind, out.Amount)); err != nil {
			return err
		}
		return enqueue(ctx, tx, "downpayment", out.ID, rcvevents.DownPaymentReceived, p.TenantID,
			downPaymentEvent(out, out.Amount, nil, domain.ReceiptPostings(out.Kind, out.Amount)))
	})
	return out, mapWriteErr(err)
}

// CancelDownPayment withdraws a request that was never paid. Nothing was posted,
// so nothing is published.
func (s *Postgres) CancelDownPayment(ctx context.Context, id string, version int) (DownPayment, error) {
	p, _ := tenant.FromContext(ctx)
	var out DownPayment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockDownPayment(ctx, tx, id, version)
		if err != nil {
			return err
		}
		if !domain.CancelAllowed(cur.Status) {
			return domainProblem(domain.CodeDownPaymentState, "the down payment is "+cur.Status+"; only an unpaid request can be cancelled")
		}
		if _, err := tx.Exec(ctx, `update down_payment set status = 'CANCELLED', version = version + 1 where tenant_id = $1 and id = $2`,
			p.TenantID, id); err != nil {
			return err
		}
		out, err = loadDownPayment(ctx, tx, id)
		return err
	})
	return out, mapWriteErr(err)
}

// ClearDownPayment clears a received down payment against its final invoice, or
// offsets a deposit against a debit item (BR-30.14, AC-30.15). The amount is
// min(remaining, item open) unless a smaller one is given; more is AR-0017. The
// item is locked NOWAIT (BR-30.7), its open amount drops by the clearing, a
// clearing row keeps the history, and downpayment.cleared.v1 carries
// Dr 2190 or 2195 / Cr 1200.
func (s *Postgres) ClearDownPayment(ctx context.Context, id string, version int, openItemID string, amount *decimal.Decimal) (DownPayment, error) {
	p, _ := tenant.FromContext(ctx)
	var out DownPayment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockDownPayment(ctx, tx, id, version)
		if err != nil {
			return err
		}
		if !domain.ClearAllowed(cur.Status) {
			return domainProblem(domain.CodeDownPaymentState, "the down payment is "+cur.Status+"; only a received one with a remainder can clear items")
		}
		item, err := lockOpenItem(ctx, tx, openItemID)
		if err != nil {
			return err
		}
		if item.CustomerID != cur.CustomerID || item.LegalEntityID != cur.LegalEntityID || item.Currency != cur.Currency {
			return httpx.NewProblem(http.StatusBadRequest, domain.CodeDownPaymentMismatch,
				"open item "+item.DocumentNumber+" is not the down payment's customer, legal entity or currency")
		}
		if !domain.ClearableSource(cur.Kind, item.SourceType) {
			return httpx.NewProblem(http.StatusBadRequest, domain.CodeDownPaymentMismatch,
				"a "+cur.Kind+" cannot clear a "+item.SourceType+" item")
		}
		if item.Status != "OPEN" && item.Status != "PARTIALLY_SETTLED" {
			return domainProblem(domain.CodeDownPaymentState, "open item "+item.DocumentNumber+" is "+item.Status)
		}
		clearing, derr := domain.ClearingAmount(cur.Remaining, item.OpenAmount, amount)
		if derr != nil {
			return domainProblemFrom(derr)
		}
		cleared := cur.ClearedAmount.Add(clearing)
		if _, err := tx.Exec(ctx, `update down_payment set cleared_amount = $3::numeric, status = $4, version = version + 1
			where tenant_id = $1 and id = $2`, p.TenantID, id, cleared.String(), domain.StatusAfterClearing(cur.Amount, cleared)); err != nil {
			return err
		}
		newOpen := item.OpenAmount.Sub(clearing)
		if _, err := tx.Exec(ctx, `update open_item set open_amount = $3::numeric, status = $4, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, item.ID, newOpen.String(), domain.ApplyStatus(newOpen)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into down_payment_clearing (tenant_id, id, legal_entity_id, down_payment_id, open_item_id, amount)
			values ($1, $2, $3, $4, $5, $6::numeric)`, p.TenantID, ids.New(), cur.LegalEntityID, id, item.ID, clearing.String()); err != nil {
			return err
		}
		if out, err = loadDownPayment(ctx, tx, id); err != nil {
			return err
		}
		if err := downPaymentJournal(ctx, tx, p.TenantID, out, "clear:"+item.ID+":"+fmt.Sprint(out.Version), today(), domain.ClearingPostings(out.Kind, clearing)); err != nil {
			return err
		}
		return enqueue(ctx, tx, "downpayment", out.ID, rcvevents.DownPaymentCleared, p.TenantID,
			downPaymentEvent(out, clearing, &item.ID, domain.ClearingPostings(out.Kind, clearing)))
	})
	return out, mapWriteErr(err)
}

// ReleaseDownPayment refunds what remains of a received security deposit and
// enqueues downpayment.released.v1 with Dr 2195 / Cr bank.
func (s *Postgres) ReleaseDownPayment(ctx context.Context, id string, version int) (DownPayment, error) {
	p, _ := tenant.FromContext(ctx)
	var out DownPayment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockDownPayment(ctx, tx, id, version)
		if err != nil {
			return err
		}
		if !domain.ReleaseAllowed(cur.Kind, cur.Status) {
			return domainProblem(domain.CodeDownPaymentState, "only a received security deposit can be released; this "+cur.Kind+" is "+cur.Status)
		}
		if _, err := tx.Exec(ctx, `update down_payment set status = 'RELEASED', released_at = now(), version = version + 1 where tenant_id = $1 and id = $2`,
			p.TenantID, id); err != nil {
			return err
		}
		if out, err = loadDownPayment(ctx, tx, id); err != nil {
			return err
		}
		if err := downPaymentJournal(ctx, tx, p.TenantID, out, "release", today(), domain.ReleasePostings(out.Kind, cur.Remaining)); err != nil {
			return err
		}
		return enqueue(ctx, tx, "downpayment", out.ID, rcvevents.DownPaymentReleased, p.TenantID,
			downPaymentEvent(out, cur.Remaining, nil, domain.ReleasePostings(out.Kind, cur.Remaining)))
	})
	return out, mapWriteErr(err)
}
