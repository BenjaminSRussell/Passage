# Runbook: license expiry and grace

Alerts: `PassageLicenseExpiresIn30Days` (info), `...In7Days` (warning) and `...In1Days` (page), LIC-1801.
Each fires for its own band only (30 to 8 days, 7 to 2 days, 1 day or less), labelled with `tenant_id`.

1. Identify the tenant and the state (`VALID`, `GRACE`, `EXPIRED`) from `tenant_license`.
2. Renewal: Passage commercial operations issues a new signed license file (Ed25519, offline signing).
   Install it with `PUT /api/v1/ops/tenants/{id}/license` using `If-Match`. A bad signature is LIC-1804.
3. GRACE (30 days, BR-50.8): logins allowed with the shell banner (LIC-1802). Resolve over-use by true-up or reassignment.
4. EXPIRED: new logins are blocked (LIC-1803); administrators can still sign in and export data. Never block export or admin access.

State rules (VALID, GRACE, EXPIRED, LIC-1801..1803 and their texts) are executable in
`docs/ops/tools/license_state.py`; its tests are the rule table. The hourly license check that applies them is the
ops service's (not built yet).
