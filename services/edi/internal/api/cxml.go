package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

// The cXML inbound exchange (#35, 35-4). A partner on a cXML network (Ariba, Coupa) posts ConfirmationRequest, ShipNoticeRequest and
// InvoiceDetailRequest documents to its own URL on the public listener, POST /cxml/v1/{endpoint}. The caller is authenticated by the
// network identity and shared secret in the header's Sender credential against the partner's profile (PUT /partners/{id}/cxml);
// nothing is parsed beyond the envelope before that. Every answer is a cXML Response with a Status code (200 accepted, 400 for a
// document or document type that cannot be taken, 401 for bad credentials, 409 for a payloadID reused with other content, EDI-0135).
// A payloadID seen before with the same content gets the first answer back and records nothing (BR-35.14).

const maxCXMLBody = 4 << 20

type cxmlConfigBody struct {
	NetworkIdentity string `json:"networkIdentity"`
	SharedSecret    string `json:"sharedSecret"`
}

type cxmlConfigView struct {
	PartnerID       string `json:"partnerId"`
	EndpointID      string `json:"endpointId"`
	URL             string `json:"url,omitempty"`
	NetworkIdentity string `json:"networkIdentity"`
}

func (a *API) cxmlView(c store.PartnerCXML) cxmlConfigView {
	v := cxmlConfigView{PartnerID: c.PartnerID, EndpointID: c.EndpointID, NetworkIdentity: c.NetworkIdentity}
	if a.AS2PublicURL != "" {
		v.URL = strings.TrimRight(a.AS2PublicURL, "/") + "/cxml/v1/" + c.EndpointID
	}
	return v
}

// putPartnerCXML sets the partner's network identity and shared secret (write-only: only its hash is kept) and returns the URL the
// partner posts to. The URL stays the same when the secret is rotated.
func (a *API) putPartnerCXML(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	var body cxmlConfigBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if n := len(strings.TrimSpace(body.NetworkIdentity)); n < 1 || n > 80 {
		fields["networkIdentity"] = "must be 1 to 80 characters"
	}
	if n := len(body.SharedSecret); n < 16 || n > 200 {
		fields["sharedSecret"] = "must be 16 to 200 characters"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	if _, err := a.store.GetPartner(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("partner"))
		return
	} else if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	saved, err := a.store.PutPartnerCXML(r.Context(), id, strings.TrimSpace(body.NetworkIdentity), hashSecret(body.SharedSecret))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNoTradeProfile, "partner has no trade profile; PUT /partners/{id}/trade-profile first"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, a.cxmlView(saved))
}

func (a *API) getPartnerCXML(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	c, err := a.store.GetPartnerCXML(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("cXML identity"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, a.cxmlView(c))
}

func writeCXML(w http.ResponseWriter, payloadID string, code int, text string) {
	w.Header().Set("Content-Type", "text/xml; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, domain.CXMLResponse(payloadID, time.Now().UTC().Format(time.RFC3339), code, text))
}

// cxmlReceive takes one cXML document from a network.
func (a *API) cxmlReceive(w http.ResponseWriter, r *http.Request) {
	endpoint := chi.URLParam(r, "endpoint")
	if !validID(endpoint) {
		http.NotFound(w, r)
		return
	}
	tid, pid, err := a.store.ResolvePublicEndpoint(r.Context(), "CXML_ENDPOINT", endpoint)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("cxml endpoint lookup", "err", err)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	sys := store.SystemContext(r.Context(), tid)
	cfg, err := a.store.GetPartnerCXML(sys, pid)
	var partner store.Partner
	if err == nil {
		partner, err = a.store.GetPartner(sys, pid)
	}
	if errors.Is(err, store.ErrNotFound) || (err == nil && (cfg.EndpointID != endpoint || partner.Status != "active")) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("cxml partner lookup", "tenant", tid, "partner", pid, "err", err)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCXMLBody))
	if err != nil {
		writeCXML(w, "", 413, "payload larger than 4 MiB")
		return
	}
	env, err := domain.ParseCXML(raw)
	if err != nil {
		writeCXML(w, "", 400, err.Error())
		return
	}
	if subtle.ConstantTimeCompare([]byte(env.SenderID), []byte(cfg.NetworkIdentity)) != 1 ||
		subtle.ConstantTimeCompare([]byte(hashSecret(env.SharedSecret)), []byte(cfg.SecretHash)) != 1 {
		slog.Warn("cxml sender not authenticated", "tenant", tid, "partner", pid, "payload", env.PayloadID)
		writeCXML(w, env.PayloadID, 401, "Unauthorized")
		return
	}
	if !domain.SupportedInbound(env) {
		writeCXML(w, env.PayloadID, 400, env.DocType+" is not accepted on this endpoint (ConfirmationRequest, ShipNoticeRequest and InvoiceDetailRequest are)")
		return
	}
	in := store.CXMLInbound{PartnerID: pid, PayloadID: env.PayloadID, DocType: env.DocType, Hash: domain.PayloadHash(raw), Raw: string(raw),
		ResponseCode: 200, ResponseText: "OK"}
	canonical, mapErr := domain.MapCXMLDocument(raw, env)
	if mapErr != nil {
		in.Error, in.ResponseCode, in.ResponseText = CodeSyntax+": "+mapErr.Error(), 400, mapErr.Error()
	} else if in.Canonical, err = json.Marshal(canonical); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	res, err := a.store.ReceiveCXML(store.EntityContext(r.Context(), tid, cfg.LegalEntityID), in)
	if err != nil {
		slog.Error("cxml receive", "tenant", tid, "partner", pid, "payload", env.PayloadID, "err", err)
		writeCXML(w, env.PayloadID, 503, "temporarily unavailable")
		return
	}
	if res.Decision == domain.RepeatConflict {
		writeCXML(w, env.PayloadID, 409, domain.CodeCXMLPayloadConflict+": payloadID "+env.PayloadID+" was already used with other content")
		return
	}
	writeCXML(w, env.PayloadID, res.ResponseCode, res.ResponseText)
}
