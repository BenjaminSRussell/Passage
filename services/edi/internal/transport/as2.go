package transport

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

// AS2 (RFC 4130) over HTTP(S): the message is the EDI payload as a MIME
// entity, signed as multipart/signed with a detached CMS signature; the
// partner answers synchronously in the HTTP response with an MDN
// (multipart/report, RFC 3798), optionally signed, which carries the
// Received-Content-MIC the sender compares with its own (BR-35.11). The
// signed entity may be encrypted (application/pkcs7-mime EnvelopedData, see
// enveloped.go), and the MDN may be asked for asynchronously
// (Receipt-Delivery-Option): the partner then answers the POST with an empty
// 2xx and posts the MDN to our URL later. Compression is not implemented.

// AS2Identity is our side: the AS2 name and the certificate and key that
// sign messages and MDNs. Key nil sends unsigned messages and MDNs.
type AS2Identity struct {
	ID   string
	Cert *x509.Certificate
	Key  crypto.Signer
}

// AS2Partner is a trading partner's AS2 profile: its name, endpoint, the
// certificate its signatures must verify against (and that we encrypt to),
// and whether its messages must be signed or encrypted.
type AS2Partner struct {
	ID               string
	URL              string
	Cert             *x509.Certificate
	RequireSigned    bool
	RequireEncrypted bool
}

const (
	as2Version      = "1.2"
	maxMDNBytes     = 1 << 20
	defaultMaxBytes = 32 << 20
	mdnProcessed    = "automatic-action/MDN-sent-automatically; processed"
)

// AS2Client sends to one partner. SignHash is the signature digest (zero is
// SHA-256); Encryption is one of the Encrypt* algorithms (empty or "none"
// sends the signed entity in clear); AsyncMDNURL, when set, asks for the MDN
// at that URL instead of in the HTTP response.
type AS2Client struct {
	Us               AS2Identity
	Partner          AS2Partner
	HTTP             *http.Client
	RequireSignedMDN bool
	SignHash         crypto.Hash
	Encryption       string
	AsyncMDNURL      string
	Now              func() time.Time
}

// MDN is a parsed disposition notification.
type MDN struct {
	OriginalMessageID string
	FinalRecipient    string
	MIC               string
	Disposition       string
	Signed            bool
}

// Processed reports whether the disposition is processed without an error
// or failure modifier.
func (m MDN) Processed() bool {
	d := strings.ToLower(m.Disposition)
	i := strings.Index(d, ";")
	if i < 0 {
		return false
	}
	t := strings.TrimSpace(d[i+1:])
	return t == "processed" || strings.HasPrefix(t, "processed/warning")
}

func as2Quote(s string) string {
	if strings.ContainsAny(s, " \t\"\\") {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	return s
}

func as2Unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.NewReplacer(`\\`, `\`, `\"`, `"`).Replace(s[1 : len(s)-1])
	}
	return s
}

func randomBoundary() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return "----=_passage_" + hex.EncodeToString(b)
}

// payloadEntity is the MIME entity that is signed: headers, blank line and
// the payload unchanged (binary transfer encoding, as RFC 4130 allows).
func payloadEntity(m Message) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Content-Type: %s\r\nContent-Transfer-Encoding: binary\r\nContent-Disposition: attachment; filename=%q\r\n\r\n", m.ContentType, m.filename())
	b.Write(m.Payload)
	return b.Bytes()
}

// micOf is the RFC 4130 §7.3.1 MIC: base64 digest and the algorithm name.
func micOf(data []byte, h crypto.Hash) string {
	d := h.New()
	d.Write(data)
	return base64.StdEncoding.EncodeToString(d.Sum(nil)) + ", " + micalgOf[h]
}

// multipartSigned wraps entity and its detached signature made with hash.
func multipartSigned(entity, sig []byte, hash crypto.Hash) (contentType string, body []byte) {
	boundary := randomBoundary()
	var b bytes.Buffer
	b.WriteString("--" + boundary + "\r\n")
	b.Write(entity)
	b.WriteString("\r\n--" + boundary + "\r\n")
	b.WriteString("Content-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=\"smime.p7s\"\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString(sig)
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n--" + boundary + "--\r\n")
	return fmt.Sprintf(`multipart/signed; protocol="application/pkcs7-signature"; micalg=%s; boundary="%s"`, micalgOf[hash], boundary), b.Bytes()
}

// splitSigned returns the exact bytes of the signed first part and the
// decoded signature of a multipart/signed body. The first part is taken byte
// for byte: re-encoding it would break the signature.
func splitSigned(boundary string, body []byte) (signed, sig []byte, err error) {
	if boundary == "" {
		return nil, nil, sigErr("multipart/signed has no boundary")
	}
	for _, nl := range []string{"\r\n", "\n"} {
		open := []byte("--" + boundary + nl)
		sep := []byte(nl + "--" + boundary)
		i := bytes.Index(body, open)
		if i < 0 || (i > 0 && body[i-1] != '\n') {
			continue
		}
		rest := body[i+len(open):]
		j := bytes.Index(rest, sep)
		if j < 0 {
			continue
		}
		signed = rest[:j]
		rest = rest[j+len(sep):]
		if !bytes.HasPrefix(rest, []byte(nl)) {
			continue
		}
		rest = rest[len(nl):]
		k := bytes.Index(rest, sep)
		if k < 0 {
			continue
		}
		hdr, part, err := parseEntity(rest[:k])
		if err != nil {
			return nil, nil, err
		}
		mt, _, _ := mime.ParseMediaType(hdr.Get("Content-Type"))
		if mt != "application/pkcs7-signature" && mt != "application/x-pkcs7-signature" {
			return nil, nil, sigErr("second part is %q, not application/pkcs7-signature", mt)
		}
		return signed, part, nil
	}
	return nil, nil, sigErr("multipart/signed body does not have two parts")
}

// parseEntity splits a MIME entity into its headers and its decoded body.
func parseEntity(raw []byte) (textproto.MIMEHeader, []byte, error) {
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
	body := raw[i+n:]
	switch strings.ToLower(strings.TrimSpace(hdr.Get("Content-Transfer-Encoding"))) {
	case "base64":
		clean := strings.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, string(body))
		body, err = base64.StdEncoding.DecodeString(clean)
		if err != nil {
			return nil, nil, fmt.Errorf("transport: base64 body: %w", err)
		}
	case "quoted-printable":
		body, err = io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
		if err != nil {
			return nil, nil, err
		}
	}
	return hdr, body, nil
}

// Send signs and posts the message and checks the synchronous MDN: it must be
// for this Message-ID, signed by the partner when RequireSignedMDN is set,
// carry the MIC we computed and say processed. A refused message (an error
// disposition) is ErrRejected; a missing or forged MDN is ErrSignature or an
// HTTP error, which a caller may retry with the same Message-ID.
func (c *AS2Client) Send(ctx context.Context, m Message) (Receipt, error) {
	if err := m.validate(); err != nil {
		return Receipt{}, err
	}
	if (m.From != "" && m.From != c.Us.ID) || (m.To != "" && m.To != c.Partner.ID) || c.Us.ID == "" || c.Partner.ID == "" || c.Partner.URL == "" {
		return Receipt{}, fmt.Errorf("%w: AS2 identities %q to %q do not match the client (%q to %q)", ErrInvalid, m.From, m.To, c.Us.ID, c.Partner.ID)
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	hash := c.SignHash
	if hash == 0 {
		hash = crypto.SHA256
	}
	if _, ok := micalgOf[hash]; !ok {
		return Receipt{}, fmt.Errorf("%w: signature digest %v is not SHA-256, SHA-384 or SHA-512", ErrInvalid, hash)
	}
	encrypt := c.Encryption != "" && c.Encryption != EncryptNone
	var (
		contentType string
		body        []byte
		mic         string
	)
	entity := payloadEntity(m)
	switch {
	case c.Us.Key != nil:
		sig, err := SignDetachedWith(entity, c.Us.Cert, c.Us.Key, now(), hash)
		if err != nil {
			return Receipt{}, err
		}
		contentType, body = multipartSigned(entity, sig, hash)
		mic = micOf(entity, hash)
		if encrypt {
			// RFC 4130 §7.3.1: the MIC stays over the signed entity; the
			// encrypted entity is the multipart/signed with its headers.
			body = append([]byte("Content-Type: "+contentType+"\r\n\r\n"), body...)
		}
	case encrypt:
		body, mic = entity, micOf(entity, hash)
	default:
		contentType, body, mic = m.ContentType, m.Payload, micOf(m.Payload, hash)
	}
	if encrypt {
		sealed, err := Encrypt(body, c.Partner.Cert, c.Encryption)
		if err != nil {
			return Receipt{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		contentType, body = `application/pkcs7-mime; smime-type=enveloped-data; name="smime.p7m"`, sealed
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Partner.URL, bytes.NewReader(body))
	if err != nil {
		return Receipt{}, err
	}
	msgID := "<" + m.ID + ">"
	h := req.Header
	h.Set("Content-Type", contentType)
	h.Set("MIME-Version", "1.0")
	h.Set("AS2-Version", as2Version)
	h.Set("AS2-From", as2Quote(c.Us.ID))
	h.Set("AS2-To", as2Quote(c.Partner.ID))
	h.Set("Message-ID", msgID)
	h.Set("Subject", "EDI "+m.filename())
	h.Set("Date", now().UTC().Format(time.RFC1123Z))
	h.Set("Disposition-Notification-To", as2Quote(c.Us.ID))
	if c.RequireSignedMDN {
		h.Set("Disposition-Notification-Options", "signed-receipt-protocol=required, pkcs7-signature; signed-receipt-micalg=required, "+micalgOf[hash])
	}
	if c.AsyncMDNURL != "" {
		h.Set("Receipt-Delivery-Option", c.AsyncMDNURL)
	}
	if encrypt {
		h.Set("Content-Transfer-Encoding", "binary")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Receipt{}, fmt.Errorf("as2 post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMDNBytes+1))
	if err != nil {
		return Receipt{}, fmt.Errorf("as2 read MDN: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return Receipt{}, fmt.Errorf("as2 post: partner answered HTTP %d", resp.StatusCode)
	}
	if c.AsyncMDNURL != "" {
		return Receipt{Transport: "as2", MessageID: m.ID, Reference: msgID, SentMIC: mic, Pending: true, At: now().UTC()}, nil
	}
	if len(raw) > maxMDNBytes {
		return Receipt{}, errors.New("as2: MDN is larger than 1 MiB")
	}
	mdn, err := ReadMDN(resp.Header.Get("Content-Type"), raw, c.Partner.Cert)
	if err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{Transport: "as2", MessageID: m.ID, Reference: mdn.OriginalMessageID, MIC: mdn.MIC, SentMIC: mic, Disposition: mdn.Disposition, SignedMDN: mdn.Signed, At: now().UTC()}
	if err := mdn.Check(msgID, mic, c.RequireSignedMDN); err != nil {
		if errors.Is(err, ErrRejected) {
			return receipt, err
		}
		return Receipt{}, err
	}
	return receipt, nil
}

// Check holds an MDN to the message it answers: it must be for msgID (with
// angle brackets), signed when requireSigned is set, say processed and carry
// the MIC we computed. A refusal by the partner is ErrRejected; anything that
// makes the MDN untrustworthy is ErrSignature.
func (m MDN) Check(msgID, sentMIC string, requireSigned bool) error {
	if requireSigned && !m.Signed {
		return sigErr("the MDN is not signed")
	}
	if m.OriginalMessageID != msgID {
		return sigErr("MDN answers %s, not %s", m.OriginalMessageID, msgID)
	}
	if !m.Processed() {
		return fmt.Errorf("%w: MDN disposition %q", ErrRejected, m.Disposition)
	}
	if normalizeMIC(m.MIC) != normalizeMIC(sentMIC) {
		return sigErr("MDN MIC %q does not match ours %q", m.MIC, sentMIC)
	}
	return nil
}

func normalizeMIC(s string) string {
	parts := strings.SplitN(s, ",", 2)
	if len(parts) != 2 {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(parts[0]) + "," + strings.ToLower(strings.TrimSpace(parts[1]))
}

// ReadMDN parses a synchronous MDN body with its Content-Type. A signed MDN
// (multipart/signed) is verified against partnerCert first.
func ReadMDN(contentType string, body []byte, partnerCert *x509.Certificate) (MDN, error) {
	var mdn MDN
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return mdn, fmt.Errorf("as2: MDN content type: %w", err)
	}
	if media == "multipart/signed" {
		signed, sig, err := splitSigned(params["boundary"], body)
		if err != nil {
			return mdn, err
		}
		if _, err := VerifyDetached(sig, signed, partnerCert); err != nil {
			return mdn, err
		}
		hdr, inner, err := parseEntity(signed)
		if err != nil {
			return mdn, err
		}
		mdn.Signed = true
		body = inner
		if media, params, err = mime.ParseMediaType(hdr.Get("Content-Type")); err != nil {
			return mdn, fmt.Errorf("as2: signed MDN content type: %w", err)
		}
	}
	if media != "multipart/report" {
		return mdn, fmt.Errorf("as2: expected an MDN (multipart/report), got %s", media)
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return mdn, fmt.Errorf("as2: MDN: %w", err)
		}
		pt, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		if pt != "message/disposition-notification" {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(p, maxMDNBytes))
		if err != nil {
			return mdn, err
		}
		fields, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(bytes.TrimRight(data, "\r\n"), "\r\n\r\n"...)))).ReadMIMEHeader()
		if err != nil {
			return mdn, fmt.Errorf("as2: disposition fields: %w", err)
		}
		mdn.OriginalMessageID = strings.TrimSpace(fields.Get("Original-Message-ID"))
		mdn.FinalRecipient = strings.TrimSpace(fields.Get("Final-Recipient"))
		mdn.MIC = strings.TrimSpace(fields.Get("Received-Content-MIC"))
		mdn.Disposition = strings.TrimSpace(fields.Get("Disposition"))
		return mdn, nil
	}
	return mdn, errors.New("as2: MDN has no message/disposition-notification part")
}
