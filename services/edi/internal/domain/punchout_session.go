package domain

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Punchout sessions (#35, 35-3). A requester opens a supplier's catalog from Passage: for cXML the buyer system sends a
// PunchOutSetupRequest and the supplier answers with the StartPage URL the browser is sent to; for SAP OCI the browser is sent to
// the supplier's URL with the HOOK_URL it must post the cart back to. Either way the cart comes back to the session's return URL
// and is validated by ValidatePunchout (BR-35.13).

// BuildPunchOutSetupRequest is the cXML 1.2 PunchOutSetupRequest operation "create". buyerIdentity and supplierIdentity are the
// network ids of the two sides, secret the supplier's shared secret (empty when the supplier needs none), buyerCookie the session's
// cookie, which the supplier must send back in the cart, and returnURL the BrowserFormPost URL.
func BuildPunchOutSetupRequest(payloadID, timestamp, buyerIdentity, supplierIdentity, secret, buyerCookie, returnURL string) string {
	var sender strings.Builder
	sender.WriteString(`<Credential domain="NetworkId"><Identity>` + esc(buyerIdentity) + `</Identity>`)
	if secret != "" {
		sender.WriteString(`<SharedSecret>` + esc(secret) + `</SharedSecret>`)
	}
	sender.WriteString(`</Credential>`)
	return `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<cXML payloadID="` + esc(payloadID) + `" timestamp="` + esc(timestamp) + `">` +
		`<Header><From><Credential domain="NetworkId"><Identity>` + esc(buyerIdentity) + `</Identity></Credential></From>` +
		`<To><Credential domain="NetworkId"><Identity>` + esc(supplierIdentity) + `</Identity></Credential></To>` +
		`<Sender>` + sender.String() + `<UserAgent>Passage EDI</UserAgent></Sender></Header>` +
		`<Request><PunchOutSetupRequest operation="create"><BuyerCookie>` + esc(buyerCookie) + `</BuyerCookie>` +
		`<BrowserFormPost><URL>` + esc(returnURL) + `</URL></BrowserFormPost></PunchOutSetupRequest></Request></cXML>`
}

type setupResponseWire struct {
	XMLName  xml.Name `xml:"cXML"`
	Response struct {
		Status struct {
			Code string `xml:"code,attr"`
			Text string `xml:"text,attr"`
		} `xml:"Status"`
		Setup *struct {
			StartPage struct {
				URL string `xml:"URL"`
			} `xml:"StartPage"`
		} `xml:"PunchOutSetupResponse"`
	} `xml:"Response"`
}

// ErrPunchoutSetup says the supplier refused or garbled the setup request.
var ErrPunchoutSetup = errors.New("punchout: setup refused")

// ParsePunchOutSetupResponse reads the supplier's answer to the setup request and returns the StartPage URL (http or https).
func ParsePunchOutSetupResponse(raw []byte) (string, error) {
	var w setupResponseWire
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&w); err != nil {
		return "", fmt.Errorf("%w: not well-formed cXML: %v", ErrPunchoutSetup, err)
	}
	code, err := strconv.Atoi(w.Response.Status.Code)
	if err != nil {
		return "", fmt.Errorf("%w: Status code %q is not numeric", ErrPunchoutSetup, w.Response.Status.Code)
	}
	if code != 200 {
		return "", fmt.Errorf("%w: supplier answered %d %s", ErrPunchoutSetup, code, w.Response.Status.Text)
	}
	if w.Response.Setup == nil {
		return "", fmt.Errorf("%w: no PunchOutSetupResponse", ErrPunchoutSetup)
	}
	u, err := url.Parse(strings.TrimSpace(w.Response.Setup.StartPage.URL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("%w: StartPage URL is not an http(s) URL", ErrPunchoutSetup)
	}
	return u.String(), nil
}

type cookieWire struct {
	XMLName xml.Name `xml:"cXML"`
	Message struct {
		POOM struct {
			BuyerCookie string `xml:"BuyerCookie"`
		} `xml:"PunchOutOrderMessage"`
	} `xml:"Message"`
}

// PunchOutBuyerCookie reads the BuyerCookie of a returned PunchOutOrderMessage.
func PunchOutBuyerCookie(raw []byte) (string, error) {
	var w cookieWire
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&w); err != nil {
		return "", fmt.Errorf("punchout: not well-formed cXML: %w", err)
	}
	return strings.TrimSpace(w.Message.POOM.BuyerCookie), nil
}

// OCIStartURL is the URL an OCI catalog is opened on: the supplier's URL with HOOK_URL set to the return URL.
func OCIStartURL(setupURL, returnURL string) (string, error) {
	u, err := url.Parse(setupURL)
	if err != nil || u.Host == "" {
		return "", errors.New("punchout: the supplier URL is not a URL")
	}
	q := u.Query()
	q.Set("HOOK_URL", returnURL)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
