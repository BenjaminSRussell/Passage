# ADR-0003: The setup checklist stays a read composition in the web app

**Status:** Proposed — 2026-11-10
**Issues:** [#503](https://github.com/BenjaminSRussell/Passage/issues/503)

## Context

#503 asks for the setup checklist (`/setup/checklist`) to be computed on the server instead of in the browser.
Today the page reads nine lists from five contexts with the signed-in person's own token, one row each
(`apps/web/src/modules/setup/checks.ts`): legal entities, fiscal calendars, sites and warehouses (org), ledgers
(ledger), products (product), vendors and customers (party) and number sequences (numbering). A check whose duty
the person lacks, or whose feature is off, is left out; a list that cannot be read shows "could not check", never
"missing". The guided steps themselves (`GET /v1/tenant/setup`) are already server-side, in identity.

The natural server owner is identity, which owns the tenant's setup steps and features. It owns none of the nine
lists.

## Options

1. **Identity reads the five services synchronously** (as batch's `GET /v1/inbox` does with service tokens).
   Architecture §3.3 allows a synchronous cross-context call only for posting, foreign-key existence (`HEAD`) and
   the named read-like engine calls; everything else crosses contexts as events. The service token model does not
   fit either: `tenant.ServiceSigner.MintDuties` names exactly one legal entity in the user's scope unless every
   duty is a registered tenant-level duty (#450 keeps that class to two duties). The checklist is tenant-wide, and
   its first check asks whether any legal entity exists, so for a new tenant there is no entity to name. Making it
   work needs five new tenant-level read duties, grants from identity to org, ledger, product, party and numbering,
   and each of them accepting entity-less tokens on list routes that row-level security scopes by entity.
2. **Identity keeps a read model fed by events** (the §3.3 default). It needs created and deleted events for all
   nine record kinds from five producers, an inbox consumer in identity (it has none today), a backfill for
   existing tenants, and it answers for the tenant, not for what this person may read.
3. **Keep the composition at the edge, with the caller's token** (chosen). §4 places read composition across
   contexts in `web-bff`'s read gateway, whose resolvers "call context REST APIs with the caller's token so OPA
   and RLS apply downstream": no new datastore and one authorization path. That gateway is not built yet; until it
   is, the shell is the edge and makes the same calls itself.

## Decision

Option 3. The checklist stays in the web app, reading each list with the person's own token. No service gains a
cross-context dependency for it.

## Consequences

- The checklist shows what the person may see. A person without `party.read` sees no vendor check rather than a
  false "no vendors yet".
- Nine small requests (`limit=1`) per visit, run in parallel and cached by the query client.
- The demo handlers and tests keep serving the nine lists as they are.
- When `web-bff` gains its read gateway, the nine checks move to one query there, not to identity. If identity
  ever gets an inbox consumer for these contexts, option 2 becomes cheaper and this decision should be revisited.
