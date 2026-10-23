# portal-bff

## Purpose & Boundaries
TS edge service `services/portal-bff-ts` (wave 3 allocation: supplier portal wave 20, customer portal wave 30). Session BFF and persisted-query GraphQL for the supplier and customer portals. External users are identities in the tenant's Keycloak realm with portal roles, linked to `party_id` by a user attribute (assumption A8).

## Interfaces
Same session endpoints as web-bff (`/bff/login`, `/callback`, `/logout`, `/me`, `/permissions`), `/graphql` (PERSISTED QUERIES ONLY) over the GraphQL read model, and selected `/api/v1/*` mutations. Upstream portal APIs: `/v1/procurement/portal/*`, `/v1/sales/portal/*`, `/v1/party/portal/profile`, `/v1/projects/portal/cases`.

## Data Ownership
absent — the source document does not address this

## Events Produced
absent — the source document does not address this

## Events Consumed
absent — the source document does not address this

## Dependencies
internal: procurement, sales, party, projects, edge-identity. external: Keycloak clients `passage-portal-supplier`, `passage-portal-customer`; payment provider (customer portal; provider product Q12; card data must never touch Passage; hosted fields must pass the same axe gate).

## Gotchas & Constraints
- Portals are online-only (installable); tenant-scoped data only — a vendor sees only its data.
- Same cookie/CSRF/PKCE rules as web-bff; bearer relay.
- GraphQL depth <= 8 and cost limits; N+1 guard; field masking by duty.

## Source Sections
- Frontend 2.3-2.4 (4837-4859), 8.1 (5564-5572); Backend 3.2 (302)
