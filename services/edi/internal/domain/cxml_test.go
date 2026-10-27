package domain

import (
	"testing"

	"pgregory.net/rapid"
)

const cxmlConfirmation = `<?xml version="1.0"?>
<cXML payloadID="pid-42@supplier" timestamp="2026-10-01T10:00:00-07:00">
  <Header>
    <From><Credential domain="NetworkId"><Identity>AN-SUPPLIER</Identity></Credential></From>
    <To><Credential domain="NetworkId"><Identity>AN-BUYER</Identity></Credential></To>
    <Sender><Credential domain="NetworkId"><Identity>AN-SENDER</Identity>
      <SharedSecret>s3cret</SharedSecret></Credential><UserAgent>test</UserAgent></Sender>
  </Header>
  <Request deploymentMode="test">
    <ConfirmationRequest><ConfirmationHeader type="accept"/></ConfirmationRequest>
  </Request>
</cXML>`

func TestParseCXML_EnvelopeAndType(t *testing.T) {
	env, err := ParseCXML([]byte(cxmlConfirmation))
	if err != nil {
		t.Fatal(err)
	}
	if env.PayloadID != "pid-42@supplier" || env.DocType != "ConfirmationRequest" || env.Kind != "request" {
		t.Fatalf("env = %+v", env)
	}
	if env.From != "AN-SUPPLIER" || env.To != "AN-BUYER" || env.SenderID != "AN-SENDER" || env.SharedSecret != "s3cret" {
		t.Fatalf("credentials = %+v", env)
	}
	if !SupportedInbound(env) {
		t.Fatal("ConfirmationRequest must be supported inbound")
	}
}

func TestParseCXML_Rejects(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"not xml", "<cXML"},
		{"no payloadID", `<cXML><Request><ShipNoticeRequest/></Request></cXML>`},
		{"no request or message", `<cXML payloadID="p"><Header/></cXML>`},
		{"empty request", `<cXML payloadID="p"><Request/></cXML>`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseCXML([]byte(tc.raw)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestSupportedInbound(t *testing.T) {
	tests := []struct {
		doc  string
		kind string
		want bool
	}{
		{"ShipNoticeRequest", "request", true},
		{"InvoiceDetailRequest", "request", true},
		{"OrderRequest", "request", false},
		{"PunchOutOrderMessage", "message", false},
		{"ConfirmationRequest", "message", false},
	}
	for _, tc := range tests {
		got := SupportedInbound(CXMLEnvelope{DocType: tc.doc, Kind: tc.kind})
		if got != tc.want {
			t.Errorf("%s/%s = %v, want %v", tc.kind, tc.doc, got, tc.want)
		}
	}
}

func TestResolveRepeat(t *testing.T) {
	tests := []struct {
		name, stored, incoming string
		want                   RepeatDecision
	}{
		{"first delivery", "", "h1", RepeatNew},
		{"identical repeat", "h1", "h1", RepeatReplay},
		{"same id different content", "h1", "h2", RepeatConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveRepeat(tc.stored, tc.incoming); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestClassifyStatus(t *testing.T) {
	tests := map[int]ResponseOutcome{
		200: OutcomeAccepted, 201: OutcomeAccepted, 400: OutcomeRejected, 404: OutcomeRejected,
		500: OutcomeRetry, 503: OutcomeRetry, 202: OutcomeRetry,
	}
	for code, want := range tests {
		if got := ClassifyStatus(code); got != want {
			t.Errorf("status %d = %s, want %s", code, got, want)
		}
	}
}

func TestCXMLResponse_RoundTripsStatus(t *testing.T) {
	raw := CXMLResponse("pid-42@supplier", "2026-10-01T10:00:00Z", 200, "OK & done")
	code, text, err := ParseCXMLStatus([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if code != 200 || text != "OK & done" {
		t.Fatalf("code=%d text=%q", code, text)
	}
}

// Property: the payload hash ignores surrounding whitespace only, so a repeat
// with identical content always replays and any content change conflicts.
func TestPayloadHash_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		body := rapid.StringMatching(`[A-Za-z0-9 ]{1,40}`).Draw(t, "body")
		a := PayloadHash([]byte(body))
		b := PayloadHash([]byte("\n  " + body + "\n"))
		if a != b {
			t.Fatalf("whitespace changed hash")
		}
		other := body + "x"
		if ResolveRepeat(a, PayloadHash([]byte(other))) != RepeatConflict {
			t.Fatalf("different content did not conflict")
		}
		if ResolveRepeat(a, b) != RepeatReplay {
			t.Fatalf("identical content did not replay")
		}
	})
}
