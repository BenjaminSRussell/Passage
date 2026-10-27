package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

type durableLedger struct {
	inner   domain.LedgerPoster
	store   store.Store
	subject string
}

func (a *API) ledgerFor(subject string) domain.LedgerPoster {
	return durableLedger{inner: a.ports.Ledger, store: a.store, subject: subject}
}

func (d durableLedger) Post(ctx context.Context, entry domain.JournalEntry) (string, error) {
	journal, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(entry.IdempotencyKey+"\x00"), journal...))
	if err := d.store.ReservePosting(ctx, store.PendingPosting{Key: entry.IdempotencyKey, Subject: d.subject, Hash: hex.EncodeToString(sum[:]), Journal: journal}); err != nil {
		return "", err
	}
	id, err := d.inner.Post(ctx, entry)
	if errors.Is(err, domain.ErrPeriodClosed) {
		if releaseErr := d.store.ReleasePosting(ctx, entry.IdempotencyKey); releaseErr != nil {
			return "", errors.Join(err, releaseErr)
		}
	}
	return id, err
}

func optionalDate(fields map[string]string, name, value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(dateLayout, value)
	if err != nil {
		fields[name] = "must be a YYYY-MM-DD date"
	}
	return t
}
