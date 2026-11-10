package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

const (
	autoBookActor           = "system:billing-autobook"
	defaultAutoBookInterval = time.Minute
	minAutoBookInterval     = 5 * time.Second
	autoBookTimeout         = 5 * time.Minute
)

type autoBooker interface {
	AutoBook(ctx context.Context, legalEntityID string) (booked, failed int, err error)
}

// autoBook books the invoice journal of posted invoices and credit notes without an API request: per configured tenant and per
// legal entity of the GL profiles it runs the same posting run as POST /v1/billing/gl-postings, under a principal scoped to that
// legal entity. A document the ledger refuses or revrec cannot allocate keeps its backoff and parking on the invoice, a run
// another replica holds is skipped, and the ledger key derives from the document id, so a tick that repeats a lost call never
// books a document twice.
type autoBook struct {
	interval time.Duration
	tenants  []string
	entities []string
	books    autoBooker
}

func newAutoBook(getenv func(string) string, profiles map[string]store.GLProfile, books autoBooker) (*autoBook, error) {
	raw := getenv("BILLING_GL_AUTOBOOK_TENANT_IDS")
	if strings.TrimSpace(raw) == "" {
		slog.Info("billing GL auto-book disabled; BILLING_GL_AUTOBOOK_TENANT_IDS is not set")
		return nil, nil
	}
	if len(profiles) == 0 {
		return nil, errors.New("BILLING_GL_AUTOBOOK_TENANT_IDS needs the ledger configured (LEDGER_URL and BILLING_GL_PROFILES)")
	}
	var tenants []string
	for _, part := range strings.Split(raw, ",") {
		id, ok := ids.Canonical(strings.TrimSpace(part))
		if !ok || id == ids.Nil {
			return nil, fmt.Errorf("BILLING_GL_AUTOBOOK_TENANT_IDS entry %q is not a UUID", part)
		}
		if !slices.Contains(tenants, id) {
			tenants = append(tenants, id)
		}
	}
	interval := defaultAutoBookInterval
	if v := getenv("BILLING_GL_AUTOBOOK_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < minAutoBookInterval {
			return nil, errors.New("BILLING_GL_AUTOBOOK_INTERVAL must be a duration of at least 5s, got " + v)
		}
		interval = d
	}
	entities := make([]string, 0, len(profiles))
	for le := range profiles {
		entities = append(entities, le)
	}
	slices.Sort(entities)
	return &autoBook{interval: interval, tenants: tenants, entities: entities, books: books}, nil
}

func (b *autoBook) loop(ctx context.Context) {
	b.once(ctx)
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.once(ctx)
		}
	}
}

func (b *autoBook) once(ctx context.Context) {
	for _, tenantID := range b.tenants {
		for _, le := range b.entities {
			runCtx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{TenantID: tenantID, LegalEntityID: le, UserID: autoBookActor}), autoBookTimeout)
			runCtx = context.WithValue(runCtx, middleware.RequestIDKey, "autobook-"+ids.New())
			booked, failed, err := b.books.AutoBook(runCtx, le)
			cancel()
			switch {
			case err != nil:
				slog.Error("billing GL auto-book failed", "tenantId", tenantID, "legalEntityId", le, "err", err)
			case booked > 0 || failed > 0:
				slog.Info("billing GL auto-book finished", "tenantId", tenantID, "legalEntityId", le, "booked", booked, "failed", failed)
			}
		}
	}
}
