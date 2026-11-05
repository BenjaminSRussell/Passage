package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/secrets"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
	"github.com/BenjaminSRussell/passage/services/edi/internal/transport"
)

// ErrPermanent marks a received payload that will never be accepted as sent
// (unreadable, unknown sender, sent by another partner): the poller parks it
// at once instead of retrying.
var ErrPermanent = errors.New("edi: payload refused")

// Receiver records one payload a transport received from partnerID. It
// returns nil when the interchange was recorded, an error wrapping
// store.ErrDuplicate when the same interchange was recorded before, one
// wrapping ErrPermanent when it can never be recorded, and any other error
// for a failure worth retrying.
type Receiver func(ctx context.Context, partnerID string, src store.InboundSource, payload []byte) error

// Defaults of the poller.
const (
	DefaultPollPartners     = 10
	DefaultMaxFilesPerPoll  = 50
	DefaultMaxFileBytes     = 4 << 20
	DefaultMaxInboundTries  = 5
	emptySHA256             = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	archiveTimestampLayout  = "20060102T150405Z"
	maxInboundErrorTextSize = 500
)

// Poller pulls partners' SFTP inboxes. Each due partner is polled at most
// once per interval: at most MaxFiles files per poll, each at most
// MaxFileBytes, in name order. A file is recognised by name and SHA-256, so a
// crash between recording and archiving only re-archives it; a failure backs
// off and is moved to the partner's failed directory (PARKED) after
// MaxAttempts or at once when it can never be accepted.
type Poller struct {
	Store        *store.Store
	Secrets      secrets.Store
	Receive      Receiver
	Partners     int
	MaxFiles     int
	MaxFileBytes int64
	MaxAttempts  int
	SFTPTimeout  time.Duration
	Now          func() time.Time
}

func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// RunOnce polls every partner due now and returns how many files it handled.
func (p *Poller) RunOnce(ctx context.Context) (int, error) {
	tenants, err := p.Store.DueTenants(ctx, "SFTP_POLL")
	if err != nil {
		return 0, err
	}
	partners := p.Partners
	if partners <= 0 {
		partners = DefaultPollPartners
	}
	n := 0
	var failed []error
	for _, tid := range tenants {
		tctx := store.SystemContext(ctx, tid)
		due, err := p.Store.ClaimSFTPPolls(tctx, partners)
		if err != nil {
			failed = append(failed, fmt.Errorf("tenant %s: %w", tid, err))
			continue
		}
		for _, rec := range due {
			handled, err := p.poll(tctx, rec)
			n += handled
			if ferr := p.Store.FinishSFTPPoll(tctx, rec.PartnerID, err == nil); ferr != nil {
				failed = append(failed, ferr)
			}
			if err != nil {
				slog.Warn("edi sftp poll failed", "tenant", tid, "partner", rec.PartnerID, "err", err)
			}
		}
	}
	return n, errors.Join(failed...)
}

// inboxFile reports whether a listed name is a file to pull: not a directory,
// not a partial upload, not one of our own subdirectories.
func inboxFile(name string, cfg domain.SFTPSettings) bool {
	return name != "" && !strings.HasSuffix(name, "/") && !strings.HasSuffix(name, ".part") && !strings.HasPrefix(name, ".") &&
		name != cfg.ArchiveDir && name != cfg.FailedDir && !strings.Contains(name, "/")
}

func (p *Poller) poll(ctx context.Context, rec store.TransportRecord) (int, error) {
	cfg := *rec.Config.SFTP
	maxBytes := p.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxFileBytes
	}
	client, err := DialSFTP(ctx, p.Secrets, cfg, p.SFTPTimeout, maxBytes)
	if err != nil {
		return 0, err
	}
	defer func() { _ = client.Close() }()
	names, err := client.List(ctx, cfg.InboundDir)
	if err != nil {
		return 0, fmt.Errorf("sftp list: %w", err)
	}
	sort.Strings(names)
	max := p.MaxFiles
	if max <= 0 {
		max = DefaultMaxFilesPerPoll
	}
	handled := 0
	for _, name := range names {
		if handled >= max {
			break
		}
		if !inboxFile(name, cfg) {
			continue
		}
		done, err := p.file(ctx, client, rec, name)
		if err != nil {
			return handled, err
		}
		if done {
			handled++
		}
	}
	return handled, nil
}

// file handles one inbox file. It reports whether the file was dealt with
// (recorded, archived or parked); an error means the connection failed and
// the poll stops.
func (p *Poller) file(ctx context.Context, client *transport.SFTPClient, rec store.TransportRecord, name string) (bool, error) {
	cfg := *rec.Config.SFTP
	src := store.InboundSource{Transport: "SFTP", Ref: name, LegalEntityID: rec.LegalEntityID}
	full := path.Join(cfg.InboundDir, name)
	stamp := p.now().UTC().Format(archiveTimestampLayout)
	data, err := client.Get(ctx, full)
	if errors.Is(err, transport.ErrTooLarge) {
		src.SHA256 = emptySHA256
		if _, err := p.Store.RecordInbound(ctx, rec.PartnerID, src, "PARKED", nil, "file is larger than the inbound limit"); err != nil {
			return false, err
		}
		return true, p.move(ctx, client, full, path.Join(cfg.InboundDir, cfg.FailedDir, stamp+"_"+name))
	}
	if err != nil {
		return false, fmt.Errorf("sftp get %s: %w", name, err)
	}
	sum := sha256.Sum256(data)
	src.SHA256 = hex.EncodeToString(sum[:])
	archive := path.Join(cfg.InboundDir, cfg.ArchiveDir, stamp+"_"+name)
	parked := path.Join(cfg.InboundDir, cfg.FailedDir, stamp+"_"+name)
	known, err := p.Store.FindInbound(ctx, rec.PartnerID, "SFTP", name)
	if err != nil {
		return false, err
	}
	prevAttempts := 0
	for _, k := range known {
		if k.SHA256 != src.SHA256 {
			continue
		}
		switch {
		case k.Status == "DONE":
			// Recorded before; the archive move did not happen.
			return true, p.move(ctx, client, full, archive)
		case k.Status == "PARKED":
			return true, p.move(ctx, client, full, parked)
		case k.NextAttemptAt != nil && k.NextAttemptAt.After(p.now()):
			return false, nil
		}
		prevAttempts = k.Attempts
	}
	err = p.Receive(ctx, rec.PartnerID, src, data)
	switch {
	case err == nil:
		return true, p.move(ctx, client, full, archive)
	case errors.Is(err, store.ErrDuplicate):
		if _, rerr := p.Store.RecordInbound(ctx, rec.PartnerID, src, "DONE", nil, clip(err.Error())); rerr != nil {
			return false, rerr
		}
		return true, p.move(ctx, client, full, archive)
	}
	max := p.MaxAttempts
	if max <= 0 {
		max = DefaultMaxInboundTries
	}
	if errors.Is(err, ErrPermanent) || prevAttempts+1 >= max {
		slog.Warn("edi sftp file parked", "partner", rec.PartnerID, "file", name, "err", err)
		if _, rerr := p.Store.RecordInbound(ctx, rec.PartnerID, src, "PARKED", nil, clip(err.Error())); rerr != nil {
			return false, rerr
		}
		return true, p.move(ctx, client, full, parked)
	}
	retry := p.now().Add(Backoff(prevAttempts + 1))
	if _, rerr := p.Store.RecordInbound(ctx, rec.PartnerID, src, "FAILED", &retry, clip(err.Error())); rerr != nil {
		return false, rerr
	}
	return false, nil
}

func clip(s string) string {
	if len(s) > maxInboundErrorTextSize {
		return s[:maxInboundErrorTextSize]
	}
	return s
}

// move renames a remote file; a failure is logged and left for the next poll
// (the stored status says what to do with the file).
func (p *Poller) move(ctx context.Context, client *transport.SFTPClient, from, to string) error {
	if err := client.Rename(ctx, from, to); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("edi sftp move failed", "from", from, "to", to, "err", err)
	}
	return nil
}
