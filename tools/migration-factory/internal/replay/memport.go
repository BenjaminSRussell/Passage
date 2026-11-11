package replay

import (
	"context"
	"fmt"
	"sync"

	"github.com/shopspring/decimal"
)

// MemoryPort is an in-memory subledger port for tests and rehearsals without a subledger service: it keeps each
// document once by idempotency key (a repeat answers the first id), rounds every amount to Scale decimals like a
// subledger that books cents (default 2), sums movements per (legal entity, key, currency, period of the document),
// and can be told to refuse documents or to fail.
type MemoryPort struct {
	// Scale is the decimals each amount is rounded to when posted; 0 means 2.
	Scale int32

	mu      sync.Mutex
	posted  map[string]string          // idempotency key -> id
	net     map[string]decimal.Decimal // legal entity|key|currency|period -> net movement
	refuse  map[string]string          // document id -> code
	failure error
	posts   int
}

// NewMemoryPort returns an empty port.
func NewMemoryPort() *MemoryPort {
	return &MemoryPort{posted: map[string]string{}, net: map[string]decimal.Decimal{}, refuse: map[string]string{}}
}

// Refuse makes the port refuse the document with the code.
func (m *MemoryPort) Refuse(documentID, code string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refuse[documentID] = code
}

// FailPosts makes every following post fail with err (nil clears it): an outage, not a refusal.
func (m *MemoryPort) FailPosts(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failure = err
}

// Posts counts the documents the port stored (a repeat under the same key is not counted).
func (m *MemoryPort) Posts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.posts
}

// Post implements Port.
func (m *MemoryPort) Post(_ context.Context, in Post) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failure != nil {
		return "", m.failure
	}
	if id, ok := m.posted[in.IdempotencyKey]; ok {
		return id, nil
	}
	if code, ok := m.refuse[in.Doc.DocumentID]; ok {
		return "", Refusal{Code: code}
	}
	scale := m.Scale
	if scale == 0 {
		scale = 2
	}
	m.posts++
	id := fmt.Sprintf("%s-%06d", in.Doc.Area, m.posts)
	m.posted[in.IdempotencyKey] = id
	k := in.LegalEntityID + "|" + in.Doc.Key + "|" + in.Doc.Currency + "|" + in.Doc.PostingDate[:7]
	m.net[k] = m.net[k].Add(in.Doc.Amount.Round(scale))
	return id, nil
}

// Movement implements Port.
func (m *MemoryPort) Movement(_ context.Context, q Activity) (decimal.Decimal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.net[q.LegalEntityID+"|"+q.Key+"|"+q.Currency+"|"+q.Period], nil
}
