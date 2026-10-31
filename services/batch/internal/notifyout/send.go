package notifyout

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

const (
	webhookTimeout = 10 * time.Second
	smtpTimeout    = 30 * time.Second
)

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

type statusError struct{ code int }

func (e statusError) Error() string { return fmt.Sprintf("webhook send: status %d", e.code) }

func errorClass(err error) string {
	var tpe *textproto.Error
	var se statusError
	switch {
	case errors.As(err, &tpe):
		return "smtp " + strconv.Itoa(tpe.Code)
	case errors.As(err, &se):
		return "http " + strconv.Itoa(se.code)
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case IsPermanent(err):
		return "rejected"
	}
	return "send error"
}

type Message struct {
	ID      string
	To      string
	Subject string
	Body    string
}

type Sender interface {
	Send(ctx context.Context, msg Message) error
}

type SMTPSender struct {
	cfg       SMTPConfig
	timeout   time.Duration
	tlsConfig *tls.Config
}

func NewSMTPSender(cfg SMTPConfig) *SMTPSender {
	return &SMTPSender{cfg: cfg, timeout: smtpTimeout}
}

func parseSingleAddress(field, value string) (string, error) {
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("smtp %s contains a line break", field)
	}
	addr, err := mail.ParseAddress(value)
	if err != nil {
		return "", fmt.Errorf("smtp %s is not a single valid address", field)
	}
	return addr.Address, nil
}

func (s *SMTPSender) Send(ctx context.Context, msg Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.ContainsAny(msg.Subject, "\r\n") {
		return permanentError{errors.New("smtp subject contains a line break")}
	}
	to, err := parseSingleAddress("to", msg.To)
	if err != nil {
		return permanentError{err}
	}
	from, err := parseSingleAddress("from", s.cfg.From)
	if err != nil {
		return permanentError{err}
	}
	raw, err := buildMessage(from, to, msg)
	if err != nil {
		return err
	}
	if err := s.deliver(ctx, from, to, raw); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("smtp send: %w", ctxErr)
		}
		var tpe *textproto.Error
		if errors.As(err, &tpe) && tpe.Code >= 500 {
			return permanentError{fmt.Errorf("smtp send: %w", err)}
		}
		return fmt.Errorf("smtp send: %w", err)
	}
	return nil
}

func buildMessage(from, to string, msg Message) ([]byte, error) {
	id := msg.ID
	if id == "" {
		id = ids.New()
	}
	domain := from[strings.LastIndex(from, "@")+1:]
	var raw bytes.Buffer
	raw.WriteString("From: " + from + "\r\nTo: " + to +
		"\r\nSubject: " + mime.QEncoding.Encode("utf-8", msg.Subject) +
		"\r\nDate: " + time.Now().UTC().Format(time.RFC1123Z) +
		"\r\nMessage-ID: <" + id + "@" + domain + ">" +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	qp := quotedprintable.NewWriter(&raw)
	if _, err := qp.Write([]byte(msg.Body)); err != nil {
		return nil, fmt.Errorf("encode smtp body: %w", err)
	}
	if err := qp.Close(); err != nil {
		return nil, fmt.Errorf("encode smtp body: %w", err)
	}
	return raw.Bytes(), nil
}

func (s *SMTPSender) clientTLSConfig() *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if s.tlsConfig != nil {
		cfg = s.tlsConfig.Clone()
	}
	cfg.ServerName = s.cfg.Host
	return cfg
}

func (s *SMTPSender) dial(ctx context.Context, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: s.timeout}
	if s.cfg.TLS == TLSImplicit {
		return (&tls.Dialer{NetDialer: dialer, Config: s.clientTLSConfig()}).DialContext(ctx, "tcp", addr)
	}
	return dialer.DialContext(ctx, "tcp", addr)
}

func (s *SMTPSender) deliver(ctx context.Context, from, to string, raw []byte) error {
	conn, err := s.dial(ctx, net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port)))
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline := time.Now().Add(s.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return err
	}
	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer client.Close()
	if s.cfg.TLS == TLSStartTLS || s.cfg.TLS == "" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("server does not advertise STARTTLS")
		}
		if err := client.StartTLS(s.clientTLSConfig()); err != nil {
			return err
		}
	}
	if err := client.Auth(smtp.PlainAuth("", s.cfg.User, s.cfg.Password, s.cfg.Host)); err != nil {
		return err
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	_ = client.Quit()
	return nil
}

type WebhookSender struct {
	cfg    WebhookConfig
	client *http.Client
}

func NewWebhookSender(cfg WebhookConfig, client *http.Client) *WebhookSender {
	c := &http.Client{Timeout: webhookTimeout}
	if client != nil {
		copied := *client
		c = &copied
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &WebhookSender{cfg: cfg, client: c}
}

func redactURLError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	redacted := &url.Error{Op: ue.Op, Err: ue.Err}
	if u, perr := url.Parse(ue.URL); perr == nil && u.Host != "" {
		redacted.URL = u.Scheme + "://" + u.Host
	}
	return redacted
}

func (w *WebhookSender) Send(ctx context.Context, msg Message) error {
	payload, err := json.Marshal(map[string]string{"to": msg.To, "subject": msg.Subject, "body": msg.Body})
	if err != nil {
		return fmt.Errorf("encode webhook payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.URL, bytes.NewReader(payload))
	if err != nil {
		return permanentError{errors.New("build webhook request: invalid url")}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.cfg.Token)
	if msg.ID != "" {
		req.Header.Set("Idempotency-Key", msg.ID)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook send: %w", redactURLError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		err := statusError{resp.StatusCode}
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
			return permanentError{err}
		}
		return err
	}
	return nil
}
