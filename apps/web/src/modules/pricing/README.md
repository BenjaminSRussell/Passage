# Pricing web module (PSG-26, wave 26)

Routes, all on the pricing service API (`services/pricing/api/openapi.yaml`):

| Route | Page | Calls |
|---|---|---|
| `/pricing` | Workspace: exact tiles (active, drafts, expiring in 30 days, journals to post, live campaigns, vendor claims to approve, customer and vendor rebates accrued this month per currency), the expiring list (active agreements ending in the next 30 days, soonest first), and the record expiry run (BR-26.9) for a legal entity and day | `GET /workspace`, `GET /condition-records?status=ACTIVE&validToFrom=&validToTo=`, `POST /condition-records:expire` |
| `/pricing/agreements` | Trade agreements grid with status, type, customer, product, as-of and ends-between filters | `GET /condition-records` |
| `/pricing/agreements/new` | Create a DRAFT agreement, optionally in a campaign; `?copy=<id>` starts from an existing one, `?campaign=<id>` fills the campaign | `POST /condition-records` |
| `/pricing/agreements/:id` | Detail with its campaign, activate (overlap 409 shown) and withdraw | `GET`, `:activate`, `:withdraw` |
| `/pricing/free-goods` | Free goods rules (FR-26.22) with status | `GET /free-goods-rules` |
| `/pricing/free-goods/new` | Create a rule: customers, product, unit, inclusive or exclusive, calculation, quantities, validity (overlap 409 shown) | `POST /free-goods-rules` |
| `/pricing/free-goods/:id` | Rule detail and withdraw | `GET /free-goods-rules/{id}`, `:withdraw` |
| `/pricing/campaigns` | Campaigns with target group, validity, budget and status (live, scheduled, ended, killed) | `GET /campaigns` |
| `/pricing/campaigns/new` | Create a campaign: validity, target customer group, budget and currency | `POST /campaigns` |
| `/pricing/campaigns/:id` | Terms, the campaign's agreements, kill and restore at the campaign version | `GET /campaigns/{id}`, `GET /condition-records?campaignId=`, `:kill`, `:restore` |
| `/pricing/procedures` | Procedure designer: versions with status and step order | `GET /procedures` |
| `/pricing/procedures/new` | Save a DRAFT version from the reference steps; `?from=<id>` copies a version into the next one | `POST /procedures` |
| `/pricing/procedures/:id` | A DRAFT's step editor (step, type, CEL requirement, exclusion group and mode, statistical, account key), save at its revision and activate; other versions read-only (PRC-0008 shown) | `GET /procedures/{id}`, `:revise`, `:activate` |
| `/pricing/journals` | Price journal log with a status filter | `GET /journals` |
| `/pricing/journals/new` | Create an OPEN journal from pasted `UPDATE, id, version, percent` / `DELETE, id, version` rows | `POST /journals` |
| `/pricing/journals/import` | Import a CSV file (header row, op first; up to 100,000 rows) as an OPEN journal; row faults listed by row number | `POST /journals:import-csv` |
| `/pricing/journals/:id` | Lines with before and after values; validate, post (PRC-0005 shown) and reverse | `GET`, `:validate`, `:post`, `:reverse` |
| `/pricing/rebates` | Customer and vendor rebate agreements with type, basis, period and tiers; type filter | `GET /rebates/agreements?partyRole=` |
| `/pricing/rebates/new` | Create a tiered agreement for a customer, a customer group or (vendor rebate) a vendor; accounts follow the type (2150 / 4150 or 2160 / 5100) | `POST /rebates/agreements` |
| `/pricing/rebates/:id` | Tiers, posting accounts, accruals by period with an exact total (retroactive rows marked), the retroactive recompute from pasted posted invoice lines, and for a vendor rebate the claims card: preview a period, claim it (credit note or payment deduction; PRC-0015 shown) and list its claims | `GET /rebates/agreements/{id}`, `GET /rebates/accruals`, `POST /rebates/agreements/{id}:retro-recompute`, `GET /rebates/agreements/{id}/claim-preview`, `GET`/`POST /rebates/vendor-claims` |
| `/pricing/rebates/claims` | Vendor rebate claims inquiry by vendor, period and status, with exact totals per currency | `GET /rebates/vendor-claims` |
| `/pricing/rebates/claims/:id` | One claim: accrued, claimed before, amount, settlement; approve at its version (PRC-0046 shown when the creator tries) | `GET /rebates/vendor-claims/{id}`, `:approve` |
| `/pricing/commission/sales-groups` | Sales groups with their reps and shares | `GET /commission/sales-groups` |
| `/pricing/commission/sales-groups/new` | Create a sales group; reps with shares that add up to 100 | `POST /commission/sales-groups` |
| `/pricing/commission/sales-groups/:id` | Reps, commission rules in search order, add a rule (409 PRC-0038 shown) | `GET /commission/sales-groups/{id}`, `GET`/`POST /commission/rules` |
| `/pricing/commission/transactions` | Commission inquiry by sales group, rep, month, customer and invoice line, with an exact total | `GET /commission/transactions` |
| `/pricing/commission/settlement` | Commission settlement export (FR-26.18): totals per rep and currency for invoices dated in a period (previous month by default), preview and download the CSV for accounts payable or payroll | `GET /commission/settlement-export` |
| `/pricing/funds` | Trade funds with budget per period, overspend and open/closed status | `GET /funds` |
| `/pricing/funds/new` | Create a fund with its budget per period and overspend flag | `POST /funds` |
| `/pricing/funds/:id` | Fund usage inquiry (per period: budget, used, remaining; per source), attribute usage (PRC-0011 / PRC-0012 shown), revise the budget, close and reopen | `GET /funds/{id}/usage`, `:attribute`, `:revise`, `:close`, `:reopen` |
| `/pricing/promotions` | Promotions with event, window, baseline, forecast lift and actual lift | `GET /promotions` |
| `/pricing/promotions/new` | Plan a promotion: event category and type, fund, product, window, 13 baseline weeks, lift and spend | `POST /promotions` |
| `/pricing/promotions/:id` | Plan, baseline weeks, actuals and effectiveness; record actuals | `GET /promotions/{id}`, `:actuals` |
| `/pricing/simulate` | Simulation / explain: price lines and read the access-by-access trace | `POST /resolve?explain=true` |

- `api/client.ts` calls `/api/v1/pricing/...` (the BFF rewrites to `/v1/pricing/...`). Problem
  details surface as `PricingApiError` with the stable code and field errors. Commands send an
  `Idempotency-Key`; a retry after a network or 5xx failure reuses it.
- Pages use the shell primitives (`shell/ui`) and `pages.module.css` tokens; every list has
  loading, empty and error states.
- Tests: `npx vitest --run src/modules/pricing` and `npx tsc --noEmit` from `apps/web`. `testServer.tsx`
  holds the fetch stub and router used by `journals.test.tsx`, `rebates.test.tsx`, `vendorRebates.test.tsx`, `commission.test.tsx`, `funds.test.tsx`,
  `promotions.test.tsx`, `campaigns.test.tsx`, `procedures.test.tsx` and `operations.test.tsx` (workspace expiry,
  journal CSV import, commission settlement); `text()` answers with a non-JSON body such as CSV.

Needs: nothing from the shell beyond the existing `shell/ui` components. No npm
dependencies were added.
