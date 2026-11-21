# ADR-0019: One posting key format, built in ledgerclient (D9)

**Status:** Accepted — 2026-11-21 (SDLC run 1, B-47 part a; closes architecture gap D9)
**Evidence:** `platform/go/ledgerclient/postingkey.go`, `platform/go/ledgerclient/callers_scan_test.go`, `docs/adr/0008-service-decomposition-and-languages.md`, `docs/adr/0001-platform-stack.md`

## Context

Posters built the ledger `Idempotency-Key` in 25 different shapes, and receiving and payables kept their own HTTP clients for
`POST /v1/ledger/journal-entries`. A retried posting could double-book when two callers keyed one document differently.

## Decision

1. **Format:** `post:<service>:<documentType>:<documentId>:<purpose>[:<revision>]`, at most 128 characters, no empty segment,
   no `:` or whitespace inside a segment. The second segment is the service directory name (`receiving`, `inventory`,
   `production`, ...). Receiving and fulfillment already wrote this shape, so the service name is the smallest additive choice.
2. **One builder:** `ledgerclient.PostingKey` (and `MustPostingKey` for constant-shaped call sites). Every poster, inventory
   included, builds its key with it and posts through `ledgerclient.Client`. Receiving and payables delegate to it.
3. **One test:** `callers_scan_test.go` fails when a `ledgerclient.Entry` or `Reversal` takes its key from anywhere but the
   builder, or when a service other than the ledger holds its own `/v1/ledger/journal-entries` client.
4. **Replay of keys stored under the old format:**
   - Posters that persist the key in a row and read it back (eam, costing, assets, receivables) resend the stored key, so a
     retry keeps deduping. Projects also checks the old `projects:txn:` and `projects:wip:` keys before it stores a booking.
   - Posters that rebuild the key at post time (production, inventory, close, intercompany, productcost, expense, billing,
     revrec, payroll, treasury, tax, and receiving invoice adjustments) send the new key. A document posted under the old key
     but not finalized locally would be booked a second time. This is accepted: production deploys only `hello-ledger` and
     `kyverno` (`docs/adr/0001-platform-stack.md`, architecture D4), so no environment holds a posting that reached the ledger
     and was not finalized. If an environment turns out to hold one, add the old key as an alias for that poster before
     deploying it.
5. **Not enforced at runtime:** `Post` does not call `ValidPostingKey`, so existing tests and stored legacy keys still post.
   The scan is the check.
