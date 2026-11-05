package notifyout

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	AdapterSMTP = "smtp"
	AdapterHTTP = "http"

	TLSStartTLS = "starttls"
	TLSImplicit = "implicit"
	TLSNone     = "none"
)

type SMTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
	TLS      string
}

type WebhookConfig struct {
	URL   string
	Token string
}

type Config struct {
	SMTP    *SMTPConfig
	Webhook *WebhookConfig
}

func (c Config) Enabled() []string {
	var out []string
	if c.SMTP != nil {
		out = append(out, AdapterSMTP)
	}
	if c.Webhook != nil {
		out = append(out, AdapterHTTP)
	}
	return out
}

type secretSource struct {
	dir    string
	getenv func(string) string
}

func (s secretSource) get(file, envKey string) (string, error) {
	if s.dir == "" {
		return strings.TrimSpace(s.getenv(envKey)), nil
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, file))
	switch {
	case err == nil:
		return strings.TrimSpace(string(raw)), nil
	case errors.Is(err, os.ErrNotExist):
		return "", nil
	default:
		return "", fmt.Errorf("read secret file %s: %w", file, err)
	}
}

func Load(getenv func(string) string) (Config, error) {
	src := secretSource{dir: getenv("NOTIFY_SECRETS_DIR"), getenv: getenv}
	allowInsecure := getenv("NOTIFY_ALLOW_INSECURE") == "true"
	var cfg Config
	if getenv("NOTIFY_SMTP_ENABLED") == "true" {
		smtp, err := loadSMTP(src, allowInsecure)
		if err != nil {
			return Config{}, err
		}
		cfg.SMTP = smtp
	}
	if getenv("NOTIFY_WEBHOOK_ENABLED") == "true" {
		hook, err := loadWebhook(src, allowInsecure)
		if err != nil {
			return Config{}, err
		}
		cfg.Webhook = hook
	}
	return cfg, nil
}

// Secret keys (files under NOTIFY_SECRETS_DIR) and their env fallbacks, in the order the batch chart's ExternalSecret
// maps them (charts/batch/templates/externalsecret.yaml; TestChartExternalSecretMapsEveryAdapterKey keeps them equal).
var (
	smtpSecrets = []struct{ file, env string }{
		{"smtp-host", "NOTIFY_SMTP_HOST"},
		{"smtp-port", "NOTIFY_SMTP_PORT"},
		{"smtp-user", "NOTIFY_SMTP_USER"},
		{"smtp-password", "NOTIFY_SMTP_PASSWORD"},
	}
	webhookTokenSecret = struct{ file, env string }{"webhook-token", "NOTIFY_WEBHOOK_TOKEN"}
)

func smtpSecretKeys() []string {
	out := make([]string, len(smtpSecrets))
	for i, s := range smtpSecrets {
		out[i] = s.file
	}
	return out
}

func webhookSecretKeys() []string { return []string{webhookTokenSecret.file} }

func loadSMTP(src secretSource, allowInsecure bool) (*SMTPConfig, error) {
	values := map[string]string{}
	var missing []string
	for _, f := range smtpSecrets {
		v, err := src.get(f.file, f.env)
		if err != nil {
			return nil, err
		}
		values[f.file] = v
		if v == "" {
			missing = append(missing, f.file)
		}
	}
	from := strings.TrimSpace(src.getenv("NOTIFY_SMTP_FROM"))
	if from == "" {
		missing = append(missing, "NOTIFY_SMTP_FROM")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("smtp notify adapter enabled but missing %s", strings.Join(missing, ", "))
	}
	port, err := strconv.Atoi(values["smtp-port"])
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("smtp notify adapter enabled but smtp-port is not a valid port")
	}
	tlsMode := strings.ToLower(strings.TrimSpace(src.getenv("NOTIFY_SMTP_TLS")))
	switch tlsMode {
	case "":
		tlsMode = TLSStartTLS
	case TLSStartTLS, TLSImplicit:
	case TLSNone:
		if !allowInsecure && !isLoopback(values["smtp-host"]) {
			return nil, errors.New("smtp notify adapter tls mode none is only allowed for a loopback host or NOTIFY_ALLOW_INSECURE=true")
		}
	default:
		return nil, errors.New("smtp notify adapter NOTIFY_SMTP_TLS must be starttls, implicit or none")
	}
	return &SMTPConfig{Host: values["smtp-host"], Port: port, User: values["smtp-user"], Password: values["smtp-password"], From: from, TLS: tlsMode}, nil
}

func loadWebhook(src secretSource, allowInsecure bool) (*WebhookConfig, error) {
	token, err := src.get(webhookTokenSecret.file, webhookTokenSecret.env)
	if err != nil {
		return nil, err
	}
	rawURL := strings.TrimSpace(src.getenv("NOTIFY_WEBHOOK_URL"))
	var missing []string
	if token == "" {
		missing = append(missing, webhookTokenSecret.file)
	}
	if rawURL == "" {
		missing = append(missing, "NOTIFY_WEBHOOK_URL")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("http notify adapter enabled but missing %s", strings.Join(missing, ", "))
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return nil, errors.New("http notify adapter NOTIFY_WEBHOOK_URL is not a valid absolute url")
	}
	if parsed.Scheme != "https" && (!allowInsecure || parsed.Scheme != "http") {
		return nil, errors.New("http notify adapter NOTIFY_WEBHOOK_URL must use https (NOTIFY_ALLOW_INSECURE=true permits http for dev)")
	}
	return &WebhookConfig{URL: rawURL, Token: token}, nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
