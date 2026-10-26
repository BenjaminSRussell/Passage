# org web module

Organization workspace for wave 6: legal-entity switcher (`passage.org.legalEntity`), Legal entities screen and legal-entity-scoped operating unit, site and warehouse lists. Discovered by the shell via `module.ts`.

Routes: `/org/reference-data` (nav "Reference data", duty `org.read`) holds the Reference data screen: countries, currencies, regions, languages, units and conversions, one tab loaded at a time. Deactivate, add custom unit and add conversion render only when `OrgLayout` receives `org.reference.write` in `duties`; the Test conversion panel needs no write duty.

Tenant-level fetch rule: reference data is tenant-wide, so `referenceFetch` (`api/reference.ts`) sends no `X-Passage-LegalEntity` or `X-Passage-LegalEntity-Scope` header on any method (the service refuses writes that carry a legal-entity scope with 403 `AUTH-0003`) and adds an `Idempotency-Key` to POST. Legal-entity-scoped screens keep using `orgFetch`.

Route `/org/hierarchy` (nav "Hierarchies", duty `org.read`) is the read-only Hierarchy designer: pick a hierarchy type, see its versions, and preview the tree as of a date with a date input and slider (`versionAsOf` over the loaded version summaries; no as-of request per move). "Show draft" previews the open draft. The displayed version is compared with its previous version (`compare?from=<versionNo>&to=<versionNo>`) and added or moved nodes carry a "changed" badge whose tooltip names the previous parent. Hierarchy reads (`api/hierarchy.ts`: types, versions, one version with nodes, compare) are tenant-level and go through `referenceFetch`, so they send no legal-entity header; org unit names come from the legal-entity context and one operating-unit list per page. Editing is not built.

Needs: none beyond the shell lockfile.
