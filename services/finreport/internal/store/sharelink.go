package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
)

// Problem codes of share links (slice 19). They are slice-local, not yet in
// the PRD catalog.
const (
	CodeShareNotFound  = "REPORT-0050" // no such link, or the token is not one
	CodeShareGone      = "REPORT-0051" // the link expired or was revoked
	CodeShareDays      = "REPORT-0052" // expiresInDays outside 1 to 90
	CodeShareRetention = "REPORT-0053" // the file is past its retention date
	CodeShareRevoked   = "REPORT-0054" // the link was revoked already
)

// ShareActor is the in-process principal that opens a file for a share link
// holder, who has no Passage identity. It reads one file named by a valid
// link and counts the download; it can do nothing else.
const ShareActor = "system:finreport-share-link"

// ShareLink is a link to one report library file (FR-15.8). Token is set only
// on the response that creates the link: the secret is not stored.
type ShareLink struct {
	ID               string     `json:"id"`
	LibraryID        string     `json:"libraryId"`
	LegalEntityID    string     `json:"legalEntityId,omitempty"`
	Status           string     `json:"status"` // active, expired or revoked
	CreatedBy        string     `json:"createdBy"`
	CreatedAt        time.Time  `json:"createdAt"`
	ExpiresAt        time.Time  `json:"expiresAt"`
	RevokedBy        string     `json:"revokedBy,omitempty"`
	RevokedAt        *time.Time `json:"revokedAt,omitempty"`
	DownloadCount    int        `json:"downloadCount"`
	LastDownloadedAt *time.Time `json:"lastDownloadedAt,omitempty"`
	Revision         int        `json:"revision"`
	Token            string     `json:"token,omitempty"`
	// Replayed is true when a repeated Idempotency-Key returned the stored link
	// (without its token).
	Replayed bool `json:"-"`
}

// ShareSpec is a request to create a link.
type ShareSpec struct {
	LibraryID      string
	ExpiresInDays  int
	Actor          string
	IdempotencyKey string
	RequestHash    string
}

const shareColumns = `id::text, library_id::text, coalesce(legal_entity_id::text, ''),
	case when revoked_at is not null then 'revoked' when expires_at <= now() then 'expired' else 'active' end,
	created_by, created_at, expires_at, coalesce(revoked_by, ''), revoked_at, download_count, last_downloaded_at, revision`

func scanShare(row pgx.Row) (ShareLink, error) {
	var l ShareLink
	err := row.Scan(&l.ID, &l.LibraryID, &l.LegalEntityID, &l.Status, &l.CreatedBy, &l.CreatedAt, &l.ExpiresAt,
		&l.RevokedBy, &l.RevokedAt, &l.DownloadCount, &l.LastDownloadedAt, &l.Revision)
	return l, err
}

// CreateShareLink makes a link to a library file that works for ExpiresInDays
// (1 to 90), or until the file's retention date when that is sooner. The key
// makes a retry return the stored link without its token (the secret was shown
// once); a different request under the same key is refused. The link, an audit
// entry and sharelink.created.v1 are written in one transaction.
func (s *Postgres) CreateShareLink(ctx context.Context, spec ShareSpec) (ShareLink, error) {
	if spec.ExpiresInDays < domain.MinShareDays || spec.ExpiresInDays > domain.MaxShareDays {
		return ShareLink{}, problem(422, CodeShareDays, fmt.Sprintf("expiresInDays must be %d to %d", domain.MinShareDays, domain.MaxShareDays))
	}
	var out ShareLink
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var hash string
		err := tx.QueryRow(ctx, `select request_hash from report_share_link where idempotency_key=$1`, spec.IdempotencyKey).Scan(&hash)
		switch {
		case err == nil:
			if hash != spec.RequestHash {
				return problem(422, CodeIdempotencyReuse, "Idempotency-Key was used for a different request")
			}
			out, err = scanShare(tx.QueryRow(ctx, `select `+shareColumns+` from report_share_link where idempotency_key=$1`, spec.IdempotencyKey))
			out.Replayed = true
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		var entity string
		var retained bool
		err = tx.QueryRow(ctx, `select coalesce(legal_entity_id::text, ''), retain_until > now() from report_library_entry where id=$1::uuid`,
			spec.LibraryID).Scan(&entity, &retained)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(404, CodeLibraryNotFound, "report library entry "+spec.LibraryID+" does not exist")
		}
		if err != nil {
			return err
		}
		if !retained {
			return problem(409, CodeShareRetention, "report library entry "+spec.LibraryID+" is past its retention date and cannot be shared")
		}
		token, tokenHash, err := domain.NewShareToken(tenantID(ctx))
		if err != nil {
			return err
		}
		id := ids.New()
		tag, err := tx.Exec(ctx, `insert into report_share_link(tenant_id, id, library_id, legal_entity_id, token_hash, created_by, expires_at, idempotency_key, request_hash)
			select $1::uuid, $2::uuid, e.id, e.legal_entity_id, $4, $5,
			       least(now() + make_interval(hours => $6::int * 24), e.retain_until), $7, $8
			from report_library_entry e where e.id=$3::uuid
			on conflict do nothing`, tenantID(ctx), id, spec.LibraryID, tokenHash, spec.Actor, spec.ExpiresInDays, spec.IdempotencyKey, spec.RequestHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return problem(409, CodeIdempotencyInFlight, "the same Idempotency-Key is being processed; retry")
		}
		if out, err = scanShare(tx.QueryRow(ctx, `select `+shareColumns+` from report_share_link where id=$1::uuid`, id)); err != nil {
			return err
		}
		out.Token = token
		if err := recordAuditTx(ctx, tx, domain.AuditEntry{Kind: domain.AuditShareLinkCreated, ActorID: spec.Actor, ObjectRef: "share_link:" + id,
			Details: map[string]any{"linkId": id, "libraryId": spec.LibraryID, "expiresAt": out.ExpiresAt.UTC().Format(time.RFC3339)}}); err != nil {
			return err
		}
		return enqueue(ctx, tx, "sharelink", id, events.ShareLinkCreated, tenantID(ctx), shareEvent(out, entity, spec.Actor))
	})
	return out, err
}

func shareEvent(l ShareLink, entity, actor string) events.ShareLinkData {
	return events.ShareLinkData{LinkID: l.ID, LibraryID: l.LibraryID, LegalEntityID: entity,
		ExpiresAt: l.ExpiresAt.UTC().Format(time.RFC3339), Actor: actor}
}

// ListShareLinks returns the links of one library file, newest first.
func (s *Postgres) ListShareLinks(ctx context.Context, libraryID string) ([]ShareLink, error) {
	out := []ShareLink{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, _, err := libraryEntryTx(ctx, tx, libraryID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+shareColumns+` from report_share_link where library_id=$1::uuid
			order by created_at desc, id desc limit 500`, libraryID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanShare(rows)
			if err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	return out, err
}

// RevokeShareLink ends a link at once. ifMatch is the revision read (412 when
// stale, 428 when missing); a link revoked already is refused (REPORT-0054).
// The revocation, an audit entry and sharelink.revoked.v1 are written in one
// transaction.
func (s *Postgres) RevokeShareLink(ctx context.Context, id string, ifMatch *int, actor string) (ShareLink, error) {
	var out ShareLink
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		l, err := scanShare(tx.QueryRow(ctx, `select `+shareColumns+` from report_share_link where id=$1::uuid for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(404, CodeShareNotFound, "share link "+id+" does not exist")
		}
		if err != nil {
			return err
		}
		if l.Status == "revoked" {
			return problem(409, CodeShareRevoked, "share link "+id+" was revoked already")
		}
		if ifMatch == nil {
			return problem(428, "precondition_required", "If-Match is required to revoke share link "+id)
		}
		if *ifMatch != l.Revision {
			return problem(412, CodeRevisionConflict, fmt.Sprintf("share link revision is %d, not %d", l.Revision, *ifMatch))
		}
		if _, err := tx.Exec(ctx, `update report_share_link set revoked_at=now(), revoked_by=$2, revision=revision+1 where id=$1::uuid`, id, actor); err != nil {
			return err
		}
		if out, err = scanShare(tx.QueryRow(ctx, `select `+shareColumns+` from report_share_link where id=$1::uuid`, id)); err != nil {
			return err
		}
		if err := recordAuditTx(ctx, tx, domain.AuditEntry{Kind: domain.AuditShareLinkRevoked, ActorID: actor, ObjectRef: "share_link:" + id,
			Details: map[string]any{"linkId": id, "libraryId": l.LibraryID}}); err != nil {
			return err
		}
		return enqueue(ctx, tx, "sharelink", id, events.ShareLinkRevoked, tenantID(ctx), shareEvent(out, l.LegalEntityID, actor))
	})
	return out, err
}

// SharedFile is what a share link opens: the link, and the library file with
// its bytes.
type SharedFile struct {
	LinkID  string
	Entry   LibraryEntry
	Content []byte
}

// RedeemShareLink opens the file a token names, for a caller with no Passage
// identity: the tenant comes from the token, the link from the hash of its
// secret. A token of the wrong shape or with no link answers 404 REPORT-0050; a
// link that expired or was revoked answers 410 REPORT-0051. A served download
// is counted on the link.
//
// The statement that counts the download is also the one that checks the
// revocation and the expiry: an UPDATE waits for a concurrent revocation to
// commit and re-reads the row, so a link revoked a moment earlier never serves
// the file. The count and the read of the file are one transaction.
func (s *Postgres) RedeemShareLink(ctx context.Context, token string) (SharedFile, error) {
	tenantUUID, hash, ok := domain.ParseShareToken(token)
	if !ok {
		return SharedFile{}, problem(404, CodeShareNotFound, "this share link does not exist")
	}
	tctx := tenant.With(ctx, tenant.Principal{TenantID: tenantUUID, UserID: ShareActor, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	var out SharedFile
	err := dbx.WithTenantTx(tctx, s.pool, func(tx pgx.Tx) error {
		var libraryID string
		err := tx.QueryRow(tctx, `update report_share_link set download_count=download_count+1, last_downloaded_at=now()
			where token_hash=$1 and revoked_at is null and expires_at > now()
			returning id::text, library_id::text`, hash).Scan(&out.LinkID, &libraryID)
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if err := tx.QueryRow(tctx, `select exists(select 1 from report_share_link where token_hash=$1)`, hash).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return problem(410, CodeShareGone, "this share link has expired or was revoked")
			}
			return problem(404, CodeShareNotFound, "this share link does not exist")
		}
		if err != nil {
			return err
		}
		out.Entry, out.Content, err = libraryEntryTx(tctx, tx, libraryID, true)
		return err
	})
	if err != nil {
		return SharedFile{}, err
	}
	return out, nil
}
