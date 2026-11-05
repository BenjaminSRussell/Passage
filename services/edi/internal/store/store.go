// Package store holds the Postgres repositories of the edi service. Every
// method runs inside dbx.WithTenantTx so row-level security applies, and every
// state change that other contexts need is written to the outbox in the same
// transaction (BR-35.5, FR-35.4).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
)

// eventSource is the CloudEvents source of every edi event.
const eventSource = "edi"

var (
	// ErrNotFound means no row matched the lookup.
	ErrNotFound = errors.New("edi: not found")
	// ErrDuplicate means a unique key (partner identity, or an inbound control
	// number inside the duplicate window) already exists.
	ErrDuplicate = errors.New("edi: duplicate")
	// ErrIdempotencyReuse means the Idempotency-Key was already used with a
	// different request.
	ErrIdempotencyReuse = errors.New("edi: idempotency key reused with a different request")
)

// Store is the edi repository set.
type Store struct {
	pool          *pgxpool.Pool
	dupWindowDays int
}

// New returns a store. dupWindowDays is the BR-35.5 duplicate interchange window.
func New(pool *pgxpool.Pool, dupWindowDays int) *Store {
	return &Store{pool: pool, dupWindowDays: dupWindowDays}
}

// Partner is a trading partner as stored.
type Partner struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ISAQualifier  string `json:"isaQualifier"`
	ISAID         string `json:"isaId"`
	Environment   string `json:"environment"`
	AckSLAMinutes int    `json:"ackSlaMinutes"`
	// The UNB identity of a partner that sends EDIFACT, and when its
	// interchanges are answered with a CONTRL (REQUESTED, ALWAYS, NEVER).
	EdifactID        string    `json:"edifactId,omitempty"`
	EdifactQualifier string    `json:"edifactQualifier,omitempty"`
	EdifactContrl    string    `json:"edifactContrl,omitempty"`
	Status           string    `json:"status"`
	Version          int       `json:"version"`
	CreatedAt        time.Time `json:"createdAt"`
}

// Edifact returns the partner's EDIFACT identity, or nil when it has none.
func (p Partner) Edifact() *domain.EdifactIdentity {
	if p.EdifactID == "" {
		return nil
	}
	return &domain.EdifactIdentity{ID: p.EdifactID, Qualifier: p.EdifactQualifier, Contrl: p.EdifactContrl}
}

// Message is one transaction set (X12) or message (EDIFACT: TransactionSet
// is the UNH message type, STControl the UNH 0062 reference) inside a
// received interchange. Ack is set for a parsed 997 and Contrl for a parsed
// CONTRL: ReceiveInterchange matches it to the outbound interchange it
// answers and sets Status to POSTED (matched) or FAILED (no match).
type Message struct {
	ID             string
	TransactionSet string
	STControl      string
	Status         string
	Canonical      []byte
	Error          string
	Ack            *domain.FunctionalAck
	Contrl         *domain.ContrlAck
}

// Inbound is one received interchange ready to be recorded. Standard is X12
// (the default when empty) or EDIFACT; ISAControl is the interchange control
// number (ISA13, or the EDIFACT UNB05 reference) the duplicate window
// applies to.
type Inbound struct {
	PartnerID  string
	Standard   string
	Version    string
	ISAControl string
	Raw        string
	AckStatus  string
	Messages   []Message
	// BuildAck returns the serialized 997 (or CONTRL) text, or "" when nothing
	// needs one (an interchange of 997s or CONTRLs is never acknowledged, and a
	// partner may not want CONTRLs). It calls next to allocate
	// each outbound control number inside the recording transaction, so a
	// rolled-back receipt never consumes a number (gap-free, BR-35.5).
	BuildAck func(next func() (int64, error)) (string, error)
	// Source is set when a transport (AS2, SFTP) delivered the interchange:
	// it is marked DONE in the same transaction, and the 997 or CONTRL is
	// queued for delivery back over the partner's transport.
	Source *InboundSource
}

// Received is the result of recording an inbound interchange. Messages carry
// their final status (a 997 is POSTED or FAILED after matching), and AckStatus
// is the stored status: a 997 that failed to read or match turns ACCEPTED
// into ACCEPTED_WITH_ERRORS.
type Received struct {
	InterchangeID string
	AckPayload    string
	AckStatus     string
	Messages      []Message
}

// InterchangeSummary is one row of the interchange list.
type InterchangeSummary struct {
	ID            string    `json:"id"`
	PartnerID     string    `json:"partnerId"`
	PartnerName   string    `json:"partnerName"`
	Direction     string    `json:"direction"`
	Standard      string    `json:"standard"`
	Version       string    `json:"version"`
	ControlNumber string    `json:"controlNumber"`
	AckStatus     string    `json:"ackStatus"`
	ReceivedAt    time.Time `json:"receivedAt"`
	MessageCount  int       `json:"messageCount"`
	FailedCount   int       `json:"failedCount"`
	// Refusal is why a transport's interchange was refused as a whole (it has
	// no messages; ack_status is REJECTED and the negative 997 or CONTRL was
	// queued back to the partner).
	Refusal string `json:"refusal,omitempty"`
}

// MessageDetail is one stored transaction set of an interchange.
type MessageDetail struct {
	ID             string          `json:"id"`
	TransactionSet string          `json:"transactionSet"`
	STControl      string          `json:"stControl"`
	Status         string          `json:"status"`
	Canonical      json.RawMessage `json:"canonical,omitempty"`
	Error          string          `json:"error,omitempty"`
}

// InterchangeDetail is one interchange with its messages, the 997 sent back
// and its deliveries over the partner's transport (those the caller's legal
// entity scope covers). The raw payload is not returned: it carries N1/N3/N4
// party data that BR-35.12 masks, and the masking viewer belongs to the
// message monitor.
type InterchangeDetail struct {
	InterchangeSummary
	AckPayload string `json:"ack997,omitempty"`
	// Contrl is the CONTRL sent back for an inbound EDIFACT interchange (the
	// ack997 field stays empty for EDIFACT).
	Contrl     string          `json:"contrl,omitempty"`
	Messages   []MessageDetail `json:"messages"`
	Deliveries []DeliveryView  `json:"deliveries,omitempty"`
}

// InterchangeFilter narrows the interchange list. Direction is IN, OUT or
// empty for both. Limit is 1..200.
type InterchangeFilter struct {
	PartnerID string
	AckStatus string
	Direction string
	Limit     int
}

func tenantID(ctx context.Context) (string, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return "", errors.New("edi: no principal in context")
	}
	return p.TenantID, nil
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

const partnerColumns = `select id::text, name, isa_qualifier, isa_id, environment, ack_sla_minutes,
	coalesce(edifact_id, ''), coalesce(edifact_qualifier, ''), coalesce(edifact_contrl, ''), status, version, created_at from trading_partner`

func scanPartner(row pgx.Row, p *Partner) error {
	err := row.Scan(&p.ID, &p.Name, &p.ISAQualifier, &p.ISAID, &p.Environment, &p.AckSLAMinutes,
		&p.EdifactID, &p.EdifactQualifier, &p.EdifactContrl, &p.Status, &p.Version, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// CreatePartner stores a partner under the caller's Idempotency-Key. A replay
// with the same key and request hash returns the stored partner with
// created=false; the same key with another request returns ErrIdempotencyReuse.
// A profile whose ISA or EDIFACT identity already exists in the environment
// returns ErrDuplicate.
func (s *Store) CreatePartner(ctx context.Context, key, requestHash string, prof domain.PartnerProfile) (Partner, bool, error) {
	prof = prof.WithDefaults()
	var out Partner
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		if replayed, err := claimKey(ctx, tx, tid, key, requestHash, &out); err != nil || replayed {
			return err
		}
		out = Partner{ID: ids.New(), Name: prof.Name, ISAQualifier: prof.ISA.Qualifier, ISAID: prof.ISA.ID,
			Environment: prof.Environment, AckSLAMinutes: prof.AckSLAMinutes}
		if e := prof.EDIFACT; e != nil {
			out.EdifactID, out.EdifactQualifier, out.EdifactContrl = e.ID, e.Qualifier, e.Contrl
		}
		err = tx.QueryRow(ctx, `insert into trading_partner (tenant_id, id, name, isa_qualifier, isa_id, environment, ack_sla_minutes,
			edifact_id, edifact_qualifier, edifact_contrl)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) returning status, version, created_at`,
			tid, out.ID, out.Name, out.ISAQualifier, out.ISAID, out.Environment, out.AckSLAMinutes,
			nullString(out.EdifactID), nullString(out.EdifactQualifier), nullString(out.EdifactContrl)).Scan(&out.Status, &out.Version, &out.CreatedAt)
		if isUnique(err) {
			return ErrDuplicate
		}
		if err != nil {
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

// claimKey claims an Idempotency-Key inside tx. When the key was already used
// with the same request hash it decodes the stored response into replay and
// reports true; another hash under the key is ErrIdempotencyReuse. A concurrent
// request with the same key waits on the primary key and then sees the stored
// response of the one that committed.
func claimKey(ctx context.Context, tx pgx.Tx, tid, key, requestHash string, replay any) (bool, error) {
	claim, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status)
		values ($1, $2, $3, 201) on conflict (tenant_id, key) do nothing`, tid, key, requestHash)
	if err != nil {
		return false, err
	}
	if claim.RowsAffected() == 1 {
		return false, nil
	}
	var storedHash string
	var body []byte
	if err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where key = $1`, key).Scan(&storedHash, &body); err != nil {
		return false, err
	}
	if storedHash != requestHash {
		return false, ErrIdempotencyReuse
	}
	return true, json.Unmarshal(body, replay)
}

// storeResponse records the response a replay of key returns.
func storeResponse(ctx context.Context, tx pgx.Tx, key string, response any) error {
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `update idempotency_keys set response_body = $2 where key = $1`, key, body)
	return err
}

// ListPartners returns the tenant's partners by name, optionally in one environment.
func (s *Store) ListPartners(ctx context.Context, environment string) ([]Partner, error) {
	out := []Partner{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, partnerColumns+` where ($1 = '' or environment = $1) order by name, environment, id`, environment)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p Partner
			if err := scanPartner(rows, &p); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// GetPartner returns one partner or ErrNotFound.
func (s *Store) GetPartner(ctx context.Context, id string) (Partner, error) {
	var out Partner
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return scanPartner(tx.QueryRow(ctx, partnerColumns+` where id = $1`, id), &out)
	})
	return out, err
}

// FindPartnerBySender resolves the active partner whose ISA qualifier and id
// match the interchange sender in the given environment (BR-35.1).
func (s *Store) FindPartnerBySender(ctx context.Context, sender domain.Party, environment string) (Partner, error) {
	var out Partner
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return scanPartner(tx.QueryRow(ctx, partnerColumns+
			` where isa_qualifier = $1 and isa_id = $2 and environment = $3 and status = 'active'`,
			sender.Qualifier, sender.ID, environment), &out)
	})
	return out, err
}

// FindPartnerByEdifactSender resolves the active partner whose EDIFACT
// identity matches the UNB sender (S002 id and qualifier) in the given
// environment, the EDIFACT counterpart of FindPartnerBySender.
func (s *Store) FindPartnerByEdifactSender(ctx context.Context, sender domain.EdifactParty, environment string) (Partner, error) {
	var out Partner
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return scanPartner(tx.QueryRow(ctx, partnerColumns+
			` where edifact_id = $1 and coalesce(edifact_qualifier, '') = $2 and environment = $3 and status = 'active'`,
			sender.ID, sender.Qualifier, environment), &out)
	})
	return out, err
}

// nextControl allocates the next outbound control number of a standard (X12
// ISA13/GS06, EDIFACT UNB05) for a partner. It upserts the counter row inside
// the caller's transaction, so numbers are gap-free and a rolled-back
// transaction gives its number back.
func nextControl(ctx context.Context, tx pgx.Tx, tid, partnerID, standard string) (int64, error) {
	var n int64
	err := tx.QueryRow(ctx, `insert into control_number_counter (tenant_id, partner_id, direction, standard, last_value)
		values ($1, $2, 'OUT', $3, 1)
		on conflict (tenant_id, partner_id, direction, standard)
		do update set last_value = control_number_counter.last_value + 1
		returning last_value`, tid, partnerID, standard).Scan(&n)
	return n, err
}

// standardOf is the stored standard of an inbound interchange (X12 when
// unset) and the set id of the acknowledgment that answers it.
func standardOf(standard string) (string, string) {
	if standard == domain.SyntaxEdifact {
		return domain.SyntaxEdifact, "CONTRL"
	}
	return domain.SyntaxX12, "997"
}

// ReceiveInterchange records an inbound X12 or EDIFACT interchange, its
// messages, one message.received event per transaction and one
// message.failed event per failed transaction in a single transaction. A
// control number already seen from the partner in the same standard inside
// the duplicate window returns ErrDuplicate and nothing is written; refused
// interchanges do not count. Outside the window the number may be reused
// (BR-35.5).
func (s *Store) ReceiveInterchange(ctx context.Context, in Inbound) (Received, error) {
	var out Received
	standard, ackSet := standardOf(in.Standard)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		// Serialize receipts of the same control number so two concurrent copies
		// cannot both pass the window check.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`,
			"edi:interchange:IN:"+standard+":"+tid+":"+in.PartnerID+":"+in.ISAControl); err != nil {
			return err
		}
		var recent bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from interchange
			where partner_id = $1 and direction = 'IN' and standard = $4 and control_number = $2 and refusal is null
			and received_at > now() - make_interval(days => $3))`, in.PartnerID, in.ISAControl, s.dupWindowDays, standard).Scan(&recent); err != nil {
			return err
		}
		if recent {
			return ErrDuplicate
		}
		ack, err := in.BuildAck(func() (int64, error) { return nextControl(ctx, tx, tid, in.PartnerID, standard) })
		if err != nil {
			return err
		}
		// Match received 997s first: their outcome decides the interchange status.
		messages := make([]Message, 0, len(in.Messages))
		status := in.AckStatus
		for _, m := range in.Messages {
			if m.Ack != nil {
				if err := matchAck(ctx, tx, tid, in.PartnerID, &m); err != nil {
					return err
				}
			}
			if m.Contrl != nil {
				if err := matchContrl(ctx, tx, tid, in.PartnerID, &m); err != nil {
					return err
				}
			}
			if (m.TransactionSet == "997" || m.TransactionSet == "CONTRL") && m.Status == "FAILED" && status == string(domain.StatusAccepted) {
				status = string(domain.StatusAcceptedWithErrors)
			}
			messages = append(messages, m)
		}
		interchangeID := ids.New()
		if _, err := tx.Exec(ctx, `insert into interchange (tenant_id, id, partner_id, direction, standard, version,
			control_number, raw_payload, ack_status, ack_payload)
			values ($1,$2,$3,'IN',$9,$4,$5,$6,$7,$8)`,
			tid, interchangeID, in.PartnerID, in.Version, in.ISAControl, in.Raw, status, nullString(ack), standard); err != nil {
			return err
		}
		for _, m := range messages {
			if _, err := tx.Exec(ctx, `insert into message (tenant_id, id, interchange_id, transaction_set, st_control, status, canonical, error)
				values ($1,$2,$3,$4,$5,$6,$7,$8)`,
				tid, m.ID, interchangeID, m.TransactionSet, m.STControl, m.Status, nullBytes(m.Canonical), nullString(m.Error)); err != nil {
				return err
			}
			if err := enqueueReceived(ctx, tx, tid, ediev.MessageReceivedData{
				MessageID: m.ID, InterchangeID: interchangeID, PartnerID: in.PartnerID, Standard: standard,
				TransactionSet: m.TransactionSet, ControlNumber: in.ISAControl, STControl: m.STControl,
				Status: m.Status,
			}); err != nil {
				return err
			}
			if m.Status == "FAILED" {
				if err := enqueueFailed(ctx, tx, tid, ediev.MessageFailedData{
					MessageID: m.ID, InterchangeID: interchangeID, PartnerID: in.PartnerID, TransactionSet: m.TransactionSet,
					ControlNumber: in.ISAControl, STControl: m.STControl, Error: m.Error,
				}); err != nil {
					return err
				}
			}
		}
		if in.Source != nil {
			if err := recordInboundDone(ctx, tx, tid, in.PartnerID, interchangeID, *in.Source); err != nil {
				return err
			}
			if ack != "" {
				if _, err := enqueueDelivery(ctx, tx, tid, in.PartnerID, interchangeID, "ACK", standard, ackSet, in.ISAControl); err != nil {
					return err
				}
			}
		}
		out = Received{InterchangeID: interchangeID, AckPayload: ack, AckStatus: status, Messages: messages}
		return nil
	})
	return out, err
}

const interchangeColumns = `select i.id::text, i.partner_id::text, p.name, i.direction, i.standard, i.version, i.control_number,
	i.ack_status, i.received_at,
	(select count(*) from message m where m.interchange_id = i.id)::int,
	(select count(*) from message m where m.interchange_id = i.id and m.status = 'FAILED')::int,
	coalesce(i.refusal, '')
	from interchange i join trading_partner p on p.id = i.partner_id`

func scanInterchange(row pgx.Row, v *InterchangeSummary) error {
	err := row.Scan(&v.ID, &v.PartnerID, &v.PartnerName, &v.Direction, &v.Standard, &v.Version, &v.ControlNumber,
		&v.AckStatus, &v.ReceivedAt, &v.MessageCount, &v.FailedCount, &v.Refusal)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// ListInterchanges returns the newest interchanges first.
func (s *Store) ListInterchanges(ctx context.Context, f InterchangeFilter) ([]InterchangeSummary, error) {
	out := []InterchangeSummary{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, interchangeColumns+`
			where ($1 = '' or i.partner_id::text = $1) and ($2 = '' or i.ack_status = $2) and ($4 = '' or i.direction = $4)
			order by i.received_at desc, i.id desc limit $3`, f.PartnerID, f.AckStatus, f.Limit, f.Direction)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v InterchangeSummary
			if err := scanInterchange(rows, &v); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

// GetInterchange returns one interchange with its messages, or ErrNotFound.
func (s *Store) GetInterchange(ctx context.Context, id string) (InterchangeDetail, error) {
	var out InterchangeDetail
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := scanInterchange(tx.QueryRow(ctx, interchangeColumns+` where i.id = $1`, id), &out.InterchangeSummary); err != nil {
			return err
		}
		var ack *string
		if err := tx.QueryRow(ctx, `select ack_payload from interchange where id = $1`, id).Scan(&ack); err != nil {
			return err
		}
		switch {
		case ack != nil && out.Standard == domain.SyntaxEdifact:
			out.Contrl = *ack
		case ack != nil:
			out.AckPayload = *ack
		}
		rows, err := tx.Query(ctx, `select id::text, transaction_set, st_control, status, canonical, coalesce(error, '')
			from message where interchange_id = $1 order by created_at, st_control, id`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		out.Messages = []MessageDetail{}
		for rows.Next() {
			var m MessageDetail
			var canonical []byte
			if err := rows.Scan(&m.ID, &m.TransactionSet, &m.STControl, &m.Status, &canonical, &m.Error); err != nil {
				return err
			}
			if len(canonical) > 0 {
				m.Canonical = canonical
			}
			out.Messages = append(out.Messages, m)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		out.Deliveries, err = listDeliveries(ctx, tx, id)
		return err
	})
	return out, err
}

// enqueueReceived writes one message.received event to the outbox in tx.
func enqueueReceived(ctx context.Context, tx pgx.Tx, tid string, data ediev.MessageReceivedData) error {
	evt, err := events.New(ediev.MessageReceived, eventSource, data.MessageID, tid, data)
	if err != nil {
		return err
	}
	if err := outbox.Enqueue(ctx, tx, "message", data.MessageID, evt); err != nil {
		return fmt.Errorf("edi: outbox: %w", err)
	}
	return nil
}

// enqueueFailed writes one message.failed event to the outbox in tx.
func enqueueFailed(ctx context.Context, tx pgx.Tx, tid string, data ediev.MessageFailedData) error {
	evt, err := events.New(ediev.MessageFailed, eventSource, data.MessageID, tid, data)
	if err != nil {
		return err
	}
	if err := outbox.Enqueue(ctx, tx, "message", data.MessageID, evt); err != nil {
		return fmt.Errorf("edi: outbox: %w", err)
	}
	return nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}
