package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/ebics"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

// Duties of the EBICS onboarding. Writing a subscriber and running the key
// ceremony steps are separate from confirming the bank's key hashes, so the
// person who started the ceremony is not the one who activates the subscriber.
const (
	dutyEBICSWrite   = "treasury.ebics.write"
	dutyEBICSKeys    = "treasury.ebics.keys"
	dutyEBICSApprove = "treasury.ebics.approve"

	CodeEBICSState        = "TRS-0498"
	CodeEBICSHashMismatch = "TRS-0499"
	CodeEBICSParked       = "TRS-0500"
	CodeEBICSBank         = "TRS-0501"
	CodeEBICSOff          = "TRS-0502"
	CodeEBICSFourEyes     = "TRS-0503"
)

// EBICS is the subscriber side the onboarding routes drive: the subscriber
// store and the H004 client.
type EBICS interface {
	CreateEBICSSubscriber(ctx context.Context, in store.NewEBICSSubscriber) (ebics.Subscriber, error)
	GetSubscriber(ctx context.Context, id string) (ebics.Subscriber, error)
	SendINI(ctx context.Context, id string) (ebics.Letter, error)
	SendHIA(ctx context.Context, id string) (ebics.Letter, error)
	Letter(ctx context.Context, id, order string) (ebics.Letter, error)
	FetchBankKeys(ctx context.Context, id, actor string) (ebics.BankKeyHashes, error)
	ConfirmBankKeys(ctx context.Context, id, x002Hash, e002Hash, confirmedBy string) (ebics.Subscriber, error)
	Unpark(ctx context.Context, id, actor string) (ebics.Subscriber, error)
}

// WithEBICS enables the EBICS subscriber routes; without it they answer 503.
func (a *API) WithEBICS(e EBICS) *API {
	a.ebics = e
	return a
}

func (a *API) registerEBICS(r chi.Router) {
	r.With(authz.RequireDuty(dutyEBICSWrite)).Post("/v1/treasury/ebics-subscribers", a.createSubscriber)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/ebics-subscribers/{id}", a.getSubscriber)
	r.With(authz.RequireDuty(dutyEBICSKeys)).Post("/v1/treasury/ebics-subscribers/{id}:send-ini", a.sendLetterOrder("INI"))
	r.With(authz.RequireDuty(dutyEBICSKeys)).Post("/v1/treasury/ebics-subscribers/{id}:send-hia", a.sendLetterOrder("HIA"))
	r.With(authz.RequireDuty(dutyEBICSKeys)).Post("/v1/treasury/ebics-subscribers/{id}:fetch-bank-keys", a.fetchBankKeys)
	r.With(authz.RequireDuty(dutyEBICSApprove)).Post("/v1/treasury/ebics-subscribers/{id}:confirm-bank-keys", a.confirmBankKeys)
	r.With(authz.RequireDuty(dutyEBICSApprove)).Post("/v1/treasury/ebics-subscribers/{id}:unpark", a.unparkSubscriber)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/ebics-subscribers/{id}/letters/{order}", a.getLetter)
}

type createSubscriberRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	BankAccountID string `json:"bankAccountId"`
	HostID        string `json:"hostId"`
	PartnerID     string `json:"partnerId"`
	UserID        string `json:"userId"`
	URL           string `json:"url"`
}

type bankKeyHashesView struct {
	X002 string `json:"x002"`
	E002 string `json:"e002"`
}

type subscriberView struct {
	ID            string             `json:"id"`
	LegalEntityID string             `json:"legalEntityId"`
	BankAccountID string             `json:"bankAccountId"`
	HostID        string             `json:"hostId"`
	PartnerID     string             `json:"partnerId"`
	UserID        string             `json:"userId"`
	URL           string             `json:"url"`
	State         string             `json:"state"`
	KeysConfirmed bool               `json:"keysConfirmed"`
	ConfirmedBy   string             `json:"confirmedBy,omitempty"`
	ConfirmedAt   *time.Time         `json:"confirmedAt,omitempty"`
	ParkedCode    string             `json:"parkedCode,omitempty"`
	ParkedText    string             `json:"parkedText,omitempty"`
	BankKeyHashes *bankKeyHashesView `json:"bankKeyHashes,omitempty"`
	Version       int                `json:"version"`
}

type letterView struct {
	Order  string            `json:"order"`
	Text   string            `json:"text"`
	Hashes map[string]string `json:"hashes"`
}

type letterOrderResponse struct {
	Subscriber *subscriberView `json:"subscriber,omitempty"`
	Letter     letterView      `json:"letter"`
}

type confirmBankKeysRequest struct {
	X002Hash string `json:"x002Hash"`
	E002Hash string `json:"e002Hash"`
}

func viewOf(s ebics.Subscriber) subscriberView {
	v := subscriberView{ID: s.ID, LegalEntityID: s.LegalEntityID, BankAccountID: s.BankAccountID, HostID: s.HostID, PartnerID: s.PartnerID,
		UserID: s.UserID, URL: s.URL, State: string(s.State), KeysConfirmed: s.KeysConfirmed, ConfirmedBy: s.ConfirmedBy, ConfirmedAt: s.ConfirmedAt,
		ParkedCode: s.ParkedCode, ParkedText: s.ParkedText, Version: s.Version}
	if h, ok := s.BankHashes(); ok {
		v.BankKeyHashes = &bankKeyHashesView{X002: h.X002, E002: h.E002}
	}
	return v
}

func letterOf(order string, l ebics.Letter) letterView {
	return letterView{Order: order, Text: l.Text, Hashes: l.Hashes}
}

// ebicsProblem maps the client's errors to problem details; others pass through.
func ebicsProblem(err error) error {
	var re *ebics.ReturnError
	switch {
	case errors.Is(err, ebics.ErrBadState):
		return httpx.NewProblem(http.StatusConflict, CodeEBICSState, err.Error())
	case errors.Is(err, ebics.ErrFourEyes):
		return httpx.NewProblem(http.StatusForbidden, CodeEBICSFourEyes, "the person who downloaded the bank keys cannot also confirm them; another person must compare the hashes")
	case errors.Is(err, ebics.ErrKeyHashMismatch):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeEBICSHashMismatch, "the hashes do not match the bank keys that were downloaded; do not activate")
	case errors.Is(err, ebics.ErrParked):
		return httpx.NewProblem(http.StatusConflict, CodeEBICSParked, "the subscriber is parked after a bank refusal; a person must review it and unpark it")
	case errors.Is(err, ebics.ErrVersionConflict):
		return httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "the subscriber was changed by someone else; read it again")
	case errors.As(err, &re):
		return httpx.NewProblem(http.StatusBadGateway, CodeEBICSBank, "the bank refused the order: "+re.Code+" "+re.Text)
	}
	return problemFrom(err)
}

func (a *API) ebicsReady(w http.ResponseWriter, r *http.Request) bool {
	if !validTenant(w, r) {
		return false
	}
	if a.ebics == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeEBICSOff, "EBICS is not enabled on this instance"))
		return false
	}
	return true
}

func subscriberID(w http.ResponseWriter, r *http.Request) (string, bool) {
	return pathID(w, r, store.CodeSubscriberNotFound, "EBICS subscriber not found")
}

func (a *API) respondSubscriber(w http.ResponseWriter, r *http.Request, status int, s ebics.Subscriber) {
	setETag(w, s.Version)
	httpx.JSON(w, status, viewOf(s))
}

func (a *API) createSubscriber(w http.ResponseWriter, r *http.Request) {
	if !a.ebicsReady(w, r) {
		return
	}
	var req createSubscriberRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.BankAccountID) {
		fields["bankAccountId"] = "must be a UUID"
	}
	for name, v := range map[string]string{"hostId": req.HostID, "partnerId": req.PartnerID, "userId": req.UserID, "url": req.URL} {
		if strings.TrimSpace(v) == "" || len(v) > 512 {
			fields[name] = "required, at most 512 characters"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if outsideScope(w, r, req.LegalEntityID) {
		return
	}
	sub, err := a.ebics.CreateEBICSSubscriber(r.Context(), store.NewEBICSSubscriber{LegalEntityID: req.LegalEntityID, BankAccountID: req.BankAccountID,
		HostID: req.HostID, PartnerID: req.PartnerID, UserID: req.UserID, URL: req.URL})
	if err != nil {
		httpx.Fail(w, r, ebicsProblem(err))
		return
	}
	a.respondSubscriber(w, r, http.StatusCreated, sub)
}

func (a *API) getSubscriber(w http.ResponseWriter, r *http.Request) {
	if !a.ebicsReady(w, r) {
		return
	}
	id, ok := subscriberID(w, r)
	if !ok {
		return
	}
	sub, err := a.ebics.GetSubscriber(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, ebicsProblem(err))
		return
	}
	a.respondSubscriber(w, r, http.StatusOK, sub)
}

// sendLetterOrder serves :send-ini and :send-hia: the order goes to the bank and
// the response carries the letter a person prints, signs and posts.
func (a *API) sendLetterOrder(order string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.ebicsReady(w, r) {
			return
		}
		id, ok := subscriberID(w, r)
		if !ok {
			return
		}
		send := a.ebics.SendINI
		if order == "HIA" {
			send = a.ebics.SendHIA
		}
		l, err := send(r.Context(), id)
		if err != nil {
			httpx.Fail(w, r, ebicsProblem(err))
			return
		}
		out := letterOrderResponse{Letter: letterOf(order, l)}
		if sub, err := a.ebics.GetSubscriber(r.Context(), id); err == nil {
			setETag(w, sub.Version)
			v := viewOf(sub)
			out.Subscriber = &v
		} else {
			slog.WarnContext(r.Context(), "ebics order sent but the subscriber could not be read back", "subscriber", id, "order", order, "err", err)
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

func (a *API) fetchBankKeys(w http.ResponseWriter, r *http.Request) {
	if !a.ebicsReady(w, r) {
		return
	}
	id, ok := subscriberID(w, r)
	if !ok {
		return
	}
	user, ok := namedUser(w, r)
	if !ok {
		return
	}
	if _, err := a.ebics.FetchBankKeys(r.Context(), id, user); err != nil {
		httpx.Fail(w, r, ebicsProblem(err))
		return
	}
	sub, err := a.ebics.GetSubscriber(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, ebicsProblem(err))
		return
	}
	a.respondSubscriber(w, r, http.StatusOK, sub)
}

// namedUser returns the authenticated person; the confirmer and the person who
// unparks are never taken from the request body.
func namedUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, _ := tenant.FromContext(r.Context())
	if strings.TrimSpace(p.UserID) == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "a named user is required for this step"))
		return "", false
	}
	return p.UserID, true
}

func (a *API) confirmBankKeys(w http.ResponseWriter, r *http.Request) {
	if !a.ebicsReady(w, r) {
		return
	}
	id, ok := subscriberID(w, r)
	if !ok {
		return
	}
	user, ok := namedUser(w, r)
	if !ok {
		return
	}
	var req confirmBankKeysRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if strings.TrimSpace(req.X002Hash) == "" || strings.TrimSpace(req.E002Hash) == "" {
		validationFailed(w, r, map[string]string{"x002Hash": "the hash printed on the bank's letter is required", "e002Hash": "the hash printed on the bank's letter is required"})
		return
	}
	sub, err := a.ebics.ConfirmBankKeys(r.Context(), id, req.X002Hash, req.E002Hash, user)
	if err != nil {
		httpx.Fail(w, r, ebicsProblem(err))
		return
	}
	a.respondSubscriber(w, r, http.StatusOK, sub)
}

func (a *API) unparkSubscriber(w http.ResponseWriter, r *http.Request) {
	if !a.ebicsReady(w, r) {
		return
	}
	id, ok := subscriberID(w, r)
	if !ok {
		return
	}
	user, ok := namedUser(w, r)
	if !ok {
		return
	}
	sub, err := a.ebics.Unpark(r.Context(), id, user)
	if err != nil {
		httpx.Fail(w, r, ebicsProblem(err))
		return
	}
	a.respondSubscriber(w, r, http.StatusOK, sub)
}

func (a *API) getLetter(w http.ResponseWriter, r *http.Request) {
	if !a.ebicsReady(w, r) {
		return
	}
	id, ok := subscriberID(w, r)
	if !ok {
		return
	}
	order := chi.URLParam(r, "order")
	if order != "INI" && order != "HIA" {
		validationFailed(w, r, map[string]string{"order": "must be INI or HIA"})
		return
	}
	l, err := a.ebics.Letter(r.Context(), id, order)
	if err != nil {
		httpx.Fail(w, r, ebicsProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, letterOf(order, l))
}
