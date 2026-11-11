// Package api exposes the edi HTTP surface: partner profiles, the dry-run map
// tester, inbound X12 receipt (850, 820 and 214 mapped, 997s matched to
// outbound documents) and EDIFACT receipt (ORDERS, DESADV and INVOIC mapped,
// CONTRL answers, received CONTRLs matched), outbound X12 and EDIFACT
// generation, and the message monitor with single and bulk reprocess, the
// masked raw segment-tree viewer and the partner SLA dashboard. Handlers
// decode, validate, check duties, then call the domain and store.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/edi/internal/certtrust"
	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/secrets"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

// Service error codes (see README). Catalog codes from PRD §6.35.18 are reused
// where they apply.
const (
	CodeValidation       = "EDI-0001"
	CodeIdempotencyKey   = "EDI-0002"
	CodeSyntax           = "EDI-0003"
	CodePartnerExists    = "EDI-0004"
	CodeUnknownSender    = "EDI-0101"
	CodeDuplicateControl = "EDI-0102"
	CodeNotFound         = "EDI-0005"
	CodeUnsupportedSet   = "EDI-0006"
	CodeOutbound         = "EDI-0007"
	CodePartnerInactive  = "EDI-0008"
	CodeNotInbound       = "EDI-0009"
	CodeIdempotencyReuse = "idempotency_key_reuse"
	CodeSourceDown       = "EDI-0010"
	CodeSourceScope      = "EDI-0011"
	CodeNoTradeProfile   = "EDI-0012"
	CodeSourceNotFinal   = "EDI-0013"
	CodeSourceForbidden  = "EDI-0014"
	CodeNoEdifactID      = "EDI-0017"
)

// Interchange list page size: default and maximum.
const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// maxInbound caps one inbound interchange at 4 MiB, matching the JSON body cap.
const maxInbound = 4 << 20

// DefaultSender is our ISA/GS identity on outbound interchanges when
// EDI_ISA_QUALIFIER and EDI_ISA_ID are not set.
var DefaultSender = domain.Party{Qualifier: "ZZ", ID: "PASSAGE"}

// DefaultEdifactSender is our UNB S002 identity on outbound EDIFACT
// interchanges when EDI_EDIFACT_ID and EDI_EDIFACT_QUALIFIER are not set.
var DefaultEdifactSender = domain.EdifactParty{ID: "PASSAGE", Qualifier: "ZZZ"}

// API holds the store used by the handlers that persist state. Sender is our
// ISA06/GS02 identity on outbound documents, EdifactSender our UNB S002
// identity on outbound EDIFACT.
type API struct {
	store         *store.Store
	now           func() time.Time
	Sender        domain.Party
	EdifactSender domain.EdifactParty
	Sources       Sources
	// Intake places inbound orders with sales (POST /messages/{id}:post); nil
	// refuses the route with 503.
	Intake Intake
	// AllowBodyOutbound mounts POST /partners/{id}/outbound, which takes the
	// canonical document from the request body. Tests and development only.
	AllowBodyOutbound bool
	// Secrets resolves the secret references of partner transports
	// (EDI_SECRETS_DIR); nil refuses AS2 and SFTP transports.
	Secrets secrets.Store
	// AS2PublicURL is the base URL partners reach the AS2 listener on
	// (EDI_AS2_PUBLIC_URL); it is shown on the transport and is needed for
	// asynchronous MDNs.
	AS2PublicURL string
	// AllowInsecureAS2 admits http:// partner AS2 URLs (development only).
	AllowInsecureAS2 bool
	// AS2Trust checks partner AS2 certificates against the trust roots and
	// CRLs (EDI_AS2_TRUST_ROOTS, EDI_AS2_CRL_DIR) when a transport is saved
	// and at every use; nil pins them only.
	AS2Trust *certtrust.Checker
	// AS2Limit is the per-endpoint rate limit of the AS2 listener; nil admits
	// every request. Asynchronous MDNs we owe are stored and posted by the
	// delivery dispatcher, not from the listener.
	AS2Limit *AS2Limiter
	// Mandates are the e-invoice mandates (35-2); nil means the defaults: Peppol built, IT SDI and MX CFDI as hooks.
	Mandates domain.Mandates
	// AccessPoint submits e-invoices to their network (a Peppol access point, the SDI, a PAC); nil answers 503 on submit.
	AccessPoint AccessPoint
	// Requisitions creates the requisition of a returned punchout cart in purchasing (35-3); nil answers 503.
	Requisitions Requisitions
	// PunchoutHTTP sends the cXML PunchOutSetupRequest; nil uses a client with a 10 second timeout that follows no redirect.
	PunchoutHTTP *http.Client
}

// New returns the API. The store may be nil for routes that do not persist
// (maps:test), which lets those routes be tested without a database.
func New(s *store.Store) *API {
	return &API{store: s, now: time.Now, Sender: DefaultSender, EdifactSender: DefaultEdifactSender}
}

// Routes returns the HTTP handler with kernel middleware and every edi route.
func (a *API) Routes() http.Handler {
	r := httpx.NewRouter()
	r.Route("/edi/v1", func(r chi.Router) {
		r.With(authz.RequireDuty("edi.admin")).Post("/partners", a.createPartner)
		r.With(authz.RequireDuty("edi.admin")).Get("/partners", a.listPartners)
		r.With(authz.RequireDuty("edi.admin")).Get("/partners/{id}", a.getPartner)
		r.With(authz.RequireDuty("edi.admin")).Patch("/partners/{id}", a.patchPartner)
		r.With(authz.RequireDuty("edi.admin")).Put("/partners/{id}/trade-profile", a.putTradeProfile)
		r.With(authz.RequireDuty("edi.admin")).Put("/partners/{id}/transport", a.putTransport)
		r.With(authz.RequireDuty("edi.admin")).Get("/partners/{id}/transport", a.getTransport)
		r.With(authz.RequireDuty("edi.admin")).Get("/partners/{id}/item-xrefs", a.listXrefs)
		r.With(authz.RequireDuty("edi.admin")).Post("/partners/{id}/item-xrefs", a.createXref)
		r.With(authz.RequireDuty("edi.admin")).Put("/partners/{id}/item-xrefs/{xrefId}", a.putXref)
		r.With(authz.RequireDuty("edi.admin")).Delete("/partners/{id}/item-xrefs/{xrefId}", a.deleteXref)
		r.With(authz.RequireDuty("edi.admin")).Get("/partners/{id}/sales-defaults", a.getSalesDefaults)
		r.With(authz.RequireDuty("edi.admin")).Put("/partners/{id}/sales-defaults", a.putSalesDefaults)
		r.With(authz.RequireDuty("edi.admin")).Get("/partners/{id}/auto-outbound", a.getAutoOutbound)
		r.With(authz.RequireDuty("edi.admin")).Put("/partners/{id}/auto-outbound", a.putAutoOutbound)
		r.With(authz.RequireDuty("edi.operate")).Post("/partners/{id}/outbound:from-source", a.outboundFromSource)
		if a.AllowBodyOutbound {
			r.With(authz.RequireDuty("edi.operate")).Post("/partners/{id}/outbound", a.createOutbound)
		}
		r.With(authz.RequireDuty("edi.admin")).Post("/maps:test", a.mapsTest)
		r.With(authz.RequireDuty("edi.operate")).Post("/inbound/x12", a.receiveX12)
		r.With(authz.RequireDuty("edi.operate")).Post("/inbound/edifact", a.receiveEdifact)
		r.With(authz.RequireDuty("edi.operate")).Get("/interchanges", a.listInterchanges)
		r.With(authz.RequireDuty("edi.operate")).Get("/interchanges/{id}", a.getInterchange)
		r.With(authz.RequireDuty("edi.operate")).Get("/interchanges/{id}/payload", a.getPayload)
		r.With(authz.RequireDuty("edi.operate")).Post("/interchanges/{id}:reprocess", a.reprocessInterchange)
		r.With(authz.RequireDuty("edi.operate")).Post("/interchanges/{id}:resend", a.resendInterchange)
		r.With(authz.RequireDuty("edi.operate")).Post("/interchanges:reprocess", a.bulkReprocess)
		r.With(authz.RequireDuty("edi.operate")).Get("/messages", a.listMessages)
		r.With(authz.RequireDuty("edi.operate")).Get("/messages/{id}", a.getMessage)
		r.With(authz.RequireDuty("edi.operate")).Post("/messages/{id}:post", a.postMessage)
		r.With(authz.RequireDuty("edi.operate")).Get("/partners/sla", a.partnerSLA)
		r.With(authz.RequireDuty("edi.requisition.create")).Post("/punchout/returns:parse", a.punchoutParse)
		r.With(authz.RequireDuty("edi.requisition.create")).Post("/punchout/sessions", a.createPunchoutSession)
		r.With(authz.RequireDuty("edi.requisition.create")).Get("/punchout/sessions", a.listPunchoutSessions)
		r.With(authz.RequireDuty("edi.requisition.create")).Get("/punchout/sessions/{id}", a.getPunchoutSession)
		r.With(authz.RequireDuty("edi.requisition.create")).Post("/punchout/sessions/{id}:cancel", a.cancelPunchoutSession)
		r.With(authz.RequireDuty("edi.requisition.create")).Post("/punchout/sessions/{id}:create-requisition", a.createRequisitionFromSession)
		r.With(authz.RequireDuty("edi.admin")).Get("/partners/{id}/cxml", a.getPartnerCXML)
		r.With(authz.RequireDuty("edi.admin")).Put("/partners/{id}/cxml", a.putPartnerCXML)
		r.With(authz.RequireDuty("edi.admin")).Get("/partners/{id}/punchout", a.getPunchoutSupplier)
		r.With(authz.RequireDuty("edi.admin")).Put("/partners/{id}/punchout", a.putPunchoutSupplier)
		r.With(authz.RequireDuty("edi.self-billing.reconcile")).Post("/self-billing:match", a.selfBillingMatch)
		r.With(authz.RequireDuty("edi.self-billing.reconcile")).Post("/self-billing:sweep", a.selfBillSweep)
		r.With(authz.RequireDuty("edi.self-billing.reconcile")).Get("/self-billing/documents", a.listSelfBillDocuments)
		r.With(authz.RequireDuty("edi.self-billing.reconcile")).Get("/self-billing/disputes", a.listDisputes)
		r.With(authz.RequireDuty("edi.self-billing.reconcile")).Post("/self-billing/disputes/{id}:resolve", a.resolveDispute)
		r.With(authz.RequireDuty("edi.admin")).Get("/partners/{id}/self-billing", a.getSelfBillConfig)
		r.With(authz.RequireDuty("edi.admin")).Put("/partners/{id}/self-billing", a.putSelfBillConfig)
		r.With(authz.RequireDuty("edi.einvoice.operate")).Post("/peppol/invoices:build", a.ublBuild)
		r.With(authz.RequireDuty("edi.einvoice.operate")).Put("/einvoice/parties/{role}/{subjectId}", a.putEInvoiceParty)
		r.With(authz.RequireDuty("edi.einvoice.operate")).Get("/einvoice/parties/{role}/{subjectId}", a.getEInvoiceParty)
		r.With(authz.RequireDuty("edi.einvoice.operate")).Post("/einvoice/invoices", a.createEInvoice)
		r.With(authz.RequireDuty("edi.einvoice.operate")).Get("/einvoice/invoices", a.listEInvoices)
		r.With(authz.RequireDuty("edi.einvoice.operate")).Get("/einvoice/invoices/{id}", a.getEInvoice)
		r.With(authz.RequireDuty("edi.einvoice.operate")).Post("/einvoice/invoices/{id}:submit", a.submitEInvoice)
		r.With(authz.RequireDuty("edi.einvoice.operate")).Post("/einvoice/invoices/{id}:response", a.recordEInvoiceResponse)
	})
	return r
}

type partnerRequest struct {
	Name             string `json:"name"`
	ISAQualifier     string `json:"isaQualifier"`
	ISAID            string `json:"isaId"`
	Environment      string `json:"environment"`
	AckSLAMinutes    int    `json:"ackSlaMinutes"`
	EdifactID        string `json:"edifactId"`
	EdifactQualifier string `json:"edifactQualifier"`
	EdifactContrl    string `json:"edifactContrl"`
}

func validation(fields map[string]string) *httpx.Problem {
	p := httpx.NewProblem(http.StatusBadRequest, CodeValidation, "request failed validation")
	p.Errors = fields
	return p
}

func (a *API) createPartner(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 255 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeIdempotencyKey, "Idempotency-Key header is required (max 255 characters)"))
		return
	}
	var req partnerRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	prof := domain.PartnerProfile{
		Name:          req.Name,
		ISA:           domain.Party{Qualifier: req.ISAQualifier, ID: req.ISAID},
		Environment:   req.Environment,
		AckSLAMinutes: req.AckSLAMinutes,
	}
	if req.EdifactID != "" || req.EdifactQualifier != "" || req.EdifactContrl != "" {
		prof.EDIFACT = &domain.EdifactIdentity{ID: req.EdifactID, Qualifier: req.EdifactQualifier, Contrl: req.EdifactContrl}
	}
	if fields := prof.Validate(); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	prof = prof.WithDefaults()
	p, created, err := a.store.CreatePartner(r.Context(), key, requestHash(prof), prof)
	if errors.Is(err, store.ErrIdempotencyReuse) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body"))
		return
	}
	if errors.Is(err, store.ErrDuplicate) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodePartnerExists, "a partner with this ISA or EDIFACT identity already exists in this environment"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, p)
}

// requestHash fingerprints the normalized partner request, so a replay that
// only differs in JSON whitespace or field order still matches its key.
func requestHash(p domain.PartnerProfile) string {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// validID accepts only the canonical 36-character UUID form Postgres reads;
// uuid.Parse alone also accepts urn:uuid: and braced forms.
func validID(s string) bool { return len(s) == 36 && ids.Valid(s) }

func notFound(what string) *httpx.Problem {
	return httpx.NewProblem(http.StatusNotFound, CodeNotFound, what+" not found")
}

func (a *API) listPartners(w http.ResponseWriter, r *http.Request) {
	env := r.URL.Query().Get("environment")
	if env != "" && env != "TEST" && env != "PROD" {
		httpx.Fail(w, r, validation(map[string]string{"environment": "must be TEST or PROD"}))
		return
	}
	items, err := a.store.ListPartners(r.Context(), env)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getPartner(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	p, err := a.store.GetPartner(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, p.Version))
	httpx.JSON(w, http.StatusOK, p)
}

func (a *API) listInterchanges(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.InterchangeFilter{PartnerID: q.Get("partnerId"), AckStatus: q.Get("ackStatus"), Direction: q.Get("direction"), Limit: defaultListLimit}
	fields := map[string]string{}
	if f.PartnerID != "" && !validID(f.PartnerID) {
		fields["partnerId"] = "must be a UUID"
	}
	if f.Direction != "" && f.Direction != "IN" && f.Direction != "OUT" {
		fields["direction"] = "must be IN or OUT"
	}
	switch f.AckStatus {
	case "", "PENDING", "ACCEPTED", "ACCEPTED_WITH_ERRORS", "REJECTED", "TIMEOUT":
	default:
		fields["ackStatus"] = "must be PENDING, ACCEPTED, ACCEPTED_WITH_ERRORS, REJECTED or TIMEOUT"
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxListLimit {
			fields["limit"] = fmt.Sprintf("must be 1 to %d", maxListLimit)
		}
		f.Limit = n
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	items, err := a.store.ListInterchanges(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getInterchange(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("interchange"))
		return
	}
	d, err := a.store.GetInterchange(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("interchange"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !piiView(r) {
		for i := range d.Messages {
			d.Messages[i].Canonical = domain.MaskCanonical(d.Messages[i].Canonical)
		}
	}
	httpx.JSON(w, http.StatusOK, d)
}

// txResult is the outcome of mapping one transaction set. order is set for
// an 850 (and an ORDERS), document for every set that mapped onto a
// canonical document; unsupported marks a set no mapping exists for.
type txResult struct {
	outcome     domain.TxOutcome
	status      string
	order       *domain.Order
	document    any
	canonical   []byte
	errText     string
	issues      []domain.Issue
	unsupported bool
}

// InboundSets lists the X12 transaction sets the receipt maps onto a
// canonical document: the 850 order, the 820 remittance and the 214
// shipment status. A 997 is matched, not mapped; any other set is rejected
// (AK5 R, EDI-0006).
var InboundSets = []string{"850", "820", "214"}

// mapTransaction maps one transaction and derives its 997 code and message
// status: AK5 A and MAPPED without issues, E and FAILED with only
// accepted-with-error issues, R and FAILED with a rejecting issue. Sets
// outside InboundSets are rejected (AK5 R).
func mapTransaction(t domain.Transaction) (txResult, error) {
	res := txResult{outcome: domain.TxOutcome{SetID: t.SetID, Control: t.Control}}
	var issues []domain.Issue
	switch t.SetID {
	case "850":
		order, is := domain.Map850(t)
		res.order, res.document, issues = &order, order, is
	case "820":
		r, is := domain.Map820(t)
		res.document, issues = r, is
	case "214":
		st, is := domain.Map214(t)
		res.document, issues = st, is
	default:
		res.outcome.Code = domain.AckRejected
		res.status = "FAILED"
		res.errText = "transaction set " + t.SetID + " is not supported yet"
		res.unsupported = true
		return res, nil
	}
	canonical, err := json.Marshal(res.document)
	if err != nil {
		return txResult{}, err
	}
	res.canonical = canonical
	res.issues = issues
	var details []string
	res.outcome.Code = domain.AckAccepted
	for _, is := range issues {
		details = append(details, is.Code+": "+is.Detail)
		if is.Reject {
			res.outcome.Code = domain.AckRejected
		} else if res.outcome.Code == domain.AckAccepted {
			res.outcome.Code = domain.AckAcceptedWithError
		}
	}
	res.errText = strings.Join(details, "; ")
	res.status = "FAILED"
	if res.outcome.Code == domain.AckAccepted {
		res.status = "MAPPED"
	}
	return res, nil
}

type mapRequest struct {
	Raw string `json:"raw"`
}

type mapResult struct {
	TransactionSet string        `json:"transactionSet"`
	STControl      string        `json:"stControl"`
	Status         string        `json:"status"`
	Order          *domain.Order `json:"order,omitempty"`
	// Document is the canonical remittance (820) or shipment status (214).
	Document any            `json:"document,omitempty"`
	Issues   []domain.Issue `json:"issues"`
}

// mapsTest runs the mapping over a sample interchange without persisting
// anything (FR-35.3 test harness).
func (a *API) mapsTest(w http.ResponseWriter, r *http.Request) {
	var req mapRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	ic, err := domain.Parse(req.Raw)
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeSyntax, err.Error()))
		return
	}
	results := []mapResult{}
	for _, g := range ic.Groups {
		for _, t := range g.Transactions {
			res, err := mapTransaction(t)
			if err != nil {
				httpx.Fail(w, r, err)
				return
			}
			mr := mapResult{TransactionSet: t.SetID, STControl: t.Control, Status: res.status, Order: res.order, Issues: res.issues}
			if res.order == nil {
				mr.Document = res.document
			}
			if mr.Issues == nil {
				mr.Issues = []domain.Issue{}
			}
			if res.unsupported {
				mr.Issues = append(mr.Issues, domain.Issue{Code: CodeUnsupportedSet, Detail: res.errText, Reject: true})
			}
			results = append(results, mr)
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"transactions": results})
}

type messageView struct {
	MessageID      string `json:"messageId"`
	TransactionSet string `json:"transactionSet"`
	STControl      string `json:"stControl"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
}

type receiveResponse struct {
	InterchangeID string        `json:"interchangeId"`
	AckStatus     string        `json:"ackStatus"`
	Messages      []messageView `json:"messages"`
	Ack997        string        `json:"ack997,omitempty"`
	Contrl        string        `json:"contrl,omitempty"`
}

func messageViews(msgs []store.Message) []messageView {
	views := make([]messageView, 0, len(msgs))
	for _, m := range msgs {
		views = append(views, messageView{MessageID: m.ID, TransactionSet: m.TransactionSet,
			STControl: m.STControl, Status: m.Status, Error: m.Error})
	}
	return views
}

type groupOutcomes struct {
	group    domain.Group
	outcomes []domain.TxOutcome
}

// receiveX12 accepts one raw X12 interchange from a partner, posted by an
// operator or an integration (the AS2 endpoint and the SFTP poller hand
// payloads to the same pipeline, see receive). It identifies the partner by
// ISA sender, records every transaction, and returns the 997 text generated
// in the same transaction.
func (a *API) receiveX12(w http.ResponseWriter, r *http.Request) {
	a.receiveRoute(w, r, domain.SyntaxX12)
}

// receiveRoute serves POST /inbound/x12 and /inbound/edifact: the payload
// must be in the route's syntax.
func (a *API) receiveRoute(w http.ResponseWriter, r *http.Request, syntax string) {
	env := r.URL.Query().Get("environment")
	if env == "" {
		env = "PROD"
	}
	if env != "TEST" && env != "PROD" {
		httpx.Fail(w, r, validation(map[string]string{"environment": "must be TEST or PROD"}))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInbound+1))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeSyntax, "could not read interchange"))
		return
	}
	res, err := a.receive(r.Context(), body, receiveOptions{Environment: env, Syntax: syntax})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, res)
}

// receiveOptions says where an inbound interchange came from. PartnerID, set
// by the transports, is the partner the transport authenticated: the ISA (or
// UNB) sender must resolve to it, so one partner cannot file interchanges in
// another's name. Source marks the transport message or file as received.
// Syntax, when set (the inbound routes), is the only syntax accepted; empty
// (the transports) detects it from the first segment.
type receiveOptions struct {
	Environment string
	PartnerID   string
	Source      *store.InboundSource
	Syntax      string
}

// receive is the inbound pipeline. It tells the syntax by the first segment
// (ISA for X12, UNA or UNB for EDIFACT) and runs the X12 or the EDIFACT
// receipt. Refusals are *httpx.Problem values (a duplicate control number is
// 409 EDI-0102); any other error is a failure worth retrying.
func (a *API) receive(ctx context.Context, body []byte, o receiveOptions) (receiveResponse, error) {
	if len(body) > maxInbound {
		return receiveResponse{}, httpx.NewProblem(http.StatusRequestEntityTooLarge, CodeSyntax, "interchange exceeds 4 MiB")
	}
	syntax := domain.DetectSyntax(body)
	switch {
	case syntax == "":
		return receiveResponse{}, httpx.NewProblem(http.StatusBadRequest, CodeSyntax, "payload does not start with an ISA, UNA or UNB segment")
	case o.Syntax != "" && syntax != o.Syntax:
		return receiveResponse{}, httpx.NewProblem(http.StatusBadRequest, CodeSyntax, "payload is "+syntax+", not "+o.Syntax)
	case syntax == domain.SyntaxEdifact:
		return a.receiveEdifactInterchange(ctx, body, o)
	}
	return a.receiveX12Interchange(ctx, body, o)
}

// receiveX12Interchange is the inbound X12 pipeline: parse, resolve the
// partner by ISA sender, map each transaction (997s are matched to outbound
// interchanges), and record everything with the 997 answer in one
// transaction.
func (a *API) receiveX12Interchange(ctx context.Context, body []byte, o receiveOptions) (receiveResponse, error) {
	ic, err := domain.Parse(string(body))
	if err != nil {
		return receiveResponse{}, httpx.NewProblem(http.StatusBadRequest, CodeSyntax, err.Error())
	}
	partner, err := a.store.FindPartnerBySender(ctx, ic.Sender, o.Environment)
	if errors.Is(err, store.ErrNotFound) {
		return receiveResponse{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeUnknownSender,
			fmt.Sprintf("interchange sender %s/%s is not configured for any partner", ic.Sender.Qualifier, ic.Sender.ID))
	}
	if err != nil {
		return receiveResponse{}, err
	}
	if o.PartnerID != "" && partner.ID != o.PartnerID {
		return receiveResponse{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeUnknownSender,
			fmt.Sprintf("interchange sender %s/%s is not the partner this transport belongs to", ic.Sender.Qualifier, ic.Sender.ID))
	}

	var (
		groups   []groupOutcomes
		all      []domain.TxOutcome
		messages []store.Message
	)
	// A TA1 is matched to the outbound interchange it answers and is never
	// acknowledged itself.
	for _, ta1 := range ic.TA1 {
		m, err := ta1Message(ta1)
		if err != nil {
			return receiveResponse{}, err
		}
		messages = append(messages, m)
	}
	for _, g := range ic.Groups {
		// A 997 group is matched to the outbound document it answers and is
		// never acknowledged itself.
		if g.FunctionalID == "FA" {
			for _, t := range g.Transactions {
				m, err := ackMessage(t)
				if err != nil {
					return receiveResponse{}, err
				}
				messages = append(messages, m)
			}
			continue
		}
		var gOut []domain.TxOutcome
		for _, t := range g.Transactions {
			res, err := mapTransaction(t)
			if err != nil {
				return receiveResponse{}, err
			}
			gOut = append(gOut, res.outcome)
			m := store.Message{ID: ids.New(), TransactionSet: t.SetID, STControl: t.Control,
				Status: res.status, Canonical: res.canonical, Error: res.errText}
			messages = append(messages, m)
		}
		if len(gOut) > 0 {
			groups = append(groups, groupOutcomes{group: g, outcomes: gOut})
			all = append(all, gOut...)
		}
	}
	if len(messages) == 0 {
		return receiveResponse{}, httpx.NewProblem(http.StatusBadRequest, CodeSyntax, "interchange carries no transactions")
	}
	// An interchange of 997s or TA1s only is accepted; the store downgrades it
	// when one cannot be read or matched.
	status := domain.StatusAccepted
	if len(all) > 0 {
		status = domain.SummarizeOutcomes(all)
	}

	at := a.now().UTC()
	buildAck := func(next func() (int64, error)) (string, error) {
		parts := make([]string, 0, len(groups))
		for _, gr := range groups {
			n, err := next()
			if err != nil {
				return "", err
			}
			ack, err := domain.Ack997(ic, gr.group, gr.outcomes, fmt.Sprintf("%09d", n), strconv.FormatInt(n, 10), at)
			if err != nil {
				return "", err
			}
			raw, err := domain.Serialize(ack)
			if err != nil {
				return "", err
			}
			parts = append(parts, raw)
		}
		return strings.Join(parts, "\n"), nil
	}

	res, err := a.store.ReceiveInterchange(ctx, store.Inbound{
		PartnerID:  partner.ID,
		Version:    ic.Version,
		ISAControl: ic.Control,
		Raw:        string(body),
		AckStatus:  string(status),
		Messages:   messages,
		BuildAck:   buildAck,
		Source:     o.Source,
	})
	if errors.Is(err, store.ErrDuplicate) {
		return receiveResponse{}, httpx.NewProblem(http.StatusConflict, CodeDuplicateControl,
			fmt.Sprintf("duplicate interchange control number %s inside the duplicate window; TA1 025", ic.Control))
	}
	if err != nil {
		return receiveResponse{}, err
	}
	return receiveResponse{
		InterchangeID: res.InterchangeID,
		AckStatus:     res.AckStatus,
		Messages:      messageViews(res.Messages),
		Ack997:        res.AckPayload,
	}, nil
}

// ackMessage reads one received 997. A 997 that cannot be read is recorded as
// FAILED with the syntax error; a readable one carries its report for the
// store to match.
func ackMessage(t domain.Transaction) (store.Message, error) {
	m := store.Message{ID: ids.New(), TransactionSet: t.SetID, STControl: t.Control, Status: "RECEIVED"}
	ack, err := domain.Parse997(t)
	if err != nil {
		m.Status, m.Error = "FAILED", err.Error()
		return m, nil
	}
	canonical, err := json.Marshal(ack)
	if err != nil {
		return store.Message{}, err
	}
	m.Ack, m.Canonical = &ack, canonical
	return m, nil
}

// ta1Message records one received TA1 (TransactionSet TA1, STControl the
// TA101 it answers) for the store to match.
func ta1Message(ta1 domain.InterchangeAck) (store.Message, error) {
	canonical, err := json.Marshal(ta1)
	if err != nil {
		return store.Message{}, err
	}
	a := ta1
	return store.Message{ID: ids.New(), TransactionSet: "TA1", STControl: ta1.Control, Status: "RECEIVED", Canonical: canonical, TA1: &a}, nil
}

type outboundRequest struct {
	TransactionSet string          `json:"transactionSet"`
	Document       json.RawMessage `json:"document"`
}

// createOutbound builds an 855, 856, 810, 820 or 214, or an EDIFACT ORDERS,
// DESADV or INVOIC, from a caller-supplied canonical document. It is mounted
// only when AllowBodyOutbound is set (tests and development); production
// documents come from outboundFromSource.
func (a *API) createOutbound(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 255 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeIdempotencyKey, "Idempotency-Key header is required (max 255 characters)"))
		return
	}
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	var req outboundRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.sendOutbound(w, r, key, id, req.TransactionSet, req.Document)
}

// sendOutbound builds the transaction (or, for ORDERS, DESADV and INVOIC,
// the EDIFACT message) for a canonical document, wraps it in an interchange
// to the partner with the next gap-free control number of the standard and
// records it with ack_status PENDING until the partner's 997 or CONTRL is
// matched. An EDIFACT document needs the partner's UNB identity (409
// EDI-0017); it goes from EdifactSender in a UNOC syntax 3 interchange that
// requests a CONTRL. When the partner has an AS2 or SFTP transport the
// interchange is queued for delivery in the same transaction (the response
// carries the delivery); the message stays MAPPED until the transport
// confirms receipt and then becomes SENT.
func (a *API) sendOutbound(w http.ResponseWriter, r *http.Request, key, partnerID, setID string, document json.RawMessage) {
	res, created, err := a.recordOutbound(r.Context(), key, partnerID, setID, document)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, res)
}

// recordOutbound builds the interchange for one canonical document, numbers it from the partner's counter and records it (and
// queues its delivery) in one transaction. It is what the routes and the event consumer share; problems come back as *httpx.Problem.
func (a *API) recordOutbound(ctx context.Context, key, partnerID, setID string, document json.RawMessage) (store.OutboundResult, bool, error) {
	edifact := domain.IsEdifactOutbound(setID)
	if domain.FunctionalIDOf(setID) == "" && !edifact {
		return store.OutboundResult{}, false, validation(map[string]string{"transactionSet": "must be " + strings.Join(domain.OutboundSets, ", ") +
			" or " + strings.Join(domain.EdifactOutboundTypes, ", ")})
	}
	var (
		tr        domain.Transaction
		msg       domain.EdifactMessage
		canonical []byte
		err       error
	)
	if edifact {
		msg, canonical, err = domain.BuildEdifactOutbound(setID, document)
	} else {
		tr, canonical, err = domain.BuildOutbound(setID, document)
	}
	if errors.Is(err, domain.ErrOutbound) {
		return store.OutboundResult{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeOutbound, err.Error())
	}
	if err != nil {
		return store.OutboundResult{}, false, err
	}
	partner, err := a.store.GetPartner(ctx, partnerID)
	if errors.Is(err, store.ErrNotFound) {
		return store.OutboundResult{}, false, notFound("partner")
	}
	if err != nil {
		return store.OutboundResult{}, false, err
	}
	if partner.Status != "active" {
		return store.OutboundResult{}, false, httpx.NewProblem(http.StatusConflict, CodePartnerInactive, "partner is inactive; no documents can be sent to it")
	}
	at := a.now()
	out := store.Outbound{PartnerID: partner.ID, TransactionSet: setID, Canonical: canonical}
	if edifact {
		if partner.Edifact() == nil {
			return store.OutboundResult{}, false, httpx.NewProblem(http.StatusConflict, CodeNoEdifactID, "partner has no EDIFACT identity (edifactId); "+setID+" cannot be addressed to it")
		}
		to := domain.EdifactParty{ID: partner.EdifactID, Qualifier: partner.EdifactQualifier}
		out.Standard = domain.SyntaxEdifact
		out.Build = func(control int64) (string, error) {
			ic, err := domain.EdifactOutbound([]domain.EdifactMessage{msg}, a.EdifactSender, to, control, partner.Environment == "TEST", at)
			if err != nil {
				return "", err
			}
			return domain.SerializeEdifact(ic, false)
		}
	} else {
		usage := "P"
		if partner.Environment == "TEST" {
			usage = "T"
		}
		to := domain.Party{Qualifier: partner.ISAQualifier, ID: partner.ISAID}
		out.Build = func(control int64) (string, error) {
			ic, err := domain.OutboundEnvelope(tr, a.Sender, to, usage, control, at)
			if err != nil {
				return "", err
			}
			return domain.Serialize(ic)
		}
	}
	sum := sha256.Sum256(append([]byte("outbound\x00"+partner.ID+"\x00"+setID+"\x00"), canonical...))
	res, created, err := a.store.CreateOutbound(ctx, key, hex.EncodeToString(sum[:]), out)
	if errors.Is(err, store.ErrIdempotencyReuse) {
		return store.OutboundResult{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	if errors.Is(err, domain.ErrSyntax) || errors.Is(err, domain.ErrEdifactSyntax) {
		return store.OutboundResult{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeOutbound, err.Error())
	}
	return res, created, err
}
