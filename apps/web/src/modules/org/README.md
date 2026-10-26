# org web module

Organization workspace for wave 6: legal-entity switcher (`passage.org.legalEntity`), Legal entities screen and legal-entity-scoped operating unit, site and warehouse lists. Discovered by the shell via `module.ts`.

Routes: `/org/reference-data` (nav "Reference data", duty `org.read`) holds the Reference data screen: countries, currencies, regions, languages, units and conversions, one tab loaded at a time. Deactivate, add custom unit and add conversion render only when `OrgLayout` receives `org.reference.write` in `duties`; the Test conversion panel needs no write duty.

Tenant-level fetch rule: reference data is tenant-wide, so `referenceFetch` (`api/reference.ts`) sends no `X-Passage-LegalEntity` or `X-Passage-LegalEntity-Scope` header on any method (the service refuses writes that carry a legal-entity scope with 403 `AUTH-0003`) and adds an `Idempotency-Key` to POST. Legal-entity-scoped screens keep using `orgFetch`.

Needs: none beyond the shell lockfile.
