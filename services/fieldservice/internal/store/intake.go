package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
)

const (
	logEmail        = "EMAIL"
	intakeNoSubject = "(no subject)"
	maxMailboxes    = 500
)

// Mailbox routes the mail of one address to its default category (BR-45.13).
type Mailbox struct {
	Address       string    `json:"address"`
	LegalEntityID string    `json:"legalEntityId"`
	CategoryID    string    `json:"categoryId"`
	CategoryCode  string    `json:"categoryCode"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Attachment is the metadata of a file received with an email; the bytes stay in
// the object store under ObjectRef.
type Attachment struct {
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
	ObjectRef   string `json:"objectRef"`
}

// InboundInput is one email handed over by the integration hub (FR-45.16).
type InboundInput struct {
	Mailbox         string
	MessageID       string
	InReplyTo       string
	FromAddress     string
	Subject         string
	Body            string
	ReceivedAt      time.Time
	CustomerPartyID *string
	Attachments     []Attachment
	AutoSubmitted   string
	XAutoreply      bool
	At              time.Time
}

// InboundMessage is a stored inbound email and where it landed.
type InboundMessage struct {
	ID               string       `json:"id"`
	Mailbox          string       `json:"mailbox"`
	MessageID        string       `json:"messageId"`
	InReplyTo        string       `json:"inReplyTo"`
	FromAddress      string       `json:"fromAddress"`
	Subject          string       `json:"subject"`
	Body             string       `json:"body"`
	ReceivedAt       time.Time    `json:"receivedAt"`
	CaseID           string       `json:"caseId"`
	CaseNumber       string       `json:"caseNumber"`
	Route            string       `json:"route"`
	ReopenedFrom     *string      `json:"reopenedFrom,omitempty"`
	ReopenedFromCase *string      `json:"reopenedFromNumber,omitempty"`
	Automated        bool         `json:"automated"`
	Acknowledge      bool         `json:"acknowledge"`
	Attachments      []Attachment `json:"attachments"`
	CreatedAt        time.Time    `json:"createdAt"`
}

// InboundFilter narrows the message list.
type InboundFilter struct {
	Mailbox string
	CaseID  string
	Route   string
	Cursor  string
	Limit   int
}

// checkMailboxCategory refuses a default category that email cannot open cases in:
// SERVICE and CUSTOMER_SERVICE open through /v1/service/cases (CASE-CAT-002), and
// COLLECTIONS needs a customer an email cannot be relied on to name (CASE-MAIL-003).
func checkMailboxCategory(cat Category) error {
	if domain.ServiceRouted(cat.CategoryType) {
		return unprocessable(domain.CodeCategoryRouted, "SERVICE and CUSTOMER_SERVICE cases open through /v1/service/cases, not a mailbox")
	}
	if domain.RequiresCustomer(cat.CategoryType) {
		return unprocessable(domain.CodeMailboxCat, "a mailbox cannot default to a "+cat.CategoryType+" category, whose cases require a customer")
	}
	return nil
}

// UpsertMailbox sets the legal entity and default category of a mailbox address. It
// reports whether the mailbox is new.
func (s *Postgres) UpsertMailbox(ctx context.Context, in Mailbox, at time.Time) (Mailbox, bool, error) {
	tid := tenantOf(ctx)
	var out Mailbox
	var created bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cat, err := getCategory(ctx, tx, tid, in.CategoryID)
		if err != nil {
			return err
		}
		if err := checkMailboxCategory(cat); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `insert into service_case_mailbox (tenant_id, address, legal_entity_id, category_id, created_at, updated_at)
			values ($1, $2, $3, $4, $5, $5)
			on conflict (tenant_id, address) do update set legal_entity_id = excluded.legal_entity_id, category_id = excluded.category_id,
				updated_at = excluded.updated_at
			returning (xmax = 0)`, tid, in.Address, in.LegalEntityID, cat.ID, at).Scan(&created); err != nil {
			return err
		}
		out = Mailbox{Address: in.Address, LegalEntityID: in.LegalEntityID, CategoryID: cat.ID, CategoryCode: cat.Code, UpdatedAt: at}
		return nil
	})
	return out, created, err
}

// ListMailboxes returns the tenant's mailboxes in the caller's legal-entity scope.
func (s *Postgres) ListMailboxes(ctx context.Context) ([]Mailbox, error) {
	tid := tenantOf(ctx)
	out := []Mailbox{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select m.address, m.legal_entity_id::text, m.category_id::text, k.code, m.updated_at
			from service_case_mailbox m join service_case_category k on k.tenant_id = m.tenant_id and k.id = m.category_id
			where m.tenant_id = $1 order by m.address limit $2`, tid, maxMailboxes)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Mailbox])
		return err
	})
	return out, err
}

const inboundColumns = `m.id::text, m.mailbox, m.message_id, m.in_reply_to, m.from_address, m.subject, m.body, m.received_at,
	m.case_id::text, g.number, m.route, m.reopened_from::text, r.number, m.automated, m.acknowledge, m.attachments, m.created_at`

const inboundFrom = ` from service_inbound_message m
	join service_generic_case g on g.tenant_id = m.tenant_id and g.id = m.case_id
	join service_case_category k on k.tenant_id = g.tenant_id and k.id = g.category_id
	left join service_generic_case r on r.tenant_id = m.tenant_id and r.id = m.reopened_from`

func scanInbound(r pgx.Row) (InboundMessage, error) {
	var m InboundMessage
	var atts []byte
	if err := r.Scan(&m.ID, &m.Mailbox, &m.MessageID, &m.InReplyTo, &m.FromAddress, &m.Subject, &m.Body, &m.ReceivedAt, &m.CaseID,
		&m.CaseNumber, &m.Route, &m.ReopenedFrom, &m.ReopenedFromCase, &m.Automated, &m.Acknowledge, &atts, &m.CreatedAt); err != nil {
		return m, err
	}
	return m, json.Unmarshal(atts, &m.Attachments)
}

func inboundByMessageID(ctx context.Context, tx pgx.Tx, tenantID, messageID string) (InboundMessage, error) {
	return scanInbound(tx.QueryRow(ctx, `select `+inboundColumns+inboundFrom+` where m.tenant_id = $1 and m.message_id = $2`, tenantID, messageID))
}

// threadHit loads a case a message may thread onto, as long as the caller may
// access it (BR-45.10): a case the caller cannot see is not matched, so the reply
// reveals nothing about it.
func threadHit(ctx context.Context, tx pgx.Tx, tenantID, query string, arg string) (*domain.ThreadCase, error) {
	var h domain.ThreadCase
	var duty, owner string
	err := tx.QueryRow(ctx, query, tenantID, arg).Scan(&h.ID, &h.Number, &h.Status, &duty, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !mayAccess(ctx, duty, owner) {
		return nil, nil
	}
	return &h, nil
}

const threadCaseSelect = `select g.id::text, g.number, g.status, k.access_duty, g.owner_id from service_generic_case g
	join service_case_category k on k.tenant_id = g.tenant_id and k.id = g.category_id`

// IngestInbound records one inbound email and threads it (FR-45.16, BR-45.13): it is
// attached to the case its [CS-n] token or In-Reply-To names unless that case is
// CLOSED or CANCELED, which opens a new case linked by reopened_from; otherwise a
// new case is opened in the mailbox's default category. The Message-ID is the
// idempotency key: a redelivery returns the stored message with replayed true.
func (s *Postgres) IngestInbound(ctx context.Context, in InboundInput) (InboundMessage, bool, error) {
	tid := tenantOf(ctx)
	var out InboundMessage
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		prior, err := inboundByMessageID(ctx, tx, tid, in.MessageID)
		if err == nil {
			out, replayed = prior, true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var mb Mailbox
		err = tx.QueryRow(ctx, `select address, legal_entity_id::text, category_id::text from service_case_mailbox where tenant_id = $1 and address = $2`,
			tid, in.Mailbox).Scan(&mb.Address, &mb.LegalEntityID, &mb.CategoryID)
		if errors.Is(err, pgx.ErrNoRows) {
			return unprocessable(domain.CodeMailbox, "no case mailbox is set up for "+in.Mailbox)
		}
		if err != nil {
			return err
		}
		var byToken, byReply *domain.ThreadCase
		if number, ok := domain.CaseToken(in.Subject); ok {
			if byToken, err = threadHit(ctx, tx, tid, threadCaseSelect+` where g.tenant_id = $1 and g.number = $2`, number); err != nil {
				return err
			}
		}
		if in.InReplyTo != "" {
			if byReply, err = threadHit(ctx, tx, tid, threadCaseSelect+` join service_inbound_message m on m.tenant_id = g.tenant_id and m.case_id = g.id
				where g.tenant_id = $1 and m.message_id = $2`, in.InReplyTo); err != nil {
				return err
			}
		}
		automated := domain.Automated(in.AutoSubmitted, in.XAutoreply)
		route := domain.Thread(byToken, byReply, automated)
		actor := caller(ctx).UserID
		var caseID string
		var reopenedFrom *string
		if route.Route == domain.RouteAttached {
			c, err := getGenericCase(ctx, tx, tid, route.Case.ID, true)
			if err != nil {
				return err
			}
			caseID = c.ID
			if _, err := tx.Exec(ctx, `update service_generic_case set version = version + 1, updated_at = $3 where tenant_id = $1 and id = $2`,
				tid, c.ID, in.At); err != nil {
				return err
			}
			if err := addLog(ctx, tx, tid, c.ID, actor, logEmail, "Email attached to case "+c.Number+" from "+in.FromAddress+": "+in.Subject, in.At); err != nil {
				return err
			}
		} else {
			if caseID, err = s.caseForMail(ctx, tx, tid, mb, in, route.Case); err != nil {
				return err
			}
			if route.Case != nil {
				reopenedFrom = &route.Case.ID
			}
		}
		atts := in.Attachments
		if atts == nil {
			atts = []Attachment{}
		}
		raw, err := json.Marshal(atts)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into service_inbound_message (tenant_id, id, mailbox, legal_entity_id, message_id, in_reply_to, from_address,
			subject, body, received_at, case_id, route, reopened_from, automated, acknowledge, attachments, created_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
			tid, ids.New(), mb.Address, mb.LegalEntityID, in.MessageID, in.InReplyTo, in.FromAddress, in.Subject, in.Body, in.ReceivedAt,
			caseID, route.Route, reopenedFrom, automated, route.Acknowledge, raw, in.At); err != nil {
			return err
		}
		out, err = inboundByMessageID(ctx, tx, tid, in.MessageID)
		return err
	})
	if isUnique(err) {
		// A concurrent delivery of the same Message-ID won; answer with its result.
		err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
			out, err = inboundByMessageID(ctx, tx, tid, in.MessageID)
			return err
		})
		return out, err == nil, err
	}
	return out, replayed, err
}

// caseForMail opens the case of a message that threads onto no open case: a
// continuation of the closed case it names (same category, customer and
// associations, linked by reopened_from) or a new case in the mailbox's default
// category, whose access duty the caller must hold (BR-45.10).
func (s *Postgres) caseForMail(ctx context.Context, tx pgx.Tx, tid string, mb Mailbox, in InboundInput, closed *domain.ThreadCase) (string, error) {
	actor := caller(ctx).UserID
	subject := strings.TrimSpace(in.Subject)
	if subject == "" {
		subject = intakeNoSubject
	}
	if r := []rune(subject); len(r) > 200 {
		subject = string(r[:200])
	}
	description := in.Body
	if r := []rune(description); len(r) > 4000 {
		description = string(r[:4000])
	}
	c := GenericCase{LegalEntityID: mb.LegalEntityID, CustomerPartyID: in.CustomerPartyID, OwnerID: actor, Subject: subject,
		Description: description, OpenedAt: in.At}
	logText := "opened from email by " + in.FromAddress + " to " + mb.Address
	var assoc []CaseAssociation
	categoryID := mb.CategoryID
	if closed != nil {
		prev, err := getGenericCase(ctx, tx, tid, closed.ID, true)
		if err != nil {
			return "", err
		}
		categoryID, c.LegalEntityID, c.Priority, assoc = prev.CategoryID, prev.LegalEntityID, prev.Priority, prev.Associations
		if c.CustomerPartyID == nil {
			c.CustomerPartyID = prev.CustomerPartyID
		}
		c.ReopenedFrom = &prev.ID
		logText = "reopened from " + prev.Number + " by email from " + in.FromAddress
	}
	cat, err := getCategory(ctx, tx, tid, categoryID)
	if err != nil {
		return "", err
	}
	if closed == nil {
		if cat.AccessDuty != "" && !caller(ctx).HasDuty(cat.AccessDuty) {
			return "", forbidCategory(cat.Code)
		}
		if err := checkMailboxCategory(cat); err != nil {
			return "", err
		}
		c.Priority = cat.DefaultPriority
	}
	id, err := insertGenericCase(ctx, tx, tid, cat, c, assoc, logText, "", "")
	if err != nil {
		return "", err
	}
	if closed != nil {
		number, err := caseNumber(ctx, tx, tid, id)
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `update service_generic_case set version = version + 1, updated_at = $3 where tenant_id = $1 and id = $2`,
			tid, closed.ID, in.At); err != nil {
			return "", err
		}
		if err := addLog(ctx, tx, tid, closed.ID, actor, logReopened, "reopened as "+number+" by email from "+in.FromAddress, in.At); err != nil {
			return "", err
		}
	}
	return id, nil
}

func caseNumber(ctx context.Context, tx pgx.Tx, tid, id string) (string, error) {
	var n string
	err := tx.QueryRow(ctx, `select number from service_generic_case where tenant_id = $1 and id = $2`, tid, id).Scan(&n)
	return n, err
}

// ListInboundMessages returns inbound messages newest first, leaving out those on
// cases the caller may not read (BR-45.10).
func (s *Postgres) ListInboundMessages(ctx context.Context, f InboundFilter) (Page[InboundMessage], error) {
	p := caller(ctx)
	duties := make([]string, 0, len(p.Duties))
	for d := range p.Duties {
		duties = append(duties, d)
	}
	sort.Strings(duties)
	return listPage(ctx, s, f.Limit, func(m InboundMessage) string { return m.ID }, scanInbound,
		`select `+inboundColumns+inboundFrom+`
		where m.tenant_id = $1
		  and (k.access_duty = '' or k.access_duty = any($2::text[]) or ($3 <> '' and g.owner_id = $3))
		  and ($4 = '' or m.mailbox = $4) and ($5 = '' or m.case_id::text = $5) and ($6 = '' or m.route = $6)
		  and `+keyset("m", "service_inbound_message", 7)+`
		order by m.created_at desc, m.id desc limit $8`,
		tenantOf(ctx), duties, p.UserID, f.Mailbox, f.CaseID, f.Route, f.Cursor)
}
