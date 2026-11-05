// Package api exposes the edi HTTP surface: partner profiles, the dry-run map
// tester, inbound X12 receipt (850s mapped, 997s matched to outbound
// documents), outbound 855/856/810 generation, and the message monitor with
// single and bulk reprocess, the masked raw segment-tree viewer and the
// partner SLA dashboard. Handlers decode, validate,
// check duties, then call the domain and store.
package api

import (
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

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
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

// API holds the store used by the handlers that persist state. Sender is our
// ISA06/GS02 identity on outbound documents.
type API struct {
	store   *store.Store
	now     func() time.Time
	Sender  domain.Party
	Sources Sources
	// AllowBodyOutbound mounts POST /partners/{id}/outbound, which takes the
	// canonical document from the request body. Tests and development only.
	AllowBodyOutbound bool
}

// New returns the API. The store may be nil for routes that do not persist
// (maps:test), which lets those routes be tested without a database.
func New(s *store.Store) *API {
	return &API{store: s, now: time.Now, Sender: DefaultSender}
}

// Routes returns the HTTP handler with kernel middleware and every edi route.
func (a *API) Routes() http.Handler {
	r := httpx.NewRouter()
	r.Route("/edi/v1", func(r chi.Router) {
		r.With(authz.RequireDuty("edi.admin")).Post("/partners", a.createPartner)
		r.With(authz.RequireDuty("edi.admin")).Get("/partners", a.listPartners)
		r.With(authz.RequireDuty("edi.admin")).Get("/partners/{id}", a.getPartner)
		r.With(authz.RequireDuty("edi.admin")).Put("/partners/{id}/trade-profile", a.putTradeProfile)
		r.With(authz.RequireDuty("edi.operate")).Post("/partners/{id}/outbound:from-source", a.outboundFromSource)
		if a.AllowBodyOutbound {
			r.With(authz.RequireDuty("edi.operate")).Post("/partners/{id}/outbound", a.createOutbound)
		}
		r.With(authz.RequireDuty("edi.admin")).Post("/maps:test", a.mapsTest)
		r.With(authz.RequireDuty("edi.operate")).Post("/inbound/x12", a.receiveX12)
		r.With(authz.RequireDuty("edi.operate")).Get("/interchanges", a.listInterchanges)
		r.With(authz.RequireDuty("edi.operate")).Get("/interchanges/{id}", a.getInterchange)
		r.With(authz.RequireDuty("edi.operate")).Get("/interchanges/{id}/payload", a.getPayload)
		r.With(authz.RequireDuty("edi.operate")).Post("/interchanges/{id}:reprocess", a.reprocessInterchange)
		r.With(authz.RequireDuty("edi.operate")).Post("/interchanges:reprocess", a.bulkReprocess)
		r.With(authz.RequireDuty("edi.operate")).Get("/messages", a.listMessages)
		r.With(authz.RequireDuty("edi.operate")).Get("/messages/{id}", a.getMessage)
		r.With(authz.RequireDuty("edi.operate")).Get("/partners/sla", a.partnerSLA)
		r.With(authz.RequireDuty("edi.requisition.create")).Post("/punchout/returns:parse", a.punchoutParse)
		r.With(authz.RequireDuty("edi.self-billing.reconcile")).Post("/self-billing:match", a.selfBillingMatch)
		r.With(authz.RequireDuty("edi.einvoice.operate")).Post("/peppol/invoices:build", a.ublBuild)
	})
	return r
}

type partnerRequest struct {
	Name          string `json:"name"`
	ISAQualifier  string `json:"isaQualifier"`
	ISAID         string `json:"isaId"`
	Environment   string `json:"environment"`
	AckSLAMinutes int    `json:"ackSlaMinutes"`
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
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodePartnerExists, "a partner with this ISA identity already exists in this environment"))
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

// txResult is the outcome of mapping one transaction set.
type txResult struct {
	outcome   domain.TxOutcome
	status    string
	order     *domain.Order
	canonical []byte
	errText   string
	issues    []domain.Issue
}

// mapTransaction maps one transaction and derives its 997 code and message
// status. Only 850 is mapped in this slice; other sets are rejected (AK5 R).
func mapTransaction(t domain.Transaction) (txResult, error) {
	res := txResult{outcome: domain.TxOutcome{SetID: t.SetID, Control: t.Control}}
	if t.SetID != "850" {
		res.outcome.Code = domain.AckRejected
		res.status = "FAILED"
		res.errText = "transaction set " + t.SetID + " is not supported yet"
		return res, nil
	}
	order, issues := domain.Map850(t)
	canonical, err := json.Marshal(order)
	if err != nil {
		return txResult{}, err
	}
	res.order = &order
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
	TransactionSet string         `json:"transactionSet"`
	STControl      string         `json:"stControl"`
	Status         string         `json:"status"`
	Order          *domain.Order  `json:"order,omitempty"`
	Issues         []domain.Issue `json:"issues"`
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
			if mr.Issues == nil {
				mr.Issues = []domain.Issue{}
			}
			if res.order == nil && res.errText != "" {
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
}

type groupOutcomes struct {
	group    domain.Group
	outcomes []domain.TxOutcome
}

// receiveX12 accepts one raw X12 interchange from a partner (the AS2 and SFTP
// transports call this route once they have unwrapped the payload). It
// identifies the partner by ISA sender, records every transaction, and returns
// the 997 text generated in the same transaction.
func (a *API) receiveX12(w http.ResponseWriter, r *http.Request) {
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
	if len(body) > maxInbound {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusRequestEntityTooLarge, CodeSyntax, "interchange exceeds 4 MiB"))
		return
	}
	ic, err := domain.Parse(string(body))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeSyntax, err.Error()))
		return
	}
	partner, err := a.store.FindPartnerBySender(r.Context(), ic.Sender, env)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeUnknownSender,
			fmt.Sprintf("interchange sender %s/%s is not configured for any partner", ic.Sender.Qualifier, ic.Sender.ID)))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	var (
		groups   []groupOutcomes
		all      []domain.TxOutcome
		messages []store.Message
	)
	for _, g := range ic.Groups {
		// A 997 group is matched to the outbound document it answers and is
		// never acknowledged itself.
		if g.FunctionalID == "FA" {
			for _, t := range g.Transactions {
				m, err := ackMessage(t)
				if err != nil {
					httpx.Fail(w, r, err)
					return
				}
				messages = append(messages, m)
			}
			continue
		}
		var gOut []domain.TxOutcome
		for _, t := range g.Transactions {
			res, err := mapTransaction(t)
			if err != nil {
				httpx.Fail(w, r, err)
				return
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
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeSyntax, "interchange carries no transactions"))
		return
	}
	// An interchange of 997s only is accepted; the store downgrades it when a
	// 997 cannot be read or matched.
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

	res, err := a.store.ReceiveInterchange(r.Context(), store.Inbound{
		PartnerID:  partner.ID,
		Version:    ic.Version,
		ISAControl: ic.Control,
		Raw:        string(body),
		AckStatus:  string(status),
		Messages:   messages,
		BuildAck:   buildAck,
	})
	if errors.Is(err, store.ErrDuplicate) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeDuplicateControl,
			fmt.Sprintf("duplicate interchange control number %s inside the duplicate window; TA1 025", ic.Control)))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	views := make([]messageView, 0, len(res.Messages))
	for _, m := range res.Messages {
		views = append(views, messageView{MessageID: m.ID, TransactionSet: m.TransactionSet,
			STControl: m.STControl, Status: m.Status, Error: m.Error})
	}
	httpx.JSON(w, http.StatusCreated, receiveResponse{
		InterchangeID: res.InterchangeID,
		AckStatus:     res.AckStatus,
		Messages:      views,
		Ack997:        res.AckPayload,
	})
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

type outboundRequest struct {
	TransactionSet string          `json:"transactionSet"`
	Document       json.RawMessage `json:"document"`
}

// createOutbound builds an 855, 856, 810, 820 or 214 from a caller-supplied canonical
// document. It is mounted only when AllowBodyOutbound is set (tests and
// development); production documents come from outboundFromSource.
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

// sendOutbound builds the transaction for a canonical document, wraps it in an
// interchange to the partner with the next gap-free control number and records
// it with ack_status PENDING until the partner's 997 is matched. Transmission
// (AS2/SFTP) is not built; the message stays MAPPED.
func (a *API) sendOutbound(w http.ResponseWriter, r *http.Request, key, partnerID, setID string, document json.RawMessage) {
	if domain.FunctionalIDOf(setID) == "" {
		httpx.Fail(w, r, validation(map[string]string{"transactionSet": "must be " + strings.Join(domain.OutboundSets, ", ")}))
		return
	}
	tr, canonical, err := domain.BuildOutbound(setID, document)
	if errors.Is(err, domain.ErrOutbound) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeOutbound, err.Error()))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	partner, err := a.store.GetPartner(r.Context(), partnerID)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if partner.Status != "active" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodePartnerInactive, "partner is inactive; no documents can be sent to it"))
		return
	}
	usage := "P"
	if partner.Environment == "TEST" {
		usage = "T"
	}
	to := domain.Party{Qualifier: partner.ISAQualifier, ID: partner.ISAID}
	at := a.now()
	sum := sha256.Sum256(append([]byte("outbound\x00"+partner.ID+"\x00"+setID+"\x00"), canonical...))
	res, created, err := a.store.CreateOutbound(r.Context(), key, hex.EncodeToString(sum[:]), store.Outbound{
		PartnerID:      partner.ID,
		TransactionSet: setID,
		Canonical:      canonical,
		Build: func(control int64) (string, error) {
			ic, err := domain.OutboundEnvelope(tr, a.Sender, to, usage, control, at)
			if err != nil {
				return "", err
			}
			return domain.Serialize(ic)
		},
	})
	if errors.Is(err, store.ErrIdempotencyReuse) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body"))
		return
	}
	if errors.Is(err, domain.ErrSyntax) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeOutbound, err.Error()))
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
	httpx.JSON(w, status, res)
}
