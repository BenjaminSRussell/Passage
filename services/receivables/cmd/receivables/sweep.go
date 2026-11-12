package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

const (
	rmaSweepActor           = "system:receivables-rma-sweep"
	defaultRMASweepInterval = 5 * time.Minute
	minRMASweepInterval     = time.Minute
	minRMAReservationTTL    = time.Hour
	rmaSweepTimeout         = 5 * time.Minute
)

// rmaSweeper is the store side the sweep drives.
type rmaSweeper interface {
	RMASweepLegalEntities(ctx context.Context) ([]string, error)
	SweepRMACredits(ctx context.Context, now time.Time, limit int) (store.RMASweepResult, error)
}

// rmaExpirer is the optional third pass: cancelling abandoned PENDING credits whose reservation outlived the TTL (#557).
type rmaExpirer interface {
	ExpireRMAReservations(ctx context.Context, now time.Time, ttl time.Duration, limit int) (store.RMAExpiryResult, error)
}

// journalSweeper is the optional second pass: the ledger journals receivables owes (30-2). The store implements it.
type journalSweeper interface {
	GLJournalLegalEntities(ctx context.Context) ([]string, error)
	PostDueJournals(ctx context.Context, now time.Time, limit int) (store.JournalSweepResult, error)
}

// rmaSweep posts the PENDING RMA credits the request path left behind (a ledger outage, a lost answer, a crash after
// the PENDING commit). Per tenant it lists the legal entities with due credits under an all-scope read principal, then
// sweeps each under a principal scoped to that legal entity, which the ledger post's service token needs.
type rmaSweep struct {
	interval time.Duration
	tenants  []string
	store    rmaSweeper
	// reservationTTL is how long an unposted RMA credit may hold its invoice room before the sweep cancels it; 0 = never.
	reservationTTL time.Duration
}

func newRMASweep(getenv func(string) string, s rmaSweeper) (*rmaSweep, error) {
	raw := getenv("RECEIVABLES_RMA_SWEEP_TENANT_IDS")
	if strings.TrimSpace(raw) == "" {
		slog.Info("receivables RMA credit sweep disabled; RECEIVABLES_RMA_SWEEP_TENANT_IDS is not set")
		return nil, nil
	}
	var tenants []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		id, ok := ids.Canonical(strings.TrimSpace(part))
		if !ok || id == ids.Nil {
			return nil, fmt.Errorf("RECEIVABLES_RMA_SWEEP_TENANT_IDS entry %q is not a UUID", part)
		}
		if !seen[id] {
			seen[id] = true
			tenants = append(tenants, id)
		}
	}
	interval := defaultRMASweepInterval
	if v := getenv("RECEIVABLES_RMA_SWEEP_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < minRMASweepInterval {
			return nil, errors.New("RECEIVABLES_RMA_SWEEP_INTERVAL must be a duration of at least 1m, got " + v)
		}
		interval = d
	}
	var ttl time.Duration
	if v := getenv("RECEIVABLES_RMA_RESERVATION_TTL"); v != "" && v != "0" {
		d, err := time.ParseDuration(v)
		if err != nil || d < minRMAReservationTTL {
			return nil, errors.New("RECEIVABLES_RMA_RESERVATION_TTL must be 0 (never expire) or a duration of at least 1h, got " + v)
		}
		ttl = d
	}
	return &rmaSweep{interval: interval, tenants: tenants, store: s, reservationTTL: ttl}, nil
}

func (s *rmaSweep) loop(ctx context.Context) {
	s.once(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.once(ctx)
		}
	}
}

func (s *rmaSweep) once(ctx context.Context) {
	s.rmaPass(ctx)
	s.journalPass(ctx)
}

// journalPass posts the journals owed to the ledger, per tenant and legal entity like the RMA pass.
func (s *rmaSweep) journalPass(ctx context.Context) {
	js, ok := s.store.(journalSweeper)
	if !ok {
		return
	}
	for _, tenantID := range s.tenants {
		listCtx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{
			TenantID: tenantID, UserID: rmaSweepActor, Duties: map[string]struct{}{tenant.DutyAllScope: {}},
		}), rmaSweepTimeout)
		entities, err := js.GLJournalLegalEntities(listCtx)
		cancel()
		if err != nil {
			slog.Error("receivables journal sweep failed", "tenantId", tenantID, "err", err)
			continue
		}
		for _, le := range entities {
			runCtx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{TenantID: tenantID, LegalEntityID: le, UserID: rmaSweepActor}), rmaSweepTimeout)
			res, err := js.PostDueJournals(runCtx, time.Now().UTC(), store.MaxJournalSweepBatch)
			cancel()
			if err != nil {
				slog.Error("receivables journal sweep failed", "tenantId", tenantID, "legalEntityId", le, "err", err)
				continue
			}
			if res != (store.JournalSweepResult{}) {
				slog.Info("receivables journal sweep finished", "tenantId", tenantID, "legalEntityId", le, "posted", res.Posted, "failed", res.Failed, "parked", res.Parked)
			}
		}
	}
}

func (s *rmaSweep) rmaPass(ctx context.Context) {
	for _, tenantID := range s.tenants {
		listCtx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{
			TenantID: tenantID, UserID: rmaSweepActor, Duties: map[string]struct{}{tenant.DutyAllScope: {}},
		}), rmaSweepTimeout)
		entities, err := s.store.RMASweepLegalEntities(listCtx)
		cancel()
		if err != nil {
			slog.Error("receivables RMA credit sweep failed", "tenantId", tenantID, "err", err)
			continue
		}
		for _, le := range entities {
			runCtx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{TenantID: tenantID, LegalEntityID: le, UserID: rmaSweepActor}), rmaSweepTimeout)
			res, err := s.store.SweepRMACredits(runCtx, time.Now().UTC(), store.MaxRMASweepBatch)
			cancel()
			if err != nil {
				slog.Error("receivables RMA credit sweep failed", "tenantId", tenantID, "legalEntityId", le, "err", err)
				continue
			}
			if res != (store.RMASweepResult{}) {
				slog.Info("receivables RMA credit sweep finished", "tenantId", tenantID, "legalEntityId", le,
					"posted", res.Posted, "failed", res.Failed, "parked", res.Parked, "committed", res.Committed)
			}
			s.expire(ctx, tenantID, le)
		}
	}
}

// expire cancels the abandoned credits of one legal entity when a reservation TTL is configured.
func (s *rmaSweep) expire(ctx context.Context, tenantID, le string) {
	ex, ok := s.store.(rmaExpirer)
	if !ok || s.reservationTTL <= 0 {
		return
	}
	runCtx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{TenantID: tenantID, LegalEntityID: le, UserID: rmaSweepActor}), rmaSweepTimeout)
	defer cancel()
	res, err := ex.ExpireRMAReservations(runCtx, time.Now().UTC(), s.reservationTTL, store.MaxRMAExpiryBatch)
	if err != nil {
		slog.Error("receivables RMA reservation expiry failed", "tenantId", tenantID, "legalEntityId", le, "err", err)
		return
	}
	if res != (store.RMAExpiryResult{}) {
		slog.Info("receivables RMA reservation expiry finished", "tenantId", tenantID, "legalEntityId", le,
			"expired", res.Expired, "skipped", res.Skipped, "failed", res.Failed)
	}
}
