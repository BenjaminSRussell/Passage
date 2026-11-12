package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/pki"
)

// Enrollment problem codes (see README).
const (
	// CodeEnrollmentInvalid answers every token that cannot be redeemed (unknown, used, expired or
	// replaced) alike, so a caller learns nothing about which tokens exist. 401.
	CodeEnrollmentInvalid = "SF-0171"
	// CodeCSRInvalid is a signing request that cannot be signed (not PEM, bad signature, weak key). 422.
	CodeCSRInvalid = "SF-0172"
	// CodeIssuerMissing is certificate issuing not being configured on this service. 503.
	CodeIssuerMissing = "SF-0173"
	// CodeCertNotFound is a certificate serial the node was never issued. 404.
	CodeCertNotFound = "SF-0185"
)

// EnrollmentTTL is how long a minted token can be redeemed.
const EnrollmentTTL = 24 * time.Hour

// EnrollmentToken is what minting returns, once: the token is not stored and cannot be read again.
type EnrollmentToken struct {
	EdgeID    string    `json:"edgeId"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// EdgeCertificate is what a connector receives for a signing request.
type EdgeCertificate struct {
	EdgeID      string    `json:"edgeId"`
	Subject     string    `json:"subject"`
	Certificate string    `json:"certificate"`
	CAChain     string    `json:"caChain"`
	Serial      string    `json:"serial"`
	NotBefore   time.Time `json:"notBefore"`
	NotAfter    time.Time `json:"notAfter"`
}

// EdgeSubject is the identity bound to a node and written into its certificate's common name.
func EdgeSubject(edgeID string) string { return "edge:" + strings.ToLower(edgeID) }

func tokenHash(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// ParseEnrollmentToken splits "<tenantId>.<edgeId>.<secret>" into its parts. The tenant and node ids
// let the redeeming call, which carries no identity, run under that tenant's row-level security; the
// secret alone is what authenticates it.
func ParseEnrollmentToken(token string) (tenantID, edgeID, secret string, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(parts[2]) < 32 || len(parts[2]) > 128 {
		return "", "", "", false
	}
	tenantID, ok1 := ids.Canonical(parts[0])
	edgeID, ok2 := ids.Canonical(parts[1])
	if !ok1 || !ok2 || tenantID == ids.Nil {
		return "", "", "", false
	}
	return tenantID, edgeID, parts[2], true
}

func enrollmentInvalid() error {
	return httpx.NewProblem(http.StatusUnauthorized, CodeEnrollmentInvalid, "the enrollment token is not valid; ask a supervisor for a new one")
}

// IssueEnrollmentToken mints a one-time token for an edge node and revokes the node's earlier
// unredeemed tokens, so one token is live per node. The node must be switched on. Minting stores only
// the hash of the secret.
func (s *Postgres) IssueEnrollmentToken(ctx context.Context, edgeID, createdBy string, now time.Time) (EnrollmentToken, error) {
	p, _ := tenant.FromContext(ctx)
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return EnrollmentToken{}, err
	}
	secret := base64.RawURLEncoding.EncodeToString(buf)
	out := EnrollmentToken{EdgeID: strings.ToLower(edgeID), ExpiresAt: now.Add(EnrollmentTTL).UTC()}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var active bool
		err := tx.QueryRow(ctx, `select active from edge_node where tenant_id = $1 and id = $2 for update`, p.TenantID, edgeID).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) {
			return edgeNotFound(http.StatusNotFound)
		}
		if err != nil {
			return err
		}
		if !active {
			return edgeInactive()
		}
		if _, err := tx.Exec(ctx, `update edge_enrollment_token set revoked_at = now()
			where tenant_id = $1 and edge_id = $2 and used_at is null and revoked_at is null`, p.TenantID, edgeID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into edge_enrollment_token (tenant_id, id, edge_id, token_hash, expires_at, created_by)
			values ($1, $2, $3, $4, $5, $6)`, p.TenantID, ids.New(), edgeID, tokenHash(secret), out.ExpiresAt, createdBy)
		return err
	})
	if err != nil {
		return EnrollmentToken{}, err
	}
	out.Token = p.TenantID + "." + out.EdgeID + "." + secret
	return out, nil
}

// Enroll redeems a token once: it signs the connector's request for the node's subject, binds that
// subject to the node (so only the certificate's identity may pull configuration or report) and records
// the certificate. The token is consumed in the same transaction, and a second redemption, an expired or
// replaced token, an unknown secret and a switched-off node are all SF-0171. A request that cannot be
// signed leaves the token usable.
func (s *Postgres) Enroll(ctx context.Context, token, csrPEM string, issuer pki.Issuer, now time.Time) (EdgeCertificate, error) {
	tenantID, edgeID, secret, ok := ParseEnrollmentToken(token)
	if !ok {
		return EdgeCertificate{}, enrollmentInvalid()
	}
	if issuer == nil {
		return EdgeCertificate{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeIssuerMissing, "certificate issuing is not configured on this service")
	}
	csr, err := pki.ParseCSR(csrPEM)
	if err != nil {
		return EdgeCertificate{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeCSRInvalid, err.Error())
	}
	ctx = tenant.With(ctx, tenant.Principal{TenantID: tenantID, UserID: "system:edge-enrollment"})
	var out EdgeCertificate
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var tokenID, wantHash string
		var expires time.Time
		var used, revoked *time.Time
		err := tx.QueryRow(ctx, `select t.id::text, t.token_hash, t.expires_at, t.used_at, t.revoked_at
			from edge_enrollment_token t where t.tenant_id = $1 and t.edge_id = $2 and t.token_hash = $3 for update`,
			tenantID, edgeID, tokenHash(secret)).Scan(&tokenID, &wantHash, &expires, &used, &revoked)
		if errors.Is(err, pgx.ErrNoRows) {
			return enrollmentInvalid()
		}
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(wantHash), []byte(tokenHash(secret))) != 1 || used != nil || revoked != nil || !now.Before(expires) {
			return enrollmentInvalid()
		}
		var active bool
		if err := tx.QueryRow(ctx, `select active from edge_node where tenant_id = $1 and id = $2 for update`, tenantID, edgeID).Scan(&active); err != nil {
			return err
		}
		if !active {
			return enrollmentInvalid()
		}
		cert, err := bindCertificate(ctx, tx, tenantID, edgeID, csr, issuer, now, "enroll", "system:edge-enrollment")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update edge_enrollment_token set used_at = now() where tenant_id = $1 and id = $2`, tenantID, tokenID); err != nil {
			return err
		}
		out = cert
		return nil
	})
	return out, err
}

// RenewCertificate issues a fresh certificate for the connector that holds the node's bound identity
// (BR-25.14: renewal starts 30 days before expiry). The subject does not change.
func (s *Postgres) RenewCertificate(ctx context.Context, edgeID, subject, csrPEM string, issuer pki.Issuer, now time.Time) (EdgeCertificate, error) {
	p, _ := tenant.FromContext(ctx)
	if issuer == nil {
		return EdgeCertificate{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeIssuerMissing, "certificate issuing is not configured on this service")
	}
	csr, err := pki.ParseCSR(csrPEM)
	if err != nil {
		return EdgeCertificate{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeCSRInvalid, err.Error())
	}
	var out EdgeCertificate
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, _, err := edgeConnector(ctx, tx, p.TenantID, edgeID, subject, true); err != nil {
			return err
		}
		out, err = bindCertificate(ctx, tx, p.TenantID, edgeID, csr, issuer, now, "renew", subject)
		return err
	})
	return out, err
}

// bindCertificate signs the request for the node's subject, records it and stamps the node with the
// subject, serial and expiry (publishing edgenode.saved, as the node's identity changed). The caller
// has locked the node row.
func bindCertificate(ctx context.Context, tx pgx.Tx, tenantID, edgeID string, csr *x509.CertificateRequest, issuer pki.Issuer, now time.Time, reason, by string) (EdgeCertificate, error) {
	subject := EdgeSubject(edgeID)
	issued, err := issuer.Issue(csr, subject, now)
	if err != nil {
		return EdgeCertificate{}, err
	}
	fp, err := certFingerprint(issued.CertPEM)
	if err != nil {
		return EdgeCertificate{}, err
	}
	if reason == "enroll" {
		// A new enrollment replaces whatever the node held: its earlier certificates (same subject) stop working.
		if _, err := tx.Exec(ctx, `update edge_certificate set revoked_at = $3, revoked_by = $4, revoke_reason = 'superseded by a new enrollment'
			where tenant_id = $1 and edge_id = $2 and revoked_at is null`, tenantID, edgeID, now, by); err != nil {
			return EdgeCertificate{}, err
		}
	}
	if _, err := tx.Exec(ctx, `insert into edge_certificate (tenant_id, id, edge_id, serial, subject, not_before, not_after, reason, fingerprint)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, tenantID, ids.New(), edgeID, issued.Serial, subject, issued.NotBefore, issued.NotAfter, reason, fp); err != nil {
		return EdgeCertificate{}, err
	}
	if _, err := tx.Exec(ctx, `update edge_node set connector_subject = $3, cert_serial = $4, cert_expires_at = $5, version = version + 1, updated_at = now()
		where tenant_id = $1 and id = $2`, tenantID, edgeID, subject, issued.Serial, issued.NotAfter); err != nil {
		return EdgeCertificate{}, edgeNameTaken(err)
	}
	node, err := scanEdgeNode(tx.QueryRow(ctx, `select `+edgeColumns+` from edge_node n where n.id = $1`, edgeID))
	if err != nil {
		return EdgeCertificate{}, err
	}
	if err := edgeEvent(ctx, tx, tenantID, node, by); err != nil {
		return EdgeCertificate{}, err
	}
	return EdgeCertificate{EdgeID: edgeID, Subject: subject, Certificate: issued.CertPEM, CAChain: issued.ChainPEM,
		Serial: issued.Serial, NotBefore: issued.NotBefore, NotAfter: issued.NotAfter}, nil
}

// EdgeNodeTenant finds the tenant of an edge node by its id with the SECURITY DEFINER function edge_node_tenant,
// before any tenant is known (the gateway forwards a connector's certificate, which names a node and no tenant).
// It returns "" for an unknown node.
//
// A certHash that is the fingerprint of a revoked certificate of the node gives "" too (edge_node_tenant_cert): every
// certificate of a node carries the same subject, so a revoked one is told apart by its fingerprint.
func (s *Postgres) EdgeNodeTenant(ctx context.Context, id, certHash string) (string, error) {
	var tenantID *string
	if err := s.pool.QueryRow(ctx, `select edge_node_tenant_cert($1::uuid, $2)::text`, id, strings.ToLower(certHash)).Scan(&tenantID); err != nil {
		return "", err
	}
	if tenantID == nil {
		return "", nil
	}
	return *tenantID, nil
}

// certFingerprint is the SHA-256 of a PEM certificate's DER, hex: what Envoy forwards as the Hash of the client
// certificate.
func certFingerprint(certPEM string) (string, error) {
	blk, _ := pem.Decode([]byte(certPEM))
	if blk == nil {
		return "", errors.New("store: the issued certificate is not PEM")
	}
	sum := sha256.Sum256(blk.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

// CertificateRecord is one certificate a node was issued, with its revocation.
type CertificateRecord struct {
	Serial       string     `json:"serial"`
	Subject      string     `json:"subject"`
	Reason       string     `json:"reason"`
	NotBefore    time.Time  `json:"notBefore"`
	NotAfter     time.Time  `json:"notAfter"`
	IssuedAt     time.Time  `json:"issuedAt"`
	Fingerprint  string     `json:"fingerprint,omitempty"`
	Current      bool       `json:"current"`
	RevokedAt    *time.Time `json:"revokedAt,omitempty"`
	RevokedBy    string     `json:"revokedBy,omitempty"`
	RevokeReason string     `json:"revokeReason,omitempty"`
}

const certCols = `c.serial, c.subject, c.reason, c.not_before, c.not_after, c.issued_at, coalesce(c.fingerprint, ''), c.serial = coalesce(n.cert_serial, ''),
	c.revoked_at, coalesce(c.revoked_by, ''), coalesce(c.revoke_reason, '')`

func scanCert(row pgx.Row) (CertificateRecord, error) {
	var c CertificateRecord
	err := row.Scan(&c.Serial, &c.Subject, &c.Reason, &c.NotBefore, &c.NotAfter, &c.IssuedAt, &c.Fingerprint, &c.Current, &c.RevokedAt, &c.RevokedBy, &c.RevokeReason)
	c.NotBefore, c.NotAfter, c.IssuedAt = c.NotBefore.UTC(), c.NotAfter.UTC(), c.IssuedAt.UTC()
	if c.RevokedAt != nil {
		t := c.RevokedAt.UTC()
		c.RevokedAt = &t
	}
	return c, err
}

// ListEdgeCertificates is the certificate history of a node, newest first (FR-25.19).
func (s *Postgres) ListEdgeCertificates(ctx context.Context, edgeID string) ([]CertificateRecord, error) {
	p, _ := tenant.FromContext(ctx)
	out := []CertificateRecord{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from edge_node where tenant_id = $1 and id = $2)`, p.TenantID, edgeID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return httpx.NewProblem(http.StatusNotFound, CodeEdgeNotFound, "edge node not found")
		}
		rows, err := tx.Query(ctx, `select `+certCols+` from edge_certificate c join edge_node n on n.tenant_id = c.tenant_id and n.id = c.edge_id
			where c.tenant_id = $1 and c.edge_id = $2 order by c.issued_at desc, c.serial`, p.TenantID, edgeID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCert(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// RevokeEdgeCertificate revokes one certificate of a node: the gateway's forwarded fingerprint of that certificate no
// longer gives an identity. A certificate issued before fingerprints were kept cannot be told apart from the node's
// other unfingerprinted ones, so revoking it revokes all of them. Revoking the node's current certificate (or a group
// it is in) also unbinds the node's connector subject, so the connector has to enroll again with a new token.
// Revoking a revoked certificate changes nothing.
func (s *Postgres) RevokeEdgeCertificate(ctx context.Context, edgeID, serial, reason, by string, now time.Time) (CertificateRecord, error) {
	p, _ := tenant.FromContext(ctx)
	var out CertificateRecord
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var curSerial *string
		err := tx.QueryRow(ctx, `select cert_serial from edge_node where tenant_id = $1 and id = $2 for update`, p.TenantID, edgeID).Scan(&curSerial)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeEdgeNotFound, "edge node not found")
		}
		if err != nil {
			return err
		}
		var legacy bool
		err = tx.QueryRow(ctx, `select fingerprint is null from edge_certificate where tenant_id = $1 and edge_id = $2 and serial = $3`, p.TenantID, edgeID, serial).Scan(&legacy)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeCertNotFound, "the node has no certificate with that serial")
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `update edge_certificate set revoked_at = $4, revoked_by = $5, revoke_reason = $6
			where tenant_id = $1 and edge_id = $2 and revoked_at is null and (serial = $3 or ($7 and fingerprint is null))
			returning serial`, p.TenantID, edgeID, serial, now, by, reason, legacy)
		if err != nil {
			return err
		}
		hitCurrent := false
		for rows.Next() {
			var sr string
			if err := rows.Scan(&sr); err != nil {
				rows.Close()
				return err
			}
			hitCurrent = hitCurrent || (curSerial != nil && *curSerial == sr)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if hitCurrent {
			if _, err := tx.Exec(ctx, `update edge_node set connector_subject = null, cert_serial = null, cert_expires_at = null, version = version + 1, updated_at = now()
				where tenant_id = $1 and id = $2`, p.TenantID, edgeID); err != nil {
				return err
			}
			node, err := scanEdgeNode(tx.QueryRow(ctx, `select `+edgeColumns+` from edge_node n where n.id = $1`, edgeID))
			if err != nil {
				return err
			}
			if err := edgeEvent(ctx, tx, p.TenantID, node, by); err != nil {
				return err
			}
		}
		out, err = scanCert(tx.QueryRow(ctx, `select `+certCols+` from edge_certificate c join edge_node n on n.tenant_id = c.tenant_id and n.id = c.edge_id
			where c.tenant_id = $1 and c.edge_id = $2 and c.serial = $3`, p.TenantID, edgeID, serial))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeCertNotFound, "the node has no certificate with that serial")
		}
		return err
	})
	return out, err
}
