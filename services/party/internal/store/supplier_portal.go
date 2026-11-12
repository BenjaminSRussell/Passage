package store

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	partyevents "github.com/BenjaminSRussell/passage/services/party/internal/events"
)

// Prospect registration and vendor-admin invitations (#511 remainders).

const (
	CodeProspectNotFound = "PTY-PROSPECT-NOT-FOUND"
	CodeProspectState    = "PTY-PROSPECT-STATE"
	CodeProspectLimit    = "PTY-PROSPECT-LIMIT"
	CodeInviteInvalid    = "PTY-INVITE-INVALID"

	// MaxOpenProspects bounds the registrations waiting for a buyer in one legal entity, so the public page cannot fill the table.
	MaxOpenProspects = 2000
)

type Prospect struct {
	ID           string     `json:"id"`
	CompanyName  string     `json:"companyName"`
	ContactName  string     `json:"contactName"`
	Email        string     `json:"email"`
	Phone        string     `json:"phone,omitempty"`
	Country      string     `json:"country"`
	TaxID        string     `json:"taxId,omitempty"`
	Website      string     `json:"website,omitempty"`
	Message      string     `json:"message,omitempty"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"createdAt"`
	DecidedAt    *time.Time `json:"decidedAt,omitempty"`
	DecisionNote string     `json:"decisionNote,omitempty"`
	PartyID      string     `json:"partyId,omitempty"`
	VendorID     string     `json:"vendorId,omitempty"`
	RequestID    string     `json:"requestId,omitempty"`
}

type Invite struct {
	ID          string     `json:"id"`
	VendorID    string     `json:"vendorId"`
	Email       string     `json:"email"`
	DisplayName string     `json:"displayName,omitempty"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	InvitedBy   string     `json:"invitedBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	AcceptedAt  *time.Time `json:"acceptedAt,omitempty"`
}

// InviteDetails is what verifying or accepting an invitation tells the portal: who to create in the identity provider and for
// which vendor. The tenant and legal entity come from the verified token, not from here.
type InviteDetails struct {
	Invite
	LegalEntityID string `json:"legalEntityId"`
	VendorName    string `json:"vendorName"`
}

// PortalStore is the persistence of prospect registration and invitations.
type PortalStore interface {
	RegisterProspect(ctx context.Context, in domain.NewProspect) (bool, error)
	ListProspects(ctx context.Context, status string, limit int) ([]Prospect, error)
	GetProspect(ctx context.Context, id string) (Prospect, error)
	RejectProspect(ctx context.Context, id, note string) (Prospect, error)
	ClaimProspect(ctx context.Context, id string) (Prospect, error)
	AcceptProspect(ctx context.Context, id, partyID, vendorID, requestID string) (Prospect, error)

	CreateInvite(ctx context.Context, vendorID, email, displayName string) (Invite, string, error)
	ListInvites(ctx context.Context, vendorID string, limit int) ([]Invite, error)
	RevokeInvite(ctx context.Context, vendorID, id string) (Invite, error)
	VerifyInvite(ctx context.Context, vendorID, token string) (InviteDetails, error)
	AcceptInvite(ctx context.Context, vendorID, token string) (InviteDetails, error)
}

func prospectNotFound() error { return notFound(CodeProspectNotFound, "not found") }

func inviteInvalid() error {
	return httpx.NewProblem(http.StatusNotFound, CodeInviteInvalid, "the invitation is not valid, has expired or was already used")
}

// RegisterProspect records a public registration. It reports false, and writes nothing, when the same email already waits for a
// buyer: the page answers the same either way, so it never tells a stranger who has registered.
func (s *Postgres) RegisterProspect(ctx context.Context, in domain.NewProspect) (bool, error) {
	le, err := singleEntity(ctx)
	if err != nil {
		return false, err
	}
	created := false
	err = run(ctx, s, func(tx pgx.Tx) error {
		var open int
		if err := tx.QueryRow(ctx, `select count(*) from vendor_prospect where tenant_id = $1 and legal_entity_id = $2 and status = 'NEW'`, tenantOf(ctx), le).Scan(&open); err != nil {
			return err
		}
		if open >= MaxOpenProspects {
			return httpx.NewProblem(http.StatusTooManyRequests, CodeProspectLimit, "too many registrations are waiting; try again later")
		}
		tag, err := tx.Exec(ctx, `insert into vendor_prospect (tenant_id, id, legal_entity_id, company_name, contact_name, email, phone, country, tax_id, website, message)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) on conflict do nothing`,
			tenantOf(ctx), ids.New(), le, in.CompanyName, in.ContactName, in.Email, in.Phone, in.Country, in.TaxID, in.Website, in.Message)
		created = tag.RowsAffected() == 1
		return err
	})
	return created, err
}

const prospectColumns = `id::text, company_name, contact_name, email, phone, country, tax_id, website, message, status, created_at, decided_at, decision_note, coalesce(party_id::text, ''), coalesce(vendor_id::text, ''), coalesce(request_id::text, '')`

func scanProspect(row pgx.Row) (Prospect, error) {
	var p Prospect
	err := row.Scan(&p.ID, &p.CompanyName, &p.ContactName, &p.Email, &p.Phone, &p.Country, &p.TaxID, &p.Website, &p.Message, &p.Status, &p.CreatedAt, &p.DecidedAt, &p.DecisionNote, &p.PartyID, &p.VendorID, &p.RequestID)
	return p, err
}

func (s *Postgres) ListProspects(ctx context.Context, status string, limit int) ([]Prospect, error) {
	out := []Prospect{}
	err := run(ctx, s, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+prospectColumns+` from vendor_prospect where tenant_id = $1 and ($2 = '' or status = $2) order by created_at desc, id limit $3`, tenantOf(ctx), status, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanProspect(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) GetProspect(ctx context.Context, id string) (Prospect, error) {
	pid, err := canonicalID(id)
	if err != nil {
		return Prospect{}, prospectNotFound()
	}
	var out Prospect
	err = run(ctx, s, func(tx pgx.Tx) error {
		var err error
		out, err = scanProspect(tx.QueryRow(ctx, `select `+prospectColumns+` from vendor_prospect where tenant_id = $1 and id = $2`, tenantOf(ctx), pid))
		return noProspect(err)
	})
	return out, err
}

// noProspect names a missing row; run would otherwise report it as a missing supplier row.
func noProspect(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return prospectNotFound()
	}
	return err
}

// RejectProspect closes a NEW registration; nothing in the master data was created for it.
func (s *Postgres) RejectProspect(ctx context.Context, id, note string) (Prospect, error) {
	pid, err := canonicalID(id)
	if err != nil {
		return Prospect{}, prospectNotFound()
	}
	var out Prospect
	err = run(ctx, s, func(tx pgx.Tx) error {
		cur, err := scanProspect(tx.QueryRow(ctx, `select `+prospectColumns+` from vendor_prospect where tenant_id = $1 and id = $2 for update`, tenantOf(ctx), pid))
		if err != nil {
			return noProspect(err)
		}
		if cur.Status != domain.ProspectNew {
			return httpx.NewProblem(http.StatusConflict, CodeProspectState, "the registration is already "+cur.Status)
		}
		out, err = scanProspect(tx.QueryRow(ctx, `update vendor_prospect set status = $3, decided_by = $4, decided_at = now(), decision_note = $5
			where tenant_id = $1 and id = $2 returning `+prospectColumns, tenantOf(ctx), pid, domain.ProspectRejected, actorOf(ctx), note))
		return err
	})
	return out, err
}

// prospectClaimTimeout is how long an accept that stopped part-way stays with the buyer who started it; after that another buyer
// may finish it.
const prospectClaimTimeout = "10 minutes"

// ClaimProspect starts a buyer's accept: a NEW registration becomes ACCEPTED, decided by the caller, before any master data is
// created, so two buyers accepting at once cannot both go on (the second is refused). An accept that stopped part-way (ACCEPTED,
// no onboarding linked yet) is claimed again by the buyer who started it, or by anyone once prospectClaimTimeout has passed, so a
// repeated call finishes it.
func (s *Postgres) ClaimProspect(ctx context.Context, id string) (Prospect, error) {
	pid, err := canonicalID(id)
	if err != nil {
		return Prospect{}, prospectNotFound()
	}
	var out Prospect
	err = run(ctx, s, func(tx pgx.Tx) error {
		var err error
		out, err = scanProspect(tx.QueryRow(ctx, `update vendor_prospect set status = $3, decided_by = $4, decided_at = now()
			where tenant_id = $1 and id = $2 and (status = 'NEW'
				or (status = $3 and request_id is null and (decided_by = $4 or decided_at < now() - $5::interval)))
			returning `+prospectColumns, tenantOf(ctx), pid, domain.ProspectAccepted, actorOf(ctx), prospectClaimTimeout))
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		cur, err := scanProspect(tx.QueryRow(ctx, `select `+prospectColumns+` from vendor_prospect where tenant_id = $1 and id = $2`, tenantOf(ctx), pid))
		if err != nil {
			return noProspect(err)
		}
		if cur.Status == domain.ProspectAccepted && cur.RequestID == "" {
			return httpx.NewProblem(http.StatusConflict, CodeProspectState, "another buyer is accepting the registration")
		}
		return httpx.NewProblem(http.StatusConflict, CodeProspectState, "the registration is already "+cur.Status)
	})
	return out, err
}

// AcceptProspect finishes the accept the caller claimed, after it created the party, the blocked vendor role and the onboarding,
// by linking them to the registration. With no request id (a repeated accept found the onboarding already open) it links the
// vendor's latest onboarding request.
func (s *Postgres) AcceptProspect(ctx context.Context, id, partyID, vendorID, requestID string) (Prospect, error) {
	pid, err := canonicalID(id)
	if err != nil {
		return Prospect{}, prospectNotFound()
	}
	var out Prospect
	err = run(ctx, s, func(tx pgx.Tx) error {
		var err error
		out, err = scanProspect(tx.QueryRow(ctx, `update vendor_prospect set party_id = nullif($5, '')::uuid, vendor_id = nullif($6, '')::uuid,
				request_id = coalesce(nullif($7, '')::uuid, (select r.id from vendor_request r where r.tenant_id = $1 and r.vendor_id = nullif($6, '')::uuid and r.kind = 'ONBOARDING' order by r.created_at desc limit 1))
			where tenant_id = $1 and id = $2 and status = $3 and request_id is null and decided_by = $4
			returning `+prospectColumns, tenantOf(ctx), pid, domain.ProspectAccepted, actorOf(ctx), partyID, vendorID, requestID))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusConflict, CodeProspectState, "the registration is not being accepted by the caller")
		}
		if err != nil {
			return err
		}
		if out.RequestID == "" {
			return supplierState("the vendor has no onboarding request")
		}
		return nil
	})
	return out, err
}

const inviteColumns = `id::text, vendor_id::text, email, display_name, role, status, expires_at, invited_by, created_at, accepted_at`

func scanInvite(row pgx.Row) (Invite, error) {
	var i Invite
	err := row.Scan(&i.ID, &i.VendorID, &i.Email, &i.DisplayName, &i.Role, &i.Status, &i.ExpiresAt, &i.InvitedBy, &i.CreatedAt, &i.AcceptedAt)
	i.Status = domain.InviteStatus(i.Status, i.ExpiresAt, time.Now())
	return i, err
}

// CreateInvite invites a colleague to a vendor and returns the token, which is not stored and cannot be read again. An open
// invitation for the same address is replaced, so "resend" works and an old link stops working.
func (s *Postgres) CreateInvite(ctx context.Context, vendorID, email, displayName string) (Invite, string, error) {
	vid, err := supplierID(vendorID)
	if err != nil {
		return Invite{}, "", err
	}
	var out Invite
	var token string
	err = run(ctx, s, func(tx pgx.Tx) error {
		le, err := vendorEntity(ctx, tx, vid)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update vendor_invite set status = 'REVOKED' where tenant_id = $1 and vendor_id = $2 and lower(email) = lower($3) and status = 'PENDING'`, tenantOf(ctx), vid, email); err != nil {
			return err
		}
		id := ids.New()
		var hash string
		token, hash = domain.NewInviteToken(tenantOf(ctx), le, vid, id)
		out, err = scanInvite(tx.QueryRow(ctx, `insert into vendor_invite (tenant_id, id, legal_entity_id, vendor_id, email, display_name, role, token_hash, expires_at, invited_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, now() + $9::interval, $10) returning `+inviteColumns,
			tenantOf(ctx), id, le, vid, email, displayName, domain.InviteRole, hash, domain.InviteTTL.String(), actorOf(ctx)))
		if err != nil {
			return err
		}
		if err := writeAudit(ctx, tx, le, vid, "invite.created", id, ""); err != nil {
			return err
		}
		return enqueueInvite(ctx, tx, partyevents.VendorInviteCreated, le, out)
	})
	return out, token, err
}

func enqueueInvite(ctx context.Context, tx pgx.Tx, eventType, le string, i Invite) error {
	evt, err := events.New(eventType, "party", i.ID, tenantOf(ctx), partyevents.VendorInviteData{InviteID: i.ID, VendorID: i.VendorID, LegalEntityID: le, Email: i.Email, ExpiresAt: i.ExpiresAt})
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "vendorinvite", i.ID, evt)
}

func (s *Postgres) ListInvites(ctx context.Context, vendorID string, limit int) ([]Invite, error) {
	out := []Invite{}
	err := run(ctx, s, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+inviteColumns+` from vendor_invite where tenant_id = $1 and vendor_id::text = $2 order by created_at desc, id limit $3`, tenantOf(ctx), vendorID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			i, err := scanInvite(rows)
			if err != nil {
				return err
			}
			out = append(out, i)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) RevokeInvite(ctx context.Context, vendorID, id string) (Invite, error) {
	iid, err := supplierID(id)
	if err != nil {
		return Invite{}, err
	}
	var out Invite
	err = run(ctx, s, func(tx pgx.Tx) error {
		cur, err := scanInvite(tx.QueryRow(ctx, `select `+inviteColumns+` from vendor_invite where tenant_id = $1 and id = $2 and vendor_id::text = $3 for update`, tenantOf(ctx), iid, vendorID))
		if err != nil {
			return err
		}
		if cur.Status == domain.InviteAccepted {
			return supplierState("the invitation was already accepted")
		}
		if out, err = scanInvite(tx.QueryRow(ctx, `update vendor_invite set status = 'REVOKED' where tenant_id = $1 and id = $2 returning `+inviteColumns, tenantOf(ctx), iid)); err != nil {
			return err
		}
		le, err := vendorEntity(ctx, tx, vendorID)
		if err != nil {
			return err
		}
		return writeAudit(ctx, tx, le, vendorID, "invite.revoked", iid, "")
	})
	return out, err
}

// openInvite finds the invitation a token names and checks the secret, the vendor of the verified caller and the state, in one
// answer for every failure so a stranger learns nothing.
func openInvite(ctx context.Context, tx pgx.Tx, vendorID, token string, lock bool) (InviteDetails, error) {
	t, ok := domain.ParseInviteToken(token)
	if !ok || !strings.EqualFold(t.VendorID, vendorID) || t.TenantID != tenantOf(ctx) {
		return InviteDetails{}, inviteInvalid()
	}
	suffix := ""
	if lock {
		suffix = " for update of i"
	}
	var d InviteDetails
	var hash string
	err := tx.QueryRow(ctx, `select i.id::text, i.vendor_id::text, i.email, i.display_name, i.role, i.status, i.expires_at, i.invited_by, i.created_at, i.accepted_at, i.legal_entity_id::text, i.token_hash, p.name
		from vendor_invite i join vendor_role v on v.tenant_id = i.tenant_id and v.id = i.vendor_id join party p on p.tenant_id = v.tenant_id and p.id = v.party_id
		where i.tenant_id = $1 and i.id::text = $2 and i.vendor_id::text = $3`+suffix, tenantOf(ctx), t.InviteID, vendorID).
		Scan(&d.ID, &d.VendorID, &d.Email, &d.DisplayName, &d.Role, &d.Status, &d.ExpiresAt, &d.InvitedBy, &d.CreatedAt, &d.AcceptedAt, &d.LegalEntityID, &hash, &d.VendorName)
	if errors.Is(err, pgx.ErrNoRows) {
		return InviteDetails{}, inviteInvalid()
	}
	if err != nil {
		return InviteDetails{}, err
	}
	d.Status = domain.InviteStatus(d.Status, d.ExpiresAt, time.Now())
	if !domain.InviteSecretMatches(t.Secret, hash) || d.Status != domain.InvitePending || !strings.EqualFold(d.LegalEntityID, t.LegalEntityID) {
		return InviteDetails{}, inviteInvalid()
	}
	return d, nil
}

// VerifyInvite checks a token without using it, so the portal can create the user before the invitation is spent.
func (s *Postgres) VerifyInvite(ctx context.Context, vendorID, token string) (InviteDetails, error) {
	var d InviteDetails
	err := run(ctx, s, func(tx pgx.Tx) error {
		var err error
		d, err = openInvite(ctx, tx, vendorID, token, false)
		return err
	})
	return d, err
}

// AcceptInvite spends an invitation. A second accept of the same token is refused.
func (s *Postgres) AcceptInvite(ctx context.Context, vendorID, token string) (InviteDetails, error) {
	var d InviteDetails
	err := run(ctx, s, func(tx pgx.Tx) error {
		var err error
		if d, err = openInvite(ctx, tx, vendorID, token, true); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update vendor_invite set status = 'ACCEPTED', accepted_at = now() where tenant_id = $1 and id = $2`, tenantOf(ctx), d.ID); err != nil {
			return err
		}
		d.Status = domain.InviteAccepted
		if err := writeAudit(ctx, tx, d.LegalEntityID, d.VendorID, "invite.accepted", d.ID, ""); err != nil {
			return err
		}
		return enqueueInvite(ctx, tx, partyevents.VendorInviteAccepted, d.LegalEntityID, d.Invite)
	})
	return d, err
}
