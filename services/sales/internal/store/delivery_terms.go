package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// Problem codes of the delivery terms (FR-27.3, BR-27.2) and the date notices.
const (
	CodeDeliveryTermsExist    = "SLS-0056"
	CodeLeadTimeApplied       = "SLS-0057"
	CodeDeliveryTermsNotFound = "SLS-0117"
	// CodePastDate is the PRD's SLS-0003 warning.
	CodePastDate = "SLS-0003"
)

// NewDeliveryTerms are validated terms for one customer of a legal entity.
type NewDeliveryTerms struct {
	LegalEntityID  string
	CustomerID     string
	Terms          domain.DeliveryTerms
	IdempotencyKey string
	RequestHash    string
	Actor          string
}

// DeliveryTerms is a customer's stored transport days and receiving calendar.
type DeliveryTerms struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	CustomerID    string `json:"customerId"`
	domain.DeliveryTerms
	Version   int       `json:"version"`
	UpdatedBy string    `json:"updatedBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// DeliveryTermsFilter narrows the list; empty fields match everything.
type DeliveryTermsFilter struct {
	LegalEntityID string
	CustomerID    string
	Limit         int
}

const termsColumns = `id::text, legal_entity_id::text, customer_id::text, transport_days, receiving_days, version, updated_by, created_at, updated_at`

func scanTerms(row pgx.Row) (DeliveryTerms, error) {
	var t DeliveryTerms
	var days []int16
	if err := row.Scan(&t.ID, &t.LegalEntityID, &t.CustomerID, &t.TransportDays, &days, &t.Version, &t.UpdatedBy, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return DeliveryTerms{}, err
	}
	t.ReceivingDays = ints(days)
	return t, nil
}

func ints(days []int16) []int {
	out := make([]int, len(days))
	for i, d := range days {
		out[i] = int(d)
	}
	return out
}

func int16s(days []int) []int16 {
	out := make([]int16, len(days))
	for i, d := range days {
		out[i] = int16(d)
	}
	return out
}

func termsNotFound() error {
	return problem(http.StatusNotFound, CodeDeliveryTermsNotFound, "delivery terms not found")
}

func termsEvent(t DeliveryTerms, previous *domain.DeliveryTerms, actor string) salesevents.DeliveryTermsData {
	d := salesevents.DeliveryTermsData{TermsID: t.ID, LegalEntityID: t.LegalEntityID, CustomerID: t.CustomerID,
		TransportDays: t.TransportDays, ReceivingDays: t.ReceivingDays, Actor: actor, Version: t.Version}
	if previous != nil {
		days := previous.TransportDays
		d.PreviousTransportDays, d.PreviousReceivingDays = &days, previous.ReceivingDays
	}
	return d
}

// CreateDeliveryTerms stores a customer's terms. A repeat of the same
// Idempotency-Key with the same body returns the stored terms with
// created=false; the key with another body is refused. A customer that
// already has terms in the legal entity is 409 SLS-0056 (change them with PUT).
func (s *Postgres) CreateDeliveryTerms(ctx context.Context, in NewDeliveryTerms) (DeliveryTerms, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out DeliveryTerms
	created := false
	terms := in.Terms.Normalise()
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var existing, hash string
		err := tx.QueryRow(ctx, `select id::text, request_hash from customer_delivery_terms where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, in.IdempotencyKey).Scan(&existing, &hash)
		if err == nil {
			if hash != in.RequestHash {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out, err = loadTerms(ctx, tx, p.TenantID, existing, false)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into customer_delivery_terms (tenant_id, id, legal_entity_id, customer_id, transport_days, receiving_days,
				idempotency_key, request_hash, created_by, updated_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)`,
			p.TenantID, id, in.LegalEntityID, in.CustomerID, terms.TransportDays, int16s(terms.ReceivingDays), in.IdempotencyKey, in.RequestHash, in.Actor); err != nil {
			return err
		}
		created = true
		out, err = loadTerms(ctx, tx, p.TenantID, id, false)
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "deliveryterms", id, salesevents.DeliveryTermsCreated, p.TenantID, termsEvent(out, nil, in.Actor))
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return DeliveryTerms{}, false, problem(http.StatusConflict, CodeDeliveryTermsExist, "this customer already has delivery terms in the legal entity; change them with PUT, or a concurrent request stored them first")
	}
	if err != nil {
		return DeliveryTerms{}, false, err
	}
	return out, created, nil
}

func loadTerms(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (DeliveryTerms, error) {
	q := `select ` + termsColumns + ` from customer_delivery_terms where tenant_id = $1 and id = $2`
	if lock {
		q += " for update"
	}
	out, err := scanTerms(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryTerms{}, termsNotFound()
	}
	return out, err
}

// ListDeliveryTerms returns the terms in the caller's scope, by customer.
func (s *Postgres) ListDeliveryTerms(ctx context.Context, f DeliveryTermsFilter) ([]DeliveryTerms, error) {
	p, _ := tenant.FromContext(ctx)
	out := []DeliveryTerms{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+termsColumns+` from customer_delivery_terms
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or customer_id::text = $3)
			order by legal_entity_id, customer_id limit $4`, p.TenantID, f.LegalEntityID, f.CustomerID, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTerms(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// GetDeliveryTerms returns one customer's terms.
func (s *Postgres) GetDeliveryTerms(ctx context.Context, id string) (DeliveryTerms, error) {
	p, _ := tenant.FromContext(ctx)
	var out DeliveryTerms
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadTerms(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// ReplaceDeliveryTerms sets new transport days and receiving weekdays under
// If-Match. They date the orders confirmed from now on; an order already
// confirmed keeps the copy of the terms it was confirmed with. Unchanged terms
// return the stored row without a new version.
func (s *Postgres) ReplaceDeliveryTerms(ctx context.Context, id string, version int, terms domain.DeliveryTerms, actor string, now time.Time) (DeliveryTerms, error) {
	p, _ := tenant.FromContext(ctx)
	var out DeliveryTerms
	terms = terms.Normalise()
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := loadTerms(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return versionConflict()
		}
		if cur.TransportDays == terms.TransportDays && sameDays(cur.ReceivingDays, terms.ReceivingDays) {
			out = cur
			return nil
		}
		if _, err := tx.Exec(ctx, `update customer_delivery_terms set transport_days = $3, receiving_days = $4, version = version + 1,
				updated_by = $5, updated_at = $6 where tenant_id = $1 and id = $2`,
			p.TenantID, id, terms.TransportDays, int16s(terms.ReceivingDays), actor, now); err != nil {
			return err
		}
		out, err = loadTerms(ctx, tx, p.TenantID, id, false)
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "deliveryterms", id, salesevents.DeliveryTermsChanged, p.TenantID, termsEvent(out, &cur.DeliveryTerms, actor))
	})
	return out, err
}

func sameDays(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// termsForCustomerTx returns the terms a customer's orders use: the stored row,
// or the default (no transport time, every day a receiving day).
func termsForCustomerTx(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, customerID string) (domain.DeliveryTerms, error) {
	var transport int
	var days []int16
	err := tx.QueryRow(ctx, `select transport_days, receiving_days from customer_delivery_terms
		where tenant_id = $1 and legal_entity_id = $2 and customer_id = $3`, tenantID, legalEntityID, customerID).Scan(&transport, &days)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DeliveryTerms{}, nil
	}
	if err != nil {
		return domain.DeliveryTerms{}, err
	}
	return domain.DeliveryTerms{TransportDays: transport, ReceivingDays: ints(days)}, nil
}
