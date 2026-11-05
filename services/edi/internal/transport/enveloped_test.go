package transport

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEnveloped_EncryptDecryptEveryCipher(t *testing.T) {
	kp := newKeyPair(t, "partner", false)
	key := kp.key.(*rsa.PrivateKey)
	for _, alg := range []string{EncryptAES128, EncryptAES192, EncryptAES256} {
		for _, n := range []int{0, 1, 15, 16, 17, 4096} {
			content := bytes.Repeat([]byte{'x'}, n)
			sealed, err := Encrypt(content, kp.cert, alg)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Decrypt(sealed, kp.cert, key)
			if err != nil || !bytes.Equal(got, content) {
				t.Fatalf("%s %d: %v", alg, n, err)
			}
		}
	}
	sealed, _ := Encrypt([]byte("secret"), kp.cert, EncryptAES256)
	other := newKeyPair(t, "other", false)
	if _, err := Decrypt(sealed, other.cert, other.key.(*rsa.PrivateKey)); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("another recipient opened it: %v", err)
	}
	// CBC is not authenticated (the signature inside is): flipping the last
	// byte of the second-to-last block turns the full padding block 0x10... into
	// an invalid pad, deterministically.
	sealed, _ = Encrypt(bytes.Repeat([]byte{'x'}, 32), kp.cert, EncryptAES128)
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-17] ^= 0x01
	if _, err := Decrypt(tampered, kp.cert, key); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("tampered envelope opened: %v", err)
	}
	if _, err := Encrypt([]byte("x"), kp.cert, "des-ede3"); err == nil {
		t.Fatal("3DES must be refused")
	}
	if _, err := Encrypt([]byte("x"), newKeyPair(t, "ec", true).cert, EncryptAES128); err == nil {
		t.Fatal("an EC recipient must be refused")
	}
	if _, err := Decrypt([]byte("not der"), kp.cert, key); !errors.Is(err, ErrDecrypt) {
		t.Fatal(err)
	}
}

func TestBERToDER_IndefiniteLengthAndConstructedOctets(t *testing.T) {
	// SEQUENCE (indefinite) { OCTET STRING constructed (indefinite) { "ab", "c" }, INTEGER 5 }
	ber := []byte{0x30, 0x80, 0x24, 0x80, 0x04, 0x02, 'a', 'b', 0x04, 0x01, 'c', 0x00, 0x00, 0x02, 0x01, 0x05, 0x00, 0x00}
	der, err := berToDER(ber)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x30, 0x08, 0x04, 0x03, 'a', 'b', 'c', 0x02, 0x01, 0x05}
	if !bytes.Equal(der, want) {
		t.Fatalf("% x", der)
	}
	if again, err := berToDER(want); err != nil || !bytes.Equal(again, want) {
		t.Fatalf("DER must pass unchanged: % x %v", again, err)
	}
	for _, bad := range [][]byte{{0x30}, {0x30, 0x80, 0x04, 0x05, 'a'}, {0x04, 0x80, 0x00, 0x00}, {0x30, 0x85, 1, 2, 3, 4, 5}} {
		if _, err := berToDER(bad); err == nil {
			t.Fatalf("% x accepted", bad)
		}
	}
}

// TestEnveloped_InteropWithOpenSSL decrypts what `openssl cms -encrypt`
// writes (BER, streamed) and has OpenSSL decrypt ours. Skipped where openssl
// is not installed.
func TestEnveloped_InteropWithOpenSSL(t *testing.T) {
	bin, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl not installed")
	}
	dir := t.TempDir()
	kp := newKeyPair(t, "us", false)
	certPath := writePEM(t, dir, "cert.pem", "CERTIFICATE", kp.cert.Raw)
	keyDER, _ := x509.MarshalPKCS8PrivateKey(kp.key)
	keyPath := writePEM(t, dir, "key.pem", "PRIVATE KEY", keyDER)
	content := []byte("Content-Type: application/edi-x12\r\n\r\n" + x12Payload)
	contentPath := filepath.Join(dir, "content")
	if err := os.WriteFile(contentPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	// -keyopt applies to the -recip named before it.
	for _, args := range [][]string{
		{"-aes256", "-recip", certPath},
		{"-aes128", "-stream", "-recip", certPath},
		{"-aes192", "-recip", certPath, "-keyopt", "rsa_padding_mode:oaep"},
	} {
		out := filepath.Join(dir, "openssl.p7m")
		cmd := append([]string{"cms", "-encrypt", "-binary", "-outform", "DER", "-in", contentPath, "-out", out}, args...)
		if b, err := exec.Command(bin, cmd...).CombinedOutput(); err != nil {
			t.Fatalf("openssl encrypt %v: %v\n%s", args, err, b)
		}
		sealed, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Decrypt(sealed, kp.cert, kp.key.(*rsa.PrivateKey))
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("%v: %v %q", args, err, got)
		}
	}

	ours, err := Encrypt(content, kp.cert, EncryptAES256)
	if err != nil {
		t.Fatal(err)
	}
	oursPath := filepath.Join(dir, "ours.p7m")
	if err := os.WriteFile(oursPath, ours, 0o600); err != nil {
		t.Fatal(err)
	}
	opened := filepath.Join(dir, "opened")
	if b, err := exec.Command(bin, "cms", "-decrypt", "-binary", "-inform", "DER", "-in", oursPath, "-recip", certPath, "-inkey", keyPath, "-out", opened).CombinedOutput(); err != nil {
		t.Fatalf("openssl rejected our envelope: %v\n%s", err, b)
	}
	if got, _ := os.ReadFile(opened); !bytes.Equal(got, content) {
		t.Fatalf("openssl opened %q", got)
	}
}

func TestAS2_EncryptedSignedSHA384RoundTrip(t *testing.T) {
	f := &as2Fixture{us: newKeyPair(t, "PASSAGE", false), partner: newKeyPair(t, "DISTRIBUTOR", false)}
	f.server = &AS2Server{
		Us: AS2Identity{ID: "DISTRIBUTOR", Cert: f.partner.cert, Key: f.partner.key},
		Partner: func(from string) (AS2Partner, bool) {
			return AS2Partner{ID: "PASSAGE", Cert: f.us.cert, RequireSigned: true, RequireEncrypted: true}, from == "PASSAGE"
		},
		Deliver: func(_ context.Context, _ string, m Message) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.got = append(f.got, m)
			return nil
		},
		Now: func() time.Time { return testAt },
	}
	srv := httptest.NewServer(f.server)
	defer srv.Close()
	c := &AS2Client{Us: AS2Identity{ID: "PASSAGE", Cert: f.us.cert, Key: f.us.key}, Partner: AS2Partner{ID: "DISTRIBUTOR", URL: srv.URL, Cert: f.partner.cert},
		HTTP: srv.Client(), RequireSignedMDN: true, SignHash: crypto.SHA384, Encryption: EncryptAES256, Now: func() time.Time { return testAt }}
	r, err := c.Send(context.Background(), x12Message("enc-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(r.MIC, ", sha-384") || r.MIC != r.SentMIC || !r.SignedMDN {
		t.Fatalf("receipt %+v", r)
	}
	if len(f.got) != 1 || string(f.got[0].Payload) != x12Payload || f.got[0].MIC != r.MIC {
		t.Fatalf("delivered %+v", f.got)
	}
	// The partner requires encryption: a signed but clear message is refused.
	c.Encryption = EncryptNone
	if _, err := c.Send(context.Background(), x12Message("enc-2")); !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "insufficient-message-security") {
		t.Fatalf("err = %v", err)
	}
	// Encrypted to a certificate the partner has no key for.
	c.Encryption = EncryptAES128
	c.Partner.Cert = newKeyPair(t, "DISTRIBUTOR", false).cert
	if _, err := c.Send(context.Background(), x12Message("enc-3")); err == nil || !strings.Contains(err.Error(), "decryption-failed") && !errors.Is(err, ErrSignature) {
		t.Fatalf("err = %v", err)
	}
}

func TestAS2_AsyncMDNAndDuplicateWarning(t *testing.T) {
	f, c := newAS2Fixture(t)
	var mu sync.Mutex
	var mdns []AsyncMDN
	f.server.SendAsyncMDN = func(_ context.Context, m AsyncMDN) {
		mu.Lock()
		defer mu.Unlock()
		mdns = append(mdns, m)
	}
	c.AsyncMDNURL = "https://passage.example/as2/v1/abc/mdn"
	r, err := c.Send(context.Background(), x12Message("async-1"))
	if err != nil || !r.Pending || r.SentMIC == "" || r.Disposition != "" {
		t.Fatalf("%+v %v", r, err)
	}
	if len(mdns) != 1 || mdns[0].URL != c.AsyncMDNURL {
		t.Fatalf("async MDNs %+v", mdns)
	}
	mdn, err := ReadMDN(mdns[0].Header.Get("Content-Type"), mdns[0].Body, f.partner.cert)
	if err != nil {
		t.Fatal(err)
	}
	if err := mdn.Check("<async-1>", r.SentMIC, true); err != nil {
		t.Fatalf("async MDN does not check: %v %+v", err, mdn)
	}
	if err := mdn.Check("<async-1>", "other, sha-256", true); !errors.Is(err, ErrSignature) {
		t.Fatalf("MIC mismatch: %v", err)
	}
	if err := mdn.Check("<async-2>", r.SentMIC, true); !errors.Is(err, ErrSignature) {
		t.Fatalf("wrong message: %v", err)
	}

	// A receiver posting the MDN: PostAsyncMDN delivers headers and body.
	var got *http.Request
	var body []byte
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
	}))
	defer sink.Close()
	if err := PostAsyncMDN(context.Background(), sink.Client(), sink.URL, mdns[0]); err != nil {
		t.Fatal(err)
	}
	if got.Header.Get("AS2-From") != "DISTRIBUTOR" || !bytes.Equal(body, mdns[0].Body) {
		t.Fatalf("posted %v %q", got.Header, body)
	}

	// A Deliver warning becomes processed/warning, which still counts as processed.
	c.AsyncMDNURL = ""
	f.deliverErr = &DispositionError{Modifier: "duplicate-document", Warning: true}
	r, err = c.Send(context.Background(), x12Message("dup-1"))
	if err != nil || !strings.Contains(r.Disposition, "processed/warning: duplicate-document") {
		t.Fatalf("%+v %v", r, err)
	}
	f.deliverErr = &DispositionError{Modifier: "unsupported-format"}
	if _, err := c.Send(context.Background(), x12Message("dup-2")); !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "processed/error: unsupported-format") {
		t.Fatalf("err = %v", err)
	}
}
