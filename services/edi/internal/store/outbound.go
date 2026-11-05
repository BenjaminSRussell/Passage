package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
)

// Outbound is one outbound document ready to be enveloped and recorded.
type Outbound struct {
	PartnerID      string
	TransactionSet string
	// Canonical is the normalized source document, stored on the message.
	Canonical []byte
	// Build serializes the interchange for the control number allocated inside
	// the recording transaction (ISA13 and GS06, gap-free per partner, BR-35.5).
	Build func(control int64) (string, error)
}

// OutboundResult is the recorded outbound interchange. A replay of the same
// Idempotency-Key returns the same result.
type OutboundResult struct {
	InterchangeID  string `json:"interchangeId"`
	MessageID      string `json:"messageId"`
	TransactionSet string `json:"transactionSet"`
	ControlNumber  string `json:"controlNumber"`
	X12            string `json:"x12"`
	// Delivery is the queued transmission when the partner has an AS2 or
	// SFTP transport; without one the document is only returned here.
	Delivery *DeliveryView `json:"delivery,omitempty"`
}

// CreateOutbound records an outbound interchange with its one message (status
// MAPPED, awaiting transport) and ack_status PENDING until the partner's 997
// is matched. When the partner has an AS2 or SFTP transport in the caller's
// scope a PENDING delivery is queued in the same transaction; the dispatcher
// sends it and the message becomes SENT once the partner confirms receipt. The control number comes from the same counter as the 997s
// sent to the partner, so outbound ISA13 values stay gap-free. A replay with
// the same key and request hash returns the stored result with created=false.
func (s *Store) CreateOutbound(ctx context.Context, key, requestHash string, o Outbound) (OutboundResult, bool, error) {
	var out OutboundResult
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		if replayed, err := claimKey(ctx, tx, tid, key, requestHash, &out); err != nil || replayed {
			return err
		}
		n, err := nextControl(ctx, tx, tid, o.PartnerID)
		if err != nil {
			return err
		}
		raw, err := o.Build(n)
		if err != nil {
			return err
		}
		out = OutboundResult{InterchangeID: ids.New(), MessageID: ids.New(), TransactionSet: o.TransactionSet,
			ControlNumber: fmt.Sprintf("%09d", n), X12: raw}
		if _, err := tx.Exec(ctx, `insert into interchange (tenant_id, id, partner_id, direction, standard, version,
			control_number, raw_payload, ack_status)
			values ($1,$2,$3,'OUT','X12',$4,$5,$6,'PENDING')`,
			tid, out.InterchangeID, o.PartnerID, domain.OutboundVersion, out.ControlNumber, raw); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into message (tenant_id, id, interchange_id, transaction_set, st_control, status, canonical)
			values ($1,$2,$3,$4,'0001','MAPPED',$5)`,
			tid, out.MessageID, out.InterchangeID, o.TransactionSet, nullBytes(o.Canonical)); err != nil {
			return err
		}
		if out.Delivery, err = enqueueDelivery(ctx, tx, tid, o.PartnerID, out.InterchangeID, "DOCUMENT", o.TransactionSet, out.ControlNumber); err != nil {
			return err
		}
		if err := storeResponse(ctx, tx, key, out); err != nil {
			return err
		}
		created = true
		return nil
	})
	return out, created, err
}

type outboundMessage struct {
	id, setID, control, status string
}

// matchAck applies a received 997 (m.Ack) to the outbound interchange whose
// ISA13 equals the 997's AK102 for the same partner. Every answered message
// still MAPPED or SENT becomes ACKED (AK5 A or E) or FAILED (AK5 R), the
// interchange takes the AK9 status unless it was already answered, and one
// message.acked event per answered message goes to the outbox in the same
// transaction. m itself becomes POSTED when it matched and FAILED otherwise.
func matchAck(ctx context.Context, tx pgx.Tx, tid, partnerID string, m *Message) error {
	ack := m.Ack
	unmatched := func(reason string) {
		m.Status, m.Error = "FAILED", domain.CodeUnmatchedAck+": "+reason
	}
	var interchangeID, control string
	err := tx.QueryRow(ctx, `select id::text, control_number from interchange
		where partner_id = $1 and direction = 'OUT' and standard = 'X12' and control_number = lpad($2, 9, '0')
		order by received_at desc, id desc limit 1 for update`, partnerID, ack.GroupControl).Scan(&interchangeID, &control)
	if errors.Is(err, pgx.ErrNoRows) {
		unmatched(fmt.Sprintf("no outbound interchange %s to this partner answers this 997", ack.GroupControl))
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `select id::text, transaction_set, st_control, status from message
		where interchange_id = $1 order by st_control, id for update`, interchangeID)
	if err != nil {
		return err
	}
	msgs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (outboundMessage, error) {
		var om outboundMessage
		return om, r.Scan(&om.id, &om.setID, &om.control, &om.status)
	})
	if err != nil {
		return err
	}
	for _, om := range msgs {
		if domain.FunctionalIDOf(om.setID) != ack.FunctionalID {
			unmatched(fmt.Sprintf("AK101 %s does not match outbound group %s (%s)", ack.FunctionalID, ack.GroupControl, om.setID))
			return nil
		}
	}

	// Each answered message and its AK5 code; a 997 without AK2 loops answers
	// the whole group with its AK9 code (P cannot name a set, so it counts as E).
	type answer struct {
		msg  outboundMessage
		code string
	}
	var answers []answer
	var notes []string
	if len(ack.Sets) == 0 {
		code := ack.Code
		if code == domain.AckPartial {
			code = domain.AckAcceptedWithError
		}
		for _, om := range msgs {
			answers = append(answers, answer{om, code})
		}
	} else {
		for _, set := range ack.Sets {
			found := false
			for _, om := range msgs {
				if om.setID == set.SetID && om.control == set.Control {
					answers = append(answers, answer{om, set.Code})
					found = true
				}
			}
			if !found {
				notes = append(notes, fmt.Sprintf("AK2 %s %s is not in outbound interchange %s", set.SetID, set.Control, control))
			}
		}
	}

	applied := 0
	for _, a := range answers {
		if a.msg.status != "MAPPED" && a.msg.status != "SENT" {
			continue // already answered by an earlier 997
		}
		status, errText, ackStatus := "ACKED", "", domain.StatusAccepted
		switch a.code {
		case domain.AckAcceptedWithError:
			errText, ackStatus = "accepted with errors by the partner 997 (AK5 E)", domain.StatusAcceptedWithErrors
		case domain.AckRejected:
			status, errText, ackStatus = "FAILED", "rejected by the partner 997 (AK5 R)", domain.StatusRejected
		}
		if _, err := tx.Exec(ctx, `update message set status = $2, error = $3 where id = $1`, a.msg.id, status, nullString(errText)); err != nil {
			return err
		}
		evt, err := events.New(ediev.MessageAcked, eventSource, a.msg.id, tid, ediev.MessageAckedData{
			MessageID: a.msg.id, InterchangeID: interchangeID, PartnerID: partnerID, TransactionSet: a.msg.setID,
			ControlNumber: control, STControl: a.msg.control, AckStatus: string(ackStatus), AckMessageID: m.ID,
		})
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, "message", a.msg.id, evt); err != nil {
			return fmt.Errorf("edi: outbox: %w", err)
		}
		applied++
	}
	if _, err := tx.Exec(ctx, `update interchange set ack_status = $2, acked_at = now() where id = $1 and ack_status in ('PENDING', 'TIMEOUT')`,
		interchangeID, string(domain.InterchangeAckStatus(ack.Code))); err != nil {
		return err
	}
	if applied == 0 && len(answers) > 0 {
		notes = append(notes, fmt.Sprintf("outbound interchange %s was already acknowledged", control))
	}
	m.Status, m.Error = "POSTED", strings.Join(notes, "; ")
	return nil
}
