package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
)

// CXMLOutboundConfig is where we post to the partner's network and who we are there.
type CXMLOutboundConfig struct {
	OutboundURL       string `json:"outboundUrl,omitempty"`
	OurIdentity       string `json:"ourIdentity,omitempty"`
	OutboundSecretRef string `json:"outboundSecretRef,omitempty"`
	VendorID          string `json:"vendorId,omitempty"`
}

// PutCXMLOutbound stores the outbound settings of a partner's cXML identity (ErrNotFound without the identity: PUT /partners/{id}/cxml
// comes first).
func (s *Store) PutCXMLOutbound(ctx context.Context, partnerID string, c CXMLOutboundConfig) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update partner_cxml set outbound_url = nullif($2,''), our_identity = nullif($3,''), outbound_secret_ref = nullif($4,''), vendor_id = nullif($5,'')::uuid, updated_at = now()
			where partner_id = $1`, partnerID, c.OutboundURL, c.OurIdentity, c.OutboundSecretRef, c.VendorID)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return err
	})
}

// GetCXMLOutbound returns the outbound settings (empty fields when none are set); ErrNotFound without a cXML identity.
func (s *Store) GetCXMLOutbound(ctx context.Context, partnerID string) (CXMLOutboundConfig, error) {
	var out CXMLOutboundConfig
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select coalesce(outbound_url, ''), coalesce(our_identity, ''), coalesce(outbound_secret_ref, ''), coalesce(vendor_id::text, '') from partner_cxml where partner_id = $1`, partnerID).
			Scan(&out.OutboundURL, &out.OurIdentity, &out.OutboundSecretRef, &out.VendorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return out, err
}

// CXMLOrder is a purchase order sent to a partner as an OrderRequest under its orderID.
type CXMLOrder struct {
	PartnerID       string
	OrderID         string
	PurchaseOrderID string
	LegalEntityID   string
	VendorID        string
}

// GetCXMLOrder finds the purchase order sent under an orderID; ErrNotFound when none was.
func (s *Store) GetCXMLOrder(ctx context.Context, partnerID, orderID string) (CXMLOrder, error) {
	out := CXMLOrder{PartnerID: partnerID, OrderID: orderID}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select purchase_order_id::text, legal_entity_id::text, vendor_id::text from cxml_order where partner_id = $1 and order_id = $2`, partnerID, orderID).
			Scan(&out.PurchaseOrderID, &out.LegalEntityID, &out.VendorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return out, err
}

// CXMLOutbound is one outbound cXML document ready to be recorded; Order is set for an OrderRequest.
type CXMLOutbound struct {
	PartnerID string
	PayloadID string
	DocType   string
	Raw       string
	Canonical []byte
	Order     *CXMLOrder
}

// CXMLOutboundRecord ids the recorded document.
type CXMLOutboundRecord struct {
	InterchangeID string `json:"interchangeId"`
	MessageID     string `json:"messageId"`
	PayloadID     string `json:"payloadId"`
}

// RecordCXMLOutbound stores an outbound document before it is posted (interchange OUT, standard CXML, ack PENDING; message MAPPED) and,
// for an OrderRequest, the order it was sent under. The payloadID is unique per document.
func (s *Store) RecordCXMLOutbound(ctx context.Context, o CXMLOutbound) (CXMLOutboundRecord, error) {
	out := CXMLOutboundRecord{InterchangeID: ids.New(), MessageID: ids.New(), PayloadID: o.PayloadID}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into interchange (tenant_id, id, partner_id, direction, standard, version, control_number, raw_payload, ack_status)
			values ($1,$2,$3,'OUT','CXML','1.2',$4,$5,'PENDING')`, tid, out.InterchangeID, o.PartnerID, o.PayloadID, o.Raw); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into message (tenant_id, id, interchange_id, transaction_set, st_control, status, canonical) values ($1,$2,$3,$4,$5,'MAPPED',$6)`,
			tid, out.MessageID, out.InterchangeID, o.DocType, o.PayloadID, nullBytes(o.Canonical)); err != nil {
			return err
		}
		if o.Order != nil {
			// A purchase order sent again (a second OrderRequest) keeps the order id it was first sent under.
			_, err := tx.Exec(ctx, `insert into cxml_order (tenant_id, partner_id, order_id, purchase_order_id, legal_entity_id, vendor_id) values ($1,$2,$3,$4,$5,$6)
				on conflict (tenant_id, partner_id, order_id) do update set sent_at = now() where cxml_order.purchase_order_id = excluded.purchase_order_id`,
				tid, o.PartnerID, o.Order.OrderID, o.Order.PurchaseOrderID, o.Order.LegalEntityID, o.Order.VendorID)
			return err
		}
		return nil
	})
	return out, err
}

// FinishCXMLOutbound records the network's answer to a posted document: ACCEPTED and SENT on a 200 Response, else REJECTED and
// FAILED with the text (a failed document is sent again by the operator as a new payload).
func (s *Store) FinishCXMLOutbound(ctx context.Context, rec CXMLOutboundRecord, accepted bool, text string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ack, status := "REJECTED", "FAILED"
		if accepted {
			ack, status = "ACCEPTED", "SENT"
		}
		if _, err := tx.Exec(ctx, `update interchange set ack_status = $2, ack_payload = nullif($3,'') where id = $1`, rec.InterchangeID, ack, text); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `update message set status = $2, error = nullif($3,'') where id = $1`, rec.MessageID, status, errText(accepted, text))
		return err
	})
}

func errText(accepted bool, text string) string {
	if accepted {
		return ""
	}
	return text
}

// DownstreamOf returns what a message became downstream ("", "" when nothing).
func (s *Store) DownstreamOf(ctx context.Context, messageID string) (kind, ref string, err error) {
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select coalesce(downstream_kind, ''), coalesce(downstream_ref::text, '') from message where id = $1`, messageID).Scan(&kind, &ref)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return kind, ref, err
}

// MarkMapped records that an inbound cXML message was put onto a service (a purchase order confirmed, an ASN created): POSTED with the
// object. A message already POSTED to the same object is left alone.
func (s *Store) MarkMapped(ctx context.Context, messageID, kind, ref string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update message set status = 'POSTED', downstream_kind = $2, downstream_ref = $3, park_code = null, error = null`+postableWhere, messageID, kind, ref)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			return nil
		}
		var status, k, r string
		err = tx.QueryRow(ctx, `select status, coalesce(downstream_kind, ''), coalesce(downstream_ref::text, '') from message where id = $1`, messageID).Scan(&status, &k, &r)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return err
		case status == "POSTED" && k == kind && r == ref:
			return nil
		}
		return ErrNotPostable
	})
}
