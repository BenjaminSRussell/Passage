package notifyout

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeSecrets(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadDisabledByDefault(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil || len(cfg.Enabled()) != 0 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadSMTPFromMountedFiles(t *testing.T) {
	dir := writeSecrets(t, map[string]string{
		"smtp-host": "mail.example.test\n", "smtp-port": "587", "smtp-user": "u", "smtp-password": "p",
	})
	cfg, err := Load(env(map[string]string{
		"NOTIFY_SMTP_ENABLED": "true", "NOTIFY_SECRETS_DIR": dir, "NOTIFY_SMTP_FROM": "noreply@example.test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if *cfg.SMTP != (SMTPConfig{Host: "mail.example.test", Port: 587, User: "u", Password: "p", From: "noreply@example.test", TLS: TLSStartTLS}) {
		t.Fatalf("smtp=%+v", *cfg.SMTP)
	}
}

func TestLoadSecretsDirIgnoresEnvAndTreatsEmptyFileAsMissing(t *testing.T) {
	dir := writeSecrets(t, map[string]string{"smtp-host": "from-file", "smtp-port": "25", "smtp-user": "u", "smtp-password": " \n"})
	_, err := Load(env(map[string]string{
		"NOTIFY_SMTP_ENABLED": "true", "NOTIFY_SECRETS_DIR": dir, "NOTIFY_SMTP_FROM": "a@b.test",
		"NOTIFY_SMTP_PASSWORD": "from-env",
	}))
	if err == nil || !strings.Contains(err.Error(), "smtp-password") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadEnvUsedWhenNoSecretsDir(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"NOTIFY_SMTP_ENABLED": "true", "NOTIFY_SMTP_FROM": "a@b.test", "NOTIFY_SMTP_HOST": "h", "NOTIFY_SMTP_PORT": "25",
		"NOTIFY_SMTP_USER": "u", "NOTIFY_SMTP_PASSWORD": "p",
	}))
	if err != nil || cfg.SMTP.Host != "h" || cfg.SMTP.Port != 25 || cfg.SMTP.TLS != TLSStartTLS {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func smtpEnv(extra map[string]string) func(string) string {
	m := map[string]string{
		"NOTIFY_SMTP_ENABLED": "true", "NOTIFY_SMTP_FROM": "a@b.test", "NOTIFY_SMTP_HOST": "mail.example.test",
		"NOTIFY_SMTP_PORT": "25", "NOTIFY_SMTP_USER": "u", "NOTIFY_SMTP_PASSWORD": "p",
	}
	for k, v := range extra {
		m[k] = v
	}
	return env(m)
}

func TestLoadSMTPTLSModes(t *testing.T) {
	cases := []struct {
		name    string
		extra   map[string]string
		wantTLS string
		wantErr bool
	}{
		{"implicit", map[string]string{"NOTIFY_SMTP_TLS": "implicit"}, TLSImplicit, false},
		{"bogus", map[string]string{"NOTIFY_SMTP_TLS": "ssl"}, "", true},
		{"none remote refused", map[string]string{"NOTIFY_SMTP_TLS": "none"}, "", true},
		{"none remote with dev flag", map[string]string{"NOTIFY_SMTP_TLS": "none", "NOTIFY_ALLOW_INSECURE": "true"}, TLSNone, false},
		{"none loopback", map[string]string{"NOTIFY_SMTP_TLS": "none", "NOTIFY_SMTP_HOST": "127.0.0.1"}, TLSNone, false},
		{"none other loopback range refused", map[string]string{"NOTIFY_SMTP_TLS": "none", "NOTIFY_SMTP_HOST": "127.0.0.2"}, "", true},
		{"none ipv6 loopback", map[string]string{"NOTIFY_SMTP_TLS": "none", "NOTIFY_SMTP_HOST": "::1"}, TLSNone, false},
		{"none localhost", map[string]string{"NOTIFY_SMTP_TLS": "none", "NOTIFY_SMTP_HOST": "localhost"}, TLSNone, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(smtpEnv(tc.extra))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v", err)
			}
			if err == nil && cfg.SMTP.TLS != tc.wantTLS {
				t.Fatalf("tls=%q", cfg.SMTP.TLS)
			}
		})
	}
}

func TestLoadFailsFastNamingMissingKeysWithoutLeakingValues(t *testing.T) {
	_, err := Load(env(map[string]string{
		"NOTIFY_SMTP_ENABLED": "true", "NOTIFY_SMTP_HOST": "h", "NOTIFY_SMTP_PASSWORD": "hunter2-secret",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, want := range []string{"smtp-port", "smtp-user", "NOTIFY_SMTP_FROM"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("%q missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "hunter2-secret") || strings.Contains(msg, "smtp-password") {
		t.Fatalf("error leaks or misreports: %q", msg)
	}
}

func TestLoadRejectsBadPort(t *testing.T) {
	_, err := Load(env(map[string]string{
		"NOTIFY_SMTP_ENABLED": "true", "NOTIFY_SMTP_HOST": "h", "NOTIFY_SMTP_PORT": "99999",
		"NOTIFY_SMTP_USER": "u", "NOTIFY_SMTP_PASSWORD": "p", "NOTIFY_SMTP_FROM": "a@b.test",
	}))
	if err == nil {
		t.Fatal("expected port error")
	}
}

func TestLoadFailsOnUnreadableSecretPath(t *testing.T) {
	dir := writeSecrets(t, nil)
	if err := os.Mkdir(filepath.Join(dir, "webhook-token"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Load(env(map[string]string{"NOTIFY_WEBHOOK_ENABLED": "true", "NOTIFY_SECRETS_DIR": dir, "NOTIFY_WEBHOOK_URL": "https://x.example.test/hook"}))
	if err == nil {
		t.Fatal("expected read error")
	}
}

func TestLoadWebhookMissingToken(t *testing.T) {
	_, err := Load(env(map[string]string{"NOTIFY_WEBHOOK_ENABLED": "true", "NOTIFY_WEBHOOK_URL": "https://x.example.test/hook"}))
	if err == nil || !strings.Contains(err.Error(), "webhook-token") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadWebhookFromFile(t *testing.T) {
	dir := writeSecrets(t, map[string]string{"webhook-token": "tok\n"})
	cfg, err := Load(env(map[string]string{"NOTIFY_WEBHOOK_ENABLED": "true", "NOTIFY_SECRETS_DIR": dir, "NOTIFY_WEBHOOK_URL": "https://x.example.test/hook"}))
	if err != nil || cfg.Webhook.Token != "tok" || len(cfg.Enabled()) != 1 || cfg.Enabled()[0] != AdapterHTTP {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadWebhookURLValidation(t *testing.T) {
	dir := writeSecrets(t, map[string]string{"webhook-token": "tok"})
	cases := []struct {
		name    string
		url     string
		insec   string
		wantErr bool
	}{
		{"https ok", "https://x.example.test/hook", "", false},
		{"http refused", "http://x.example.test/hook", "", true},
		{"http dev flag", "http://x.example.test/hook", "true", false},
		{"ftp refused even with flag", "ftp://x.example.test", "true", true},
		{"no host", "https:///hook", "", true},
		{"relative", "/hook", "", true},
		{"garbage", "ht!tp://%zz", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(env(map[string]string{
				"NOTIFY_WEBHOOK_ENABLED": "true", "NOTIFY_SECRETS_DIR": dir, "NOTIFY_WEBHOOK_URL": tc.url, "NOTIFY_ALLOW_INSECURE": tc.insec,
			}))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestWebhookSenderPostsWithBearerToken(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	sender := NewWebhookSender(WebhookConfig{URL: srv.URL, Token: "tok"}, srv.Client())
	if err := sender.Send(context.Background(), Message{To: "a@b.test", Subject: "s", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" || !strings.Contains(gotBody, `"to":"a@b.test"`) {
		t.Fatalf("auth=%q body=%q", gotAuth, gotBody)
	}
}

func TestWebhookSenderErrorOmitsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	err := NewWebhookSender(WebhookConfig{URL: srv.URL, Token: "tok-secret"}, srv.Client()).Send(context.Background(), Message{})
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "tok-secret") {
		t.Fatalf("err=%v", err)
	}
}

func TestWebhookSenderTreatsRedirectAsFailureAndDoesNotFollow(t *testing.T) {
	var followed bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed = true
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()
	original := srv.Client()
	for name, client := range map[string]*http.Client{"default": nil, "injected": original} {
		err := NewWebhookSender(WebhookConfig{URL: srv.URL, Token: "tok"}, client).Send(context.Background(), Message{})
		if err == nil || !strings.Contains(err.Error(), "302") {
			t.Fatalf("%s: err=%v", name, err)
		}
	}
	if followed {
		t.Fatal("redirect was followed")
	}
	if original.CheckRedirect != nil {
		t.Fatal("injected client was mutated")
	}
}

func TestWebhookSenderErrorStripsURLPathAndQuery(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/hook/secret-path?key=secret-query"
	srv.Close()
	err := NewWebhookSender(WebhookConfig{URL: url, Token: "t"}, nil).Send(context.Background(), Message{})
	if err == nil || strings.Contains(err.Error(), "secret-path") || strings.Contains(err.Error(), "secret-query") {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("host missing: %v", err)
	}
}

type smtpMode int

const (
	modePlain smtpMode = iota
	modeStartTLS
	modeStartTLSNotAdvertised
	modeImplicitTLS
	modeHang
	modeDropOnQuit
	modeRejectRcpt
	modeTempRcpt
)

type fakeSMTP struct {
	ln        net.Listener
	mode      smtpMode
	tlsConf   *tls.Config
	mu        sync.Mutex
	auth      string
	data      string
	rcpt      string
	startedAt int
}

func testTLS(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &tls.Config{Certificates: srv.TLS.Certificates}, pool
}

func startFakeSMTP(t *testing.T, mode smtpMode, tlsConf *tls.Config) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if mode == modeImplicitTLS {
		ln = tls.NewListener(ln, tlsConf)
	}
	f := &fakeSMTP{ln: ln, mode: mode, tlsConf: tlsConf}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if mode == modeHang {
			time.Sleep(5 * time.Second)
			return
		}
		f.serve(conn)
	}()
	return f
}

func (f *fakeSMTP) serve(conn net.Conn) {
	r := bufio.NewReader(conn)
	say := func(s string) { conn.Write([]byte(s + "\r\n")) }
	say("220 fake ESMTP")
	inData := false
	var data strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				f.mu.Lock()
				f.data = data.String()
				f.mu.Unlock()
				inData = false
				say("250 queued")
				continue
			}
			data.WriteString(line + "\n")
			continue
		}
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			say("250-fake")
			if f.mode == modeStartTLS && f.startedAt == 0 {
				say("250-STARTTLS")
			}
			say("250 AUTH PLAIN")
		case line == "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(conn, f.tlsConf)
			if err := tc.Handshake(); err != nil {
				return
			}
			conn = tc
			r = bufio.NewReader(tc)
			f.startedAt = 1
		case strings.HasPrefix(line, "AUTH PLAIN "):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
			f.mu.Lock()
			f.auth = strings.ReplaceAll(string(raw), "\x00", "|")
			f.mu.Unlock()
			say("235 ok")
		case strings.HasPrefix(line, "MAIL"):
			say("250 ok")
		case strings.HasPrefix(line, "RCPT"):
			f.mu.Lock()
			f.rcpt = line
			f.mu.Unlock()
			switch f.mode {
			case modeRejectRcpt:
				say("550 no such user")
			case modeTempRcpt:
				say("451 try later")
			default:
				say("250 ok")
			}
		case line == "DATA":
			inData = true
			say("354 go")
		case line == "QUIT":
			if f.mode != modeDropOnQuit {
				say("221 bye")
			}
			return
		default:
			say("250 ok")
		}
	}
}

func (f *fakeSMTP) config(tlsMode string) SMTPConfig {
	host, portStr, _ := net.SplitHostPort(f.ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return SMTPConfig{Host: host, Port: port, User: "u", Password: "p", From: "noreply@example.test", TLS: tlsMode}
}

func trustedSender(cfg SMTPConfig, pool *x509.CertPool) *SMTPSender {
	s := NewSMTPSender(cfg)
	s.tlsConfig = &tls.Config{RootCAs: pool}
	return s
}

func TestSMTPSenderDeliversWithAuthPlain(t *testing.T) {
	f := startFakeSMTP(t, modePlain, nil)
	err := NewSMTPSender(f.config(TLSNone)).Send(context.Background(), Message{To: "to@example.test", Subject: "Hello", Body: "World"})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth != "|u|p" {
		t.Fatalf("auth=%q", f.auth)
	}
	if !strings.Contains(f.rcpt, "to@example.test") || !strings.Contains(f.data, "Subject: Hello") || !strings.Contains(f.data, "World") {
		t.Fatalf("rcpt=%q data=%q", f.rcpt, f.data)
	}
}

func TestSMTPSenderStartTLSDelivers(t *testing.T) {
	tlsConf, pool := testTLS(t)
	f := startFakeSMTP(t, modeStartTLS, tlsConf)
	if err := trustedSender(f.config(TLSStartTLS), pool).Send(context.Background(), Message{To: "to@example.test", Subject: "s", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth != "|u|p" || !strings.Contains(f.data, "b") {
		t.Fatalf("auth=%q data=%q", f.auth, f.data)
	}
}

func TestSMTPSenderRefusesWhenStartTLSNotAdvertised(t *testing.T) {
	for _, mode := range []string{TLSStartTLS, ""} {
		f := startFakeSMTP(t, modeStartTLSNotAdvertised, nil)
		err := NewSMTPSender(f.config(mode)).Send(context.Background(), Message{To: "to@example.test"})
		if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
			t.Fatalf("mode=%q err=%v", mode, err)
		}
		f.mu.Lock()
		auth := f.auth
		f.mu.Unlock()
		if auth != "" {
			t.Fatalf("mode=%q credentials sent in cleartext: %q", mode, auth)
		}
	}
}

func TestSMTPSenderImplicitTLSDelivers(t *testing.T) {
	tlsConf, pool := testTLS(t)
	f := startFakeSMTP(t, modeImplicitTLS, tlsConf)
	if err := trustedSender(f.config(TLSImplicit), pool).Send(context.Background(), Message{To: "to@example.test", Subject: "s", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth != "|u|p" {
		t.Fatalf("auth=%q", f.auth)
	}
}

func TestSMTPSenderImplicitTLSRejectsUntrustedCert(t *testing.T) {
	tlsConf, _ := testTLS(t)
	f := startFakeSMTP(t, modeImplicitTLS, tlsConf)
	if err := NewSMTPSender(f.config(TLSImplicit)).Send(context.Background(), Message{To: "to@example.test"}); err == nil {
		t.Fatal("expected certificate error")
	}
}

func TestSMTPSenderHungServerTimesOut(t *testing.T) {
	f := startFakeSMTP(t, modeHang, nil)
	sender := NewSMTPSender(f.config(TLSNone))
	sender.timeout = 200 * time.Millisecond
	start := time.Now()
	err := sender.Send(context.Background(), Message{To: "to@example.test"})
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestSMTPSenderHonoursContextCancel(t *testing.T) {
	f := startFakeSMTP(t, modeHang, nil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	err := NewSMTPSender(f.config(TLSNone)).Send(ctx, Message{To: "to@example.test"})
	if !errors.Is(err, context.Canceled) || time.Since(start) > 3*time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestSMTPSenderAddsDateMessageIDAndTransferEncoding(t *testing.T) {
	f := startFakeSMTP(t, modePlain, nil)
	msg := Message{ID: "msg-123", To: "to@example.test", Subject: "s", Body: "Caf\u00e9 =\nline two"}
	if err := NewSMTPSender(f.config(TLSNone)).Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, want := range []string{"Date: ", "Message-ID: <msg-123@example.test>", "Content-Transfer-Encoding: quoted-printable", "Caf=C3=A9 =3D"} {
		if !strings.Contains(f.data, want) {
			t.Fatalf("missing %q in %q", want, f.data)
		}
	}
}

func TestSMTPSenderIgnoresQuitFailureAfterAcceptedData(t *testing.T) {
	f := startFakeSMTP(t, modeDropOnQuit, nil)
	if err := NewSMTPSender(f.config(TLSNone)).Send(context.Background(), Message{To: "to@example.test", Subject: "s", Body: "b"}); err != nil {
		t.Fatalf("accepted message reported as failed: %v", err)
	}
}

func TestWebhookSenderSendsIdempotencyKey(t *testing.T) {
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("Idempotency-Key")
	}))
	defer srv.Close()
	if err := NewWebhookSender(WebhookConfig{URL: srv.URL, Token: "t"}, srv.Client()).Send(context.Background(), Message{ID: "m-1"}); err != nil || key != "m-1" {
		t.Fatalf("key=%q err=%v", key, err)
	}
}

func TestSMTPSenderEncodesNonASCIISubject(t *testing.T) {
	f := startFakeSMTP(t, modePlain, nil)
	if err := NewSMTPSender(f.config(TLSNone)).Send(context.Background(), Message{To: "to@example.test", Subject: "Caf\u00e9 approval", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(f.data, "Subject: =?utf-8?q?Caf=C3=A9_approval?=") {
		t.Fatalf("data=%q", f.data)
	}
}

func TestSMTPSenderRejectsInvalidAddressesAndHeaderInjection(t *testing.T) {
	cases := []struct {
		name string
		from string
		msg  Message
	}{
		{"subject crlf", "a@b.test", Message{To: "x@y.test", Subject: "hi\r\nBcc: evil@z.test"}},
		{"to crlf", "a@b.test", Message{To: "x@y.test\r\nBcc: evil@z.test"}},
		{"to lf", "a@b.test", Message{To: "x@y.test\nBcc: evil@z.test"}},
		{"from crlf", "a@b.test\r\nBcc: evil@z.test", Message{To: "x@y.test"}},
		{"two recipients", "a@b.test", Message{To: "a@x.test, b@y.test"}},
		{"two senders", "a@b.test, c@d.test", Message{To: "x@y.test"}},
		{"not an address", "a@b.test", Message{To: "nobody"}},
		{"empty from", "", Message{To: "x@y.test"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender := NewSMTPSender(SMTPConfig{Host: "127.0.0.1", Port: 1, From: tc.from, TLS: TLSNone})
			if err := sender.Send(context.Background(), tc.msg); err == nil {
				t.Fatal("expected rejection before dialing")
			} else if strings.Contains(err.Error(), "connect") || strings.Contains(err.Error(), "refused") {
				t.Fatalf("reached the network: %v", err)
			}
		})
	}
}

func TestSMTPSenderConnectionFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	port, _ := strconv.Atoi(portStr)
	err = NewSMTPSender(SMTPConfig{Host: host, Port: port, User: "u", Password: "pw-secret", From: "a@b.test", TLS: TLSNone}).
		Send(context.Background(), Message{To: "x@y.test"})
	if err == nil || strings.Contains(err.Error(), "pw-secret") {
		t.Fatalf("err=%v", err)
	}
}

func TestPermanentErrorClassification(t *testing.T) {
	status := func(code int) error {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
		defer srv.Close()
		return NewWebhookSender(WebhookConfig{URL: srv.URL, Token: "t"}, srv.Client()).Send(context.Background(), Message{})
	}
	for code, want := range map[int]bool{400: true, 404: true, 408: false, 429: false, 500: false, 503: false} {
		if got := IsPermanent(status(code)); got != want {
			t.Fatalf("webhook %d permanent=%v want %v", code, got, want)
		}
	}
	if err := NewSMTPSender(SMTPConfig{Host: "127.0.0.1", Port: 1, From: "a@b.test", TLS: TLSNone}).Send(context.Background(), Message{To: "nobody"}); !IsPermanent(err) {
		t.Fatalf("invalid address not permanent: %v", err)
	}
	if IsPermanent(errors.New("connection refused")) {
		t.Fatal("plain error is permanent")
	}
	if got := errorClass(status(404)); got != "http 404" {
		t.Fatalf("class = %q", got)
	}
}

func TestSMTPReplyCodesDecidePermanence(t *testing.T) {
	for mode, want := range map[smtpMode]bool{modeRejectRcpt: true, modeTempRcpt: false} {
		f := startFakeSMTP(t, mode, nil)
		err := NewSMTPSender(f.config(TLSNone)).Send(context.Background(), Message{To: "to@example.test", Subject: "s"})
		if err == nil || IsPermanent(err) != want || errorClass(err) == "send error" {
			t.Fatalf("mode %d: err=%v permanent=%v class=%q", mode, err, IsPermanent(err), errorClass(err))
		}
	}
}
