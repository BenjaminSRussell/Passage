package store

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
)

// CodePreconditionRequired is the 428 problem for a PUT that changes an existing configuration row
// without If-Match (the replace would otherwise overwrite a concurrent edit unseen).
const CodePreconditionRequired = "LEDGER_PRECONDITION_REQUIRED"

// Optimistic concurrency on a PUT of a configuration row (account, account structure version, dimension
// value, description template, journal name), in two steps:
//
//  1. checkLock, on the row read FOR UPDATE: a tag that does not match the stored lock_version is 412
//     LEDGER_VERSION_CONFLICT, also when the row does not exist (there is nothing for it to match).
//  2. the upsert's conflict update, built by lockedUpdate, applies only at the caller's tag or when it
//     changes nothing. So a create needs no tag, a replay of the stored content without a tag is an
//     idempotent 200 that keeps the tag (a retried create, a re-run seed or migration load), and only a
//     real change of an existing row needs If-Match. When the update is refused the upsert returns no
//     row, and lockLost answers 428 (no tag) or 412. This also covers a row created by a concurrent
//     request after step 1 found none, which the row lock of step 1 cannot hold.
//
// lock_version increments only when a column changes.

// checkLock is step 1. found is whether the row exists, current its lock_version and ifMatch the entity
// tag the caller sent (0 when absent).
func checkLock(found bool, current, ifMatch int, what string) error {
	if ifMatch != 0 && (!found || ifMatch != current) {
		return versionConflict(what)
	}
	return nil
}

// lockLost is step 2's answer when the upsert returned no row: the conflict update was refused.
func lockLost(err error, ifMatch int, what string) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if ifMatch == 0 {
		return httpx.NewProblem(http.StatusPreconditionRequired, CodePreconditionRequired,
			"send If-Match with the "+what+" entity tag (its lockVersion) to change it")
	}
	return versionConflict(what)
}

func versionConflict(what string) error {
	return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
		"the "+what+" changed since it was read; reload it and try again")
}

// lockedUpdate is the tail of a configuration upsert's "on conflict ... do update set <columns>, " clause:
// the lock_version and updated_at bump, only when one of cols changes, and the guard that applies the
// update only at the tag in parameter tagParam or when nothing changes. table and cols are constants.
func lockedUpdate(table string, tagParam int, cols ...string) string {
	stored := make([]string, len(cols))
	sent := make([]string, len(cols))
	for i, c := range cols {
		stored[i] = table + "." + c
		sent[i] = "excluded." + c
	}
	changed := fmt.Sprintf("(%s) is distinct from (%s)", strings.Join(stored, ", "), strings.Join(sent, ", "))
	return fmt.Sprintf(`lock_version = %[1]s.lock_version + (case when %[2]s then 1 else 0 end),
				updated_at = case when %[2]s then now() else %[1]s.updated_at end
			where %[1]s.lock_version = $%[3]d::int or not %[2]s`, table, changed, tagParam)
}
