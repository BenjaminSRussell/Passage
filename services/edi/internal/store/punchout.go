package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
)

// PunchoutSupplier is a supplier's punchout settings (§6.35.13): where the catalog is set up, the network identities of the two
// sides, the secret reference of the shared secret (a file in the mounted secrets, never the secret), and the BR-35.13 inputs.
type PunchoutSupplier struct {
	PartnerID        string                     `json:"partnerId"`
	LegalEntityID    string                     `json:"legalEntityId"`
	SetupURL         string                     `json:"setupUrl"`
	BuyerIdentity    string                     `json:"buyerIdentity"`
	SupplierIdentity string                     `json:"supplierIdentity"`
	SharedSecretRef  string                     `json:"sharedSecretRef,omitempty"`
	Currencies       []string                   `json:"currencies"`
	UoMMap           map[string]string          `json:"uomMap"`
	TolerancePct     decimal.Decimal            `json:"tolerancePct"`
	ReferencePrices  map[string]decimal.Decimal `json:"referencePrices"`
	SessionMinutes   int                        `json:"sessionMinutes"`
}

// Domain is the supplier's BR-35.13 settings.
func (s PunchoutSupplier) Domain() domain.SupplierPunchout {
	return domain.SupplierPunchout{Currencies: s.Currencies, UoMMap: s.UoMMap, TolerancePct: s.TolerancePct, ReferencePrices: s.ReferencePrices}
}

// PutPunchoutSupplier stores the settings under the legal entity of the partner's trade profile (ErrNotFound without one).
func (s *Store) PutPunchoutSupplier(ctx context.Context, partnerID string, p PunchoutSupplier) error {
	uom, _ := json.Marshal(p.UoMMap)
	prices, _ := json.Marshal(p.ReferencePrices)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `insert into punchout_supplier (tenant_id, partner_id, legal_entity_id, setup_url, buyer_identity, supplier_identity, shared_secret_ref,
			currencies, uom_map, tolerance_pct, reference_prices, session_minutes)
			select $1::uuid, partner_id, legal_entity_id, $3, $4, $5, $6, $7, $8, $9, $10, $11 from partner_trade_profile where partner_id = $2
			on conflict (tenant_id, partner_id) do update set setup_url = excluded.setup_url, buyer_identity = excluded.buyer_identity,
			supplier_identity = excluded.supplier_identity, shared_secret_ref = excluded.shared_secret_ref, currencies = excluded.currencies,
			uom_map = excluded.uom_map, tolerance_pct = excluded.tolerance_pct, reference_prices = excluded.reference_prices,
			session_minutes = excluded.session_minutes, updated_at = now()`,
			tid, partnerID, p.SetupURL, p.BuyerIdentity, p.SupplierIdentity, p.SharedSecretRef, p.Currencies, uom, p.TolerancePct, prices, p.SessionMinutes)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// GetPunchoutSupplier returns the settings or ErrNotFound.
func (s *Store) GetPunchoutSupplier(ctx context.Context, partnerID string) (PunchoutSupplier, error) {
	p := PunchoutSupplier{PartnerID: partnerID}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var uom, prices []byte
		err := tx.QueryRow(ctx, `select legal_entity_id::text, setup_url, buyer_identity, supplier_identity, shared_secret_ref, currencies, uom_map, tolerance_pct,
			reference_prices, session_minutes from punchout_supplier where partner_id = $1`, partnerID).
			Scan(&p.LegalEntityID, &p.SetupURL, &p.BuyerIdentity, &p.SupplierIdentity, &p.SharedSecretRef, &p.Currencies, &uom, &p.TolerancePct, &prices, &p.SessionMinutes)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		p.UoMMap, p.ReferencePrices = map[string]string{}, map[string]decimal.Decimal{}
		if err := json.Unmarshal(uom, &p.UoMMap); err != nil {
			return err
		}
		return json.Unmarshal(prices, &p.ReferencePrices)
	})
	return p, err
}

// PunchoutSession is one visit to a supplier's catalog by a requester.
type PunchoutSession struct {
	ID            string          `json:"id"`
	PartnerID     string          `json:"partnerId"`
	LegalEntityID string          `json:"legalEntityId"`
	RequesterID   string          `json:"requesterId"`
	Format        string          `json:"format"`
	Status        string          `json:"status"`
	StartURL      string          `json:"startUrl,omitempty"`
	ExpiresAt     time.Time       `json:"expiresAt"`
	ReturnedAt    *time.Time      `json:"returnedAt,omitempty"`
	Cart          json.RawMessage `json:"cart,omitempty"`
	RequisitionID string          `json:"requisitionId,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
	// SecretHash and BuyerCookie are the session's credentials; they are never returned.
	SecretHash  string `json:"-"`
	BuyerCookie string `json:"-"`
}

// Errors of the session lifecycle.
var (
	ErrPunchoutState   = errors.New("edi: punchout session is not open")
	ErrPunchoutExpired = errors.New("edi: punchout session expired")
)

const sessionColumns = `select id::text, partner_id::text, legal_entity_id::text, requester_id::text, format, status, coalesce(start_url, ''), expires_at, returned_at,
	cart, coalesce(requisition_id::text, ''), created_at, secret_hash, buyer_cookie from punchout_session`

func scanSession(row pgx.Row, s *PunchoutSession) error {
	var cart []byte
	err := row.Scan(&s.ID, &s.PartnerID, &s.LegalEntityID, &s.RequesterID, &s.Format, &s.Status, &s.StartURL, &s.ExpiresAt, &s.ReturnedAt, &cart,
		&s.RequisitionID, &s.CreatedAt, &s.SecretHash, &s.BuyerCookie)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	s.Cart = cart
	return err
}

// CreatePunchoutSession stores an OPEN session (its id, hashes and expiry are set by the caller).
func (s *Store) CreatePunchoutSession(ctx context.Context, p PunchoutSession) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into punchout_session (tenant_id, id, partner_id, legal_entity_id, requester_id, format, status, secret_hash, buyer_cookie, start_url, expires_at)
			values ($1,$2,$3,$4,$5,$6,'OPEN',$7,$8,nullif($9,''),$10)`,
			tid, p.ID, p.PartnerID, p.LegalEntityID, p.RequesterID, p.Format, p.SecretHash, p.BuyerCookie, p.StartURL, p.ExpiresAt)
		return err
	})
}

// GetPunchoutSession returns one session or ErrNotFound.
func (s *Store) GetPunchoutSession(ctx context.Context, id string) (PunchoutSession, error) {
	var out PunchoutSession
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return scanSession(tx.QueryRow(ctx, sessionColumns+` where id = $1`, id), &out)
	})
	return out, err
}

// ListPunchoutSessions lists the requester's sessions newest first.
func (s *Store) ListPunchoutSessions(ctx context.Context, requesterID string, limit int) ([]PunchoutSession, error) {
	out := []PunchoutSession{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sessionColumns+` where requester_id::text = $1 order by created_at desc, id limit $2`, requesterID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p PunchoutSession
			if err := scanSession(rows, &p); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// ReturnPunchoutCart stores the validated cart of an OPEN, unexpired session: RETURNED with its lines, or REJECTED when the cart
// total did not match (EDI-0134). One punchout.returned event carries the lines.
func (s *Store) ReturnPunchoutCart(ctx context.Context, id string, res domain.CartResult, now time.Time) (PunchoutSession, error) {
	var out PunchoutSession
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		if err := scanSession(tx.QueryRow(ctx, sessionColumns+` where id = $1 for update`, id), &out); err != nil {
			return err
		}
		if out.Status != "OPEN" {
			return ErrPunchoutState
		}
		if !now.Before(out.ExpiresAt) {
			return ErrPunchoutExpired
		}
		status := "RETURNED"
		if res.CartRejected {
			status = "REJECTED"
		}
		cart, err := json.Marshal(res)
		if err != nil {
			return err
		}
		issues, err := json.Marshal(res.Issues)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update punchout_session set status = $2, cart = $3, issues = $4, returned_at = $5 where id = $1`, id, status, cart, issues, now); err != nil {
			return err
		}
		out.Status, out.Cart, out.ReturnedAt = status, cart, &now
		data := ediev.PunchoutReturnedData{SessionID: id, PartnerID: out.PartnerID, LegalEntityID: out.LegalEntityID, RequesterID: out.RequesterID,
			CartRejected: res.CartRejected, Lines: []ediev.PunchoutLine{}}
		for _, l := range res.Lines {
			data.Lines = append(data.Lines, ediev.PunchoutLine{Line: l.Line, SupplierPartID: l.SupplierPartID, Description: l.Description,
				Quantity: l.Quantity.String(), UnitCode: l.UnitCode, UnitPrice: l.UnitPrice.String(), Currency: l.Currency, UNSPSC: l.UNSPSC,
				LeadTimeDays: l.LeadTimeDays, Catalog: l.Catalog, NeedsApproval: l.NeedsApproval})
		}
		return enqueueEvent(ctx, tx, tid, ediev.PunchoutReturned, "punchout", id, data)
	})
	return out, err
}

// CancelPunchoutSession cancels an OPEN session.
func (s *Store) CancelPunchoutSession(ctx context.Context, id string) (PunchoutSession, error) {
	var out PunchoutSession
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := scanSession(tx.QueryRow(ctx, sessionColumns+` where id = $1 for update`, id), &out); err != nil {
			return err
		}
		if out.Status != "OPEN" {
			return ErrPunchoutState
		}
		out.Status = "CANCELLED"
		_, err := tx.Exec(ctx, `update punchout_session set status = 'CANCELLED' where id = $1`, id)
		return err
	})
	return out, err
}

// SetPunchoutRequisition records the requisition a RETURNED session became. It is set once.
func (s *Store) SetPunchoutRequisition(ctx context.Context, id, requisitionID string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update punchout_session set requisition_id = $2 where id = $1 and requisition_id is null`, id, requisitionID)
		return err
	})
}

// ResolvePublicEndpoint finds the tenant and partner behind a public endpoint id: kind CXML_ENDPOINT (a partner's cXML URL) or
// PUNCHOUT_RETURN (a session's return URL). Nothing else is returned, so a caller learns no more than the id's owner.
func (s *Store) ResolvePublicEndpoint(ctx context.Context, kind, ref string) (tenantID, partnerID string, err error) {
	err = s.pool.QueryRow(ctx, `select tenant_id::text, partner_id::text from edi_public_endpoint($1, $2::uuid)`, kind, ref).Scan(&tenantID, &partnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return tenantID, partnerID, err
}

// PunchoutSupplierRef is a supplier a requester can open a punchout catalog of: an active partner with punchout settings.
type PunchoutSupplierRef struct {
	PartnerID     string   `json:"partnerId"`
	Name          string   `json:"name"`
	LegalEntityID string   `json:"legalEntityId"`
	Currencies    []string `json:"currencies"`
}

// ListPunchoutSuppliers lists the active partners with punchout settings in the caller's legal entities, by name (#35, 35-3).
func (s *Store) ListPunchoutSuppliers(ctx context.Context, limit int) ([]PunchoutSupplierRef, error) {
	out := []PunchoutSupplierRef{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select p.id::text, p.name, s.legal_entity_id::text, s.currencies from punchout_supplier s
			join trading_partner p on p.tenant_id = s.tenant_id and p.id = s.partner_id
			where p.status = 'active' order by p.name, p.id limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r PunchoutSupplierRef
			if err := rows.Scan(&r.PartnerID, &r.Name, &r.LegalEntityID, &r.Currencies); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
