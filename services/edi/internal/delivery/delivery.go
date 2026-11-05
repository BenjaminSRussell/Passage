// Package delivery runs the transport jobs of the edi service: the dispatcher
// sends queued outbound interchanges and 997s over each partner's AS2 or SFTP
// transport and records the MDN or upload, ends waits for asynchronous MDNs
// that ran out, and posts the asynchronous MDNs we owe partners; the poller
// pulls inbound files
// from partners' SFTP inboxes into the receipt pipeline. Both find due work
// across tenants through the store's SECURITY DEFINER index and then work one
// tenant at a time under row-level security.
package delivery

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/secrets"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
	"github.com/BenjaminSRussell/passage/services/edi/internal/transport"
)

// Defaults of the dispatcher.
const (
	DefaultBatch       = 20
	DefaultLease       = 5 * time.Minute
	DefaultMaxAttempts = 8
	maxBackoff         = time.Hour
)

// Backoff is the wait before attempt n+1 after n failed attempts: one minute
// doubling, at most an hour.
func Backoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > 7 {
		return maxBackoff
	}
	d := time.Minute << (n - 1)
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// AS2MDNURL is where partners post asynchronous MDNs for an endpoint.
func AS2MDNURL(publicURL, endpointID string) string {
	return publicURL + "/as2/v1/" + endpointID + "/mdn"
}

// DefaultMDNTimeout is how long a delivery waits for its asynchronous MDN
// before it is sent again (or FAILED after the last attempt).
const DefaultMDNTimeout = 24 * time.Hour

// Dispatcher sends due deliveries and the asynchronous MDNs we owe. HTTP is
// the client for AS2 (its transport decides TLS trust; MDNs are posted
// without following redirects whatever its policy); AS2PublicURL is the base
// URL partners reach our AS2 listener on, needed for asynchronous MDNs;
// MDNTimeout bounds the wait for one (DefaultMDNTimeout when zero).
type Dispatcher struct {
	Store        *store.Store
	Secrets      secrets.Store
	HTTP         *http.Client
	AS2PublicURL string
	Batch        int
	Lease        time.Duration
	MaxAttempts  int
	MDNTimeout   time.Duration
	SFTPTimeout  time.Duration
	Now          func() time.Time
}

func (d *Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Dispatcher) maxAttempts() int {
	if d.MaxAttempts <= 0 {
		return DefaultMaxAttempts
	}
	return d.MaxAttempts
}

func (d *Dispatcher) mdnTimeout() time.Duration {
	if d.MDNTimeout <= 0 {
		return DefaultMDNTimeout
	}
	return d.MDNTimeout
}

func (d *Dispatcher) limits() (int, time.Duration) {
	batch, lease := d.Batch, d.Lease
	if batch <= 0 {
		batch = DefaultBatch
	}
	if lease <= 0 {
		lease = DefaultLease
	}
	return batch, lease
}

// RunOnce ends the waits for asynchronous MDNs that ran out, then sends
// every delivery and every MDN we owe that is due now, tenant by tenant, and
// returns how many send attempts it made. A failing tenant does not stop the
// others.
func (d *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	var failed []error
	if err := d.expireMDNWaits(ctx); err != nil {
		failed = append(failed, err)
	}
	n, err := d.sendDeliveries(ctx)
	if err != nil {
		failed = append(failed, err)
	}
	m, err := d.sendMDNs(ctx)
	if err != nil {
		failed = append(failed, err)
	}
	return n + m, errors.Join(failed...)
}

// expireMDNWaits moves AWAITING_MDN deliveries whose wait ran out back to
// PENDING, to be sent again with the same Message-ID (the partner drops the
// duplicate and answers with an MDN), or to FAILED once the attempts are
// used up, which publishes delivery.failed. Either way the delivery records
// the timeout and the error says so.
func (d *Dispatcher) expireMDNWaits(ctx context.Context) error {
	tenants, err := d.Store.DueTenants(ctx, "MDN_TIMEOUT")
	if err != nil {
		return err
	}
	batch, _ := d.limits()
	timeout, max := d.mdnTimeout(), d.maxAttempts()
	var failed []error
	for _, tid := range tenants {
		tctx := store.SystemContext(ctx, tid)
		expired, err := d.Store.ExpireAwaitingMDN(tctx, batch, func(w store.AwaitingMDN) store.DeliveryOutcome {
			if w.Attempts >= max {
				return store.DeliveryOutcome{Status: "FAILED",
					Error: fmt.Sprintf("no asynchronous MDN within %s after %d attempts; resend to try again", timeout, w.Attempts)}
			}
			return store.DeliveryOutcome{Status: "PENDING", RetryAt: d.now().Add(Backoff(w.Attempts)),
				Error: fmt.Sprintf("no asynchronous MDN within %s; sending again with the same Message-ID", timeout)}
		})
		if err != nil {
			failed = append(failed, fmt.Errorf("tenant %s: %w", tid, err))
			continue
		}
		for _, e := range expired {
			slog.Warn("edi delivery MDN timeout", "tenant", tid, "partner", e.PartnerID, "delivery", e.ID, "attempt", e.Attempts,
				"mdnTimeouts", e.MDNTimeouts+1, "status", e.Outcome.Status)
		}
	}
	return errors.Join(failed...)
}

func (d *Dispatcher) sendDeliveries(ctx context.Context) (int, error) {
	tenants, err := d.Store.DueTenants(ctx, "DELIVERY")
	if err != nil {
		return 0, err
	}
	batch, lease := d.limits()
	n := 0
	var failed []error
	for _, tid := range tenants {
		tctx := store.SystemContext(ctx, tid)
		claimed, err := d.Store.ClaimDeliveries(tctx, batch, lease)
		if err != nil {
			failed = append(failed, fmt.Errorf("tenant %s: %w", tid, err))
			continue
		}
		for _, c := range claimed {
			o := d.attempt(tctx, c)
			if err := d.Store.FinishDelivery(tctx, c.ID, o); err != nil {
				failed = append(failed, fmt.Errorf("tenant %s delivery %s: %w", tid, c.ID, err))
				continue
			}
			level := slog.LevelInfo
			if o.Status != "DELIVERED" && o.Status != "AWAITING_MDN" {
				level = slog.LevelWarn
			}
			slog.Log(ctx, level, "edi delivery", "tenant", tid, "partner", c.PartnerID, "delivery", c.ID, "transport", c.Transport,
				"kind", c.PayloadKind, "attempt", c.Attempts, "status", o.Status, "err", o.Error)
			n++
		}
	}
	return n, errors.Join(failed...)
}

func contentType(standard string) string {
	if standard == "EDIFACT" {
		return transport.ContentEdifact
	}
	return transport.ContentX12
}

// attempt sends one claimed delivery and says what to record.
func (d *Dispatcher) attempt(ctx context.Context, c store.ClaimedDelivery) store.DeliveryOutcome {
	msg := transport.Message{ID: c.MessageID, Filename: c.Filename, ContentType: contentType(c.Standard), Payload: []byte(c.Payload)}
	var (
		r   transport.Receipt
		err error
	)
	switch cfg := c.TransportCfg.Config; {
	case cfg.Kind == domain.TransportAS2 && cfg.AS2 != nil:
		r, err = d.sendAS2(ctx, c.TransportCfg, msg)
	case cfg.Kind == domain.TransportSFTP && cfg.SFTP != nil:
		r, err = d.sendSFTP(ctx, *cfg.SFTP, msg)
	default:
		return store.DeliveryOutcome{Status: "FAILED", Error: "the partner has no " + c.Transport + " transport"}
	}
	switch {
	case err == nil && r.Pending:
		return store.DeliveryOutcome{Status: "AWAITING_MDN", SentMIC: r.SentMIC, Reference: r.Reference, MDNDueAt: d.now().Add(d.mdnTimeout())}
	case err == nil:
		return store.DeliveryOutcome{Status: "DELIVERED", SentMIC: r.SentMIC, MDNMIC: r.MIC, MDNDisposition: r.Disposition, MDNSigned: r.SignedMDN, Reference: r.Reference}
	case errors.Is(err, transport.ErrRejected):
		return store.DeliveryOutcome{Status: "REJECTED", SentMIC: r.SentMIC, MDNMIC: r.MIC, MDNDisposition: r.Disposition, MDNSigned: r.SignedMDN, Error: err.Error()}
	case errors.Is(err, transport.ErrInvalid):
		return store.DeliveryOutcome{Status: "FAILED", Error: err.Error()}
	}
	if c.Attempts >= d.maxAttempts() {
		return store.DeliveryOutcome{Status: "FAILED", Error: fmt.Sprintf("gave up after %d attempts: %v", c.Attempts, err)}
	}
	return store.DeliveryOutcome{Status: "PENDING", RetryAt: d.now().Add(Backoff(c.Attempts)), Error: err.Error()}
}

// MDNTargetAllowed reports whether an asynchronous MDN may be posted to
// target (the sender's Receipt-Delivery-Option): only to the scheme and host
// of the partner's configured AS2 URL and without credentials. The header is
// the sender's to choose, and posting wherever it says would let anyone make
// the service call arbitrary URLs.
func MDNTargetAllowed(target, partnerURL string) bool {
	t, err := url.Parse(target)
	if err != nil || partnerURL == "" {
		return false
	}
	base, err := url.Parse(partnerURL)
	if err != nil {
		return false
	}
	return t.Scheme != "" && t.Scheme == base.Scheme && t.Host != "" && strings.EqualFold(t.Host, base.Host) && t.User == nil
}

// sendMDNs posts the asynchronous MDNs we owe that are due, tenant by
// tenant, and returns how many it tried.
func (d *Dispatcher) sendMDNs(ctx context.Context) (int, error) {
	tenants, err := d.Store.DueTenants(ctx, "MDN")
	if err != nil {
		return 0, err
	}
	batch, lease := d.limits()
	n := 0
	var failed []error
	for _, tid := range tenants {
		tctx := store.SystemContext(ctx, tid)
		claimed, err := d.Store.ClaimMDNs(tctx, batch, lease)
		if err != nil {
			failed = append(failed, fmt.Errorf("tenant %s: %w", tid, err))
			continue
		}
		for _, c := range claimed {
			o := d.postMDN(tctx, c)
			if err := d.Store.FinishMDN(tctx, c.ID, o); err != nil {
				failed = append(failed, fmt.Errorf("tenant %s mdn %s: %w", tid, c.ID, err))
				continue
			}
			level := slog.LevelInfo
			if o.Status != "DELIVERED" {
				level = slog.LevelWarn
			}
			slog.Log(ctx, level, "edi async MDN", "tenant", tid, "partner", c.PartnerID, "mdn", c.ID, "attempt", c.Attempts,
				"status", o.Status, "err", o.Error)
			n++
		}
	}
	return n, errors.Join(failed...)
}

// postMDN posts one owed MDN and says what to record. The target is checked
// again against the partner's current AS2 URL, and redirects are never
// followed (a 3xx is a failed attempt).
func (d *Dispatcher) postMDN(ctx context.Context, c store.ClaimedMDN) store.MDNOutcome {
	if c.PartnerURL == "" {
		return store.MDNOutcome{Status: "FAILED", Error: "the partner no longer has an AS2 transport"}
	}
	if !MDNTargetAllowed(c.TargetURL, c.PartnerURL) {
		return store.MDNOutcome{Status: "FAILED", Error: "Receipt-Delivery-Option is not on the partner's AS2 host"}
	}
	hc := &http.Client{Timeout: time.Minute}
	if d.HTTP != nil {
		copied := *d.HTTP
		hc = &copied
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	err := transport.PostAsyncMDN(cctx, hc, c.TargetURL, transport.AsyncMDN{URL: c.TargetURL, Header: http.Header(c.Header), Body: c.Body})
	switch {
	case err == nil:
		return store.MDNOutcome{Status: "DELIVERED"}
	case c.Attempts >= d.maxAttempts():
		return store.MDNOutcome{Status: "FAILED", Error: fmt.Sprintf("gave up after %d attempts: %v", c.Attempts, err)}
	}
	return store.MDNOutcome{Status: "PENDING", RetryAt: d.now().Add(Backoff(c.Attempts)), Error: err.Error()}
}

var signHash = map[string]crypto.Hash{"sha-256": crypto.SHA256, "sha-384": crypto.SHA384, "sha-512": crypto.SHA512}

// AS2Endpoint is the AS2 configuration of one partner with its secrets
// loaded: our identity and the pinned partner certificate.
type AS2Endpoint struct {
	Us      transport.AS2Identity
	Partner transport.AS2Partner
}

// LoadAS2 reads our signing key and certificate from the secret store and
// parses the partner certificate. Errors never carry secret material.
func LoadAS2(s secrets.Store, cfg domain.AS2Settings, now time.Time) (AS2Endpoint, error) {
	if s == nil {
		return AS2Endpoint{}, errors.New("no secret store is configured (EDI_SECRETS_DIR)")
	}
	cert, key, err := secrets.AS2Identity(s, cfg.SigningKeyRef)
	if err != nil {
		return AS2Endpoint{}, fmt.Errorf("signing key %q: %w", cfg.SigningKeyRef, err)
	}
	partnerCert, msg := domain.ParsePartnerCertificate(cfg.PartnerCertificate, now)
	if msg != "" {
		return AS2Endpoint{}, fmt.Errorf("partner certificate %s", msg)
	}
	return AS2Endpoint{
		Us: transport.AS2Identity{ID: cfg.OurAS2ID, Cert: cert, Key: key},
		Partner: transport.AS2Partner{ID: cfg.PartnerAS2ID, URL: cfg.URL, Cert: partnerCert, RequireSigned: true,
			RequireEncrypted: cfg.EncryptionAlgorithm != "" && cfg.EncryptionAlgorithm != transport.EncryptNone},
	}, nil
}

func (d *Dispatcher) sendAS2(ctx context.Context, rec store.TransportRecord, msg transport.Message) (transport.Receipt, error) {
	cfg := *rec.Config.AS2
	ep, err := LoadAS2(d.Secrets, cfg, d.now())
	if err != nil {
		return transport.Receipt{}, err
	}
	client := &transport.AS2Client{Us: ep.Us, Partner: ep.Partner, HTTP: d.HTTP, RequireSignedMDN: true,
		SignHash: signHash[cfg.SigningAlgorithm], Encryption: cfg.EncryptionAlgorithm, Now: d.Now}
	if cfg.MDNMode == "async" {
		if d.AS2PublicURL == "" {
			return transport.Receipt{}, errors.New("asynchronous MDNs need EDI_AS2_PUBLIC_URL")
		}
		client.AsyncMDNURL = AS2MDNURL(d.AS2PublicURL, rec.EndpointID)
	}
	msg.From, msg.To = cfg.OurAS2ID, cfg.PartnerAS2ID
	return client.Send(ctx, msg)
}

// DialSFTP opens the partner's SFTP server with the credential its profile
// references; maxBytes caps downloads.
func DialSFTP(ctx context.Context, s secrets.Store, cfg domain.SFTPSettings, timeout time.Duration, maxBytes int64) (*transport.SFTPClient, error) {
	if s == nil {
		return nil, errors.New("no secret store is configured (EDI_SECRETS_DIR)")
	}
	conf := transport.SFTPConfig{Addr: net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)), User: cfg.User,
		HostKeyFingerprint: cfg.HostKeyFingerprint, Timeout: timeout, MaxFileBytes: maxBytes}
	var err error
	if cfg.AuthMethod == "password" {
		conf.Password, err = secrets.Password(s, cfg.CredentialRef)
	} else {
		conf.Signer, err = secrets.SSHSigner(s, cfg.CredentialRef)
	}
	if err != nil {
		return nil, fmt.Errorf("sftp credential %q: %w", cfg.CredentialRef, err)
	}
	return transport.DialSFTP(ctx, conf)
}

func (d *Dispatcher) sendSFTP(ctx context.Context, cfg domain.SFTPSettings, msg transport.Message) (transport.Receipt, error) {
	client, err := DialSFTP(ctx, d.Secrets, cfg, d.SFTPTimeout, 0)
	if err != nil {
		return transport.Receipt{}, err
	}
	defer func() { _ = client.Close() }()
	return transport.SFTP{Files: client, OutDir: cfg.OutboundDir, Now: d.Now}.Send(ctx, msg)
}

// Run calls RunOnce every interval until ctx ends, logging failures.
func Run(ctx context.Context, name string, interval time.Duration, once func(context.Context) (int, error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := once(ctx); err != nil && ctx.Err() == nil {
			slog.Error("edi "+name+" run failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
