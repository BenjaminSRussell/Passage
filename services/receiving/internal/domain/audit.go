package domain

import (
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

const (
	CodeAuditBlocked     = "AP-AUD-001"
	CodeNotAuditBlocked  = "AP-AUD-002"
	CodeAuditSelfRelease = "AP-AUD-003"

	InvoiceStatusPosted        = "POSTED"
	InvoiceStatusAuditBlocked  = "AUDIT_BLOCKED"
	InvoiceStatusAuditRejected = "AUDIT_REJECTED"
	InvoiceSourceManual        = "MANUAL"
	InvoiceSourceERS           = "ERS"

	sampleBuckets = 10000
)

var bucketsPerPct = decimal.NewFromInt(sampleBuckets / 100)

type Sampler struct{ seed []byte }

const minSeedBytes = 16

func NewSampler(seed []byte) (*Sampler, error) {
	if len(seed) < minSeedBytes {
		return nil, fmt.Errorf("the audit sample seed needs at least %d bytes", minSeedBytes)
	}
	return &Sampler{seed: append([]byte(nil), seed...)}, nil
}

func NewRandomSampler() (*Sampler, error) {
	seed := make([]byte, 32)
	if _, err := crand.Read(seed); err != nil {
		return nil, err
	}
	return NewSampler(seed)
}

func ValidSampleRate(ratePct decimal.Decimal) bool {
	return !ratePct.IsNegative() && !ratePct.GreaterThan(hundred) && ratePct.Exponent() >= -2
}

func (s *Sampler) Blocks(invoiceID string, ratePct decimal.Decimal) bool {
	if s == nil || !ratePct.IsPositive() {
		return false
	}
	mac := hmac.New(sha256.New, s.seed)
	mac.Write([]byte(strings.ToLower(invoiceID)))
	bucket := binary.BigEndian.Uint64(mac.Sum(nil)[:8]) % sampleBuckets
	return int64(bucket) < ratePct.Mul(bucketsPerPct).IntPart()
}
