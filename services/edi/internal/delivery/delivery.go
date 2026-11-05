// Package delivery runs the transport jobs of the edi service: the dispatcher
// sends queued outbound interchanges and 997s over each partner's AS2 or SFTP
// transport and records the MDN or upload, and the poller pulls inbound files
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
	"strconv"
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

// Dispatcher sends due deliveries. HTTP is the client for AS2 (its transport
// decides TLS trust); AS2PublicURL is the base URL partners reach our AS2
// listener on, needed for asynchronous MDNs.
type Dispatcher struct {
	Store        *store.Store
	Secrets      secrets.Store
	HTTP         *http.Client
	AS2PublicURL string
	Batch        int
	Lease        time.Duration
	MaxAttempts  int
	SFTPTimeout  time.Duration
	Now          func() time.Time
}

func (d *Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// RunOnce sends every delivery due now, tenant by tenant, and returns how many
// attempts it made. A failing tenant does not stop the others.
func (d *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	tenants, err := d.Store.DueTenants(ctx, "DELIVERY")
	if err != nil {
		return 0, err
	}
	batch, lease := d.Batch, d.Lease
	if batch <= 0 {
		batch = DefaultBatch
	}
	if lease <= 0 {
		lease = DefaultLease
	}
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
		return store.DeliveryOutcome{Status: "AWAITING_MDN", SentMIC: r.SentMIC, Reference: r.Reference}
	case err == nil:
		return store.DeliveryOutcome{Status: "DELIVERED", SentMIC: r.SentMIC, MDNMIC: r.MIC, MDNDisposition: r.Disposition, MDNSigned: r.SignedMDN, Reference: r.Reference}
	case errors.Is(err, transport.ErrRejected):
		return store.DeliveryOutcome{Status: "REJECTED", SentMIC: r.SentMIC, MDNMIC: r.MIC, MDNDisposition: r.Disposition, MDNSigned: r.SignedMDN, Error: err.Error()}
	case errors.Is(err, transport.ErrInvalid):
		return store.DeliveryOutcome{Status: "FAILED", Error: err.Error()}
	}
	max := d.MaxAttempts
	if max <= 0 {
		max = DefaultMaxAttempts
	}
	if c.Attempts >= max {
		return store.DeliveryOutcome{Status: "FAILED", Error: fmt.Sprintf("gave up after %d attempts: %v", c.Attempts, err)}
	}
	return store.DeliveryOutcome{Status: "PENDING", RetryAt: d.now().Add(Backoff(c.Attempts)), Error: err.Error()}
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
