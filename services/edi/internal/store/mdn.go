package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
)

// OwedMDN is an asynchronous MDN we owe a partner: the signed MDN as it will
// be posted (headers and body), the URL the partner asked for and the
// Message-ID of the partner message it answers (without angle brackets).
type OwedMDN struct {
	OriginalMessageID string
	MessageID         string
	TargetURL         string
	Header            map[string][]string
	Body              []byte
}

func insertMDN(ctx context.Context, tx pgx.Tx, tid, partnerID, legalEntityID, interchangeID string, m OwedMDN) error {
	hdr, err := json.Marshal(m.Header)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into transport_mdn (tenant_id, id, partner_id, legal_entity_id, original_message_id, mdn_message_id,
		target_url, headers, body, interchange_id, status) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'PENDING')`,
		tid, ids.New(), partnerID, legalEntityID, m.OriginalMessageID, m.MessageID, m.TargetURL, hdr, m.Body, nullString(interchangeID))
	return err
}

// QueueMDN stores an MDN we owe outside a receipt: one that refuses the
// message or reports it as a duplicate (nothing was recorded). The MDN
// answering a recorded message is stored with the receipt instead
// (InboundSource.MDN).
func (s *Store) QueueMDN(ctx context.Context, partnerID, legalEntityID string, m OwedMDN) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		return insertMDN(ctx, tx, tid, partnerID, legalEntityID, "", m)
	})
}

// ClaimedMDN is an owed MDN leased to one dispatcher, with the partner's
// current AS2 URL (empty when the partner no longer has an AS2 transport).
type ClaimedMDN struct {
	ID                string
	PartnerID         string
	OriginalMessageID string
	MessageID         string
	TargetURL         string
	Header            map[string][]string
	Body              []byte
	Attempts          int
	PartnerURL        string
}

// ClaimMDNs leases up to limit due PENDING MDNs of the context's tenant, like
// ClaimDeliveries: the next attempt moves lease ahead and the attempt count
// goes up in one statement with skip locked.
func (s *Store) ClaimMDNs(ctx context.Context, limit int, lease time.Duration) ([]ClaimedMDN, error) {
	var out []ClaimedMDN
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `with claimed as (
				select id as claimed_id from transport_mdn where status = 'PENDING' and next_attempt_at <= now()
				order by next_attempt_at, id limit $1 for update skip locked)
			update transport_mdn m set attempts = m.attempts + 1, next_attempt_at = now() + make_interval(secs => $2)
			from claimed where m.id = claimed.claimed_id
			returning m.id::text, m.partner_id::text, m.original_message_id, m.mdn_message_id, m.target_url, m.headers, m.body, m.attempts,
				coalesce((select t.as2_url from partner_transport t where t.partner_id = m.partner_id and t.kind = 'AS2'), '')`,
			limit, lease.Seconds())
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (ClaimedMDN, error) {
			var c ClaimedMDN
			var hdr []byte
			if err := r.Scan(&c.ID, &c.PartnerID, &c.OriginalMessageID, &c.MessageID, &c.TargetURL, &hdr, &c.Body, &c.Attempts, &c.PartnerURL); err != nil {
				return c, err
			}
			return c, json.Unmarshal(hdr, &c.Header)
		})
		return err
	})
	return out, err
}

// MDNOutcome is what one attempt to post an owed MDN achieved: DELIVERED,
// FAILED, or PENDING to try again at RetryAt.
type MDNOutcome struct {
	Status  string
	RetryAt time.Time
	Error   string
}

// FinishMDN records the outcome of an attempt on a claimed MDN.
func (s *Store) FinishMDN(ctx context.Context, id string, o MDNOutcome) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var retry any
		if o.Status == "PENDING" {
			retry = o.RetryAt
		}
		tag, err := tx.Exec(ctx, `update transport_mdn set status = $2, next_attempt_at = coalesce($3, next_attempt_at), last_error = $4,
			delivered_at = case when $2 = 'DELIVERED' then now() else delivered_at end where id = $1`,
			id, o.Status, retry, nullString(clip(o.Error, 1000)))
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return err
	})
}
