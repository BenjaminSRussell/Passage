package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// Share link limits (PRD §2.15.14 `reporting.share_link_expiry_days`: 14 by
// default, 1 to 90).
const (
	MinShareDays = 1
	MaxShareDays = 90
)

const (
	shareSecretBytes = 32
	shareTenantHex   = 32
)

// NewShareToken returns a new bearer token for a link of tenantID and the
// SHA-256 (hex) of its secret, the only part that is stored. The token is the
// tenant id as 32 hex digits, a dot and 256 random bits in unpadded base64url:
// the tenant part lets the unauthenticated download route open the right
// tenant's data without any header, and carries no authority by itself.
func NewShareToken(tenantID string) (token, hash string, err error) {
	t := strings.ReplaceAll(strings.ToLower(tenantID), "-", "")
	if len(t) != shareTenantHex || !isHex(t) {
		return "", "", fmt.Errorf("tenant id %q is not a UUID", tenantID)
	}
	buf := make([]byte, shareSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(buf)
	return t + "." + secret, hashSecret(secret), nil
}

// ParseShareToken splits a token into the tenant id (canonical UUID text) and
// the hash of its secret. Anything that is not exactly the shape NewShareToken
// writes is refused, so malformed input never reaches the database; so is the
// nil (platform) tenant, which no link belongs to.
//
// The tenant part is not trusted: it only picks the tenant whose row-level
// security the lookup runs under, and the lookup is by the SHA-256 of the
// secret, so a secret presented under any other tenant finds nothing.
func ParseShareToken(token string) (tenantID, hash string, ok bool) {
	t, secret, found := strings.Cut(token, ".")
	if !found || len(t) != shareTenantHex || !isHex(t) || t == strings.Repeat("0", shareTenantHex) {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(raw) != shareSecretBytes || base64.RawURLEncoding.EncodeToString(raw) != secret {
		return "", "", false
	}
	tenantID = t[0:8] + "-" + t[8:12] + "-" + t[12:16] + "-" + t[16:20] + "-" + t[20:]
	return tenantID, hashSecret(secret), true
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
