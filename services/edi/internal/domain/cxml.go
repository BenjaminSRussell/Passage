package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
)

// CodeCXMLPayloadConflict is raised when a payloadID is reused with different
// content (BR-35.14).
const CodeCXMLPayloadConflict = "EDI-0135"

// cXML document types accepted from a network (FR-35.14). OrderRequest is
// outbound only and is not in this set.
var cxmlInboundTypes = map[string]bool{
	"ConfirmationRequest":  true,
	"ShipNoticeRequest":    true,
	"InvoiceDetailRequest": true,
}

// CXMLEnvelope is the header and document type of one cXML 1.2 payload.
type CXMLEnvelope struct {
	PayloadID    string
	Timestamp    string
	From         string // NetworkId identity of the sender side
	To           string
	SenderID     string // NetworkId identity used for authentication
	SharedSecret string // compared with the partner profile; never returned to clients
	DocType      string // OrderRequest, ConfirmationRequest, PunchOutOrderMessage, ...
	Kind         string // request, message or response
}

type cxmlCredential struct {
	Domain       string `xml:"domain,attr"`
	Identity     string `xml:"Identity"`
	SharedSecret string `xml:"SharedSecret"`
}

type cxmlCredentialSet struct {
	Credentials []cxmlCredential `xml:"Credential"`
}

type cxmlChild struct {
	XMLName xml.Name
}

type cxmlChildren struct {
	Children []cxmlChild `xml:",any"`
}

type cxmlWire struct {
	XMLName   xml.Name `xml:"cXML"`
	PayloadID string   `xml:"payloadID,attr"`
	Timestamp string   `xml:"timestamp,attr"`
	Header    *struct {
		From   cxmlCredentialSet `xml:"From"`
		To     cxmlCredentialSet `xml:"To"`
		Sender cxmlCredentialSet `xml:"Sender"`
	} `xml:"Header"`
	Request  *cxmlChildren `xml:"Request"`
	Message  *cxmlChildren `xml:"Message"`
	Response *cxmlResponse `xml:"Response"`
}

type cxmlResponse struct {
	Status struct {
		Code string `xml:"code,attr"`
		Text string `xml:"text,attr"`
	} `xml:"Status"`
}

func firstIdentity(s cxmlCredentialSet) string {
	for _, c := range s.Credentials {
		if c.Domain == "NetworkId" {
			return c.Identity
		}
	}
	if len(s.Credentials) > 0 {
		return s.Credentials[0].Identity
	}
	return ""
}

// ParseCXML reads the envelope of a cXML payload and classifies its document.
func ParseCXML(raw []byte) (CXMLEnvelope, error) {
	var w cxmlWire
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&w); err != nil {
		return CXMLEnvelope{}, fmt.Errorf("cxml: not well-formed: %w", err)
	}
	if w.PayloadID == "" {
		return CXMLEnvelope{}, errors.New("cxml: payloadID attribute is required")
	}
	env := CXMLEnvelope{PayloadID: w.PayloadID, Timestamp: w.Timestamp}
	if w.Header != nil {
		env.From = firstIdentity(w.Header.From)
		env.To = firstIdentity(w.Header.To)
		env.SenderID = firstIdentity(w.Header.Sender)
		for _, c := range w.Header.Sender.Credentials {
			if c.SharedSecret != "" {
				env.SharedSecret = c.SharedSecret
			}
		}
	}
	switch {
	case w.Request != nil:
		env.Kind = "request"
		env.DocType = firstChild(w.Request.Children)
	case w.Message != nil:
		env.Kind = "message"
		env.DocType = firstChild(w.Message.Children)
	case w.Response != nil:
		env.Kind = "response"
		env.DocType = "Response"
	default:
		return CXMLEnvelope{}, errors.New("cxml: payload has no Request, Message or Response")
	}
	if env.DocType == "" {
		return CXMLEnvelope{}, errors.New("cxml: Request or Message has no document element")
	}
	return env, nil
}

func firstChild(children []cxmlChild) string {
	if len(children) == 0 {
		return ""
	}
	return children[0].XMLName.Local
}

// SupportedInbound reports whether the document type is accepted from a network.
func SupportedInbound(env CXMLEnvelope) bool {
	return env.Kind == "request" && cxmlInboundTypes[env.DocType]
}

// PayloadHash is the content fingerprint used for the BR-35.14 comparison. Leading
// and trailing whitespace is ignored; everything else must match byte for byte.
func PayloadHash(raw []byte) string {
	sum := sha256.Sum256(bytes.TrimSpace(raw))
	return hex.EncodeToString(sum[:])
}

// RepeatDecision is the outcome of a payloadID lookup.
type RepeatDecision string

const (
	RepeatNew      RepeatDecision = "NEW"      // first time this payloadID is seen
	RepeatReplay   RepeatDecision = "REPLAY"   // same content: return the original response
	RepeatConflict RepeatDecision = "CONFLICT" // different content: reject with EDI-0135
)

// ResolveRepeat applies BR-35.14. storedHash is empty when the payloadID is new.
func ResolveRepeat(storedHash, incomingHash string) RepeatDecision {
	switch {
	case storedHash == "":
		return RepeatNew
	case storedHash == incomingHash:
		return RepeatReplay
	default:
		return RepeatConflict
	}
}

// CXMLResponse builds the synchronous Response that acknowledges a payload.
func CXMLResponse(payloadID, timestamp string, code int, text string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>`+"\n"+
		`<cXML payloadID="%s" timestamp="%s"><Response><Status code="%d" text="%s"/></Response></cXML>`,
		esc(payloadID), esc(timestamp), code, esc(text))
}

// ResponseOutcome classifies a Status code from a network (FR-35.14): 200 and 201
// are accepted, other 4xx are rejected, anything else is retried later.
type ResponseOutcome string

const (
	OutcomeAccepted ResponseOutcome = "ACCEPTED"
	OutcomeRejected ResponseOutcome = "REJECTED"
	OutcomeRetry    ResponseOutcome = "RETRY"
)

// ClassifyStatus maps an HTTP-style cXML status code to an outcome.
func ClassifyStatus(code int) ResponseOutcome {
	switch {
	case code == 200 || code == 201:
		return OutcomeAccepted
	case code >= 400 && code < 500:
		return OutcomeRejected
	default:
		return OutcomeRetry
	}
}

// ParseCXMLStatus reads the Status code and text of a cXML Response.
func ParseCXMLStatus(raw []byte) (int, string, error) {
	var w cxmlWire
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&w); err != nil {
		return 0, "", fmt.Errorf("cxml: not well-formed: %w", err)
	}
	if w.Response == nil {
		return 0, "", errors.New("cxml: no Response element")
	}
	code, err := strconv.Atoi(w.Response.Status.Code)
	if err != nil {
		return 0, "", fmt.Errorf("cxml: Status code %q is not numeric", w.Response.Status.Code)
	}
	return code, w.Response.Status.Text, nil
}
