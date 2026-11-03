package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	partyevents "github.com/BenjaminSRussell/passage/services/party/internal/events"
	"github.com/BenjaminSRussell/passage/services/party/internal/numbering"
)

const (
	CodeVendorNotFound  = "PTY-VENDOR-NOT-FOUND"
	CodeVendorInvalid   = "PTY-VENDOR-INVALID"
	CodeVendorExists    = "PTY-VENDOR-EXISTS"
	CodeBankInvalid     = "PTY-BANK-INVALID"
	CodeBankUnavailable = "PTY-BANK-ENCRYPTION-UNAVAILABLE"
	CodePartyMerged     = "PTY-PARTY-MERGED"
	FieldBankAccountNo  = "vendor_bank_account.account_number"
	FieldBankRoutingNo  = "vendor_bank_account.routing_number"
	vendorColumns       = `id::text, party_id::text, legal_entity_id::text, account_number, vendor_group_id::text, coalesce((select g.code from vendor_group g where g.tenant_id = vendor_role.tenant_id and g.id = vendor_role.vendor_group_id), ''), payment_term_id::text, currency_code, blocked, version`
	bankColumns         = `id::text, vendor_id::text, account_holder, bank_country, currency_code, account_last4`
	foreignKeyViolation = "23503"
	vendorKeyPrefix     = "vendor-role:"
)

// NewVendorRole is a vendor role to add; Key and Hash carry the optional
// Idempotency-Key and the hash of the request body.
type NewVendorRole struct {
	In   domain.NewVendor
	Key  string
	Hash string
}

type VendorStore interface {
	// CreateVendorRole adds the vendor role to partyID in the caller's single
	// legal entity. The bool is false for an idempotent replay.
	CreateVendorRole(ctx context.Context, partyID string, in NewVendorRole) (domain.Vendor, bool, error)
	GetVendor(ctx context.Context, vendorID string) (domain.Vendor, error)
	ListPartyVendors(ctx context.Context, partyID string) ([]domain.Vendor, error)
	CreateVendorGroup(ctx context.Context, in NewVendorGroupRequest) (domain.VendorGroup, bool, error)
	GetVendorGroup(ctx context.Context, groupID string) (domain.VendorGroup, error)
	ListVendorGroups(ctx context.Context) ([]domain.VendorGroup, error)
	AddBankAccount(ctx context.Context, vendorID string, in domain.NewBankAccount) (domain.BankAccount, error)
	ListBankAccounts(ctx context.Context, vendorID string, reveal bool) ([]domain.BankAccount, error)
}

func (s *Postgres) WithKeys(keys *fieldcrypt.Keys) *Postgres {
	s.keys = keys
	return s
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation
}

func vendorNotFound() error { return notFound(CodeVendorNotFound, "vendor not found") }

func scanVendor(row pgx.Row) (domain.Vendor, error) {
	var v domain.Vendor
	err := row.Scan(&v.ID, &v.PartyID, &v.LegalEntityID, &v.AccountNumber, &v.VendorGroupID, &v.VendorGroupCode, &v.PaymentTermID, &v.CurrencyCode, &v.Blocked, &v.Version)
	return v, err
}

// CreateVendor adds a vendor role without an Idempotency-Key.
func (s *Postgres) CreateVendor(ctx context.Context, partyID string, in domain.NewVendor) (domain.Vendor, error) {
	v, _, err := s.CreateVendorRole(ctx, partyID, NewVendorRole{In: in})
	return v, err
}

func (s *Postgres) CreateVendorRole(ctx context.Context, partyID string, in NewVendorRole) (domain.Vendor, bool, error) {
	v, created, err := s.createVendorOnce(ctx, partyID, in)
	if err != nil && in.Key != "" && isUniqueViolation(err) {
		// A concurrent request with the same key may have just committed: the
		// second attempt then finds its key and replays it.
		v, created, err = s.createVendorOnce(ctx, partyID, in)
	}
	switch {
	case isUniqueViolation(err):
		return domain.Vendor{}, false, httpx.NewProblem(http.StatusConflict, CodeVendorExists, "the party already has a vendor role in this legal entity, or the account number is taken")
	case isForeignKeyViolation(err):
		p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeVendorInvalid, "vendor failed validation")
		p.Errors = map[string]string{"paymentTermId": "does not exist"}
		return domain.Vendor{}, false, p
	}
	return v, created, err
}

func (s *Postgres) createVendorOnce(ctx context.Context, partyID string, in NewVendorRole) (domain.Vendor, bool, error) {
	caller, _ := tenant.FromContext(ctx)
	scopes := tenant.LegalEntityScopeList(caller)
	if len(scopes) != 1 {
		return domain.Vendor{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, numbering.CodeEntityRequired, "a vendor role belongs to exactly one legal entity; the caller must carry one")
	}
	id, err := canonicalID(partyID)
	if err != nil {
		return domain.Vendor{}, false, ErrNotFound
	}
	v, created := domain.Vendor{}, false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var key *string
		if in.Key != "" {
			k := vendorKeyPrefix + scopes[0] + ":" + id + ":" + in.Key
			key = &k
			var storedHash string
			err := tx.QueryRow(ctx, `select request_hash from idempotency_keys where tenant_id = $1 and key = $2`, caller.TenantID, k).Scan(&storedHash)
			if err == nil {
				if storedHash != in.Hash {
					return httpx.NewProblem(http.StatusConflict, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
				}
				v, err = scanVendor(tx.QueryRow(ctx, `select `+vendorColumns+` from vendor_role where tenant_id = $1 and idempotency_key = $2`, caller.TenantID, k))
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
		if status == "merged" {
			return httpx.NewProblem(http.StatusConflict, CodePartyMerged, "party was merged; add the vendor role to the surviving party")
		}
		v, err = scanVendor(tx.QueryRow(ctx, `insert into vendor_role (tenant_id, id, party_id, legal_entity_id, account_number, vendor_group_id, payment_term_id, currency_code, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7::text::uuid, $8, $9) returning `+vendorColumns,
			caller.TenantID, ids.New(), id, scopes[0], in.In.AccountNumber, in.In.VendorGroupID, in.In.PaymentTermID, in.In.CurrencyCode, key))
		if err != nil {
			return err
		}
		if key != nil {
			if _, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status) values ($1, $2, $3, 201)`, caller.TenantID, *key, in.Hash); err != nil {
				return err
			}
		}
		created = true
		return nil
	})
	return v, created, err
}

func (s *Postgres) GetVendor(ctx context.Context, vendorID string) (domain.Vendor, error) {
	caller, _ := tenant.FromContext(ctx)
	id, err := canonicalID(vendorID)
	if err != nil {
		return domain.Vendor{}, vendorNotFound()
	}
	var v domain.Vendor
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		v, err = scanVendor(tx.QueryRow(ctx, `select `+vendorColumns+` from vendor_role where tenant_id = $1 and id = $2`, caller.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Vendor{}, vendorNotFound()
	}
	return v, err
}

func (s *Postgres) ListPartyVendors(ctx context.Context, partyID string) ([]domain.Vendor, error) {
	caller, _ := tenant.FromContext(ctx)
	id, err := canonicalID(partyID)
	if err != nil {
		return nil, ErrNotFound
	}
	vendors := []domain.Vendor{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var known bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from party where tenant_id = $1 and id = $2)`, caller.TenantID, id).Scan(&known); err != nil {
			return err
		}
		if !known {
			return ErrNotFound
		}
		rows, err := tx.Query(ctx, `select `+vendorColumns+` from vendor_role where tenant_id = $1 and party_id = $2 order by legal_entity_id`, caller.TenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanVendor(rows)
			if err != nil {
				return err
			}
			vendors = append(vendors, v)
		}
		return rows.Err()
	})
	return vendors, err
}

func (s *Postgres) AddBankAccount(ctx context.Context, vendorID string, in domain.NewBankAccount) (domain.BankAccount, error) {
	caller, _ := tenant.FromContext(ctx)
	if err := requireUser(caller); err != nil {
		return domain.BankAccount{}, err
	}
	if s.keys == nil {
		return domain.BankAccount{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeBankUnavailable, "bank details cannot be stored until field encryption keys are configured")
	}
	id, err := canonicalID(vendorID)
	if err != nil {
		return domain.BankAccount{}, vendorNotFound()
	}
	account, err := s.keys.Seal(caller.TenantID, FieldBankAccountNo, []byte(in.AccountNumber))
	if err != nil {
		return domain.BankAccount{}, err
	}
	var routing []byte
	if in.RoutingNumber != "" {
		if routing, err = s.keys.Seal(caller.TenantID, FieldBankRoutingNo, []byte(in.RoutingNumber)); err != nil {
			return domain.BankAccount{}, err
		}
	}
	var b domain.BankAccount
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `insert into vendor_bank_account (tenant_id, id, vendor_id, legal_entity_id, account_holder, bank_country, currency_code, account_last4, account_number_sealed, routing_number_sealed, created_by)
			select tenant_id, $3, id, legal_entity_id, $4, $5, $6, $7, $8, $9, $10 from vendor_role where tenant_id = $1 and id = $2
			returning `+bankColumns, caller.TenantID, id, ids.New(), in.AccountHolder, in.BankCountry, in.CurrencyCode, in.Last4(), account, routing, caller.UserID).
			Scan(&b.ID, &b.VendorID, &b.AccountHolder, &b.BankCountry, &b.CurrencyCode, &b.AccountLast4)
		if errors.Is(err, pgx.ErrNoRows) {
			return vendorNotFound()
		}
		return err
	})
	return b, err
}

func (s *Postgres) ListBankAccounts(ctx context.Context, vendorID string, reveal bool) ([]domain.BankAccount, error) {
	caller, _ := tenant.FromContext(ctx)
	if reveal && s.keys == nil {
		return nil, httpx.NewProblem(http.StatusServiceUnavailable, CodeBankUnavailable, "bank details cannot be read until field encryption keys are configured")
	}
	id, err := canonicalID(vendorID)
	if err != nil {
		return nil, vendorNotFound()
	}
	accounts := []domain.BankAccount{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var legalEntityID string
		if err := tx.QueryRow(ctx, `select legal_entity_id::text from vendor_role where tenant_id = $1 and id = $2`, caller.TenantID, id).Scan(&legalEntityID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return vendorNotFound()
			}
			return err
		}
		rows, err := tx.Query(ctx, `select `+bankColumns+`, account_number_sealed, routing_number_sealed from vendor_bank_account where tenant_id = $1 and vendor_id = $2 order by created_at, id`, caller.TenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b domain.BankAccount
			var account, routing []byte
			if err := rows.Scan(&b.ID, &b.VendorID, &b.AccountHolder, &b.BankCountry, &b.CurrencyCode, &b.AccountLast4, &account, &routing); err != nil {
				return err
			}
			if reveal {
				if err := s.reveal(caller.TenantID, &b, account, routing); err != nil {
					return err
				}
			}
			accounts = append(accounts, b)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		if !reveal {
			return nil
		}
		evt, err := events.New(partyevents.VendorBankRevealed, domain.EventSource, id, caller.TenantID, partyevents.VendorBankRevealedData{
			VendorID: id, LegalEntityID: legalEntityID, ActorUserID: caller.UserID, ActorService: caller.Service, AccountCount: len(accounts), RevealedAt: time.Now().UTC(),
		})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, domain.AggregateVendor, id, evt)
	})
	return accounts, err
}

func (s *Postgres) reveal(tenantID string, b *domain.BankAccount, account, routing []byte) error {
	plain, err := s.keys.Open(tenantID, FieldBankAccountNo, account)
	if err != nil {
		return err
	}
	b.AccountNumber = string(plain)
	if routing != nil {
		if plain, err = s.keys.Open(tenantID, FieldBankRoutingNo, routing); err != nil {
			return err
		}
		b.RoutingNumber = string(plain)
	}
	return nil
}
