package bankconnect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

const (
	ProtocolEBICS = "ebics"

	EventPrepared = "prepared"
	EventSent     = "sent"
	EventFailed   = "failed"
	EventUnknown  = "unknown"

	ebicsOK       = "000000"
	maxReplyBytes = 1 << 20
)

// Signature is a detached signature made inside the key store. The private key
// is never part of it.
type Signature struct {
	Bytes      []byte
	KeyVersion int
}

// ErrAmbiguous marks a transport failure after the request may have reached the
// bank. The file is audited as unknown and is never resent automatically: a
// person checks the bank's records first.
var ErrAmbiguous = errors.New("outcome unknown: the bank may have received the file")

// Signer signs bank files with a key that stays in the key store. There is no
// method that returns key material.
type Signer interface {
	Sign(ctx context.Context, keyName string, payload []byte) (Signature, error)
}

// File is one bank file to send for a bank account.
type File struct {
	LegalEntityID string
	BankAccountID string
	Name          string
	OrderType     string
	KeyName       string
	Data          []byte
}

// Receipt is the bank's acknowledgement of a transfer.
type Receipt struct{ Reference string }

// Transport delivers a signed file to a bank.
type Transport interface {
	Protocol() string
	Send(ctx context.Context, f File, sig Signature) (Receipt, error)
}

// AuditEntry is one append-only row of the bank file audit (AC4): every file
// gets a prepared row before it leaves, then a sent or failed row.
type AuditEntry struct {
	FileID        string
	Event         string
	LegalEntityID string
	BankAccountID string
	Protocol      string
	FileName      string
	OrderType     string
	SHA256        string
	SizeBytes     int
	KeyName       string
	KeyVersion    int
	BankReference string
	Error         string
}

// AuditSink stores audit entries; a returned error means the entry is not durable.
type AuditSink interface {
	RecordBankFile(ctx context.Context, e AuditEntry) error
}

// Sender signs, audits and delivers bank files.
type Sender struct {
	Signer    Signer
	Transport Transport
	Audit     AuditSink
}

// Send audits the file as prepared, signs it in the key store, delivers it and
// audits the outcome (sent, failed, or unknown when the transport cannot tell).
// Outcome rows are written even when the caller's context is cancelled. If the prepared row cannot be written nothing is signed or
// sent. If the file was delivered but the closing row cannot be written, the
// receipt is returned with the error so the caller can reconcile.
func (s Sender) Send(ctx context.Context, f File) (Receipt, error) {
	sum := sha256.Sum256(f.Data)
	entry := AuditEntry{
		FileID: ids.New(), LegalEntityID: f.LegalEntityID, BankAccountID: f.BankAccountID,
		Protocol: s.Transport.Protocol(), FileName: f.Name, OrderType: f.OrderType,
		SHA256: hex.EncodeToString(sum[:]), SizeBytes: len(f.Data), KeyName: f.KeyName,
	}
	entry.Event = EventPrepared
	if err := s.Audit.RecordBankFile(ctx, entry); err != nil {
		return Receipt{}, fmt.Errorf("audit bank file: %w", err)
	}
	sig, err := s.Signer.Sign(ctx, f.KeyName, f.Data)
	entry.KeyVersion = sig.KeyVersion
	var receipt Receipt
	if err == nil {
		receipt, err = s.Transport.Send(ctx, f, sig)
	}
	outcome := context.WithoutCancel(ctx)
	if err != nil {
		entry.Event, entry.Error = EventFailed, err.Error()
		if errors.Is(err, ErrAmbiguous) {
			entry.Event = EventUnknown
		}
		return Receipt{}, errors.Join(err, s.Audit.RecordBankFile(outcome, entry))
	}
	entry.Event, entry.BankReference = EventSent, receipt.Reference
	if auditErr := s.Audit.RecordBankFile(outcome, entry); auditErr != nil {
		return receipt, fmt.Errorf("file sent (reference %s) but audit failed: %w", receipt.Reference, auditErr)
	}
	return receipt, nil
}

// VaultTransit signs through Vault's transit engine (POST /v1/transit/sign/:key/sha2-256).
// Only the token, key name and input travel; Vault hashes and signs the input
// itself. The algorithm is pinned to RSASSA-PSS with SHA-256 and a salt as long as the hash (EBICS A006).
type VaultTransit struct {
	Addr   string
	Token  string
	Client *http.Client
}

func (v VaultTransit) Sign(ctx context.Context, keyName string, payload []byte) (Signature, error) {
	if err := checkURL(v.Addr); err != nil {
		return Signature{}, err
	}
	body, err := json.Marshal(map[string]string{"input": base64.StdEncoding.EncodeToString(payload), "signature_algorithm": "pss", "salt_length": "hash"})
	if err != nil {
		return Signature{}, err
	}
	endpoint := strings.TrimRight(v.Addr, "/") + "/v1/transit/sign/" + url.PathEscape(keyName) + "/sha2-256"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Signature{}, err
	}
	req.Header.Set("X-Vault-Token", v.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient(v.Client).Do(req)
	if err != nil {
		return Signature{}, fmt.Errorf("vault sign: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return Signature{}, fmt.Errorf("vault sign: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Signature{}, fmt.Errorf("vault sign: status %d", resp.StatusCode)
	}
	var out struct {
		Data struct {
			Signature  string `json:"signature"`
			KeyVersion int    `json:"key_version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Signature{}, errors.New("vault sign: malformed reply")
	}
	parts := strings.SplitN(out.Data.Signature, ":", 3)
	if len(parts) != 3 || parts[0] != "vault" || !strings.HasPrefix(parts[1], "v") {
		return Signature{}, errors.New("vault sign: malformed signature")
	}
	sig, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil || len(sig) == 0 {
		return Signature{}, errors.New("vault sign: malformed signature")
	}
	return Signature{Bytes: sig, KeyVersion: out.Data.KeyVersion}, nil
}

// EBICS delivers a file as an EBICS upload order. The subset built here is one
// unencrypted upload request carrying the order data and the electronic
// signature, and the response's technical return code.
type EBICS struct {
	URL       string
	HostID    string
	PartnerID string
	UserID    string
	Client    *http.Client
}

func (EBICS) Protocol() string { return ProtocolEBICS }

type ebicsRequest struct {
	XMLName   xml.Name `xml:"urn:org:ebics:H004 ebicsRequest"`
	Version   string   `xml:"Version,attr"`
	HostID    string   `xml:"header>static>HostID"`
	PartnerID string   `xml:"header>static>PartnerID"`
	UserID    string   `xml:"header>static>UserID"`
	OrderType string   `xml:"header>static>OrderDetails>OrderType"`
	Signature string   `xml:"body>SignatureData"`
	OrderData string   `xml:"body>DataTransfer>OrderData"`
}

type ebicsResponse struct {
	ReturnCode    string `xml:"header>mutable>ReturnCode"`
	ReportText    string `xml:"header>mutable>ReportText"`
	TransactionID string `xml:"header>static>TransactionID"`
}

func (e EBICS) Send(ctx context.Context, f File, sig Signature) (Receipt, error) {
	if err := checkURL(e.URL); err != nil {
		return Receipt{}, err
	}
	body, err := xml.Marshal(ebicsRequest{
		Version: "H004", HostID: e.HostID, PartnerID: e.PartnerID, UserID: e.UserID, OrderType: f.OrderType,
		Signature: base64.StdEncoding.EncodeToString(sig.Bytes), OrderData: base64.StdEncoding.EncodeToString(f.Data),
	})
	if err != nil {
		return Receipt{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.URL, bytes.NewReader(body))
	if err != nil {
		return Receipt{}, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=UTF-8")
	if err := ctx.Err(); err != nil {
		return Receipt{}, fmt.Errorf("ebics upload not started: %w", err)
	}
	resp, err := httpClient(e.Client).Do(req)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			return Receipt{}, fmt.Errorf("ebics upload not sent: %w", err)
		}
		return Receipt{}, fmt.Errorf("ebics upload: %w: %w", ErrAmbiguous, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return Receipt{}, fmt.Errorf("ebics upload: %w: %w", ErrAmbiguous, err)
	}
	var out ebicsResponse
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return Receipt{}, fmt.Errorf("ebics upload refused: status %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || xml.Unmarshal(raw, &out) != nil {
		return Receipt{}, fmt.Errorf("ebics upload: %w: status %d, unreadable reply", ErrAmbiguous, resp.StatusCode)
	}
	if out.ReturnCode != ebicsOK {
		return Receipt{}, fmt.Errorf("ebics upload refused: %s %s", out.ReturnCode, out.ReportText)
	}
	return Receipt{Reference: out.TransactionID}, nil
}

// checkURL accepts https, and http only to a loopback host (local fakes).
func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid url %q", raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		if h := u.Hostname(); h == "localhost" || h == "127.0.0.1" || h == "::1" {
			return nil
		}
	}
	return fmt.Errorf("url %q must use https", raw)
}

func httpClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 30 * time.Second}
}
