package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
)

// GS1Company is the tenant's GS1 company prefix and the serial counter SSCCs are drawn from (#32, 32-3).
type GS1Company struct {
	CompanyPrefix string    `json:"companyPrefix"`
	Extension     int       `json:"extension"`
	NextSerial    int64     `json:"nextSerial"`
	Issued        int64     `json:"issued"`
	UpdatedBy     string    `json:"updatedBy"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// ErrGS1NotSet is returned by GetGS1Company before a prefix is set.
var ErrGS1NotSet = errors.New("store: no GS1 company prefix")

func scanGS1(row pgx.Row) (GS1Company, error) {
	var c GS1Company
	err := row.Scan(&c.CompanyPrefix, &c.Extension, &c.NextSerial, &c.UpdatedBy, &c.UpdatedAt)
	c.Issued = c.NextSerial - 1
	return c, err
}

// GetGS1Company returns the settings, or ErrGS1NotSet.
func (s *Store) GetGS1Company(ctx context.Context) (GS1Company, error) {
	var out GS1Company
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = scanGS1(tx.QueryRow(ctx, `select company_prefix, extension, next_serial, updated_by, updated_at from gs1_company where tenant_id = $1`, tenantID(ctx)))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrGS1NotSet
		}
		return err
	})
	return out, err
}

// PutGS1Company sets the company prefix and extension digit. Once SSCCs have been issued the prefix cannot change
// (a different prefix is a different number space and the old labels would no longer match their settings); the
// extension digit can.
func (s *Store) PutGS1Company(ctx context.Context, prefix string, extension int, by string) (GS1Company, error) {
	var out GS1Company
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := scanGS1(tx.QueryRow(ctx, `select company_prefix, extension, next_serial, updated_by, updated_at from gs1_company where tenant_id = $1 for update`, tenantID(ctx)))
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			out, err = scanGS1(tx.QueryRow(ctx, `insert into gs1_company (tenant_id, company_prefix, extension, updated_by) values ($1, $2, $3, $4)
				returning company_prefix, extension, next_serial, updated_by, updated_at`, tenantID(ctx), prefix, extension, by))
			return err
		case err != nil:
			return err
		case cur.CompanyPrefix != prefix && cur.Issued > 0:
			return &domain.Error{Code: domain.CodeGS1Locked, Detail: "SSCCs were issued under the prefix " + cur.CompanyPrefix + "; it cannot be changed"}
		}
		out, err = scanGS1(tx.QueryRow(ctx, `update gs1_company set company_prefix = $2, extension = $3, updated_by = $4, updated_at = now() where tenant_id = $1
			returning company_prefix, extension, next_serial, updated_by, updated_at`, tenantID(ctx), prefix, extension, by))
		return err
	})
	return out, err
}

// nextSSCC draws the next SSCC under a row lock, so two receipts never get the same one.
func nextSSCC(ctx context.Context, tx pgx.Tx) (string, error) {
	var prefix string
	var ext int
	var n int64
	err := tx.QueryRow(ctx, `select company_prefix, extension, next_serial from gs1_company where tenant_id = $1 for update`, tenantID(ctx)).Scan(&prefix, &ext, &n)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", &domain.Error{Code: domain.CodeGS1NotSet, Detail: "set the GS1 company prefix (PUT /v1/warehouse/gs1-settings) before pallets are labelled"}
	}
	if err != nil {
		return "", err
	}
	sscc, err := domain.SSCCForSerial(ext, prefix, n)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `update gs1_company set next_serial = next_serial + 1 where tenant_id = $1`, tenantID(ctx)); err != nil {
		return "", err
	}
	return sscc, nil
}

// identifyPallet gives a pallet received now its identity: an SSCC (its license plate when the ASN named one, else
// drawn from the tenant's counter) and a license plate id (the ASN's, else the SSCC). A pallet the ASN gave a
// non-SSCC license plate and no SSCC is labelled with that id alone while no GS1 prefix is set, as before.
func identifyPallet(ctx context.Context, tx pgx.Tx, l *domain.ReceiptLine) error {
	if l.SSCC == "" && domain.ValidSSCC(l.LPID) {
		l.SSCC = l.LPID
	}
	if l.SSCC == "" {
		sscc, err := nextSSCC(ctx, tx)
		var de *domain.Error
		switch {
		case err == nil:
			l.SSCC = sscc
		case errors.As(err, &de) && de.Code == domain.CodeGS1NotSet && l.LPID != "":
			// the ASN named a license plate and no GS1 prefix is set: receive it as before, unlabelled with an SSCC
		default:
			return err
		}
	}
	if l.LPID == "" {
		l.LPID = l.SSCC
	}
	return nil
}
