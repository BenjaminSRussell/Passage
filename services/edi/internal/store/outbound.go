package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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
// Standard is X12 (the default when empty) or EDIFACT: an EDIFACT document
// (TransactionSet is its UNH message type) is numbered on the partner's
// EDIFACT counter, the one the CONTRLs we send it use.
type Outbound struct {
	PartnerID      string
	Standard       string
	TransactionSet string
	// Canonical is the normalized source document, stored on the message.
	Canonical []byte
	// Build serializes the interchange for the control number allocated inside
	// the recording transaction (ISA13 and GS06, or UNB 0020, gap-free per
	// partner and standard, BR-35.5).
	Build func(control int64) (string, error)
}

// OutboundResult is the recorded outbound interchange. A replay of the same
// Idempotency-Key returns the same result. ControlNumber is ISA13 (nine
// digits) or the UNB 0020 reference; X12 or Edifact is the serialized
// interchange.
type OutboundResult struct {
	InterchangeID  string `json:"interchangeId"`
	MessageID      string `json:"messageId"`
	Standard       string `json:"standard,omitempty"`
	TransactionSet string `json:"transactionSet"`
	ControlNumber  string `json:"controlNumber"`
	X12            string `json:"x12,omitempty"`
	Edifact        string `json:"edifact,omitempty"`
	// Delivery is the queued transmission when the partner has an AS2 or
	// SFTP transport; without one the document is only returned here.
	Delivery *DeliveryView `json:"delivery,omitempty"`
}

// CreateOutbound records an outbound interchange with its one message (status
// MAPPED, awaiting transport) and ack_status PENDING until the partner's 997
// (or, for EDIFACT, its CONTRL) is matched. When the partner has an AS2 or
// SFTP transport in the caller's scope a PENDING delivery is queued in the
// same transaction; the dispatcher sends it and the message becomes SENT once
// the partner confirms receipt. The control number comes from the same
// counter as the 997s (or CONTRLs) sent to the partner, so outbound ISA13 and
// UNB 0020 values stay gap-free. A replay with the same key and request hash
// returns the stored result with created=false.
func (s *Store) CreateOutbound(ctx context.Context, key, requestHash string, o Outbound) (OutboundResult, bool, error) {
	var out OutboundResult
	created := false
	standard, _ := standardOf(o.Standard)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		if replayed, err := claimKey(ctx, tx, tid, key, requestHash, &out); err != nil || replayed {
			return err
		}
		n, err := nextControl(ctx, tx, tid, o.PartnerID, standard)
		if err != nil {
			return err
		}
		raw, err := o.Build(n)
		if err != nil {
			return err
		}
		out = OutboundResult{InterchangeID: ids.New(), MessageID: ids.New(), Standard: standard, TransactionSet: o.TransactionSet}
		// The one message is ST02 0001 in X12 and UNH 0062 1 in EDIFACT.
		version, ref := domain.OutboundVersion, "0001"
		if standard == domain.SyntaxEdifact {
			out.ControlNumber, out.Edifact = strconv.FormatInt(n, 10), raw
			version, ref = domain.EdifactOutboundVersion, "1"
		} else {
			out.ControlNumber, out.X12 = fmt.Sprintf("%09d", n), raw
		}
		if _, err := tx.Exec(ctx, `insert into interchange (tenant_id, id, partner_id, direction, standard, version,
			control_number, raw_payload, ack_status)
			values ($1,$2,$3,'OUT',$7,$4,$5,$6,'PENDING')`,
			tid, out.InterchangeID, o.PartnerID, version, out.ControlNumber, raw, standard); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into message (tenant_id, id, interchange_id, transaction_set, st_control, status, canonical)
			values ($1,$2,$3,$4,$6,'MAPPED',$5)`,
			tid, out.MessageID, out.InterchangeID, o.TransactionSet, nullBytes(o.Canonical), ref); err != nil {
			return err
		}
		if out.Delivery, err = enqueueDelivery(ctx, tx, tid, o.PartnerID, out.InterchangeID, "DOCUMENT", standard, o.TransactionSet, out.ControlNumber); err != nil {
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

// lockOutbound finds and locks the partner's latest outbound interchange of
// the standard with this control number, with its messages. ok is false when
// there is none.
func lockOutbound(ctx context.Context, tx pgx.Tx, partnerID, standard, control string) (interchangeID string, msgs []outboundMessage, ok bool, err error) {
	err = tx.QueryRow(ctx, `select id::text from interchange
		where partner_id = $1 and direction = 'OUT' and standard = $2 and control_number = $3
		order by received_at desc, id desc limit 1 for update`, partnerID, standard, control).Scan(&interchangeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	rows, err := tx.Query(ctx, `select id::text, transaction_set, st_control, status from message
		where interchange_id = $1 order by st_control, id for update`, interchangeID)
	if err != nil {
		return "", nil, false, err
	}
	msgs, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (outboundMessage, error) {
		var om outboundMessage
		return om, r.Scan(&om.id, &om.setID, &om.control, &om.status)
	})
	return interchangeID, msgs, err == nil, err
}

// ackAnswer is what a partner's acknowledgment says about one outbound
// message: its ack status and the error text stored on it when it is not
// plainly accepted.
type ackAnswer struct {
	msg       outboundMessage
	ackStatus domain.AckStatus
	errText   string
}

// applyAnswers records the partner's acknowledgment on the answered outbound
// messages still MAPPED or SENT (ACKED, or FAILED when rejected), publishes
// one message.acked per message in the outbox, and gives the interchange
// status and acked_at unless it was already answered. It returns how many
// messages it changed; answers for messages an earlier acknowledgment
// answered change nothing.
func applyAnswers(ctx context.Context, tx pgx.Tx, tid, partnerID, interchangeID, control, ackMessageID string,
	answers []ackAnswer, status domain.AckStatus) (int, error) {
	applied := 0
	for _, a := range answers {
		if a.msg.status != "MAPPED" && a.msg.status != "SENT" {
			continue // already answered by an earlier acknowledgment
		}
		msgStatus := "ACKED"
		if a.ackStatus == domain.StatusRejected {
			msgStatus = "FAILED"
		}
		if _, err := tx.Exec(ctx, `update message set status = $2, error = $3 where id = $1`, a.msg.id, msgStatus, nullString(a.errText)); err != nil {
			return 0, err
		}
		evt, err := events.New(ediev.MessageAcked, eventSource, a.msg.id, tid, ediev.MessageAckedData{
			MessageID: a.msg.id, InterchangeID: interchangeID, PartnerID: partnerID, TransactionSet: a.msg.setID,
			ControlNumber: control, STControl: a.msg.control, AckStatus: string(a.ackStatus), AckMessageID: ackMessageID,
		})
		if err != nil {
			return 0, err
		}
		if err := outbox.Enqueue(ctx, tx, "message", a.msg.id, evt); err != nil {
			return 0, fmt.Errorf("edi: outbox: %w", err)
		}
		applied++
	}
	if _, err := tx.Exec(ctx, `update interchange set ack_status = $2, acked_at = now() where id = $1 and ack_status in ('PENDING', 'TIMEOUT')`,
		interchangeID, string(status)); err != nil {
		return 0, err
	}
	return applied, nil
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
	control := fmt.Sprintf("%09s", ack.GroupControl)
	interchangeID, msgs, ok, err := lockOutbound(ctx, tx, partnerID, domain.SyntaxX12, control)
	if err != nil {
		return err
	}
	if !ok {
		unmatched(fmt.Sprintf("no outbound interchange %s to this partner answers this 997", ack.GroupControl))
		return nil
	}
	for _, om := range msgs {
		if domain.FunctionalIDOf(om.setID) != ack.FunctionalID {
			unmatched(fmt.Sprintf("AK101 %s does not match outbound group %s (%s)", ack.FunctionalID, ack.GroupControl, om.setID))
			return nil
		}
	}

	// Each answered message and its AK5 code; a 997 without AK2 loops answers
	// the whole group with its AK9 code (P cannot name a set, so it counts as E).
	answer := func(om outboundMessage, code string) ackAnswer {
		switch code {
		case domain.AckAcceptedWithError:
			return ackAnswer{om, domain.StatusAcceptedWithErrors, "accepted with errors by the partner 997 (AK5 E)"}
		case domain.AckRejected:
			return ackAnswer{om, domain.StatusRejected, "rejected by the partner 997 (AK5 R)"}
		}
		return ackAnswer{msg: om, ackStatus: domain.StatusAccepted}
	}
	var answers []ackAnswer
	var notes []string
	if len(ack.Sets) == 0 {
		code := ack.Code
		if code == domain.AckPartial {
			code = domain.AckAcceptedWithError
		}
		for _, om := range msgs {
			answers = append(answers, answer(om, code))
		}
	} else {
		for _, set := range ack.Sets {
			found := false
			for _, om := range msgs {
				if om.setID == set.SetID && om.control == set.Control {
					answers = append(answers, answer(om, set.Code))
					found = true
				}
			}
			if !found {
				notes = append(notes, fmt.Sprintf("AK2 %s %s is not in outbound interchange %s", set.SetID, set.Control, control))
			}
		}
	}
	applied, err := applyAnswers(ctx, tx, tid, partnerID, interchangeID, control, m.ID, answers, domain.InterchangeAckStatus(ack.Code))
	if err != nil {
		return err
	}
	if applied == 0 && len(answers) > 0 {
		notes = append(notes, fmt.Sprintf("outbound interchange %s was already acknowledged", control))
	}
	m.Status, m.Error = "POSTED", strings.Join(notes, "; ")
	return nil
}

// matchContrl applies a received CONTRL (m.Contrl) to the partner's outbound
// EDIFACT interchange whose UNB 0020 equals the UCI 0020, as matchAck does
// for a 997: each message still MAPPED or SENT becomes ACKED, or FAILED when
// the UCI rejects the interchange or a UCM rejects the message; the
// interchange takes the CONTRL's status and acked_at unless it was already
// answered; one message.acked per answered message. m becomes POSTED when it
// matched and FAILED with EDI-0161 otherwise.
func matchContrl(ctx context.Context, tx pgx.Tx, tid, partnerID string, m *Message) error {
	c := m.Contrl
	interchangeID, msgs, ok, err := lockOutbound(ctx, tx, partnerID, domain.SyntaxEdifact, c.InterchangeRef)
	if err != nil {
		return err
	}
	if !ok {
		m.Status, m.Error = "FAILED", fmt.Sprintf("%s: no outbound EDIFACT interchange %s to this partner answers this CONTRL", domain.CodeUnmatchedAck, c.InterchangeRef)
		return nil
	}
	sent := make([]domain.EdifactMessage, len(msgs))
	for i, om := range msgs {
		sent[i] = domain.EdifactMessage{Ref: om.control, Type: om.setID}
	}
	got, notes := c.AnswerMessages(sent)
	answers := make([]ackAnswer, len(got))
	for i, a := range got {
		answers[i] = ackAnswer{msg: msgs[i], ackStatus: a.Status, errText: a.Detail}
	}
	applied, err := applyAnswers(ctx, tx, tid, partnerID, interchangeID, c.InterchangeRef, m.ID, answers, c.Status(len(msgs)))
	if err != nil {
		return err
	}
	if applied == 0 && len(answers) > 0 {
		notes = append(notes, fmt.Sprintf("outbound interchange %s was already acknowledged", c.InterchangeRef))
	}
	m.Status, m.Error = "POSTED", strings.Join(notes, "; ")
	return nil
}

// matchTA1 applies a received TA1 (m.TA1) to the partner's outbound X12
// interchange whose ISA13 equals TA101. A TA1 R rejects the interchange as a
// whole: every message still MAPPED or SENT becomes FAILED with the TA105
// note, the interchange becomes REJECTED with acked_at, and message.acked is
// published per message. A TA1 A or E only says the envelope was read; the
// 997 answers the groups, so nothing changes and the TA1 is noted. m becomes
// POSTED when it matched and FAILED with EDI-0161 otherwise.
func matchTA1(ctx context.Context, tx pgx.Tx, tid, partnerID string, m *Message) error {
	ta1 := m.TA1
	interchangeID, msgs, ok, err := lockOutbound(ctx, tx, partnerID, domain.SyntaxX12, ta1.Control)
	if err != nil {
		return err
	}
	if !ok {
		m.Status, m.Error = "FAILED", fmt.Sprintf("%s: no outbound interchange %s to this partner answers this TA1", domain.CodeUnmatchedAck, ta1.Control)
		return nil
	}
	if ta1.Code != domain.TA1Rejected {
		m.Status, m.Error = "POSTED", fmt.Sprintf("interchange %s accepted by the partner TA1 (TA104 %s, TA105 %s); its 997 answers the groups", ta1.Control, ta1.Code, ta1.Note)
		return nil
	}
	answers := make([]ackAnswer, len(msgs))
	for i, om := range msgs {
		answers[i] = ackAnswer{msg: om, ackStatus: domain.StatusRejected, errText: "interchange rejected by the partner TA1 (TA105 " + ta1.Note + ")"}
	}
	applied, err := applyAnswers(ctx, tx, tid, partnerID, interchangeID, ta1.Control, m.ID, answers, domain.StatusRejected)
	if err != nil {
		return err
	}
	m.Status = "POSTED"
	if applied == 0 && len(answers) > 0 {
		m.Error = fmt.Sprintf("outbound interchange %s was already acknowledged", ta1.Control)
	}
	return nil
}
