package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/edi/internal/delivery"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
	"github.com/BenjaminSRussell/passage/services/edi/internal/transport"
)

// maxAS2Body caps one AS2 request: a 4 MiB interchange with base64, the
// signature and the envelope stays well below it.
const maxAS2Body = 8 << 20

// maxAsyncMDNBody caps an asynchronous MDN.
const maxAsyncMDNBody = 1 << 20

// AS2Routes is the handler of the AS2 listener (EDI_AS2_ADDR). Partners call
// it without a Passage identity, so it has no tenant middleware and accepts
// no X-Passage-* header: the endpoint id in the path selects one partner
// transport (tenant, partner and legal entity come from that row), and the
// caller is authenticated by its CMS signature against the partner
// certificate pinned there, with AS2-From and AS2-To matching the profile.
// Nothing is parsed before the signature verifies. Mount it on its own
// listener and expose that through a route that does not require a JWT; the
// main API router keeps requiring one. With AS2Limit set, each endpoint id
// has a token bucket checked before anything else (no database lookup, key
// loading or signature work): over the limit the caller gets 429 with
// Retry-After.
//
//	POST /as2/v1/{endpoint}      an AS2 message; answered with an MDN
//	POST /as2/v1/{endpoint}/mdn  an asynchronous MDN for one of our messages
func (a *API) AS2Routes() http.Handler {
	r := httpx.NewRouterWithoutTenant()
	limited := r.With(a.as2RateLimit)
	limited.Post("/as2/v1/{endpoint}", a.as2Receive)
	limited.Post("/as2/v1/{endpoint}/mdn", a.as2AsyncMDN)
	return r
}

// as2RateLimit answers 429 when the endpoint's bucket is empty. Only
// well-formed endpoint ids get a bucket; anything else is a 404 later that
// costs nothing.
func (a *API) as2RateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint := chi.URLParam(r, "endpoint")
		if a.AS2Limit != nil && validID(endpoint) {
			ok, wait, first := a.AS2Limit.Allow(endpoint)
			if !ok {
				if first {
					slog.Warn("as2 rate limit reached", "endpoint", endpoint)
				}
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// as2Partner resolves the endpoint to its active AS2 partner transport and
// loads its keys. A 404 says nothing about why: unknown endpoints, other
// transports and inactive partners look the same.
func (a *API) as2Partner(w http.ResponseWriter, r *http.Request) (context.Context, store.TransportRecord, store.Partner, delivery.AS2Endpoint, bool) {
	var (
		rec     store.TransportRecord
		partner store.Partner
		ep      delivery.AS2Endpoint
	)
	endpoint := chi.URLParam(r, "endpoint")
	if !validID(endpoint) {
		http.NotFound(w, r)
		return nil, rec, partner, ep, false
	}
	tid, pid, err := a.store.ResolveAS2Endpoint(r.Context(), endpoint)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return nil, rec, partner, ep, false
	}
	if err != nil {
		slog.Error("as2 endpoint lookup", "err", err)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return nil, rec, partner, ep, false
	}
	sys := store.SystemContext(r.Context(), tid)
	rec, err = a.store.GetTransport(sys, pid)
	if err == nil {
		partner, err = a.store.GetPartner(sys, pid)
	}
	if errors.Is(err, store.ErrNotFound) || (err == nil && (rec.Config.AS2 == nil || rec.EndpointID != endpoint || partner.Status != "active")) {
		http.NotFound(w, r)
		return nil, rec, partner, ep, false
	}
	if err != nil {
		slog.Error("as2 partner lookup", "tenant", tid, "partner", pid, "err", err)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return nil, rec, partner, ep, false
	}
	ep, err = delivery.LoadAS2(a.Secrets, *rec.Config.AS2, a.now())
	if err != nil {
		slog.Error("as2 keys unavailable", "tenant", tid, "partner", pid, "err", err)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return nil, rec, partner, ep, false
	}
	// From here on the work runs as the edi service in the one legal entity of
	// the partner's trade profile.
	return store.EntityContext(r.Context(), tid, rec.LegalEntityID), rec, partner, ep, true
}

func (a *API) as2Receive(w http.ResponseWriter, r *http.Request) {
	ctx, rec, partner, ep, ok := a.as2Partner(w, r)
	if !ok {
		return
	}
	origID := strings.Trim(strings.TrimSpace(r.Header.Get("Message-ID")), "<>")
	// The Message-ID of the asynchronous MDN stored with the receipt, if any.
	var stored string
	// Every MDN of one request goes to the same Receipt-Delivery-Option.
	var targetChecked, targetOK bool
	allowed := func(m transport.AsyncMDN) bool {
		if !targetChecked {
			targetChecked, targetOK = true, a.mdnTargetAllowed(partner.ID, m, ep.Partner.URL)
		}
		return targetOK
	}
	srv := &transport.AS2Server{
		Us: ep.Us,
		Partner: func(from string) (transport.AS2Partner, bool) {
			return ep.Partner, from == ep.Partner.ID
		},
		Deliver: func(_ context.Context, _ string, m transport.Message) error {
			var owed *store.OwedMDN
			if m.AsyncMDN != nil && allowed(*m.AsyncMDN) {
				o := owedMDN(m.ID, *m.AsyncMDN)
				owed = &o
			}
			err := a.deliverAS2(ctx, partner, rec, m, owed)
			if err == nil && owed != nil {
				stored = owed.MessageID
			}
			return err
		},
		SendAsyncMDN: func(_ context.Context, mdn transport.AsyncMDN) error {
			o := owedMDN(origID, mdn)
			if stored != "" && o.MessageID == stored {
				return nil // queued with the receipt
			}
			if !allowed(mdn) {
				return nil
			}
			if err := a.store.QueueMDN(ctx, partner.ID, rec.LegalEntityID, o); err != nil {
				slog.Error("as2 async MDN not queued", "partner", partner.ID, "err", err)
				return err
			}
			return nil
		},
		MaxBytes: maxAS2Body,
		Now:      a.now,
	}
	srv.ServeHTTP(w, r)
}

// owedMDN is the asynchronous MDN to store for the partner message origID.
func owedMDN(origID string, m transport.AsyncMDN) store.OwedMDN {
	return store.OwedMDN{OriginalMessageID: origID, MessageID: strings.Trim(m.Header.Get("Message-ID"), "<>"),
		TargetURL: m.URL, Header: m.Header, Body: m.Body}
}

// mdnTargetAllowed holds the Receipt-Delivery-Option of a received message to
// the partner's configured AS2 host (delivery.MDNTargetAllowed). An MDN for
// any other URL is not stored or sent; the message is still processed.
func (a *API) mdnTargetAllowed(partnerID string, m transport.AsyncMDN, partnerURL string) bool {
	if len(m.URL) > 2048 || !delivery.MDNTargetAllowed(m.URL, partnerURL) {
		slog.Warn("as2 async MDN not sent: Receipt-Delivery-Option is not on the partner's AS2 host", "partner", partnerID)
		return false
	}
	return true
}

// deliverAS2 hands a verified AS2 payload to the receipt pipeline and maps
// the outcome to an MDN disposition. mdn, when set, is the asynchronous MDN
// answering the message as processed; it is stored in the receipt
// transaction, so it exists exactly when the interchange was recorded.
func (a *API) deliverAS2(ctx context.Context, partner store.Partner, rec store.TransportRecord, m transport.Message, mdn *store.OwedMDN) error {
	log := slog.With("partner", partner.ID, "as2MessageId", m.ID)
	sum := sha256.Sum256(m.Payload)
	src := store.InboundSource{Transport: "AS2", Ref: m.ID, SHA256: hex.EncodeToString(sum[:]), LegalEntityID: rec.LegalEntityID, MDN: mdn}
	if len(src.Ref) > 255 {
		return &transport.DispositionError{Modifier: "unexpected-processing-error"}
	}
	known, err := a.store.FindInbound(ctx, partner.ID, "AS2", m.ID)
	if err != nil {
		log.Error("as2 duplicate check", "err", err)
		return err
	}
	for _, k := range known {
		if k.SHA256 == src.SHA256 && k.Status == "DONE" {
			return &transport.DispositionError{Modifier: "duplicate-document", Warning: true}
		}
		if k.SHA256 != src.SHA256 {
			log.Warn("as2 Message-ID reused with other content")
			return &transport.DispositionError{Modifier: "unexpected-processing-error"}
		}
	}
	if !x12Payload(m) {
		log.Warn("as2 payload refused", "contentType", m.ContentType)
		return &transport.DispositionError{Modifier: "unexpected-processing-error"}
	}
	err = a.ReceiveFromTransport(ctx, partner.ID, src, m.Payload)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrDuplicate):
		return &transport.DispositionError{Modifier: "duplicate-document", Warning: true}
	case errors.Is(err, delivery.ErrPermanent):
		log.Warn("as2 payload refused", "err", err)
		return &transport.DispositionError{Modifier: "unexpected-processing-error"}
	default:
		log.Error("as2 receipt failed", "err", err)
		return err
	}
}

// x12Payload accepts application/edi-x12, and an untyped body that starts
// with an ISA segment. EDIFACT receipt is not built yet.
func x12Payload(m transport.Message) bool {
	if m.ContentType == transport.ContentX12 {
		return true
	}
	switch m.ContentType {
	case "", "application/octet-stream", "text/plain":
		return strings.HasPrefix(strings.TrimLeft(string(m.Payload[:min(len(m.Payload), 64)]), " \t\r\n"), "ISA")
	}
	return false
}

// ReceiveFromTransport records an X12 interchange an AS2 message or an SFTP
// file brought from partnerID; ctx carries the service principal of the
// transport's tenant (and legal entity). It returns nil when recorded, an
// error wrapping store.ErrDuplicate for an interchange recorded before, one
// wrapping delivery.ErrPermanent for a payload that can never be recorded,
// and any other error for a failure worth retrying (delivery.Receiver).
func (a *API) ReceiveFromTransport(ctx context.Context, partnerID string, src store.InboundSource, payload []byte) error {
	partner, err := a.store.GetPartner(ctx, partnerID)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: partner %s is gone", delivery.ErrPermanent, partnerID)
	}
	if err != nil {
		return err
	}
	_, err = a.receive(ctx, payload, receiveOptions{Environment: partner.Environment, PartnerID: partner.ID, Source: &src})
	var p *httpx.Problem
	switch {
	case err == nil:
		return nil
	case errors.As(err, &p) && p.Code == CodeDuplicateControl:
		return fmt.Errorf("%w: %s", store.ErrDuplicate, p.Detail)
	case errors.As(err, &p):
		return fmt.Errorf("%w: %s %s", delivery.ErrPermanent, p.Code, p.Detail)
	}
	return err
}

// as2AsyncMDN receives the asynchronous MDN for one of our deliveries. It
// must be signed by the partner certificate, come from the partner's AS2 id
// to ours, answer a delivery that waits for it and carry our MIC.
func (a *API) as2AsyncMDN(w http.ResponseWriter, r *http.Request) {
	ctx, _, partner, ep, ok := a.as2Partner(w, r)
	if !ok {
		return
	}
	from := strings.Trim(strings.TrimSpace(r.Header.Get("AS2-From")), `"`)
	to := strings.Trim(strings.TrimSpace(r.Header.Get("AS2-To")), `"`)
	if from != ep.Partner.ID || to != ep.Us.ID {
		http.Error(w, "unknown AS2-From or AS2-To", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAsyncMDNBody+1))
	if err != nil || len(body) > maxAsyncMDNBody {
		http.Error(w, "MDN too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	mdn, err := transport.ReadMDN(r.Header.Get("Content-Type"), body, ep.Partner.Cert)
	if err != nil || !mdn.Signed {
		slog.Warn("as2 async MDN refused", "partner", partner.ID, "err", err, "signed", mdn.Signed)
		http.Error(w, "MDN must be signed by the partner certificate", http.StatusBadRequest)
		return
	}
	msgID := strings.TrimSuffix(strings.TrimPrefix(mdn.OriginalMessageID, "<"), ">")
	out, err := a.store.ApplyAsyncMDN(ctx, partner.ID, msgID, func(wait store.AwaitingMDN) store.DeliveryOutcome {
		o := store.DeliveryOutcome{MDNMIC: mdn.MIC, MDNDisposition: mdn.Disposition, MDNSigned: true}
		switch err := mdn.Check(mdn.OriginalMessageID, wait.SentMIC, true); {
		case err == nil:
			o.Status = "DELIVERED"
		case errors.Is(err, transport.ErrRejected):
			o.Status, o.Error = "REJECTED", err.Error()
		default:
			o.Status, o.Error = "AWAITING_MDN", err.Error()
		}
		return o
	})
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no delivery waits for this MDN", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("as2 async MDN", "partner", partner.ID, "err", err)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if out.Status == "AWAITING_MDN" {
		slog.Warn("as2 async MDN does not match the delivery", "partner", partner.ID, "err", out.Error)
		http.Error(w, "MDN does not match the message", http.StatusBadRequest)
		return
	}
	slog.Info("as2 async MDN", "partner", partner.ID, "status", out.Status)
	w.WriteHeader(http.StatusOK)
}
