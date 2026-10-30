package notifyout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

const webhookTimeout = 10 * time.Second

type Message struct {
	To      string
	Subject string
	Body    string
}

type Sender interface {
	Send(ctx context.Context, msg Message) error
}

type SMTPSender struct{ cfg SMTPConfig }

func NewSMTPSender(cfg SMTPConfig) *SMTPSender { return &SMTPSender{cfg: cfg} }

func (s *SMTPSender) Send(ctx context.Context, msg Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, field := range []string{msg.To, msg.Subject, s.cfg.From} {
		if strings.ContainsAny(field, "\r\n") {
			return errors.New("smtp header value contains a line break")
		}
	}
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	auth := smtp.PlainAuth("", s.cfg.User, s.cfg.Password, s.cfg.Host)
	raw := "From: " + s.cfg.From + "\r\nTo: " + msg.To + "\r\nSubject: " + msg.Subject +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + msg.Body
	if err := smtp.SendMail(addr, auth, s.cfg.From, []string{msg.To}, []byte(raw)); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return nil
}

type WebhookSender struct {
	cfg    WebhookConfig
	client *http.Client
}

func NewWebhookSender(cfg WebhookConfig, client *http.Client) *WebhookSender {
	if client == nil {
		client = &http.Client{Timeout: webhookTimeout}
	}
	return &WebhookSender{cfg: cfg, client: client}
}

func (w *WebhookSender) Send(ctx context.Context, msg Message) error {
	payload, err := json.Marshal(map[string]string{"to": msg.To, "subject": msg.Subject, "body": msg.Body})
	if err != nil {
		return fmt.Errorf("encode webhook payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.URL, bytes.NewReader(payload))
	if err != nil {
		return errors.New("build webhook request: invalid url")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.cfg.Token)
	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook send: status %d", resp.StatusCode)
	}
	return nil
}
