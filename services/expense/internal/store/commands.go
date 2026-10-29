package store

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// CodeOwnDecision refuses an approver decision on an item the same user requested
// (MssApprove "own items blocked", 8.43.17). Listed in the service README.
const CodeOwnDecision = "EXP-SOD-001"

// Command is the common input of a state-changing custom method (decide, pay, return,
// submit): the Idempotency-Key and its request hash, and the If-Match version (0 when
// the header was absent).
type Command struct {
	Key             string
	Hash            string
	ExpectedVersion int
}

// priorCommand looks the key up in idempotency_keys. found=true means the command
// already ran with this request and the caller replays the current row; a key stored
// with another request is EXP-IDEM-001. Commands that refuse store nothing, so a
// refused command can be retried with the same key once the cause is fixed.
func priorCommand(ctx context.Context, tx pgx.Tx, tenantID string, c Command) (bool, error) {
	var stored string
	err := tx.QueryRow(ctx, `select request_hash from idempotency_keys where tenant_id=$1 and key=$2`, tenantID, c.Key).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if stored != c.Hash {
		return false, reuse()
	}
	return true, nil
}

// checkVersion enforces If-Match: a positive expected version must equal the row's.
func checkVersion(c Command, current int, code, what string) error {
	if c.ExpectedVersion > 0 && c.ExpectedVersion != current {
		return httpx.NewProblem(http.StatusPreconditionFailed, code,
			what+" changed since it was read; it is now at version "+strconv.Itoa(current))
	}
	return nil
}

// decider returns the principal for an approver decision. The MssApprove own-items rule
// (8.43.17) compares user ids, so a decision needs an authenticated user.
func decider(ctx context.Context) (tenant.Principal, error) {
	p, err := principal(ctx)
	if err != nil {
		return p, err
	}
	if p.UserID == "" {
		return p, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "an approval decision needs an authenticated user")
	}
	return p, nil
}

func ownDecision(what string) error {
	return httpx.NewProblem(http.StatusForbidden, CodeOwnDecision, "you requested this "+what+"; another approver must decide it")
}

// pageOf trims a limit+1 result to limit and returns the next cursor (empty on the last page).
func pageOf[T any](rows []T, limit int, id func(T) string) ([]T, string) {
	if len(rows) > limit {
		rows = rows[:limit]
		return rows, id(rows[limit-1])
	}
	return rows, ""
}
