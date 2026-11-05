# Tenant administrator guide (PSG-50, FR-50.11, FR-50.12)

Audience: customer administrators who hold the TenantAdmin duty. This guide covers what a customer
administrator sees and decides about the Passage tenant: licence use, users, segregation of duties,
cost, releases, data export and incident communication. Operator procedures (regions, failover,
provisioning, licence install, the `/api/v1/ops/*` control plane) are in the
[administrator guide](administrator-guide.md), which is for platform operators, not tenant administrators.
Track 3 of the [training curriculum](training-curriculum.md) is the course for this guide.

## Status of the screens this guide uses

| Task | On main | Not yet (owner) |
|---|---|---|
| Licence state, licence inquiry (user types used against licensed) and the shell banner at GRACE | the state rules (`docs/ops/tools/license_state.py`) | licence store, hourly check, banner and inquiry (ops service, wave 1 shell); licence usage counts (wave 3 `UsageSnapshot`) |
| User and role assignment | role-assignment tables in `services/identity` | the assignment API and the admin-security screens (wave 2 identity) |
| SoD conflicts and mitigations | `/compliance/sod/conflicts`, `/compliance/sod/check` (wave 48) | |
| Monthly cost statement, `GET /api/v1/ops/tenants/{id}/cost`, cost and capacity screen | the calculators (`docs/ops/tools/cost_meter.py`, `capacity_estimate.py`) | the meter job, the endpoint and the screen (ops service) |
| Release ring | `release.cadence` documented in the [upgrade strategy](upgrade-strategy.md) | setting it from tenant management (ops service) |
| Status page subscription | | `passage-status-page` (deployment-cicd, wave 5) |

Until the ops service ships, ask the platform operator for the licence usage, the cost statement and a ring
change. Every gap above is listed in [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## 1. Your duty

The TenantAdmin duty (PRD 9.50.17) allows reading the tenant's cost (`GET /api/v1/ops/tenants/{id}/cost`)
and assigning user types within the licence. It does not allow provisioning or decommissioning a tenant,
changing regions or installing a licence: those are platform.global.admin, and signing a licence file is
LicenseIssuer. One person should not hold TenantAdmin and platform.global.admin for the same tenant (a MEDIUM SoD
conflict).

## 2. Legal entities and scope

A user works only in the legal entities they are granted. A request can be narrowed to some of those
entities, but never widened beyond the grant; rows of other entities are not visible to the user at all.
Set up legal entities with the functional owners before users are assigned (see the ledger and
organization functional guides).

## 3. Licence and user types

The tenant has one signed licence file. It sets the number of users for each type and an expiry.

| User type | Typical use | Maps from (PRD 9.50.11) |
|---|---|---|
| full | works across modules, approves and posts | Dynamics full user, SAP professional |
| operational | transactional work in one area | Dynamics activity or functional user, SAP functional |
| self-service | requests and reads, for example expenses and leave | Dynamics team member, SAP productivity |

Only the full-user count moves the licence state. Operational and self-service users are counted in the
licence inquiry for the usage report.

| State | What it means | What you should do |
|---|---|---|
| VALID | full users within the licence and not expired | nothing |
| GRACE | more full users active than licensed, or the licence expired less than `license.grace_days` ago (default 30) | sign-in still works, with a banner (LIC-1802). Reassign full users or ask for a true-up; renew an expired licence before the grace period ends |
| EXPIRED | more than `license.grace_days` after expiry | renew. New sign-ins are blocked (LIC-1803); administrators can still sign in and export data (BR-50.8) |

GRACE for over-use lasts until the full-user count is back within the licence; it has no deadline today
(an open product decision). LIC-1801 warns from 30 days before expiry, with alerts at 30, 7 and 1 days.
A licence whose signature does not verify (LIC-1804) is reinstalled from the issued file by the platform
operator; nobody edits a licence file.

## 4. Assigning users and roles

1. Give each user one user type, within the licence counts.
2. Assign roles that match the user's job. Access comes from the duties in a role, not from a job title.
3. Check segregation of duties before go-live (`/compliance/sod/check` for a role combination,
   `/compliance/sod/conflicts` for open conflicts). Go-live needs 0 unmitigated HIGH conflicts for go-live
   users (go/no-go item 4): either remove the conflicting role or record a mitigation with its approver.
4. Review the user list every month and remove leavers on their last day. The licence inquiry shows the
   active users per type against the licence.
5. Keep at least two people with TenantAdmin, so that one absence does not stop administration.

Lab 3.1 in the training curriculum walks through an assignment and the resulting licence usage line.

## 5. Cost statement

Each month (on the 2nd, 02:00 UTC) Passage produces one cost statement for the tenant, in USD and EUR, kept
7 years as a billing record. EUR uses the ECB month-average rate, and both are rounded to 0.01 (BR-50.10).
The statement lists:

- CPU core-hours and memory GB-hours
- storage, Kafka and lakehouse GB
- AI tokens
- the cost in USD and EUR, and the cost model version used

Each line traces to the metered usage (lab 3.2). Read it with
`GET /api/v1/ops/tenants/{id}/cost?month=YYYY-MM` or the PDF and CSV exports. Taxes on Passage's own invoices
are outside the statement.

Passage checks the meter every month: the sum of all tenants' metered cost on a cluster must be within 5% of
the provider's bill. A larger difference raises COST-1901 and Passage investigates it under the
[cost meter runbook](runbooks/cost-meter-drift.md). No statement is edited by hand; a corrected month is
re-run with the stored query version.

For planned growth, ask the platform operator for a capacity estimate with your concurrent users, journal
lines per hour, order lines per hour and storage. The method is in [capacity-model.md](capacity-model.md).

## 6. Release rings and releases

The tenant's release ring is the parameter `release.cadence`:

| Ring | Behaviour |
|---|---|
| WEEKLY | the default; receives every weekly release |
| BIWEEKLY | receives every second weekly release |
| MONTHLY | a pinned ring: the tenant stays on one release for the month |

Use the pinned ring for a period that must not change, such as a year-end close; lab 3.3 plans one and the
notice to users. Database migrations are automatic and zero-downtime, so a release normally needs no action
from you. Any manual step is listed in the release notes with its owner, and a release note with no manual
step says so. A release never breaks clients of the previous release (N-1, PRD 9.50.15).

## 7. Data export and decommission

Tenant data can be exported in every licence state, including EXPIRED (BR-50.8).

Only the platform operator decommissions a tenant, with a second approver. Passage first exports all tenant
data encrypted and keeps the export for the longest retention that the tenant's country packs require (for
example 10 years for DE and FR, BR-50.9), unless you take custody of the export and sign a release. Tenant
schemas are dropped only after the export is verified. The steps are in
[tenant-provisioning.md](tenant-provisioning.md).

## 8. Incidents and status

Subscribe to the public status page for the components API, UI, Analytics, Integrations and Mobile. An
incident notice shows the component, the impact, the timeline and the resolution, and stays public for one
year. Notices are published in English, and in German or French where your contract sets that language.

Response times are set by your support terms. During hypercare after go-live, Sev-1 has a 15-minute and
Sev-2 a 1-hour response, 24x5 (see the [go-live checklist](go-live-checklist.md)).

## 9. Messages you may see

| Code | Message (PRD 9.50.18) | What to do |
|---|---|---|
| LIC-1801 | License expires in {days} days. | start the renewal |
| LIC-1802 | License in grace period: {used} full users active, {licensed} licensed. | reassign full users, ask for a true-up, or renew |
| LIC-1803 | License expired; sign-in blocked. Administrators can still sign in and export data. | renew |
| LIC-1804 | License signature invalid. | ask the platform operator to reinstall the issued file |
| COST-1901 | Metered cost for {month} differs from provider billing by {pct}%. | none; Passage investigates. Ask the platform operator before relying on that month's statement |
| OPS-1703 | This region is read-only during failover; retry in {s} s. | none; retry after the time shown |
