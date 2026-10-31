package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

var (
	ErrNoFieldCrypt  = errors.New("field encryption keys are not configured; one-time payees cannot be stored")
	ErrPayeeNotFound = errors.New("one-time payee not found")
)

const (
	payeeNameField    = "receiving_one_time_payee.name"
	payeeAddressField = "receiving_one_time_payee.address"
	payeeTaxIDField   = "receiving_one_time_payee.tax_id"
	payeeEmailField   = "receiving_one_time_payee.email"
	payeeBankField    = "receiving_one_time_payee.bank"
)

type OneTimePayee struct {
	ID            string
	LegalEntityID string
	Country       string
	Name          string
	Address       string
	TaxID         string
	Email         string
	Bank          *domain.PayeeBank
	CreatedBy     string
	CreatedAt     time.Time
}

func (s *Postgres) WithFieldCrypt(k *fieldcrypt.Keys) *Postgres {
	s.crypt = k
	return s
}

func (s *Postgres) sealOptional(tenantID, field, plain string) ([]byte, error) {
	if plain == "" {
		return nil, nil
	}
	return s.crypt.Seal(tenantID, field, []byte(plain))
}

func (s *Postgres) CreateOneTimePayee(ctx context.Context, in OneTimePayee) (OneTimePayee, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return OneTimePayee{}, err
	}
	if s.crypt == nil {
		return OneTimePayee{}, ErrNoFieldCrypt
	}
	name, err := s.crypt.Seal(tenantID, payeeNameField, []byte(in.Name))
	if err != nil {
		return OneTimePayee{}, err
	}
	address, err := s.crypt.Seal(tenantID, payeeAddressField, []byte(in.Address))
	if err != nil {
		return OneTimePayee{}, err
	}
	taxID, err := s.sealOptional(tenantID, payeeTaxIDField, in.TaxID)
	if err != nil {
		return OneTimePayee{}, err
	}
	email, err := s.sealOptional(tenantID, payeeEmailField, in.Email)
	if err != nil {
		return OneTimePayee{}, err
	}
	var bank []byte
	if in.Bank != nil {
		plain, err := json.Marshal(in.Bank)
		if err != nil {
			return OneTimePayee{}, err
		}
		if bank, err = s.crypt.Seal(tenantID, payeeBankField, plain); err != nil {
			return OneTimePayee{}, err
		}
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO receiving_one_time_payee (tenant_id, id, legal_entity_id, country, name_sealed, address_sealed, tax_id_sealed, email_sealed, bank_sealed, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING created_at`,
			tenantID, in.ID, in.LegalEntityID, in.Country, name, address, taxID, email, bank, in.CreatedBy).Scan(&in.CreatedAt)
	})
	return in, err
}

func checkPayeeForInvoice(ctx context.Context, tx pgx.Tx, tenantID string, in Invoice) error {
	var legalEntityID string
	err := tx.QueryRow(ctx, `SELECT legal_entity_id::text FROM receiving_one_time_payee WHERE tenant_id = $1 AND id = $2`, tenantID, in.OneTimePayeeID).Scan(&legalEntityID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPayeeNotFound
	}
	if err != nil {
		return err
	}
	if legalEntityID != in.LegalEntityID {
		return &domain.Error{Code: domain.CodeInvalidReceipt, Detail: "the one-time payee belongs to another legal entity"}
	}
	return nil
}
