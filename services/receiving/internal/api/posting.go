package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const dutyReceivingAdmin = "ReceivingAdmin"

type pendingTracker struct {
	mu       sync.Mutex
	started  map[string]time.Time
	subjects map[string][]string
}

func (t *pendingTracker) add(key string, subjects []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.started == nil {
		t.started, t.subjects = map[string]time.Time{}, map[string][]string{}
	}
	if _, ok := t.started[key]; !ok {
		t.started[key] = time.Now()
	}
	t.subjects[key] = subjects
}

func (t *pendingTracker) drop(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.started, key)
	delete(t.subjects, key)
}

func (t *pendingTracker) finish(subjects ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, held := range t.subjects {
		if slices.ContainsFunc(subjects, func(s string) bool { return slices.Contains(held, s) }) {
			delete(t.started, key)
			delete(t.subjects, key)
		}
	}
}

func (t *pendingTracker) stats() (open int, oldest time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, started := range t.started {
		oldest = max(oldest, time.Since(started))
	}
	return len(t.started), oldest
}

type durableLedger struct {
	inner    domain.LedgerPoster
	store    store.Store
	tracker  *pendingTracker
	subjects []string
}

func (a *API) ledgerFor(subjects ...string) domain.LedgerPoster {
	unique := slices.Compact(slices.Sorted(slices.Values(subjects)))
	return durableLedger{inner: a.ports.Ledger, store: a.store, tracker: &a.pendingPostings, subjects: unique}
}

func (d durableLedger) Post(ctx context.Context, entry domain.JournalEntry) (string, error) {
	journal, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	base := entry.IdempotencyKey
	sum := sha256.Sum256(append([]byte(base+"\x00"), journal...))
	key, err := d.store.ReservePosting(ctx, store.PendingPosting{BaseKey: base, Subjects: d.subjects, Hash: hex.EncodeToString(sum[:]), Journal: journal})
	if err != nil {
		return "", err
	}
	entry.IdempotencyKey = key
	d.tracker.add(entry.IdempotencyKey, d.subjects)
	id, err := d.inner.Post(ctx, entry)
	if errors.Is(err, domain.ErrPeriodClosed) || errors.Is(err, domain.ErrLedgerRejected) {
		d.tracker.drop(entry.IdempotencyKey)
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

type pendingBody struct {
	Key       string   `json:"key"`
	Subjects  []string `json:"subjects"`
	CreatedAt string   `json:"createdAt"`
	AgeSecond int64    `json:"ageSeconds"`
}

func toPendingBody(r store.PendingRow) pendingBody {
	return pendingBody{Key: r.Key, Subjects: r.Subjects, CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339), AgeSecond: int64(time.Since(r.CreatedAt).Seconds())}
}

func (a *API) listPending(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.ListPending(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := struct {
		Items []pendingBody `json:"items"`
	}{Items: make([]pendingBody, 0, len(rows))}
	for _, row := range rows {
		out.Items = append(out.Items, toPendingBody(row))
	}
	httpx.JSON(w, http.StatusOK, out)
}

type resolveBody struct {
	Key           string `json:"key"`
	Outcome       string `json:"outcome"`
	LedgerEntryID string `json:"ledgerEntryId,omitempty"`
}

func unavailable(detail string) *httpx.Problem {
	return httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", detail)
}

func (a *API) resolvePending(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenant.FromContext(r.Context()); !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return
	}
	key, err := url.PathUnescape(chi.URLParam(r, "key"))
	if err != nil || key == "" {
		httpx.Fail(w, r, validationProblem(map[string]string{"key": "must be a pending posting key"}))
		return
	}
	ctx := r.Context()
	reverser, ok := a.ports.Ledger.(domain.LedgerReverser)
	if !ok {
		httpx.Fail(w, r, unavailable("the ledger adapter cannot reverse entries"))
		return
	}
	row, err := a.store.ClaimPending(ctx, key, a.resolveMinAge, a.resolveLease)
	switch {
	case errors.Is(err, store.ErrPendingNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "RCV-NF-003", "pending posting not found"))
		return
	case errors.Is(err, store.ErrPendingFinished):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, domain.CodeInvalidTransition, "the posting is already finished as "+row.Outcome))
		return
	case errors.Is(err, store.ErrPendingClaimed):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, domain.CodePostingPending, "another resolve is already working on this posting"))
		return
	case errors.Is(err, store.ErrPendingTooRecent):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, domain.CodePostingPending, "the posting is younger than "+a.resolveMinAge.String()+", so its command may still be running; try again later"))
		return
	case err != nil:
		writeError(w, r, err)
		return
	}
	abandon := func(err error) {
		a.releaseClaim(ctx, row.Key)
		writeError(w, r, err)
	}
	var entry domain.JournalEntry
	if err := json.Unmarshal(row.Journal, &entry); err != nil {
		abandon(err)
		return
	}
	entry.IdempotencyKey = row.Key
	id, err := a.ports.Ledger.Post(ctx, entry)
	switch {
	case errors.Is(err, domain.ErrPeriodClosed) || errors.Is(err, domain.ErrLedgerRejected):
		a.finishPending(w, r, row.Key, store.OutcomeRejected, "")
		return
	case errors.Is(err, domain.ErrLedgerConflict):
		abandon(httpx.NewProblem(http.StatusConflict, domain.CodePostingPending, "the ledger holds a different entry under this key; resolve it in the ledger"))
		return
	case err != nil:
		abandon(unavailable("the ledger could not be reached; try again"))
		return
	}
	switch err := reverser.Reverse(ctx, id, row.Key+":reverse", entry.PostingDate); {
	case errors.Is(err, domain.ErrPeriodClosed):
		abandon(httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodePeriodClosed, "the ledger period of the entry is closed, so the entry cannot be reversed"))
		return
	case errors.Is(err, domain.ErrLedgerRejected):
		abandon(httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeLedgerRejected, "the ledger refused the reversal; see the ledger for the reason"))
		return
	case err != nil:
		abandon(unavailable("the ledger entry exists but could not be reversed yet; try again"))
		return
	}
	a.finishPending(w, r, row.Key, store.OutcomeReversed, id)
}

func (a *API) releaseClaim(ctx context.Context, key string) {
	if err := a.store.ReleaseClaim(context.WithoutCancel(ctx), key); err != nil {
		slog.ErrorContext(ctx, "release resolve claim failed", "err", err, "key", key, "correlationId", middleware.GetReqID(ctx))
	}
}

func (a *API) finishPending(w http.ResponseWriter, r *http.Request, key, outcome, entryID string) {
	if err := a.store.FinishPending(context.WithoutCancel(r.Context()), key, outcome); err != nil {
		a.releaseClaim(r.Context(), key)
		writeError(w, r, err)
		return
	}
	a.pendingPostings.drop(key)
	httpx.JSON(w, http.StatusOK, resolveBody{Key: key, Outcome: outcome, LedgerEntryID: entryID})
}
