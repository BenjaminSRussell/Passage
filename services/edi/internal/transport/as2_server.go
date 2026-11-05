package transport

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

// DispositionError lets a Deliver callback choose the MDN disposition
// modifier: Warning gives "processed/warning: <Modifier>" (for example
// duplicate-document for a retransmission already processed), otherwise
// "processed/error: <Modifier>". Any other Deliver error is
// unexpected-processing-error.
type DispositionError struct {
	Modifier string
	Warning  bool
}

func (e *DispositionError) Error() string {
	if e.Warning {
		return "as2: processed with warning " + e.Modifier
	}
	return "as2: processing error " + e.Modifier
}

// AsyncMDN is an MDN to post to a partner's asynchronous MDN URL: the URL the
// partner asked for in Receipt-Delivery-Option, the headers and the body.
type AsyncMDN struct {
	URL     string
	Header  http.Header
	Body    []byte
	Partner AS2Partner
}

// AS2Server receives AS2 messages for our identity and answers each with an
// MDN. Partner resolves the AS2-From name; Deliver hands the verified (and
// decrypted) payload on (to the inbound receipt) and its error becomes an
// error disposition (see DispositionError). When the sender asks for an
// asynchronous MDN the request is answered with an empty 200 and the MDN goes
// to SendAsyncMDN, which decides whether and where to post it (or queues it
// for posting); without SendAsyncMDN the MDN is returned synchronously. The
// request is answered only after SendAsyncMDN returns: an error there is a
// 503, so the sender retransmits instead of waiting for an MDN that was never
// queued.
type AS2Server struct {
	Us           AS2Identity
	Partner      func(as2From string) (AS2Partner, bool)
	Deliver      func(ctx context.Context, from string, m Message) error
	SendAsyncMDN func(ctx context.Context, mdn AsyncMDN) error
	MaxBytes     int64
	Now          func() time.Time
}

func errDisposition(modifier string) string {
	return "automatic-action/MDN-sent-automatically; processed/error: " + modifier
}

func (s *AS2Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "AS2 accepts POST only", http.StatusMethodNotAllowed)
		return
	}
	from, to := as2Unquote(r.Header.Get("AS2-From")), as2Unquote(r.Header.Get("AS2-To"))
	partner, ok := s.Partner(from)
	if !ok || to != s.Us.ID {
		http.Error(w, "unknown AS2-From or AS2-To", http.StatusForbidden)
		return
	}
	msgID := strings.TrimSpace(r.Header.Get("Message-ID"))
	if msgID == "" || len(msgID) > 255 {
		http.Error(w, "Message-ID is required (at most 255 characters)", http.StatusBadRequest)
		return
	}
	limit := s.MaxBytes
	if limit <= 0 {
		limit = defaultMaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > limit {
		http.Error(w, "message too large", http.StatusRequestEntityTooLarge)
		return
	}
	msg, mic, disposition := s.open(partner, r.Header.Get("Content-Type"), body)
	msg.ID, msg.From, msg.To, msg.MIC = strings.Trim(msgID, "<>"), from, to, mic
	wantMDN := r.Header.Get("Disposition-Notification-To") != ""
	asyncURL := strings.TrimSpace(r.Header.Get("Receipt-Delivery-Option"))
	async := wantMDN && asyncURL != "" && s.SendAsyncMDN != nil
	if disposition == mdnProcessed {
		if async {
			// Built before Deliver so it can be stored with the receipt.
			hdr, mdn := s.buildMDN(r.Header, from, msgID, mic, disposition)
			msg.AsyncMDN = &AsyncMDN{URL: asyncURL, Header: hdr, Body: mdn, Partner: partner}
		}
		if err := s.Deliver(r.Context(), from, msg); err != nil {
			msg.AsyncMDN = nil
			var de *DispositionError
			switch {
			case errors.As(err, &de) && de.Warning:
				disposition = "automatic-action/MDN-sent-automatically; processed/warning: " + de.Modifier
			case errors.As(err, &de):
				disposition = errDisposition(de.Modifier)
			default:
				disposition = errDisposition("unexpected-processing-error")
			}
		}
	}
	if !wantMDN {
		w.WriteHeader(http.StatusOK)
		return
	}
	if async {
		m := msg.AsyncMDN
		if m == nil {
			hdr, mdn := s.buildMDN(r.Header, from, msgID, mic, disposition)
			m = &AsyncMDN{URL: asyncURL, Header: hdr, Body: mdn, Partner: partner}
		}
		if err := s.SendAsyncMDN(r.Context(), *m); err != nil {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	hdr, mdn := s.buildMDN(r.Header, from, msgID, mic, disposition)
	for k, v := range hdr {
		w.Header()[k] = v
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(mdn)
}

// open decrypts and verifies a received body. It returns the payload message,
// the MIC for the MDN and the disposition: processed, or an error naming why
// the message cannot be accepted. Nothing is handed on unless the partner's
// signature verified (or the partner is allowed to send unsigned).
func (s *AS2Server) open(partner AS2Partner, contentType string, body []byte) (Message, string, string) {
	var msg Message
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return msg, "", errDisposition("unexpected-processing-error")
	}
	encrypted := false
	if media == "application/pkcs7-mime" {
		if t := strings.ToLower(params["smime-type"]); t != "" && t != "enveloped-data" {
			return msg, "", errDisposition("decryption-failed")
		}
		key, ok := s.Us.Key.(*rsa.PrivateKey)
		if !ok {
			return msg, "", errDisposition("decryption-failed")
		}
		inner, err := Decrypt(body, s.Us.Cert, key)
		if err != nil {
			return msg, "", errDisposition("decryption-failed")
		}
		hdr, entityBody, err := splitEntity(inner)
		if err != nil {
			return msg, "", errDisposition("unexpected-processing-error")
		}
		encrypted = true
		if media, params, err = mime.ParseMediaType(hdr.Get("Content-Type")); err != nil {
			return msg, "", errDisposition("unexpected-processing-error")
		}
		if media != "multipart/signed" {
			// Encrypted but not signed: the MIC is over the decrypted entity.
			if partner.RequireSigned {
				return msg, "", errDisposition("insufficient-message-security")
			}
			_, payload, err := parseEntity(inner)
			if err != nil {
				return msg, "", errDisposition("unexpected-processing-error")
			}
			msg.ContentType, msg.Payload = media, payload
			if _, p, err := mime.ParseMediaType(hdr.Get("Content-Disposition")); err == nil {
				msg.Filename = p["filename"]
			}
			return msg, micOf(inner, crypto.SHA256), mdnProcessed
		}
		body = entityBody
	}
	if partner.RequireEncrypted && !encrypted {
		return msg, "", errDisposition("insufficient-message-security")
	}
	switch {
	case media == "multipart/signed":
		signed, sig, err := splitSigned(params["boundary"], body)
		if err != nil {
			return msg, "", errDisposition("integrity-check-failed")
		}
		hash, err := VerifyDetached(sig, signed, partner.Cert)
		if err != nil {
			return msg, "", errDisposition("authentication-failed")
		}
		hdr, payload, err := parseEntity(signed)
		if err != nil {
			return msg, "", errDisposition("unexpected-processing-error")
		}
		msg.ContentType, _, _ = mime.ParseMediaType(hdr.Get("Content-Type"))
		msg.Payload = payload
		if _, p, err := mime.ParseMediaType(hdr.Get("Content-Disposition")); err == nil {
			msg.Filename = p["filename"]
		}
		return msg, micOf(signed, hash), mdnProcessed
	case partner.RequireSigned:
		return msg, "", errDisposition("insufficient-message-security")
	default:
		msg.ContentType, msg.Payload = media, body
		return msg, micOf(body, crypto.SHA256), mdnProcessed
	}
}

// splitEntity splits a MIME entity into its headers and its raw (still
// encoded) body, which a multipart/signed body must stay.
func splitEntity(raw []byte) (textproto.MIMEHeader, []byte, error) {
	sep, n := []byte("\r\n\r\n"), 4
	i := bytes.Index(raw, sep)
	if j := bytes.Index(raw, []byte("\n\n")); i < 0 || (j >= 0 && j < i) {
		i, n = j, 2
	}
	if i < 0 {
		return nil, nil, errors.New("transport: MIME entity has no header block")
	}
	hdr, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(append([]byte(nil), raw[:i]...), "\r\n\r\n"...)))).ReadMIMEHeader()
	if err != nil {
		return nil, nil, fmt.Errorf("transport: MIME headers: %w", err)
	}
	return hdr, raw[i+n:], nil
}

// buildMDN returns the headers and body of the MDN for a received message,
// signed when the sender asked for a signed receipt and we hold a key.
func (s *AS2Server) buildMDN(req http.Header, to, origID, mic, disposition string) (http.Header, []byte) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	boundary := randomBoundary()
	var b bytes.Buffer
	text := "The AS2 message has been received and processed."
	if disposition != mdnProcessed {
		text = "The AS2 message was not processed as sent: " + disposition
	}
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=us-ascii\r\n\r\n%s\r\n", boundary, text)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: message/disposition-notification\r\n\r\n", boundary)
	fmt.Fprintf(&b, "Reporting-UA: passage-edi\r\nOriginal-Recipient: rfc822; %s\r\nFinal-Recipient: rfc822; %s\r\nOriginal-Message-ID: %s\r\n", s.Us.ID, s.Us.ID, origID)
	if mic != "" {
		fmt.Fprintf(&b, "Received-Content-MIC: %s\r\n", mic)
	}
	fmt.Fprintf(&b, "Disposition: %s\r\n\r\n--%s--\r\n", disposition, boundary)
	reportType := fmt.Sprintf(`multipart/report; report-type=disposition-notification; boundary="%s"`, boundary)
	contentType, body := reportType, b.Bytes()
	if s.Us.Key != nil && strings.Contains(strings.ToLower(req.Get("Disposition-Notification-Options")), "pkcs7-signature") {
		hash := requestedMICAlg(req.Get("Disposition-Notification-Options"))
		entity := append([]byte("Content-Type: "+reportType+"\r\n\r\n"), body...)
		if sig, err := SignDetachedWith(entity, s.Us.Cert, s.Us.Key, now(), hash); err == nil {
			contentType, body = multipartSigned(entity, sig, hash)
		}
	}
	h := http.Header{}
	h.Set("Content-Type", contentType)
	h.Set("MIME-Version", "1.0")
	h.Set("AS2-Version", as2Version)
	h.Set("AS2-From", as2Quote(s.Us.ID))
	h.Set("AS2-To", as2Quote(to))
	h.Set("Message-ID", "<mdn-"+randomBoundary()[14:]+">")
	h.Set("Date", now().UTC().Format(time.RFC1123Z))
	return h, body
}

// requestedMICAlg picks the first supported digest named by
// signed-receipt-micalg, SHA-256 when none is named.
func requestedMICAlg(options string) crypto.Hash {
	lower := strings.ToLower(options)
	i := strings.Index(lower, "signed-receipt-micalg")
	if i < 0 {
		return crypto.SHA256
	}
	rest := lower[i+len("signed-receipt-micalg"):]
	if j := strings.Index(rest, ";"); j >= 0 {
		rest = rest[:j]
	}
	for _, part := range strings.Split(strings.TrimPrefix(strings.TrimSpace(rest), "="), ",") {
		part = strings.TrimSpace(part)
		for _, h := range []crypto.Hash{crypto.SHA256, crypto.SHA384, crypto.SHA512} {
			if part == micalgOf[h] {
				return h
			}
		}
	}
	return crypto.SHA256
}

// PostAsyncMDN posts an MDN to url and expects a 2xx.
func PostAsyncMDN(ctx context.Context, hc *http.Client, url string, mdn AsyncMDN) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(mdn.Body))
	if err != nil {
		return err
	}
	for k, v := range mdn.Header {
		req.Header[k] = v
	}
	if hc == nil {
		hc = &http.Client{Timeout: time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("as2 async MDN: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxMDNBytes))
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("as2 async MDN: partner answered HTTP %d", resp.StatusCode)
	}
	return nil
}
