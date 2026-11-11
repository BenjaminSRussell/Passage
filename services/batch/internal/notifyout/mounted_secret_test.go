package notifyout

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/batch/internal/domain"
)

// These tests start from the Secret volume the batch chart mounts (ExternalSecret batch-notify, one file per key at
// NOTIFY_SECRETS_DIR) and go through Load to a real send, so a credential that does not come from that volume
// cannot reach the wire.

func TestMountedSMTPSecretReachesTheServer(t *testing.T) {
	f := startFakeSMTP(t, modePlain, nil)
	host, port, _ := net.SplitHostPort(f.ln.Addr().String())
	dir := writeSecrets(t, map[string]string{
		"smtp-host": host + "\n", "smtp-port": port + "\n", "smtp-user": "mailer\n", "smtp-password": "from-vault\n",
	})
	cfg, err := Load(env(map[string]string{
		"NOTIFY_SMTP_ENABLED": "true", "NOTIFY_SECRETS_DIR": dir, "NOTIFY_SMTP_FROM": "notify@example.test",
		"NOTIFY_SMTP_TLS": TLSNone,
		// Env credentials must not be used while a secrets dir is configured.
		"NOTIFY_SMTP_USER": "env-user", "NOTIFY_SMTP_PASSWORD": "env-password",
	}))
	if err != nil {
		t.Fatal(err)
	}
	senders := SendersFor(cfg)
	sender, ok := senders[domain.ChannelEmail]
	if !ok {
		t.Fatalf("no email sender in %v", senders)
	}
	if err := sender.Send(context.Background(), Message{ID: "m1", To: "to@example.test", Subject: "Hi", Body: "Body"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth != "|mailer|from-vault" {
		t.Fatalf("AUTH PLAIN carried %q, want the mounted user and password", f.auth)
	}
	if !strings.Contains(f.data, "From: notify@example.test") || !strings.Contains(f.data, "Body") {
		t.Fatalf("data=%q", f.data)
	}
}

func TestMountedWebhookTokenReachesTheReceiver(t *testing.T) {
	var gotAuth, gotKey, gotType string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotKey, gotType = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"), r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	dir := writeSecrets(t, map[string]string{"webhook-token": "vault-token\n"})
	cfg, err := Load(env(map[string]string{
		"NOTIFY_WEBHOOK_ENABLED": "true", "NOTIFY_SECRETS_DIR": dir, "NOTIFY_WEBHOOK_URL": srv.URL + "/hook",
		"NOTIFY_WEBHOOK_TOKEN": "env-token",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := NewWebhookSender(*cfg.Webhook, srv.Client()).Send(context.Background(), Message{ID: "m2", To: "u1", Subject: "s", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer vault-token" || gotKey != "m2" || gotType != "application/json" {
		t.Fatalf("authorization=%q idempotency-key=%q content-type=%q", gotAuth, gotKey, gotType)
	}
}

func TestMissingMountFailsClosedWithoutEnvFallback(t *testing.T) {
	// NOTIFY_SECRETS_DIR set but nothing mounted there (ExternalSecret not synced, or a key missing in Vault).
	missing := filepath.Join(t.TempDir(), "not-mounted")
	full := map[string]string{
		"NOTIFY_SECRETS_DIR": missing, "NOTIFY_SMTP_FROM": "a@b.test", "NOTIFY_WEBHOOK_URL": "https://x.example.test/hook",
		"NOTIFY_SMTP_HOST": "h", "NOTIFY_SMTP_PORT": "25", "NOTIFY_SMTP_USER": "u", "NOTIFY_SMTP_PASSWORD": "p",
		"NOTIFY_WEBHOOK_TOKEN": "t",
	}
	cases := []struct {
		adapter, flag string
		want          []string
	}{
		{AdapterSMTP, "NOTIFY_SMTP_ENABLED", smtpSecretKeys()},
		{AdapterHTTP, "NOTIFY_WEBHOOK_ENABLED", webhookSecretKeys()},
	}
	for _, tc := range cases {
		t.Run(tc.adapter, func(t *testing.T) {
			vars := map[string]string{tc.flag: "true"}
			for k, v := range full {
				vars[k] = v
			}
			cfg, err := Load(env(vars))
			if err == nil {
				t.Fatalf("loaded %+v from env although a secrets dir is configured", cfg)
			}
			for _, key := range tc.want {
				if !strings.Contains(err.Error(), key) {
					t.Fatalf("error %q does not name %s", err, key)
				}
			}
		})
	}
}

func TestChartExternalSecretMapsEveryAdapterKey(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "charts", "batch", "templates", "externalsecret.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	tpl := string(raw)
	smtpBlock := between(t, tpl, ".Values.notify.smtp.enabled", "{{- end }}")
	// Only the keys the range walks: the secretKey/remoteRef helpers below it take quoted dict keys of their own.
	keyList := regexp.MustCompile(`range \$key := list ([^}]*)}}`).FindStringSubmatch(smtpBlock)
	if keyList == nil {
		t.Fatalf("no `range $key := list ...` in the smtp block:\n%s", smtpBlock)
	}
	var charted []string
	for _, m := range regexp.MustCompile(`"([a-z-]+)"`).FindAllStringSubmatch(keyList[1], -1) {
		charted = append(charted, m[1])
	}
	if !slices.Equal(charted, smtpSecretKeys()) {
		t.Fatalf("chart maps smtp keys %v, loader reads %v", charted, smtpSecretKeys())
	}
	hookBlock := between(t, tpl, ".Values.notify.webhook.enabled", "{{- end }}")
	for _, key := range webhookSecretKeys() {
		if !strings.Contains(hookBlock, "secretKey: "+key) {
			t.Fatalf("chart does not map webhook key %s", key)
		}
	}
}

func between(t *testing.T, s, start, end string) string {
	t.Helper()
	i := strings.Index(s, start)
	if i < 0 {
		t.Fatalf("%q not in chart template", start)
	}
	s = s[i:]
	j := strings.Index(s, end)
	if j < 0 {
		t.Fatalf("%q after %q not in chart template", end, start)
	}
	return s[:j]
}
