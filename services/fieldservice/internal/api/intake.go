package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

const (
	maxMessageID      = 998
	maxAddress        = 320
	maxMailBody       = 100000
	maxAttachments    = 50
	maxAttachmentSize = 25 << 20
)

var emailAddress = regexp.MustCompile(`^[^@\s<>]+@[^@\s<>]+\.[^@\s<>]+$`)

func (a *API) registerIntake(r chi.Router) {
	setupRead := authz.Require(anyDuty{dutyGenMaintain, dutyGenCreate, dutyCaseSetup}, "fieldservice.case.maintain, fieldservice.case.create or fieldservice.case-setup.admin", "")
	r.With(authz.RequireDuty(dutyCaseSetup)).Post("/v1/case-mailboxes", a.upsertMailbox)
	r.With(setupRead).Get("/v1/case-mailboxes", a.listMailboxes)
	r.With(authz.RequireDuty(dutyGenCreate)).Post("/v1/inbound-messages", a.ingestInbound)
	r.With(authz.RequireDuty(dutyGenMaintain)).Get("/v1/inbound-messages", a.listInbound)
}

type mailboxRequest struct {
	Address       string `json:"address"`
	LegalEntityID string `json:"legalEntityId"`
	CategoryID    string `json:"categoryId"`
}

func addressField(fields map[string]string, name, v string) {
	if len(v) > maxAddress || !emailAddress.MatchString(v) {
		fields[name] = fmt.Sprintf("must be an email address of at most %d characters", maxAddress)
	}
}

// upsertMailbox sets the legal entity and default category of a case mailbox
// (8.45.13 "Case mailboxes and default categories"). The lower-cased address is the
// natural key, so a repeat is idempotent without an Idempotency-Key: 201 when the
// mailbox is new, 200 when it was updated.
func (a *API) upsertMailbox(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req mailboxRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Address = strings.ToLower(strings.TrimSpace(req.Address))
	fields := map[string]string{}
	addressField(fields, "address", req.Address)
	uuidField(fields, "legalEntityId", req.LegalEntityID)
	uuidField(fields, "categoryId", req.CategoryID)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	mb, created, err := a.store.UpsertMailbox(r.Context(), store.Mailbox{Address: req.Address, LegalEntityID: req.LegalEntityID, CategoryID: req.CategoryID}, a.now().UTC())
	if err != nil {
		a.fail(w, r, "upsert case mailbox", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, mb)
}

func (a *API) listMailboxes(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	items, err := a.store.ListMailboxes(r.Context())
	if err != nil {
		a.fail(w, r, "list case mailboxes", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

type inboundRequest struct {
	Mailbox         string             `json:"mailbox"`
	MessageID       string             `json:"messageId"`
	InReplyTo       string             `json:"inReplyTo"`
	FromAddress     string             `json:"fromAddress"`
	Subject         string             `json:"subject"`
	Body            string             `json:"body"`
	ReceivedAt      *time.Time         `json:"receivedAt"`
	CustomerPartyID *string            `json:"customerPartyId"`
	Attachments     []store.Attachment `json:"attachments"`
	AutoSubmitted   string             `json:"autoSubmitted"`
	XAutoreply      bool               `json:"xAutoreply"`
}

func messageIDField(fields map[string]string, name, v string, required bool) {
	if (required && v == "") || len(v) > maxMessageID || strings.ContainsAny(v, " \t\r\n") {
		fields[name] = fmt.Sprintf("must be a Message-ID of at most %d characters without whitespace", maxMessageID)
	}
}

// ingestInbound records one inbound email from the integration hub and threads it
// onto a case (FR-45.16, BR-45.13). The Message-ID is the idempotency key: a
// redelivery answers 200 with Idempotent-Replayed and the stored result. A new case
// answers 201 and says whether the hub sends the acknowledgment; an attached message
// answers 200 with the info message CASE-MAIL-001.
func (a *API) ingestInbound(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req inboundRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Mailbox = strings.ToLower(strings.TrimSpace(req.Mailbox))
	req.FromAddress = strings.ToLower(strings.TrimSpace(req.FromAddress))
	fields := map[string]string{}
	addressField(fields, "mailbox", req.Mailbox)
	addressField(fields, "fromAddress", req.FromAddress)
	messageIDField(fields, "messageId", req.MessageID, true)
	messageIDField(fields, "inReplyTo", req.InReplyTo, false)
	if len([]rune(req.Subject)) > maxMessageID {
		fields["subject"] = fmt.Sprintf("must be at most %d characters", maxMessageID)
	}
	if len([]rune(req.Body)) > maxMailBody {
		fields["body"] = fmt.Sprintf("must be at most %d characters", maxMailBody)
	}
	if req.ReceivedAt == nil {
		fields["receivedAt"] = "is required (RFC 3339)"
	}
	optionalUUID(fields, "customerPartyId", req.CustomerPartyID)
	if len(req.AutoSubmitted) > 100 {
		fields["autoSubmitted"] = "must be at most 100 characters"
	}
	if len(req.Attachments) > maxAttachments {
		fields["attachments"] = fmt.Sprintf("at most %d attachments", maxAttachments)
	}
	for i, att := range req.Attachments {
		key := fmt.Sprintf("attachments[%d]", i)
		switch {
		case att.Name == "" || len([]rune(att.Name)) > 255:
			fields[key] = "name must be 1 to 255 characters"
		case len(att.ContentType) > 255:
			fields[key] = "contentType must be at most 255 characters"
		case att.SizeBytes < 0 || att.SizeBytes > maxAttachmentSize:
			fields[key] = "sizeBytes must be 0 to 25 MiB"
		case att.ObjectRef == "" || len(att.ObjectRef) > 512:
			fields[key] = "objectRef must be 1 to 512 characters"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	m, replayed, err := a.store.IngestInbound(r.Context(), store.InboundInput{
		Mailbox: req.Mailbox, MessageID: req.MessageID, InReplyTo: req.InReplyTo, FromAddress: req.FromAddress, Subject: req.Subject,
		Body: req.Body, ReceivedAt: req.ReceivedAt.UTC(), CustomerPartyID: req.CustomerPartyID, Attachments: req.Attachments,
		AutoSubmitted: req.AutoSubmitted, XAutoreply: req.XAutoreply, At: a.now().UTC(),
	})
	if err != nil {
		a.fail(w, r, "ingest inbound message", err)
		return
	}
	res := inboundResult{InboundMessage: m}
	status := http.StatusCreated
	if m.Route == domain.RouteAttached {
		status = http.StatusOK
		res.Message = &infoMessage{Code: domain.CodeMailAttached, Text: "Email attached to case " + m.CaseNumber}
	}
	a.respond(w, replayed, status, res)
}

// infoMessage is a non-error message of the PRD catalog (8.45.18) returned with a result.
type infoMessage struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

type inboundResult struct {
	store.InboundMessage
	Message *infoMessage `json:"message,omitempty"`
}

// listInbound lists inbound email newest first, leaving out messages on cases the
// caller may not read (BR-45.10).
func (a *API) listInbound(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.InboundFilter{Mailbox: strings.ToLower(q.Get("mailbox")), CaseID: q.Get("caseId"), Route: q.Get("route")}
	fields := map[string]string{}
	if f.Mailbox != "" {
		addressField(fields, "mailbox", f.Mailbox)
	}
	if f.CaseID != "" {
		uuidField(fields, "caseId", f.CaseID)
	}
	if f.Route != "" && !oneOf(f.Route, domain.RouteAttached, domain.RouteNewCase) {
		fields["route"] = "must be ATTACHED or NEW_CASE"
	}
	f.Cursor, f.Limit = pageParams(fields, q.Get("cursor"), q.Get("limit"))
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	page, err := a.store.ListInboundMessages(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list inbound messages", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}
