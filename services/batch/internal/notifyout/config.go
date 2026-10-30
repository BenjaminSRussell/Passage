package notifyout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	AdapterSMTP = "smtp"
	AdapterHTTP = "http"
)

type SMTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
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
	if s.dir != "" {
		raw, err := os.ReadFile(filepath.Join(s.dir, file))
		switch {
		case err == nil:
			if v := strings.TrimSpace(string(raw)); v != "" {
				return v, nil
			}
		case !errors.Is(err, os.ErrNotExist):
			return "", fmt.Errorf("read secret file %s: %w", file, err)
		}
	}
	return strings.TrimSpace(s.getenv(envKey)), nil
}

func Load(getenv func(string) string) (Config, error) {
	src := secretSource{dir: getenv("NOTIFY_SECRETS_DIR"), getenv: getenv}
	var cfg Config
	if getenv("NOTIFY_SMTP_ENABLED") == "true" {
		smtp, err := loadSMTP(src)
		if err != nil {
			return Config{}, err
		}
		cfg.SMTP = smtp
	}
	if getenv("NOTIFY_WEBHOOK_ENABLED") == "true" {
		hook, err := loadWebhook(src)
		if err != nil {
			return Config{}, err
		}
		cfg.Webhook = hook
	}
	return cfg, nil
}

func loadSMTP(src secretSource) (*SMTPConfig, error) {
	values := map[string]string{}
	for _, f := range []struct{ file, env string }{
		{"smtp-host", "NOTIFY_SMTP_HOST"},
		{"smtp-port", "NOTIFY_SMTP_PORT"},
		{"smtp-user", "NOTIFY_SMTP_USER"},
		{"smtp-password", "NOTIFY_SMTP_PASSWORD"},
	} {
		v, err := src.get(f.file, f.env)
		if err != nil {
			return nil, err
		}
		values[f.file] = v
	}
	from := strings.TrimSpace(src.getenv("NOTIFY_SMTP_FROM"))
	var missing []string
	for _, name := range []string{"smtp-host", "smtp-port", "smtp-user", "smtp-password"} {
		if values[name] == "" {
			missing = append(missing, name)
		}
	}
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
	return &SMTPConfig{Host: values["smtp-host"], Port: port, User: values["smtp-user"], Password: values["smtp-password"], From: from}, nil
}

func loadWebhook(src secretSource) (*WebhookConfig, error) {
	token, err := src.get("webhook-token", "NOTIFY_WEBHOOK_TOKEN")
	if err != nil {
		return nil, err
	}
	url := strings.TrimSpace(src.getenv("NOTIFY_WEBHOOK_URL"))
	var missing []string
	if token == "" {
		missing = append(missing, "webhook-token")
	}
	if url == "" {
		missing = append(missing, "NOTIFY_WEBHOOK_URL")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("http notify adapter enabled but missing %s", strings.Join(missing, ", "))
	}
	return &WebhookConfig{URL: url, Token: token}, nil
}
