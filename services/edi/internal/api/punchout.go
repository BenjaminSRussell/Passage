package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/safenet"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

// Punchout sessions (#35, 35-3). POST /punchout/sessions opens a supplier's catalog for the requester: it records the session,
// sends the cXML PunchOutSetupRequest (or builds the OCI URL with HOOK_URL) and returns the URL to send the browser to. The cart
// comes back to POST /punchout/v1/return/{session}?t=<secret> on the public listener, is validated (BR-35.13, EDI-0133/0134) and
// kept on the session; POST /punchout/sessions/{id}:create-requisition turns a returned cart into a DRAFT requisition of the
// requester in purchasing (wave 17). The return URL's secret is the supplier browser's only credential: it is random, stored
// hashed, valid until the session expires and used once.

const (
	CodePunchoutNoURL     = "EDI-0032"
	CodePunchoutSetup     = "EDI-0033"
	CodeRequisitionRefuse = "EDI-0034"
	CodePunchoutState     = "EDI-0035"

	maxPunchoutBody  = 2 << 20
	punchoutSetupTTL = 10 * time.Second
)

// Requisitions creates a requisition in purchasing for the requester in the context. sources.Client implements it.
type Requisitions interface {
	CreateRequisition(ctx context.Context, legalEntityID, key string, body sources.RequisitionRequest) (sources.RequisitionRef, error)
}

type punchoutSupplierBody struct {
	SetupURL         string            `json:"setupUrl"`
	BuyerIdentity    string            `json:"buyerIdentity"`
	SupplierIdentity string            `json:"supplierIdentity"`
	SharedSecretRef  string            `json:"sharedSecretRef"`
	Currencies       []string          `json:"currencies"`
	UoMMap           map[string]string `json:"uomMap"`
	TolerancePct     string            `json:"tolerancePct"`
	ReferencePrices  map[string]string `json:"referencePrices"`
	SessionMinutes   int               `json:"sessionMinutes"`
}

func (a *API) putPunchoutSupplier(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	var body punchoutSupplierBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	sup, supFields := supplierDTO{Currencies: body.Currencies, UoMMap: body.UoMMap, TolerancePct: body.TolerancePct, ReferencePrices: body.ReferencePrices}.toDomain()
	for k, v := range supFields {
		fields[strings.TrimPrefix(k, "supplier.")] = v
	}
	if u, err := url.Parse(body.SetupURL); err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && a.AllowInsecureAS2)) {
		fields["setupUrl"] = "must be an https URL"
	}
	if n := len(strings.TrimSpace(body.BuyerIdentity)); n < 1 || n > 80 {
		fields["buyerIdentity"] = "must be 1 to 80 characters"
	}
	if n := len(strings.TrimSpace(body.SupplierIdentity)); n < 1 || n > 80 {
		fields["supplierIdentity"] = "must be 1 to 80 characters"
	}
	if len(body.Currencies) == 0 {
		fields["currencies"] = "at least one currency is required"
	}
	if body.SessionMinutes == 0 {
		body.SessionMinutes = 60
	}
	if body.SessionMinutes < 5 || body.SessionMinutes > 480 {
		fields["sessionMinutes"] = "must be between 5 and 480"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	p := store.PunchoutSupplier{SetupURL: body.SetupURL, BuyerIdentity: body.BuyerIdentity, SupplierIdentity: body.SupplierIdentity, SharedSecretRef: body.SharedSecretRef,
		Currencies: sup.Currencies, UoMMap: nonNilMap(sup.UoMMap), TolerancePct: sup.TolerancePct, ReferencePrices: nonNilPrices(sup.ReferencePrices), SessionMinutes: body.SessionMinutes}
	if _, err := a.store.GetPartner(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("partner"))
		return
	} else if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.store.PutPunchoutSupplier(r.Context(), id, p); errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNoTradeProfile, "partner has no trade profile; PUT /partners/{id}/trade-profile first"))
		return
	} else if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	saved, err := a.store.GetPunchoutSupplier(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func nonNilPrices(m map[string]decimal.Decimal) map[string]decimal.Decimal {
	if m == nil {
		return map[string]decimal.Decimal{}
	}
	return m
}

func (a *API) getPunchoutSupplier(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	p, err := a.store.GetPunchoutSupplier(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("punchout settings"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func (a *API) punchoutClient() *http.Client {
	if a.PunchoutHTTP != nil {
		return a.PunchoutHTTP
	}
	// The supplier's and the network's URLs come from partner settings: only public addresses are dialed (safenet).
	return safenet.Client(punchoutSetupTTL, a.AllowInsecureAS2)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

type punchoutSessionRequest struct {
	PartnerID string `json:"partnerId"`
	Format    string `json:"format"`
}

func callerUser(r *http.Request) string {
	p, _ := tenant.FromContext(r.Context())
	return p.UserID
}

// createPunchoutSession opens a supplier's catalog for the caller.
func (a *API) createPunchoutSession(w http.ResponseWriter, r *http.Request) {
	var req punchoutSessionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Format = strings.ToUpper(strings.TrimSpace(req.Format))
	fields := map[string]string{}
	if !validID(req.PartnerID) {
		fields["partnerId"] = "must be a UUID"
	}
	if req.Format == "" {
		req.Format = "CXML"
	}
	if req.Format != "CXML" && req.Format != "OCI" {
		fields["format"] = "must be CXML or OCI"
	}
	user := callerUser(r)
	if !validID(user) {
		fields["requester"] = "a user is required"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	if a.AS2PublicURL == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodePunchoutNoURL, "the public URL the supplier returns the cart to is not configured (EDI_AS2_PUBLIC_URL)"))
		return
	}
	sup, err := a.store.GetPunchoutSupplier(r.Context(), req.PartnerID)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("punchout settings of the partner"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if p, err := a.store.GetPartner(r.Context(), req.PartnerID); err != nil || p.Status != "active" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodePartnerInactive, "partner is inactive or unknown"))
		return
	}
	id, secret, cookie := ids.New(), "", ""
	if secret, err = randomHex(24); err == nil {
		cookie, err = randomHex(16)
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	returnURL := strings.TrimRight(a.AS2PublicURL, "/") + "/punchout/v1/return/" + id + "?t=" + secret
	startURL := ""
	switch req.Format {
	case "OCI":
		startURL, err = domain.OCIStartURL(sup.SetupURL, returnURL)
		if err != nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodePunchoutSetup, err.Error()))
			return
		}
	default:
		startURL, err = a.punchoutSetup(r.Context(), sup, cookie, returnURL)
		if err != nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodePunchoutSetup, err.Error()))
			return
		}
	}
	sess := store.PunchoutSession{ID: id, PartnerID: req.PartnerID, LegalEntityID: sup.LegalEntityID, RequesterID: user, Format: req.Format,
		StartURL: startURL, ExpiresAt: a.now().UTC().Add(time.Duration(sup.SessionMinutes) * time.Minute), SecretHash: hashSecret(secret), BuyerCookie: cookie}
	if err := a.store.CreatePunchoutSession(r.Context(), sess); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	created, err := a.store.GetPunchoutSession(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, created)
}

// punchoutSetup sends the cXML PunchOutSetupRequest and returns the supplier's StartPage URL.
func (a *API) punchoutSetup(ctx context.Context, sup store.PunchoutSupplier, cookie, returnURL string) (string, error) {
	secret := ""
	if sup.SharedSecretRef != "" {
		if a.Secrets == nil {
			return "", errors.New("the supplier's shared secret cannot be read: no secrets are mounted")
		}
		raw, err := a.Secrets.Read(sup.SharedSecretRef)
		if err != nil {
			return "", errors.New("the supplier's shared secret cannot be read")
		}
		secret = strings.TrimSpace(string(raw))
	}
	doc := domain.BuildPunchOutSetupRequest(ids.New(), a.now().UTC().Format(time.RFC3339), sup.BuyerIdentity, sup.SupplierIdentity, secret, cookie, returnURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sup.SetupURL, strings.NewReader(doc))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "text/xml; charset=UTF-8")
	resp, err := a.punchoutClient().Do(req)
	if err != nil {
		slog.WarnContext(ctx, "punchout setup transport failed", "partner", sup.PartnerID, "err", err)
		return "", errors.New("the supplier could not be reached")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", errors.New("the supplier's answer could not be read")
	}
	return domain.ParsePunchOutSetupResponse(raw)
}

func (a *API) ownSession(w http.ResponseWriter, r *http.Request) (store.PunchoutSession, bool) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("punchout session"))
		return store.PunchoutSession{}, false
	}
	s, err := a.store.GetPunchoutSession(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && s.RequesterID != callerUser(r)) {
		httpx.Fail(w, r, notFound("punchout session"))
		return s, false
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return s, false
	}
	return s, true
}

func (a *API) getPunchoutSession(w http.ResponseWriter, r *http.Request) {
	if s, ok := a.ownSession(w, r); ok {
		httpx.JSON(w, http.StatusOK, s)
	}
}

func (a *API) listPunchoutSessions(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListPunchoutSessions(r.Context(), callerUser(r), listLimit(r))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// listPunchoutSuppliers lists the suppliers whose punchout catalog the caller can open (#35, 35-3): what the web screen offers.
func (a *API) listPunchoutSuppliers(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListPunchoutSuppliers(r.Context(), listLimit(r))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) cancelPunchoutSession(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	out, err := a.store.CancelPunchoutSession(r.Context(), s.ID)
	if errors.Is(err, store.ErrPunchoutState) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodePunchoutState, "only an open session can be cancelled"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// punchoutReturn takes the cart back from the supplier's page (the requester's browser posts it) or from the supplier's server. It
// is on the public listener and needs no Passage identity: the session id in the path and the secret in the query select and open one
// session. cXML arrives as the raw body or in the cxml-urlencoded or cxml-base64 field of a form post; OCI as NEW_ITEM-... fields.
func (a *API) punchoutReturn(w http.ResponseWriter, r *http.Request) {
	sid, token := chi.URLParam(r, "session"), r.URL.Query().Get("t")
	if !validID(sid) || token == "" || len(token) > 200 {
		http.NotFound(w, r)
		return
	}
	tid, _, err := a.store.ResolvePublicEndpoint(r.Context(), "PUNCHOUT_RETURN", sid)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("punchout endpoint lookup", "err", err)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	sess, err := a.store.GetPunchoutSession(store.SystemContext(r.Context(), tid), sid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.Error("punchout session lookup", "session", sid, "err", err)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if subtle.ConstantTimeCompare([]byte(hashSecret(token)), []byte(sess.SecretHash)) != 1 {
		http.NotFound(w, r)
		return
	}
	ctx := store.EntityContext(r.Context(), tid, sess.LegalEntityID)

	r.Body = http.MaxBytesReader(w, r.Body, maxPunchoutBody)
	cart, perr := a.readReturnedCart(r, sess)
	if perr != nil {
		http.Error(w, perr.Error(), http.StatusBadRequest)
		return
	}
	sup, err := a.store.GetPunchoutSupplier(ctx, sess.PartnerID)
	if err != nil {
		slog.Error("punchout supplier lookup", "session", sid, "err", err)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	res := domain.ValidatePunchout(cart, sup.Domain())
	if res.Lines == nil {
		res.Lines = []domain.CartLine{}
	}
	if res.Issues == nil {
		res.Issues = []domain.Issue{}
	}
	if _, err := a.store.ReturnPunchoutCart(ctx, sid, res, a.now().UTC()); err != nil {
		switch {
		case errors.Is(err, store.ErrPunchoutExpired):
			http.Error(w, "the punchout session expired; start a new one", http.StatusGone)
		case errors.Is(err, store.ErrPunchoutState):
			http.Error(w, "the cart of this session was already returned or the session was cancelled", http.StatusConflict)
		default:
			slog.Error("punchout cart store", "session", sid, "err", err)
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if res.CartRejected {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = fmt.Fprintf(w, "The cart was not accepted: its total differs from its lines (%s). Return to Passage and try again.\n", domain.CodePunchoutTotal)
		return
	}
	_, _ = fmt.Fprintf(w, "Cart received with %d line(s). Return to Passage to continue.\n", len(res.Lines))
}

func (a *API) readReturnedCart(r *http.Request, sess store.PunchoutSession) (domain.RawCart, error) {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var doc []byte
	fields := map[string]string{}
	if mt == "application/x-www-form-urlencoded" {
		if err := r.ParseForm(); err != nil {
			return domain.RawCart{}, errors.New("the form post could not be read")
		}
		for k, v := range r.PostForm {
			if len(v) > 0 {
				fields[k] = v[0]
			}
		}
		switch {
		case fields["cxml-urlencoded"] != "":
			doc = []byte(fields["cxml-urlencoded"])
		case fields["cxml-base64"] != "":
			raw, err := base64.StdEncoding.DecodeString(fields["cxml-base64"])
			if err != nil {
				return domain.RawCart{}, errors.New("cxml-base64 is not base64")
			}
			doc = raw
		}
	} else if sess.Format == "CXML" {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return domain.RawCart{}, errors.New("the cart is larger than 2 MiB")
		}
		doc = raw
	}
	if sess.Format == "OCI" {
		if len(fields) == 0 {
			return domain.RawCart{}, errors.New("an OCI cart is a form post of NEW_ITEM fields")
		}
		return domain.ParsePunchoutOCI(fields)
	}
	if len(bytes.TrimSpace(doc)) == 0 {
		return domain.RawCart{}, errors.New("a cXML cart is the PunchOutOrderMessage")
	}
	cookie, err := domain.PunchOutBuyerCookie(doc)
	if err != nil {
		return domain.RawCart{}, err
	}
	if subtle.ConstantTimeCompare([]byte(cookie), []byte(sess.BuyerCookie)) != 1 {
		return domain.RawCart{}, errors.New("the BuyerCookie is not the session's")
	}
	return domain.ParsePunchoutCXML(doc)
}

// createRequisitionFromSession turns a RETURNED cart into a DRAFT requisition of the requester in purchasing. Idempotent per
// session: a repeat returns the requisition already created.
func (a *API) createRequisitionFromSession(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownSession(w, r)
	if !ok {
		return
	}
	if s.RequisitionID != "" {
		httpx.JSON(w, http.StatusOK, map[string]string{"sessionId": s.ID, "requisitionId": s.RequisitionID})
		return
	}
	if s.Status != "RETURNED" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodePunchoutState, "only a session whose cart was returned and accepted becomes a requisition"))
		return
	}
	if a.Requisitions == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeSourceDown, "purchasing is not configured"))
		return
	}
	var cart domain.CartResult
	if err := json.Unmarshal(s.Cart, &cart); err != nil || len(cart.Lines) == 0 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeRequisitionRefuse, "the returned cart has no lines to order"))
		return
	}
	body := sources.RequisitionRequest{LegalEntityID: s.LegalEntityID, Currency: cart.Lines[0].Currency}
	today := a.now().UTC()
	for _, l := range cart.Lines {
		if l.Currency != body.Currency {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeRequisitionRefuse, "the cart mixes currencies; a requisition has one"))
			return
		}
		desc := l.Description
		if desc == "" {
			desc = l.SupplierPartID
		}
		category := l.UNSPSC
		if category == "" {
			category = "PUNCHOUT"
		}
		body.Lines = append(body.Lines, sources.RequisitionLine{Description: desc, Category: category, Qty: l.Quantity.String(), Unit: l.UnitCode,
			EstimatedUnitPrice: l.UnitPrice.String(), RequestedDate: today.AddDate(0, 0, l.LeadTimeDays).Format("2006-01-02"),
			CatalogSource: "PUNCHOUT", SupplierPartID: partID(l.SupplierPartID), NeedsApproval: l.NeedsApproval})
	}
	ref, err := a.Requisitions.CreateRequisition(r.Context(), s.LegalEntityID, "punchout:"+s.ID, body)
	var refused *sources.RequisitionRefusedError
	switch {
	case errors.As(err, &refused):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeRequisitionRefuse, refused.Error()))
		return
	case errors.Is(err, sources.ErrForbidden):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, CodeSourceForbidden, "the caller may not create requisitions in this legal entity"))
		return
	case err != nil:
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeSourceDown, "purchasing could not create the requisition"))
		return
	}
	if err := a.store.SetPunchoutRequisition(r.Context(), s.ID, ref.ID); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]string{"sessionId": s.ID, "requisitionId": ref.ID})
}

// partID is the supplier part id purchasing keeps (at most 100 characters); a longer one is left out rather than refused.
func partID(id string) string {
	if len(id) > 100 {
		return ""
	}
	return id
}
