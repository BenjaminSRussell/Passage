# Pricing web module (PSG-26, wave 26)

Routes, all on the pricing service API (`services/pricing/api/openapi.yaml`):

| Route | Page | Calls |
|---|---|---|
| `/pricing` | Workspace: exact tiles (active, drafts, expiring in 30 days, journals to post, live campaigns, vendor claims to approve, customer and vendor rebates accrued this month per currency), the expiring list (active agreements ending in the next 30 days, soonest first), and the record expiry run (BR-26.9) for a legal entity and day | `GET /workspace`, `GET /condition-records?status=ACTIVE&validToFrom=&validToTo=`, `POST /condition-records:expire` |
| `/pricing/agreements` | Trade agreements grid with status, type, customer, product, as-of and ends-between filters, and Export CSV of every match | `GET /condition-records`, `GET /condition-records:export` |
| `/pricing/agreements/new` | Create a DRAFT agreement, optionally in a campaign; `?copy=<id>` starts from an existing one, `?campaign=<id>` fills the campaign | `POST /condition-records` |
| `/pricing/agreements/:id` | Detail with its campaign, activate (overlap 409 shown) and withdraw | `GET`, `:activate`, `:withdraw` |
| `/pricing/free-goods` | Free goods rules (FR-26.22) with status | `GET /free-goods-rules` |
| `/pricing/free-goods/new` | Create a rule: customers, product, unit, inclusive or exclusive, calculation ("Legal entity default" sends none, so the entity's `freeGoodsCalcRule` applies), quantities, validity (overlap 409 shown) | `POST /free-goods-rules` |
| `/pricing/free-goods/:id` | Rule detail, Edit rule (calculation, quantities, minimum, validity at the version read; once the rule has started its offer and first day are read-only and its last day moves only within yesterday and later) and withdraw | `GET /free-goods-rules/{id}`, `:revise`, `:withdraw` |
| `/pricing/commodity/bases` | Commodity cost basis types (FR-26.21) | `GET /commodity/bases` |
| `/pricing/commodity/bases/new` | Create a cost basis type: code, name, currency, unit (duplicate code 409 shown) | `POST /commodity/bases` |
| `/pricing/commodity/bases/:id` | Cost basis type, record an index quote (a second quote for a day is the next revision) and the quotes inquiry | `GET /commodity/bases/{id}`, `GET`/`POST …/index-values` |
| `/pricing/commodity/calculations` | Commodity price calculations with status | `GET /commodity/calculations` |
| `/pricing/commodity/calculations/new` | Create a calculation: index x factor + margin, decimals, staleness window, the product price key | `POST /commodity/calculations` |
| `/pricing/commodity/calculations/:id` | Preview a run, run it (creates a VALIDATED price journal, link to review), run history, withdraw | `GET /commodity/calculations/{id}`, `:preview`, `:run`, `GET …/runs`, `:withdraw` |
| `/pricing/campaigns` | Campaigns with target group, validity, budget and status (live, scheduled, ended, killed) | `GET /campaigns` |
| `/pricing/campaigns/new` | Create a campaign: validity, target customer group, budget and currency | `POST /campaigns` |
| `/pricing/campaigns/:id` | Terms, the campaign's agreements, edit (name, validity, group, budget), kill and restore at the campaign version | `GET /campaigns/{id}`, `GET /condition-records?campaignId=`, `:revise`, `:kill`, `:restore` |
| `/pricing/procedures` | Procedure designer: versions with status and step order | `GET /procedures` |
| `/pricing/procedures/new` | Save a DRAFT version from the reference steps; `?from=<id>` copies a version into the next one | `POST /procedures` |
| `/pricing/procedures/:id` | A DRAFT's step editor (step, type, CEL requirement, exclusion group and mode, statistical, account key), save at its revision and activate; other versions read-only (PRC-0008 shown) | `GET /procedures/{id}`, `:revise`, `:activate` |
| `/pricing/journals` | Price journal log with a status filter | `GET /journals` |
| `/pricing/journals/new` | Create an OPEN journal from pasted `UPDATE, id, version, percent` / `DELETE, id, version` rows | `POST /journals` |
| `/pricing/journals/import` | Import a CSV file (header row, op first; up to 100,000 rows) as an OPEN journal; row faults listed by row number | `POST /journals:import-csv` |
| `/pricing/journals/:id` | Lines with before and after values, who created and who posted it; validate, post (PRC-0005, PRC-0056 and the four-eyes PRC-0057 shown) and reverse; Download audit extract (CSV of every line) once posted or reversed | `GET`, `:validate`, `:post`, `:reverse`, `GET …/audit-extract?format=csv` |
| `/pricing/brokers` | Broker contracts (FR-26.16) with rate, scope, validity and status filter | `GET /broker-contracts` |
| `/pricing/brokers/new` | Create a broker contract: broker vendor, customer and product scope, percentage of net or amount per unit, validity, accounts | `POST /broker-contracts` |
| `/pricing/brokers/:id` | Terms, accruals (credit note lines reverse in proportion), period claim preview and BC- claim, close the contract | `GET /broker-contracts/{id}`, `GET …/accruals`, `GET …/claim-preview`, `POST /contract-claims`, `:close` |
| `/pricing/royalties` | Royalty agreements (FR-26.17) with tiers, minimum per period and status filter | `GET /royalty-agreements` |
| `/pricing/royalties/new` | Create a royalty agreement: licensor vendor, product or group, net sales or quantity, tiers, minimum royalty, period | `POST /royalty-agreements` |
| `/pricing/royalties/:id` | Terms, accruals, close a period (minimum royalty shortfall, PRC-0017 notice), preview and RY- claim, close the agreement | `GET /royalty-agreements/{id}`, `GET …/accruals`, `:close-period`, `GET …/claim-preview`, `POST /contract-claims`, `:close` |
| `/pricing/contract-claims` | Broker and royalty claims inquiry by type, vendor, period and status, with exact totals per currency | `GET /contract-claims` |
| `/pricing/contract-claims/:id` | Claim detail and approval for AP (the creator cannot approve, PRC-0046) | `GET /contract-claims/{id}`, `:approve` |
| `/pricing/rebates` | Customer and vendor rebate agreements with type, basis, period and tiers; type filter | `GET /rebates/agreements?partyRole=` |
| `/pricing/rebates/new` | Create a tiered agreement for a customer, a customer group or (vendor rebate) a vendor; accounts follow the type (2150 / 4150 or 2160 / 5100) | `POST /rebates/agreements` |
| `/pricing/rebates/:id` | Tiers, posting accounts, accruals by period with an exact total (retroactive rows marked), the retroactive recompute from pasted posted invoice lines, and for a vendor rebate the claims card: preview a period, claim it (credit note or payment deduction; PRC-0015 shown) and list its claims | `GET /rebates/agreements/{id}`, `GET /rebates/accruals`, `GET …/statement`, `POST /rebates/agreements/{id}:retro-recompute`, `GET /rebates/agreements/{id}/claim-preview`, `GET`/`POST /rebates/vendor-claims` |
| `/pricing/rebates/claims` | Vendor rebate claims inquiry by vendor, period and status, with exact totals per currency | `GET /rebates/vendor-claims` |
| `/pricing/rebates/claims/:id` | One claim: accrued, claimed before, amount, settlement; approve at its version (PRC-0046 shown when the creator tries) | `GET /rebates/vendor-claims/{id}`, `:approve` |
| `/pricing/commission/sales-groups` | Sales groups with their reps and shares | `GET /commission/sales-groups` |
| `/pricing/commission/sales-groups/new` | Create a sales group; reps with shares that add up to 100 | `POST /commission/sales-groups` |
| `/pricing/commission/sales-groups/:id` | Reps, commission rules in search order, add a rule (409 PRC-0038 shown) | `GET /commission/sales-groups/{id}`, `GET`/`POST /commission/rules` |
| `/pricing/commission/transactions` | Commission inquiry by sales group, rep, month, customer and invoice line, with an exact total | `GET /commission/transactions` |
| `/pricing/commission/settlement` | Commission settlement export (FR-26.18): totals per rep and currency for invoices dated in a period (previous month by default), preview, download the CSV for accounts payable or payroll, and record the export (pricing.commission.maintain) | `GET /commission/settlement-export`, `POST /commission/settlement-exports` |
| `/pricing/commission/exports` | Recorded settlement exports (FR-26.18): what was handed to accounts payable or payroll per period, filter by target and status, status issued or voided | `GET /commission/settlement-exports` |
| `/pricing/commission/exports/:id` | One recorded export: the rows frozen when it was recorded, the file digest, download of the same file, void with a reason (pricing.commission.maintain) | `GET /commission/settlement-exports/{id}`, `GET …/{id}/file`, `POST …/{id}:void` |
| `/pricing/funds` | Trade funds with budget per period, overspend and open/closed status | `GET /funds` |
| `/pricing/funds/new` | Create a fund with its budget per period and overspend flag | `POST /funds` |
| `/pricing/funds/:id` | Fund usage inquiry (per period: budget, used, remaining; per source), attribute usage (PRC-0011 / PRC-0012 shown), revise the budget, close and reopen | `GET /funds/{id}/usage`, `:attribute`, `:revise`, `:close`, `:reopen` |
| `/pricing/promotions` | Promotions with event, window, baseline, forecast lift and actual lift | `GET /promotions` |
| `/pricing/promotions/new` | Plan a promotion: event category and type, fund, product, window, 13 baseline weeks, lift and spend | `POST /promotions` |
| `/pricing/promotions/:id` | Plan, baseline weeks, actuals and effectiveness; record actuals | `GET /promotions/{id}`, `:actuals` |
| `/pricing/parameters` | Pricing parameters of a legal entity: `journalFourEyes` (default off), the pricing date and total discount bases, smart rounding bands, price search mode, small order thresholds and the free goods calculation rule (`freeGoodsCalcRule`, default WHOLE_UNITS), saved at the version read; read with pricing.view, the save is offered only with pricing.admin | `GET`/`PUT /parameters` |
| `/pricing/simulate` | Simulation / explain: price lines and read the access-by-access trace | `POST /resolve?explain=true` |
| `/pricing/compare` | Compare procedure versions: one tuple under two versions, line by line | `GET /procedures`, `POST /compare` |
| `/pricing/price-list` | Price list by customer: the active agreements that apply on a date, and the net price at each quantity break (by product, product group and sales channel; CSV export) | `GET /price-list`, `GET /price-list/net-prices` |

- `api/client.ts` calls `/api/v1/pricing/...` (the BFF rewrites to `/v1/pricing/...`). Problem
  details surface as `PricingApiError` with the stable code and field errors. Commands send an
  `Idempotency-Key`; a retry after a network or 5xx failure reuses it.
- Pages use the shell primitives (`shell/ui`) and `pages.module.css` tokens; every list has
  loading, empty and error states.
- Tests: `npx vitest --run src/modules/pricing` and `npx tsc --noEmit` from `apps/web`. `testServer.tsx`
  holds the fetch stub and router used by `journals.test.tsx`, `rebates.test.tsx`, `vendorRebates.test.tsx`, `commission.test.tsx`, `funds.test.tsx`,
  `promotions.test.tsx`, `campaigns.test.tsx`, `procedures.test.tsx`, `parameters.test.tsx` and `operations.test.tsx` (workspace expiry,
  journal CSV import, commission settlement); `text()` answers with a non-JSON body such as CSV.

Needs: nothing from the shell beyond the existing `shell/ui` components, `shell/principal` and `shell/dutyGate`. No npm
dependencies were added.
