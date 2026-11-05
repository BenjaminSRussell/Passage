package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const x12Payload = "ISA*00*          *00*          *ZZ*PASSAGE        *ZZ*DISTRIBUTOR    *261105*0900*U*00401*000000042*0*T*>~GS*IN*PASSAGE*DISTRIBUTOR*20261105*0900*42*X*004010~ST*810*0001~SE*2*0001~GE*1*42~IEA*1*000000042~"

type as2Fixture struct {
	us, partner keyPair
	server      *AS2Server
	srv         *httptest.Server
	mu          sync.Mutex
	got         []Message
	deliverErr  error
}

// newAS2Fixture starts a partner AS2 server (DISTRIBUTOR) that trusts our
// certificate, and returns a client from PASSAGE to it.
func newAS2Fixture(t *testing.T) (*as2Fixture, *AS2Client) {
	t.Helper()
	f := &as2Fixture{us: newKeyPair(t, "PASSAGE", false), partner: newKeyPair(t, "DISTRIBUTOR", true)}
	f.server = &AS2Server{
		Us: AS2Identity{ID: "DISTRIBUTOR", Cert: f.partner.cert, Key: f.partner.key},
		Partner: func(from string) (AS2Partner, bool) {
			return AS2Partner{ID: "PASSAGE", Cert: f.us.cert, RequireSigned: true}, from == "PASSAGE"
		},
		Deliver: func(_ context.Context, _ string, m Message) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.deliverErr != nil {
				return f.deliverErr
			}
			f.got = append(f.got, m)
			return nil
		},
		Now: func() time.Time { return testAt },
	}
	f.srv = httptest.NewServer(f.server)
	t.Cleanup(f.srv.Close)
	c := &AS2Client{
		Us:               AS2Identity{ID: "PASSAGE", Cert: f.us.cert, Key: f.us.key},
		Partner:          AS2Partner{ID: "DISTRIBUTOR", URL: f.srv.URL, Cert: f.partner.cert},
		HTTP:             f.srv.Client(),
		RequireSignedMDN: true,
		Now:              func() time.Time { return testAt },
	}
	return f, c
}

func x12Message(id string) Message {
	return Message{ID: id, ContentType: ContentX12, Payload: []byte(x12Payload)}
}

func TestAS2_SignedMessageAndSignedMDNRoundTrip(t *testing.T) {
	f, c := newAS2Fixture(t)
	r, err := c.Send(context.Background(), x12Message("passage-000000042"))
	if err != nil {
		t.Fatal(err)
	}
	if !r.SignedMDN || r.Transport != "as2" || r.Reference != "<passage-000000042>" || !strings.HasSuffix(r.MIC, ", sha-256") || !strings.HasSuffix(r.Disposition, "; processed") {
		t.Fatalf("receipt %+v", r)
	}
	if len(f.got) != 1 || string(f.got[0].Payload) != x12Payload || f.got[0].ContentType != ContentX12 || f.got[0].ID != "passage-000000042" || f.got[0].Filename != "passage-000000042.x12" {
		t.Fatalf("delivered %+v", f.got)
	}
	// A binary payload with CRLF and a boundary-like line survives unchanged.
	tricky := Message{ID: "edifact-1", ContentType: ContentEdifact, Payload: []byte("UNA:+.? '\r\nUNB+UNOC:3+A+B+261105:0900+1'\r\n--not-a-boundary\r\n\x00\xff'UNZ+0+1'")}
	if _, err := c.Send(context.Background(), tricky); err != nil {
		t.Fatal(err)
	}
	if string(f.got[1].Payload) != string(tricky.Payload) {
		t.Fatalf("payload changed: %q", f.got[1].Payload)
	}
}

func TestAS2_PartnerRefusalsAreErrRejected(t *testing.T) {
	t.Run("signature by a key the partner does not trust", func(t *testing.T) {
		_, c := newAS2Fixture(t)
		c.Us.Key = newKeyPair(t, "PASSAGE", false).key
		_, err := c.Send(context.Background(), x12Message("m1"))
		if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "authentication-failed") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unsigned message to a partner that requires signing", func(t *testing.T) {
		_, c := newAS2Fixture(t)
		c.Us.Key = nil
		_, err := c.Send(context.Background(), x12Message("m2"))
		if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "insufficient-message-security") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("delivery fails", func(t *testing.T) {
		f, c := newAS2Fixture(t)
		f.deliverErr = errors.New("store down")
		_, err := c.Send(context.Background(), x12Message("m3"))
		if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "unexpected-processing-error") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unknown AS2-To", func(t *testing.T) {
		_, c := newAS2Fixture(t)
		c.Partner.ID = "SOMEONE-ELSE"
		if _, err := c.Send(context.Background(), x12Message("m4")); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestAS2_ForgedOrMismatchedMDNIsRefused(t *testing.T) {
	t.Run("MDN signed by another key", func(t *testing.T) {
		f, c := newAS2Fixture(t)
		f.server.Us.Key = newKeyPair(t, "DISTRIBUTOR", true).key
		if _, err := c.Send(context.Background(), x12Message("m5")); !errors.Is(err, ErrSignature) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unsigned MDN when a signed one is required", func(t *testing.T) {
		f, c := newAS2Fixture(t)
		f.server.Us.Key = nil
		if _, err := c.Send(context.Background(), x12Message("m6")); !errors.Is(err, ErrSignature) {
			t.Fatalf("err = %v", err)
		}
		c.RequireSignedMDN = false
		c.Partner.Cert = nil
		if r, err := c.Send(context.Background(), x12Message("m7")); err != nil || r.SignedMDN {
			t.Fatalf("unsigned MDN accepted when not required: %+v %v", r, err)
		}
	})
	t.Run("MDN for another message or MIC", func(t *testing.T) {
		mdn, err := os.ReadFile(filepath.Join("..", "..", "testdata", "as2", "mdn-processed.txt"))
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", `multipart/report; report-type=disposition-notification; boundary="mdnboundary"`)
			_, _ = w.Write(mdn)
		}))
		defer srv.Close()
		c := &AS2Client{Us: AS2Identity{ID: "PASSAGE"}, Partner: AS2Partner{ID: "DISTRIBUTOR", URL: srv.URL}, HTTP: srv.Client()}
		if _, err := c.Send(context.Background(), x12Message("other")); !errors.Is(err, ErrSignature) || !strings.Contains(err.Error(), "MDN answers") {
			t.Fatalf("err = %v", err)
		}
		if _, err := c.Send(context.Background(), x12Message("passage-000000042@PASSAGE")); !errors.Is(err, ErrSignature) || !strings.Contains(err.Error(), "MIC") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestReadMDN_GoldenPartnerMDN parses an MDN as a third-party gateway writes
// it (LF line ends, a different Reporting-UA).
func TestReadMDN_GoldenPartnerMDN(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "as2", "mdn-processed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	mdn, err := ReadMDN(`multipart/report; report-type=disposition-notification; boundary="mdnboundary"`, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mdn.OriginalMessageID != "<passage-000000042@PASSAGE>" || mdn.MIC != "7v7F++Fq0FNA6tfz9m6aJFHPsThPHmXYXRK1zsaTRh4=, sha-256" || !mdn.Processed() || mdn.Signed {
		t.Fatalf("%+v", mdn)
	}
	for _, d := range []string{"automatic-action/MDN-sent-automatically; processed/error: authentication-failed", "manual-action/MDN-sent-manually; failed/Failure: unsupported format", "garbage"} {
		if (MDN{Disposition: d}).Processed() {
			t.Fatalf("%q must not count as processed", d)
		}
	}
	if !(MDN{Disposition: "automatic-action/MDN-sent-automatically; processed/warning: duplicate-document"}).Processed() {
		t.Fatal("a warning is still processed")
	}
	if _, err := ReadMDN("text/plain", raw, nil); err == nil {
		t.Fatal("a non-MDN body must be refused")
	}
}

func TestAS2Server_RejectsGetAndMissingMessageID(t *testing.T) {
	f, _ := newAS2Fixture(t)
	resp, err := http.Get(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL, strings.NewReader("x"))
	req.Header.Set("AS2-From", "PASSAGE")
	req.Header.Set("AS2-To", "DISTRIBUTOR")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no Message-ID = %d", resp.StatusCode)
	}
}

func TestMessageValidationAndLoopback(t *testing.T) {
	l := &Loopback{Now: func() time.Time { return testAt }}
	for _, m := range []Message{
		{ID: "../etc/passwd", ContentType: ContentX12, Payload: []byte("x")},
		{ID: "ok", ContentType: "text/plain", Payload: []byte("x")},
		{ID: "ok", ContentType: ContentX12},
		{ID: "ok", Filename: "a/b", ContentType: ContentX12, Payload: []byte("x")},
	} {
		if _, err := l.Send(context.Background(), m); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", m, err)
		}
	}
	r, err := l.Send(context.Background(), x12Message("a"))
	if err != nil || r.Reference != "loopback/1/a.x12" || len(l.Messages()) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	l.Reject = func(Message) error { return errors.New("no") }
	if _, err := l.Send(context.Background(), x12Message("b")); !errors.Is(err, ErrRejected) {
		t.Fatal(err)
	}
}
