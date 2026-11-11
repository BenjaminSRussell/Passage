package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
)

// PartnerCXML is a partner's cXML identity: the network identity it sends under, the id of the URL it posts to, and the hash of its
// shared secret (the secret itself is never stored).
type PartnerCXML struct {
	PartnerID       string `json:"partnerId"`
	LegalEntityID   string `json:"legalEntityId"`
	EndpointID      string `json:"endpointId"`
	NetworkIdentity string `json:"networkIdentity"`
	SecretHash      string `json:"-"`
}

// PutPartnerCXML stores the identity and the secret hash of a partner under the legal entity of its trade profile (ErrNotFound without
// one). The endpoint id is created once and kept, so the partner's URL does not change when the secret is rotated.
func (s *Store) PutPartnerCXML(ctx context.Context, partnerID, networkIdentity, secretHash string) (PartnerCXML, error) {
	out := PartnerCXML{PartnerID: partnerID}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `insert into partner_cxml (tenant_id, partner_id, legal_entity_id, endpoint_id, network_identity, secret_hash)
			select $1::uuid, partner_id, legal_entity_id, $3::uuid, $4, $5 from partner_trade_profile where partner_id = $2
			on conflict (tenant_id, partner_id) do update set network_identity = excluded.network_identity, secret_hash = excluded.secret_hash, updated_at = now()
			returning legal_entity_id::text, endpoint_id::text, network_identity`, tid, partnerID, ids.New(), networkIdentity, secretHash).
			Scan(&out.LegalEntityID, &out.EndpointID, &out.NetworkIdentity)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return out, err
}

// GetPartnerCXML returns the identity or ErrNotFound.
func (s *Store) GetPartnerCXML(ctx context.Context, partnerID string) (PartnerCXML, error) {
	out := PartnerCXML{PartnerID: partnerID}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select legal_entity_id::text, endpoint_id::text, network_identity, secret_hash from partner_cxml where partner_id = $1`, partnerID).
			Scan(&out.LegalEntityID, &out.EndpointID, &out.NetworkIdentity, &out.SecretHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return out, err
}

// CXMLInbound is one authenticated payload from a network, ready to be recorded.
type CXMLInbound struct {
	PartnerID string
	PayloadID string
	DocType   string
	Hash      string
	Raw       string
	Canonical []byte
	// Error is set when the document could not be mapped: the message is recorded FAILED and the network answers ResponseCode.
	Error        string
	ResponseCode int
	ResponseText string
}

// CXMLResult is what to answer: the recorded response of a new payload or, for a repeat of the same content, the original one.
type CXMLResult struct {
	Decision     domain.RepeatDecision
	ResponseCode int
	ResponseText string
	MessageID    string
}

// ReceiveCXML records a payload under BR-35.14: a payloadID not seen before is stored (interchange, message, message.received and the
// response it gets); the same payloadID with the same content returns the first response and stores nothing; the same payloadID with
// other content stores nothing and is a conflict (EDI-0135). Concurrent copies of one payload serialize on the payload id.
func (s *Store) ReceiveCXML(ctx context.Context, in CXMLInbound) (CXMLResult, error) {
	var out CXMLResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "edi:cxml:"+tid+":"+in.PartnerID+":"+in.PayloadID); err != nil {
			return err
		}
		var stored string
		var code int
		var text, msg string
		err = tx.QueryRow(ctx, `select content_hash, response_code, response_text, coalesce(message_id::text, '') from cxml_payload where partner_id = $1 and payload_id = $2`,
			in.PartnerID, in.PayloadID).Scan(&stored, &code, &text, &msg)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			out = CXMLResult{Decision: domain.ResolveRepeat(stored, in.Hash), ResponseCode: code, ResponseText: text, MessageID: msg}
			return nil
		}
		interchangeID, messageID := ids.New(), ids.New()
		status, ack := "MAPPED", "ACCEPTED"
		if in.Error != "" {
			status, ack = "FAILED", "REJECTED"
		}
		if _, err := tx.Exec(ctx, `insert into interchange (tenant_id, id, partner_id, direction, standard, version, control_number, raw_payload, ack_status)
			values ($1,$2,$3,'IN','CXML','1.2',$4,$5,$6)`, tid, interchangeID, in.PartnerID, in.PayloadID, in.Raw, ack); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into message (tenant_id, id, interchange_id, transaction_set, st_control, status, canonical, error)
			values ($1,$2,$3,$4,$5,$6,$7,$8)`, tid, messageID, interchangeID, in.DocType, in.PayloadID, status, nullBytes(in.Canonical), nullString(in.Error)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into cxml_payload (tenant_id, partner_id, payload_id, doc_type, content_hash, response_code, response_text, message_id)
			values ($1,$2,$3,$4,$5,$6,$7,$8)`, tid, in.PartnerID, in.PayloadID, in.DocType, in.Hash, in.ResponseCode, in.ResponseText, messageID); err != nil {
			return err
		}
		if err := enqueueReceived(ctx, tx, tid, ediev.MessageReceivedData{MessageID: messageID, InterchangeID: interchangeID, PartnerID: in.PartnerID,
			Standard: "CXML", TransactionSet: in.DocType, ControlNumber: in.PayloadID, STControl: in.PayloadID, Status: status}); err != nil {
			return err
		}
		if in.Error != "" {
			if err := enqueueFailed(ctx, tx, tid, ediev.MessageFailedData{MessageID: messageID, InterchangeID: interchangeID, PartnerID: in.PartnerID,
				TransactionSet: in.DocType, ControlNumber: in.PayloadID, STControl: in.PayloadID, Error: in.Error}); err != nil {
				return err
			}
		}
		out = CXMLResult{Decision: domain.RepeatNew, ResponseCode: in.ResponseCode, ResponseText: in.ResponseText, MessageID: messageID}
		return nil
	})
	return out, err
}
