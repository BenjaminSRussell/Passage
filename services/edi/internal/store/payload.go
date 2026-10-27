package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

// Payload is the raw X12 text recorded for one interchange, inbound as
// received or outbound as generated. The API turns it into the segment tree
// and masks personal data (BR-35.12); it is never returned as text.
type Payload struct {
	InterchangeID string
	Direction     string
	Raw           string
}

// GetPayload returns the stored raw payload of one interchange, or ErrNotFound.
func (s *Store) GetPayload(ctx context.Context, id string) (Payload, error) {
	out := Payload{InterchangeID: id}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select direction, raw_payload from interchange where id = $1`, id).Scan(&out.Direction, &out.Raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return out, err
}
