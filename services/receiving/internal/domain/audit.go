package domain

import (
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"
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

type AuditPolicy struct {
	RatePct           decimal.Decimal
	Threshold         decimal.Decimal
	ThresholdCurrency string
	RuleVersion       int
}

func (p AuditPolicy) Weight(total decimal.Decimal, currency string) decimal.Decimal {
	if !p.Threshold.IsPositive() || currency != p.ThresholdCurrency || total.GreaterThanOrEqual(p.Threshold) {
		return decimal.NewFromInt(1)
	}
	return decimal.Max(total, decimal.Zero).Div(p.Threshold)
}

func (s *Sampler) Blocks(invoiceID string, ratePct decimal.Decimal) bool {
	return s.Decide(invoiceID, AuditPolicy{RatePct: ratePct}, decimal.Zero, "")
}

func (s *Sampler) Decide(invoiceID string, p AuditPolicy, total decimal.Decimal, currency string) bool {
	if !p.RatePct.IsPositive() {
		return false
	}
	if s == nil {
		return true
	}
	mac := hmac.New(sha256.New, s.seed)
	mac.Write([]byte(strings.ToLower(invoiceID) + "|" + strconv.Itoa(p.RuleVersion)))
	bucket := binary.BigEndian.Uint64(mac.Sum(nil)[:8]) % sampleBuckets
	return decimal.NewFromInt(int64(bucket)).LessThan(p.RatePct.Mul(bucketsPerPct).Mul(p.Weight(total, currency)))
}
