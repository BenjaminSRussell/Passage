package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	partyevents "github.com/BenjaminSRussell/passage/services/party/internal/events"
	"github.com/BenjaminSRussell/passage/services/party/internal/numbering"
)

const (
	CodeCustomerInvalid = "PTY-CUSTOMER-INVALID"
	CodeCustomerExists  = "PTY-CUSTOMER-EXISTS"
	customerKeyPrefix   = "customer-role:"
	customerColumns     = `id::text, party_id::text, legal_entity_id::text, account_number, customer_group_id::text, payment_term_id::text, delivery_term_id::text, delivery_mode_id::text, posting_profile_id::text, invoice_account_party_id::text, currency_code, credit_limit::text, credit_currency, credit_rating, credit_hold, blocked, version`
)

// NewCustomerRole is a customer role to add; Key and Hash carry the optional
// Idempotency-Key and the hash of the request body. Import is set for the
// migration loader (party.import.write): it may load a legacy credit limit as
// is, where a person's positive initial limit on a protected legal entity is
// refused so it goes through the credit-limit approval route instead.
type NewCustomerRole struct {
	In     domain.NewCustomer
	Key    string
	Hash   string
	Import bool
}

type CustomerStore interface {
	// CreateCustomer adds the customer role to partyID in the caller's single
	// legal entity. The bool is false for an idempotent replay.
	CreateCustomer(ctx context.Context, partyID string, in NewCustomerRole) (domain.Customer, bool, error)
	// GetCustomer reads one customer role; row-level security keeps it to the caller's legal entities.
	GetCustomer(ctx context.Context, customerID string) (domain.Customer, error)
}

func (s *Postgres) GetCustomer(ctx context.Context, customerID string) (domain.Customer, error) {
	caller, _ := tenant.FromContext(ctx)
	id, err := canonicalID(customerID)
	if err != nil {
		return domain.Customer{}, notFound(CodeCustomerNotFound, "customer not found")
	}
	var c domain.Customer
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err = scanCustomer(tx.QueryRow(ctx, `select `+customerColumns+` from customer_role where tenant_id = $1 and id = $2`, caller.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Customer{}, notFound(CodeCustomerNotFound, "customer not found")
	}
	return c, err
}

func scanCustomer(row pgx.Row) (domain.Customer, error) {
	var c domain.Customer
	err := row.Scan(&c.ID, &c.PartyID, &c.LegalEntityID, &c.AccountNumber, &c.CustomerGroupID, &c.PaymentTermID, &c.DeliveryTermID, &c.DeliveryModeID, &c.PostingProfileID,
		&c.InvoiceAccountPartyID, &c.CurrencyCode, &c.CreditLimit, &c.CreditCurrency, &c.CreditRating, &c.CreditHold, &c.Blocked, &c.Version)
	return c, err
}

func customerInvalid(field, msg string) error {
	p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeCustomerInvalid, "customer failed validation")
	p.Errors = map[string]string{field: msg}
	return p
}

func constraintOf(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

func (s *Postgres) CreateCustomer(ctx context.Context, partyID string, in NewCustomerRole) (domain.Customer, bool, error) {
	c, created, err := s.createCustomerOnce(ctx, partyID, in)
	if err != nil && in.Key != "" && isUniqueViolation(err) {
		// A concurrent request with the same key may have just committed: the
		// second attempt then finds its key and replays it.
		c, created, err = s.createCustomerOnce(ctx, partyID, in)
	}
	switch {
	case isUniqueViolation(err):
		return domain.Customer{}, false, httpx.NewProblem(http.StatusConflict, CodeCustomerExists, "the party already has a customer role in this legal entity, or the account number is taken")
	case isForeignKeyViolation(err):
		return domain.Customer{}, false, customerInvalid("paymentTermId", "does not exist")
	}
	return c, created, err
}

func (s *Postgres) createCustomerOnce(ctx context.Context, partyID string, in NewCustomerRole) (domain.Customer, bool, error) {
	caller, _ := tenant.FromContext(ctx)
	scopes := tenant.LegalEntityScopeList(caller)
	if len(scopes) != 1 {
		return domain.Customer{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, numbering.CodeEntityRequired, "a customer role belongs to exactly one legal entity; the caller must carry one")
	}
	id, err := canonicalID(partyID)
	if err != nil {
		return domain.Customer{}, false, ErrNotFound
	}
	c := in.In
	var out domain.Customer
	created := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var key *string
		if in.Key != "" {
			k := customerKeyPrefix + scopes[0] + ":" + id + ":" + in.Key
			key = &k
			var storedHash string
			err := tx.QueryRow(ctx, `select request_hash from idempotency_keys where tenant_id = $1 and key = $2`, caller.TenantID, k).Scan(&storedHash)
			if err == nil {
				if storedHash != in.Hash {
					return httpx.NewProblem(http.StatusConflict, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
				}
				out, err = scanCustomer(tx.QueryRow(ctx, `select `+customerColumns+` from customer_role where tenant_id = $1 and idempotency_key = $2`, caller.TenantID, k))
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var status string
		if err := tx.QueryRow(ctx, `select status from party where tenant_id = $1 and id = $2 for share`, caller.TenantID, id).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status == domain.StatusMerged {
			return httpx.NewProblem(http.StatusConflict, CodePartyMerged, "party was merged; add the customer role to the surviving party")
		}
		var ok bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from payment_term where tenant_id = $1 and id = $2)`, caller.TenantID, c.PaymentTermID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return customerInvalid("paymentTermId", "does not exist")
		}
		if err := tx.QueryRow(ctx, `select exists (select 1 from posting_profile where tenant_id = $1 and id = $2 and legal_entity_id = $3)`, caller.TenantID, c.PostingProfileID, scopes[0]).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return customerInvalid("postingProfileId", "does not exist in this legal entity")
		}
		if c.CreditLimit != nil && !in.Import {
			var held bool
			if err := tx.QueryRow(ctx, `select $3::text::numeric > 0 and coalesce((select $4::text = any(c.fields) or (c.unprotected_by = $5 and c.unprotected_at > now() - interval '24 hours')
				from protected_field_config c where c.tenant_id = $1 and c.legal_entity_id = $2), true)`, caller.TenantID, scopes[0], *c.CreditLimit, domain.FieldCreditLimit, caller.UserID).Scan(&held); err != nil {
				return err
			}
			if held {
				return customerInvalid("creditLimit", "credit_limit is a protected field in this legal entity: create the customer without it, then request the limit through POST /v1/party/customers/{id}/credit-limit for approval")
			}
		}
		if c.InvoiceAccountPartyID != nil {
			if err := tx.QueryRow(ctx, `select exists (select 1 from party where tenant_id = $1 and id = $2 and status <> 'merged')`, caller.TenantID, *c.InvoiceAccountPartyID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return customerInvalid("invoiceAccountPartyId", "does not exist or was merged")
			}
		}
		out, err = scanCustomer(tx.QueryRow(ctx, `insert into customer_role (tenant_id, id, party_id, legal_entity_id, account_number, customer_group_id, payment_term_id, delivery_term_id, delivery_mode_id,
			posting_profile_id, invoice_account_party_id, currency_code, credit_limit, credit_currency, credit_rating, credit_hold, blocked, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8::text::uuid, $9::text::uuid, $10, $11::text::uuid, $12, $13::text::numeric, $14, $15, $16, $17, $18) returning `+customerColumns,
			caller.TenantID, ids.New(), id, scopes[0], c.AccountNumber, c.CustomerGroupID, c.PaymentTermID, c.DeliveryTermID, c.DeliveryModeID,
			c.PostingProfileID, c.InvoiceAccountPartyID, c.CurrencyCode, c.CreditLimit, c.CreditCurrency, c.CreditRating, c.CreditHold, c.Blocked, key))
		if err != nil {
			return err
		}
		if key != nil {
			if _, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status) values ($1, $2, $3, 201)`, caller.TenantID, *key, in.Hash); err != nil {
				return err
			}
		}
		if err := enqueueCustomer(ctx, tx, caller.TenantID, out.ID, partyevents.CustomerCreated, partyevents.CustomerCreatedData{
			CustomerID: out.ID, PartyID: out.PartyID, LegalEntityID: out.LegalEntityID, AccountNumber: out.AccountNumber, CustomerGroupID: out.CustomerGroupID,
			CurrencyCode: out.CurrencyCode, CreditLimit: out.CreditLimit, Blocked: out.Blocked,
		}); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return domain.Customer{}, false, err
	}
	return out, created, nil
}
