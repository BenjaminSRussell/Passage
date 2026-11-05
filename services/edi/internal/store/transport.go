package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
)

// ErrNoTradeProfile means the partner exists but has no trade profile the
// caller may see, which a transport needs (it is scoped to the profile's
// legal entity).
var ErrNoTradeProfile = errors.New("edi: partner has no trade profile")

// TransportRecord is a partner's stored transport.
type TransportRecord struct {
	PartnerID     string
	LegalEntityID string
	EndpointID    string
	Config        domain.PartnerTransport
	Version       int
	UpdatedAt     time.Time
}

const transportColumns = `select partner_id::text, legal_entity_id::text, endpoint_id::text, kind,
	coalesce(as2_partner_id, ''), coalesce(as2_our_id, ''), coalesce(as2_url, ''), coalesce(as2_partner_cert, ''), coalesce(as2_signing_key_ref, ''),
	coalesce(as2_signing_alg, ''), coalesce(as2_encryption_alg, ''), coalesce(as2_mdn_mode, ''),
	coalesce(sftp_host, ''), coalesce(sftp_port, 0), coalesce(sftp_user, ''), coalesce(sftp_host_key_fingerprint, ''), coalesce(sftp_auth_method, ''),
	coalesce(sftp_credential_ref, ''), coalesce(sftp_outbound_dir, ''), coalesce(sftp_inbound_dir, ''), coalesce(sftp_archive_dir, ''),
	coalesce(sftp_failed_dir, ''), coalesce(sftp_poll_seconds, 0), version, updated_at from partner_transport`

func scanTransport(row pgx.Row, t *TransportRecord) error {
	var a domain.AS2Settings
	var s domain.SFTPSettings
	err := row.Scan(&t.PartnerID, &t.LegalEntityID, &t.EndpointID, &t.Config.Kind,
		&a.PartnerAS2ID, &a.OurAS2ID, &a.URL, &a.PartnerCertificate, &a.SigningKeyRef, &a.SigningAlgorithm, &a.EncryptionAlgorithm, &a.MDNMode,
		&s.Host, &s.Port, &s.User, &s.HostKeyFingerprint, &s.AuthMethod, &s.CredentialRef, &s.OutboundDir, &s.InboundDir, &s.ArchiveDir,
		&s.FailedDir, &s.PollIntervalSeconds, &t.Version, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	switch t.Config.Kind {
	case domain.TransportAS2:
		t.Config.AS2 = &a
	case domain.TransportSFTP:
		t.Config.SFTP = &s
	}
	return nil
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

// PutTransport stores the partner's transport, bound to the legal entity of
// its trade profile. The AS2 endpoint id is kept across updates, so the URL
// given to the partner stays valid. A partner the caller cannot see is
// ErrNotFound; one without a trade profile in the caller's scope is
// ErrNoTradeProfile.
func (s *Store) PutTransport(ctx context.Context, partnerID string, cfg domain.PartnerTransport) (TransportRecord, error) {
	var out TransportRecord
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		var a domain.AS2Settings
		var sf domain.SFTPSettings
		if cfg.AS2 != nil {
			a = *cfg.AS2
		}
		if cfg.SFTP != nil {
			sf = *cfg.SFTP
		}
		poll := cfg.Kind == domain.TransportSFTP && sf.InboundDir != ""
		tag, err := tx.Exec(ctx, `insert into partner_transport (tenant_id, partner_id, legal_entity_id, kind, endpoint_id,
			as2_partner_id, as2_our_id, as2_url, as2_partner_cert, as2_signing_key_ref, as2_signing_alg, as2_encryption_alg, as2_mdn_mode,
			sftp_host, sftp_port, sftp_user, sftp_host_key_fingerprint, sftp_auth_method, sftp_credential_ref, sftp_outbound_dir,
			sftp_inbound_dir, sftp_archive_dir, sftp_failed_dir, sftp_poll_seconds, sftp_next_poll_at)
			select $1::uuid, partner_id, legal_entity_id, $3, $4::uuid, $5, $6, $7, $8, $9, $10, $11, $12,
				$13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, case when $24 then now() end
			from partner_trade_profile where partner_id = $2
			on conflict (tenant_id, partner_id) do update set legal_entity_id = excluded.legal_entity_id, kind = excluded.kind,
				as2_partner_id = excluded.as2_partner_id, as2_our_id = excluded.as2_our_id, as2_url = excluded.as2_url,
				as2_partner_cert = excluded.as2_partner_cert, as2_signing_key_ref = excluded.as2_signing_key_ref,
				as2_signing_alg = excluded.as2_signing_alg, as2_encryption_alg = excluded.as2_encryption_alg, as2_mdn_mode = excluded.as2_mdn_mode,
				sftp_host = excluded.sftp_host, sftp_port = excluded.sftp_port, sftp_user = excluded.sftp_user,
				sftp_host_key_fingerprint = excluded.sftp_host_key_fingerprint, sftp_auth_method = excluded.sftp_auth_method,
				sftp_credential_ref = excluded.sftp_credential_ref, sftp_outbound_dir = excluded.sftp_outbound_dir,
				sftp_inbound_dir = excluded.sftp_inbound_dir, sftp_archive_dir = excluded.sftp_archive_dir,
				sftp_failed_dir = excluded.sftp_failed_dir, sftp_poll_seconds = excluded.sftp_poll_seconds,
				sftp_next_poll_at = case when $24 then coalesce(partner_transport.sftp_next_poll_at, now()) end,
				sftp_poll_failures = 0, version = partner_transport.version + 1, updated_at = now()`,
			tid, partnerID, cfg.Kind, ids.New(),
			nullString(a.PartnerAS2ID), nullString(a.OurAS2ID), nullString(a.URL), nullString(a.PartnerCertificate), nullString(a.SigningKeyRef),
			nullString(a.SigningAlgorithm), nullString(a.EncryptionAlgorithm), nullString(a.MDNMode),
			nullString(sf.Host), nullInt(sf.Port), nullString(sf.User), nullString(sf.HostKeyFingerprint), nullString(sf.AuthMethod),
			nullString(sf.CredentialRef), nullString(sf.OutboundDir), nullString(sf.InboundDir), nullString(sf.ArchiveDir),
			nullString(sf.FailedDir), nullInt(sf.PollIntervalSeconds), poll)
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "42501" {
			// The stored transport belongs to a legal entity outside the caller's scope.
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from trading_partner where id = $1)`, partnerID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
			return ErrNoTradeProfile
		}
		return scanTransport(tx.QueryRow(ctx, transportColumns+` where partner_id = $1`, partnerID), &out)
	})
	return out, err
}

// GetTransport returns the partner's transport, or ErrNotFound when it has
// none the caller may see.
func (s *Store) GetTransport(ctx context.Context, partnerID string) (TransportRecord, error) {
	var out TransportRecord
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return scanTransport(tx.QueryRow(ctx, transportColumns+` where partner_id = $1`, partnerID), &out)
	})
	return out, err
}

// ResolveAS2Endpoint maps an AS2 inbound endpoint id to its tenant and
// partner. It runs without a principal (the AS2 caller has no Passage
// identity): the SECURITY DEFINER lookup returns the two ids and nothing
// else, and the caller is then authenticated by its signature.
func (s *Store) ResolveAS2Endpoint(ctx context.Context, endpointID string) (tenantID, partnerID string, err error) {
	err = s.pool.QueryRow(ctx, `select tenant_id::text, partner_id::text from edi_as2_endpoint($1::uuid)`, endpointID).Scan(&tenantID, &partnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return tenantID, partnerID, err
}

// DueTenants lists the tenants with transport work due: "DELIVERY" or
// "SFTP_POLL".
func (s *Store) DueTenants(ctx context.Context, work string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `select t::text from edi_transport_due_tenants($1) t`, work)
	if err != nil {
		return nil, fmt.Errorf("edi: due %s tenants: %w", work, err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// SystemContext is the principal background jobs and the AS2 endpoint use for
// one tenant: no user, every legal entity of the tenant.
func SystemContext(ctx context.Context, tenantID string) context.Context {
	return tenant.With(ctx, tenant.Principal{TenantID: tenantID, Service: "edi", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
}

// EntityContext is the principal the AS2 endpoint processes a message under:
// the tenant and the one legal entity of the partner's trade profile.
func EntityContext(ctx context.Context, tenantID, legalEntityID string) context.Context {
	return tenant.With(ctx, tenant.Principal{TenantID: tenantID, LegalEntityID: legalEntityID, Service: "edi", Duties: map[string]struct{}{}})
}

// DeliveryView is one delivery of an interchange as the API shows it.
type DeliveryView struct {
	ID             string     `json:"id"`
	PayloadKind    string     `json:"payloadKind"`
	Transport      string     `json:"transport"`
	MessageID      string     `json:"messageId"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	NextAttemptAt  *time.Time `json:"nextAttemptAt,omitempty"`
	MDNDisposition string     `json:"mdnDisposition,omitempty"`
	MDNSigned      *bool      `json:"mdnSigned,omitempty"`
	Reference      string     `json:"reference,omitempty"`
	LastError      string     `json:"lastError,omitempty"`
	SentAt         *time.Time `json:"sentAt,omitempty"`
	DeliveredAt    *time.Time `json:"deliveredAt,omitempty"`
	// MDNDueAt is when an AWAITING_MDN delivery stops waiting for its
	// asynchronous MDN; MDNTimeouts counts the waits that ran out.
	MDNDueAt    *time.Time `json:"mdnDueAt,omitempty"`
	MDNTimeouts int        `json:"mdnTimeouts,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

// enqueueDelivery records a PENDING delivery of the interchange over the
// partner's transport when it has an AS2 or SFTP transport the caller may
// see; otherwise it does nothing and returns nil. standard (X12 or EDIFACT)
// is the syntax of what is sent, setID the transaction set, message type or
// TA1 that names the file.
func enqueueDelivery(ctx context.Context, tx pgx.Tx, tid, partnerID, interchangeID, payloadKind, standard, setID, control string) (*DeliveryView, error) {
	var kind, entity string
	err := tx.QueryRow(ctx, `select kind, legal_entity_id::text from partner_transport where partner_id = $1`, partnerID).Scan(&kind, &entity)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && kind == domain.TransportAPI) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id := ids.New()
	compact := strings.ReplaceAll(id, "-", "")
	ext := ".x12"
	if standard == domain.SyntaxEdifact {
		ext = ".edi"
	}
	msgID := "edi-" + compact + "@passage"
	filename := fmt.Sprintf("%s_%s_%s%s", setID, fileSafe(control), compact[:12], ext)
	d := DeliveryView{ID: id, PayloadKind: payloadKind, Transport: kind, MessageID: msgID, Status: "PENDING"}
	if err := tx.QueryRow(ctx, `insert into transport_delivery (tenant_id, id, partner_id, legal_entity_id, interchange_id, payload_kind,
		transport, message_id, filename, status) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'PENDING') returning created_at`,
		tid, id, partnerID, entity, interchangeID, payloadKind, kind, msgID, filename).Scan(&d.CreatedAt); err != nil {
		return nil, err
	}
	return &d, nil
}

// fileSafe turns a control number into a file name part: an EDIFACT UNB05
// may carry characters a file name (and the delivery's filename check) does
// not take, and is clipped to 35 characters.
func fileSafe(control string) string {
	b := []byte(clip(control, 35))
	for i, c := range b {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '@') {
			b[i] = '_'
		}
	}
	if len(b) == 0 {
		return "0"
	}
	return string(b)
}

const deliveryColumns = `select id::text, payload_kind, transport, message_id, status, attempts,
	case when status = 'PENDING' then next_attempt_at end, coalesce(mdn_disposition, ''), mdn_signed, coalesce(reference, ''),
	coalesce(last_error, ''), sent_at, delivered_at, mdn_due_at, mdn_timeouts, created_at from transport_delivery`

func scanDelivery(row pgx.Row) (DeliveryView, error) {
	var d DeliveryView
	err := row.Scan(&d.ID, &d.PayloadKind, &d.Transport, &d.MessageID, &d.Status, &d.Attempts, &d.NextAttemptAt, &d.MDNDisposition,
		&d.MDNSigned, &d.Reference, &d.LastError, &d.SentAt, &d.DeliveredAt, &d.MDNDueAt, &d.MDNTimeouts, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

func listDeliveries(ctx context.Context, tx pgx.Tx, interchangeID string) ([]DeliveryView, error) {
	rows, err := tx.Query(ctx, deliveryColumns+` where interchange_id = $1 order by created_at, id`, interchangeID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DeliveryView, error) { return scanDelivery(r) })
}

// ErrNotResendable means the interchange has no delivery that failed or was
// rejected, or it is not an outbound interchange.
var ErrNotResendable = errors.New("edi: nothing to resend")

// Resend queues a new delivery (a new Message-ID) of an outbound interchange
// whose latest document delivery FAILED or was REJECTED.
func (s *Store) Resend(ctx context.Context, interchangeID string) (DeliveryView, error) {
	var out DeliveryView
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		var partnerID, direction, standard, control, setID, status string
		err = tx.QueryRow(ctx, `select i.partner_id::text, i.direction, i.standard, i.control_number,
			coalesce((select m.transaction_set from message m where m.interchange_id = i.id order by m.st_control limit 1), ''),
			coalesce((select d.status from transport_delivery d where d.interchange_id = i.id and d.payload_kind = 'DOCUMENT'
				order by d.created_at desc, d.id desc limit 1), '')
			from interchange i where i.id = $1 for update of i`, interchangeID).Scan(&partnerID, &direction, &standard, &control, &setID, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if direction != "OUT" || (status != "FAILED" && status != "REJECTED") {
			return ErrNotResendable
		}
		d, err := enqueueDelivery(ctx, tx, tid, partnerID, interchangeID, "DOCUMENT", standard, setID, control)
		if err != nil {
			return err
		}
		if d == nil {
			return ErrNotResendable
		}
		out = *d
		return nil
	})
	return out, err
}

// ClaimedDelivery is a delivery leased to one dispatcher with what it sends.
type ClaimedDelivery struct {
	ID            string
	PartnerID     string
	InterchangeID string
	PayloadKind   string
	Transport     string
	MessageID     string
	Filename      string
	Attempts      int
	Standard      string
	Payload       string
	TransportCfg  TransportRecord
}

// ClaimDeliveries leases up to limit due PENDING deliveries of the context's
// tenant: their next attempt moves lease ahead and their attempt count goes
// up, in one statement with skip locked, so concurrent dispatchers never send
// the same row at once and a crashed one gives its rows back when the lease
// ends. A delivery whose partner no longer has its transport is FAILED.
func (s *Store) ClaimDeliveries(ctx context.Context, limit int, lease time.Duration) ([]ClaimedDelivery, error) {
	var out []ClaimedDelivery
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `with claimed as (
				select id as claimed_id from transport_delivery where status = 'PENDING' and next_attempt_at <= now()
				order by next_attempt_at, id limit $1 for update skip locked)
			update transport_delivery d set attempts = d.attempts + 1, next_attempt_at = now() + make_interval(secs => $2)
			from claimed where d.id = claimed.claimed_id
			returning d.id::text, d.partner_id::text, d.interchange_id::text, d.payload_kind, d.transport, d.message_id, d.filename, d.attempts`,
			limit, lease.Seconds())
		if err != nil {
			return err
		}
		claimed, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ClaimedDelivery, error) {
			var c ClaimedDelivery
			return c, r.Scan(&c.ID, &c.PartnerID, &c.InterchangeID, &c.PayloadKind, &c.Transport, &c.MessageID, &c.Filename, &c.Attempts)
		})
		if err != nil {
			return err
		}
		for _, c := range claimed {
			var payload *string
			if err := tx.QueryRow(ctx, `select standard, case when $2 = 'ACK' then ack_payload else raw_payload end from interchange where id = $1`,
				c.InterchangeID, c.PayloadKind).Scan(&c.Standard, &payload); err != nil {
				return err
			}
			err := scanTransport(tx.QueryRow(ctx, transportColumns+` where partner_id = $1`, c.PartnerID), &c.TransportCfg)
			if errors.Is(err, ErrNotFound) || payload == nil || (err == nil && c.TransportCfg.Config.Kind != c.Transport) {
				if err := finishDelivery(ctx, tx, tid, c.ID, DeliveryOutcome{Status: "FAILED",
					Error: "the partner's " + strings.ToLower(c.Transport) + " transport or the payload is gone"}); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			c.Payload = *payload
			out = append(out, c)
		}
		return nil
	})
	return out, err
}

// DeliveryOutcome is what one attempt achieved. Status is DELIVERED,
// AWAITING_MDN (until MDNDueAt), REJECTED, FAILED, or PENDING to try again
// at RetryAt. MDNTimeout marks the outcome of a wait for an asynchronous MDN
// that ran out.
type DeliveryOutcome struct {
	Status         string
	RetryAt        time.Time
	MDNDueAt       time.Time
	MDNTimeout     bool
	SentMIC        string
	MDNMIC         string
	MDNDisposition string
	MDNSigned      bool
	Reference      string
	Error          string
}

// clip cuts s to at most n bytes without splitting a UTF-8 sequence, so a
// clipped text stays valid in a UTF-8 database.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// FinishDelivery records the outcome of an attempt on a claimed delivery.
// When a document is DELIVERED its MAPPED messages become SENT and one
// message.sent event per message goes to the outbox in the same transaction;
// a delivery that ends FAILED publishes delivery.failed the same way.
func (s *Store) FinishDelivery(ctx context.Context, id string, o DeliveryOutcome) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		return finishDelivery(ctx, tx, tid, id, o)
	})
}

func finishDelivery(ctx context.Context, tx pgx.Tx, tid, id string, o DeliveryOutcome) error {
	var retry, due any
	if o.Status == "PENDING" {
		retry = o.RetryAt
	}
	if o.Status == "AWAITING_MDN" && !o.MDNDueAt.IsZero() {
		due = o.MDNDueAt
	}
	var partnerID, interchangeID, kind, transport, msgID string
	var attempts, timeouts int
	err := tx.QueryRow(ctx, `update transport_delivery set status = $2, next_attempt_at = coalesce($3, next_attempt_at),
		sent_mic = coalesce($4, sent_mic), mdn_mic = coalesce($5, mdn_mic), mdn_disposition = coalesce($6, mdn_disposition),
		mdn_signed = case when $6::text is null then mdn_signed else $7 end, reference = coalesce($8, reference), last_error = $9,
		sent_at = case when $2 in ('AWAITING_MDN', 'DELIVERED') then coalesce(sent_at, now()) else sent_at end,
		delivered_at = case when $2 = 'DELIVERED' then now() else delivered_at end,
		mdn_due_at = $10, mdn_timeouts = mdn_timeouts + case when $11 then 1 else 0 end
		where id = $1 returning partner_id::text, interchange_id::text, payload_kind, transport, message_id, attempts, mdn_timeouts`,
		id, o.Status, retry, nullString(clip(o.SentMIC, 200)), nullString(clip(o.MDNMIC, 200)), nullString(clip(o.MDNDisposition, 500)), o.MDNSigned,
		nullString(clip(o.Reference, 1024)), nullString(clip(o.Error, 1000)), due, o.MDNTimeout).
		Scan(&partnerID, &interchangeID, &kind, &transport, &msgID, &attempts, &timeouts)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if o.Status == "FAILED" {
		evt, err := events.New(ediev.DeliveryFailed, eventSource, id, tid, ediev.DeliveryFailedData{
			DeliveryID: id, InterchangeID: interchangeID, PartnerID: partnerID, PayloadKind: kind, Transport: transport,
			TransportMessageID: msgID, Attempts: attempts, MDNTimeouts: timeouts, MDNTimeout: o.MDNTimeout, Error: clip(o.Error, 1000),
		})
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, "delivery", id, evt); err != nil {
			return fmt.Errorf("edi: outbox: %w", err)
		}
		return nil
	}
	if o.Status != "DELIVERED" || kind != "DOCUMENT" {
		return nil
	}
	rows, err := tx.Query(ctx, `update message m set status = 'SENT' from interchange i
		where m.interchange_id = $1 and i.id = m.interchange_id and m.status = 'MAPPED'
		returning m.id::text, m.transaction_set, m.st_control, i.control_number`, interchangeID)
	if err != nil {
		return err
	}
	type sent struct{ id, set, st, control string }
	msgs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (sent, error) {
		var m sent
		return m, r.Scan(&m.id, &m.set, &m.st, &m.control)
	})
	if err != nil {
		return err
	}
	for _, m := range msgs {
		evt, err := events.New(ediev.MessageSent, eventSource, m.id, tid, ediev.MessageSentData{
			MessageID: m.id, InterchangeID: interchangeID, PartnerID: partnerID, TransactionSet: m.set, ControlNumber: m.control,
			STControl: m.st, Transport: transport, DeliveryID: id, TransportMessageID: msgID,
		})
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, "message", m.id, evt); err != nil {
			return fmt.Errorf("edi: outbox: %w", err)
		}
	}
	return nil
}

// AwaitingMDN is a delivery waiting for its asynchronous MDN. PartnerID,
// Attempts and MDNTimeouts are set for ExpireAwaitingMDN.
type AwaitingMDN struct {
	ID          string
	PartnerID   string
	SentMIC     string
	Attempts    int
	MDNTimeouts int
}

// ExpiredMDNWait is one delivery ExpireAwaitingMDN stopped waiting for, with
// the outcome recorded.
type ExpiredMDNWait struct {
	AwaitingMDN
	Outcome DeliveryOutcome
}

// ExpireAwaitingMDN takes up to limit AWAITING_MDN deliveries of the
// context's tenant whose wait ran out (mdn_due_at passed), lets decide choose
// what becomes of each (PENDING to send again with the same Message-ID, or
// FAILED) and records it as an MDN timeout. Rows are locked with skip locked,
// so concurrent dispatchers and an MDN arriving at the same moment never both
// win: the MDN then finds the row no longer waiting, or the sweep skips it.
func (s *Store) ExpireAwaitingMDN(ctx context.Context, limit int, decide func(AwaitingMDN) DeliveryOutcome) ([]ExpiredMDNWait, error) {
	var out []ExpiredMDNWait
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select id::text, partner_id::text, coalesce(sent_mic, ''), attempts, mdn_timeouts from transport_delivery
			where status = 'AWAITING_MDN' and mdn_due_at <= now() order by mdn_due_at, id limit $1 for update skip locked`, limit)
		if err != nil {
			return err
		}
		waits, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (AwaitingMDN, error) {
			var w AwaitingMDN
			return w, r.Scan(&w.ID, &w.PartnerID, &w.SentMIC, &w.Attempts, &w.MDNTimeouts)
		})
		if err != nil {
			return err
		}
		for _, w := range waits {
			o := decide(w)
			o.MDNTimeout = true
			if err := finishDelivery(ctx, tx, tid, w.ID, o); err != nil {
				return err
			}
			out = append(out, ExpiredMDNWait{AwaitingMDN: w, Outcome: o})
		}
		return nil
	})
	return out, err
}

// ApplyAsyncMDN locks the partner's AS2 delivery with this Message-ID
// (without angle brackets) that waits for an MDN, lets decide judge the MDN
// against it and records the outcome; AWAITING_MDN keeps it waiting with the
// error noted. ErrNotFound when no delivery waits for this MDN.
func (s *Store) ApplyAsyncMDN(ctx context.Context, partnerID, messageID string, decide func(AwaitingMDN) DeliveryOutcome) (DeliveryOutcome, error) {
	var out DeliveryOutcome
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		var w AwaitingMDN
		err = tx.QueryRow(ctx, `select id::text, coalesce(sent_mic, '') from transport_delivery
			where partner_id = $1 and message_id = $2 and transport = 'AS2' and status = 'AWAITING_MDN' for update`, partnerID, messageID).Scan(&w.ID, &w.SentMIC)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = decide(w)
		if out.Status == "AWAITING_MDN" {
			// An MDN that does not check leaves the delivery waiting; only the error is kept.
			_, err := tx.Exec(ctx, `update transport_delivery set last_error = $2 where id = $1`, w.ID, nullString(clip(out.Error, 1000)))
			return err
		}
		return finishDelivery(ctx, tx, tid, w.ID, out)
	})
	return out, err
}

// InboundSource identifies what a transport received: the AS2 Message-ID or
// the SFTP file name, and the SHA-256 of the payload. MDN, when set, is the
// asynchronous MDN that answers the message as processed: it is queued in the
// transaction that records the interchange.
type InboundSource struct {
	Transport     string
	Ref           string
	SHA256        string
	LegalEntityID string
	MDN           *OwedMDN
}

// InboundRecord is the stored state of one received file or message.
type InboundRecord struct {
	SHA256        string
	Status        string
	InterchangeID string
	Attempts      int
	NextAttemptAt *time.Time
	LastError     string
}

// FindInbound returns what is stored for a transport ref of the partner
// (several rows when the same name came with different content).
func (s *Store) FindInbound(ctx context.Context, partnerID, transport, ref string) ([]InboundRecord, error) {
	var out []InboundRecord
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select content_sha256, status, coalesce(interchange_id::text, ''), attempts, next_attempt_at, coalesce(last_error, '')
			from transport_inbound where partner_id = $1 and transport = $2 and ref = $3 order by received_at`, partnerID, transport, ref)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (InboundRecord, error) {
			var rec InboundRecord
			return rec, r.Scan(&rec.SHA256, &rec.Status, &rec.InterchangeID, &rec.Attempts, &rec.NextAttemptAt, &rec.LastError)
		})
		return err
	})
	return out, err
}

// RecordInbound stores the outcome of handling a received file or message
// outside a receipt: DONE (a duplicate interchange that was already
// recorded), FAILED with the next attempt, or PARKED. It returns the attempt
// count after this one.
func (s *Store) RecordInbound(ctx context.Context, partnerID string, src InboundSource, status string, retryAt *time.Time, errText string) (int, error) {
	var attempts int
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `insert into transport_inbound (tenant_id, partner_id, legal_entity_id, transport, ref, content_sha256, status,
			attempts, next_attempt_at, last_error) values ($1, $2, $3, $4, $5, $6, $7, 1, $8, $9)
			on conflict (tenant_id, partner_id, transport, ref, content_sha256) do update set status = excluded.status,
			attempts = transport_inbound.attempts + 1, next_attempt_at = excluded.next_attempt_at, last_error = excluded.last_error, updated_at = now()
			returning attempts`, tid, partnerID, src.LegalEntityID, src.Transport, src.Ref, src.SHA256, status, retryAt,
			nullString(clip(errText, 1000))).Scan(&attempts)
	})
	return attempts, err
}

// recordInboundDone marks the source DONE with its interchange inside the
// receipt transaction and queues the asynchronous MDN that answers it.
func recordInboundDone(ctx context.Context, tx pgx.Tx, tid, partnerID, interchangeID string, src InboundSource) error {
	if src.MDN != nil {
		if err := insertMDN(ctx, tx, tid, partnerID, src.LegalEntityID, interchangeID, *src.MDN); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `insert into transport_inbound (tenant_id, partner_id, legal_entity_id, transport, ref, content_sha256, status,
		interchange_id, attempts) values ($1, $2, $3, $4, $5, $6, 'DONE', $7, 1)
		on conflict (tenant_id, partner_id, transport, ref, content_sha256) do update set status = 'DONE', interchange_id = excluded.interchange_id,
		attempts = transport_inbound.attempts + 1, next_attempt_at = null, last_error = null, updated_at = now()`,
		tid, partnerID, src.LegalEntityID, src.Transport, src.Ref, src.SHA256, interchangeID)
	return err
}

// Refusal is an interchange a transport brought from an authenticated
// partner that the receipt refused as a whole (its syntax, a sender that is
// not the partner, or a duplicate control number), with the negative 997,
// TA1 or CONTRL that answers it. Standard is X12 or EDIFACT, Control the
// ISA13 or UNB05 as read, Reason the refusal. BuildAck works as
// Inbound.BuildAck. AckSet names the acknowledgment's delivery file (997 or
// CONTRL by standard when empty, TA1). SourceStatus is what the transport
// source becomes: PARKED (the default: a retransmission is refused again) or
// DONE (a duplicate: a retransmission is a duplicate again).
type Refusal struct {
	PartnerID    string
	Standard     string
	Version      string
	Control      string
	Raw          string
	Reason       string
	AckSet       string
	SourceStatus string
	BuildAck     func(next func() (int64, error)) (string, error)
	Source       InboundSource
}

// ErrNoAck means no acknowledgment could be built for a refused
// interchange; RecordRefusal then writes nothing.
var ErrNoAck = errors.New("edi: no acknowledgment for the refused interchange")

// RecordRefusal records a refused interchange in one transaction: the
// interchange (REJECTED, the reason, no messages, the acknowledgment as its
// ack payload), its transport source PARKED (or DONE, see SourceStatus) with
// it, so a retransmission gets no second acknowledgment, and the
// acknowledgment queued over the partner's transport. The control number comes from the
// standard's gap-free counter. A refused interchange never counts in the
// duplicate window. The source's asynchronous MDN is not stored: the
// transport answers the refusal with its error MDN. It returns the
// interchange id, or ErrNoAck with nothing written when BuildAck returns "".
func (s *Store) RecordRefusal(ctx context.Context, r Refusal) (string, error) {
	interchangeID := ids.New()
	standard, ackSet := standardOf(r.Standard)
	if r.AckSet != "" {
		ackSet = r.AckSet
	}
	srcStatus := r.SourceStatus
	if srcStatus != "DONE" {
		srcStatus = "PARKED"
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		ack, err := r.BuildAck(func() (int64, error) { return nextControl(ctx, tx, tid, r.PartnerID, standard) })
		if err != nil {
			return err
		}
		if ack == "" {
			return ErrNoAck
		}
		reason := clip(r.Reason, 1000)
		if reason == "" {
			reason = "refused"
		}
		if _, err := tx.Exec(ctx, `insert into interchange (tenant_id, id, partner_id, direction, standard, version,
			control_number, raw_payload, ack_status, ack_payload, refusal)
			values ($1, $2, $3, 'IN', $4, $5, $6, $7, 'REJECTED', $8, $9)`,
			tid, interchangeID, r.PartnerID, standard, r.Version, r.Control, r.Raw, ack, reason); err != nil {
			return err
		}
		src := r.Source
		if _, err := tx.Exec(ctx, `insert into transport_inbound (tenant_id, partner_id, legal_entity_id, transport, ref, content_sha256, status,
			interchange_id, attempts, last_error) values ($1, $2, $3, $4, $5, $6, $9, $7, 1, $8)
			on conflict (tenant_id, partner_id, transport, ref, content_sha256) do update set status = excluded.status, interchange_id = excluded.interchange_id,
			attempts = transport_inbound.attempts + 1, next_attempt_at = null, last_error = excluded.last_error, updated_at = now()`,
			tid, r.PartnerID, src.LegalEntityID, src.Transport, src.Ref, src.SHA256, interchangeID, reason, srcStatus); err != nil {
			return err
		}
		_, err = enqueueDelivery(ctx, tx, tid, r.PartnerID, interchangeID, "ACK", standard, ackSet, r.Control)
		return err
	})
	if err != nil {
		return "", err
	}
	return interchangeID, nil
}

// ClaimSFTPPolls leases up to limit partners of the context's tenant whose
// SFTP inbox is due: the next poll moves one interval ahead before the poll
// runs, so concurrent pollers skip each other.
func (s *Store) ClaimSFTPPolls(ctx context.Context, limit int) ([]TransportRecord, error) {
	var out []TransportRecord
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `with claimed as (
				select partner_id as claimed_id from partner_transport where kind = 'SFTP' and sftp_next_poll_at <= now()
				order by sftp_next_poll_at, partner_id limit $1 for update skip locked)
			update partner_transport t set sftp_next_poll_at = now() + make_interval(secs => t.sftp_poll_seconds)
			from claimed where t.partner_id = claimed.claimed_id returning t.partner_id::text`, limit)
		if err != nil {
			return err
		}
		partners, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, p := range partners {
			var t TransportRecord
			if err := scanTransport(tx.QueryRow(ctx, transportColumns+` where partner_id = $1`, p), &t); err != nil {
				return err
			}
			out = append(out, t)
		}
		return nil
	})
	return out, err
}

// FinishSFTPPoll records a poll: success resets the failure count; a failure
// (the server could not be reached or listed) backs the next poll off
// exponentially from the interval, up to six hours.
func (s *Store) FinishSFTPPoll(ctx context.Context, partnerID string, ok bool) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if ok {
			_, err := tx.Exec(ctx, `update partner_transport set sftp_poll_failures = 0 where partner_id = $1 and kind = 'SFTP'`, partnerID)
			return err
		}
		_, err := tx.Exec(ctx, `update partner_transport set sftp_poll_failures = least(sftp_poll_failures + 1, 16),
			sftp_next_poll_at = now() + make_interval(secs => least(sftp_poll_seconds * power(2, least(sftp_poll_failures + 1, 16)), 21600))
			where partner_id = $1 and kind = 'SFTP' and sftp_next_poll_at is not null`, partnerID)
		return err
	})
}
