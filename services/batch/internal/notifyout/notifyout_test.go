package notifyout

import (
	"bufio"
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
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
	if *cfg.SMTP != (SMTPConfig{Host: "mail.example.test", Port: 587, User: "u", Password: "p", From: "noreply@example.test"}) {
		t.Fatalf("smtp=%+v", *cfg.SMTP)
	}
}

func TestLoadFileWinsOverEnvAndEnvFallsBack(t *testing.T) {
	dir := writeSecrets(t, map[string]string{"smtp-host": "from-file"})
	cfg, err := Load(env(map[string]string{
		"NOTIFY_SMTP_ENABLED": "true", "NOTIFY_SECRETS_DIR": dir, "NOTIFY_SMTP_FROM": "a@b.test",
		"NOTIFY_SMTP_HOST": "from-env", "NOTIFY_SMTP_PORT": "25", "NOTIFY_SMTP_USER": "u", "NOTIFY_SMTP_PASSWORD": "p",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMTP.Host != "from-file" || cfg.SMTP.Port != 25 {
		t.Fatalf("smtp=%+v", *cfg.SMTP)
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
	_, err := Load(env(map[string]string{"NOTIFY_WEBHOOK_ENABLED": "true", "NOTIFY_SECRETS_DIR": dir, "NOTIFY_WEBHOOK_URL": "http://x"}))
	if err == nil {
		t.Fatal("expected read error")
	}
}

func TestLoadWebhookMissingToken(t *testing.T) {
	_, err := Load(env(map[string]string{"NOTIFY_WEBHOOK_ENABLED": "true", "NOTIFY_WEBHOOK_URL": "http://x"}))
	if err == nil || !strings.Contains(err.Error(), "webhook-token") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadWebhookFromFile(t *testing.T) {
	dir := writeSecrets(t, map[string]string{"webhook-token": "tok\n"})
	cfg, err := Load(env(map[string]string{"NOTIFY_WEBHOOK_ENABLED": "true", "NOTIFY_SECRETS_DIR": dir, "NOTIFY_WEBHOOK_URL": "http://x"}))
	if err != nil || cfg.Webhook.Token != "tok" || len(cfg.Enabled()) != 1 || cfg.Enabled()[0] != AdapterHTTP {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
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

type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	auth string
	data string
	rcpt string
}

func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
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
			say("250 AUTH PLAIN")
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
			say("250 ok")
		case line == "DATA":
			inData = true
			say("354 go")
		case line == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func TestSMTPSenderDeliversWithAuth(t *testing.T) {
	f := startFakeSMTP(t)
	host, portStr, _ := net.SplitHostPort(f.ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	sender := NewSMTPSender(SMTPConfig{Host: host, Port: port, User: "u", Password: "p", From: "noreply@example.test"})
	if err := sender.Send(context.Background(), Message{To: "to@example.test", Subject: "Hello", Body: "World"}); err != nil {
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

func TestSMTPSenderRejectsHeaderInjection(t *testing.T) {
	sender := NewSMTPSender(SMTPConfig{Host: "127.0.0.1", Port: 1, From: "a@b.test"})
	err := sender.Send(context.Background(), Message{To: "x@y.test", Subject: "hi\r\nBcc: evil@z.test"})
	if err == nil || !strings.Contains(err.Error(), "line break") {
		t.Fatalf("err=%v", err)
	}
}

func TestSMTPSenderConnectionFailure(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	port, _ := strconv.Atoi(portStr)
	err := NewSMTPSender(SMTPConfig{Host: host, Port: port, User: "u", Password: "pw-secret", From: "a@b.test"}).
		Send(context.Background(), Message{To: "x@y.test"})
	if err == nil || strings.Contains(err.Error(), "pw-secret") {
		t.Fatalf("err=%v", err)
	}
}
