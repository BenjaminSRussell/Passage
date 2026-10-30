package domain

import (
	crand "crypto/rand"
	"encoding/binary"
	"math/rand/v2"
	"sync"

	"github.com/shopspring/decimal"
)

const (
	CodeAuditBlocked     = "AP-AUD-001"
	CodeNotAuditBlocked  = "AP-AUD-002"
	CodeAuditSelfRelease = "AP-AUD-003"

	InvoiceStatusPosted       = "POSTED"
	InvoiceStatusAuditBlocked = "AUDIT_BLOCKED"
	InvoiceSourceManual       = "MANUAL"
	InvoiceSourceERS          = "ERS"

	sampleBuckets = 10000
)

var bucketsPerPct = decimal.NewFromInt(sampleBuckets / 100)

type Sampler struct {
	mu  sync.Mutex
	rng *rand.Rand
}

func NewSampler(seed1, seed2 uint64) *Sampler {
	return &Sampler{rng: rand.New(rand.NewPCG(seed1, seed2))}
}

func NewRandomSampler() (*Sampler, error) {
	var seed [16]byte
	if _, err := crand.Read(seed[:]); err != nil {
		return nil, err
	}
	return NewSampler(binary.LittleEndian.Uint64(seed[:8]), binary.LittleEndian.Uint64(seed[8:])), nil
}

func ValidSampleRate(ratePct decimal.Decimal) bool {
	return !ratePct.IsNegative() && !ratePct.GreaterThan(hundred) && ratePct.Exponent() >= -2
}

func (s *Sampler) Blocks(ratePct decimal.Decimal) bool {
	if s == nil || !ratePct.IsPositive() {
		return false
	}
	threshold := ratePct.Mul(bucketsPerPct).IntPart()
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(s.rng.Uint64N(sampleBuckets)) < threshold
}
