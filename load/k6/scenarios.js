// Passage load campaign S1-S7 (PSG-50, PRD 9.50 FR-50.1).
//
// Run:  k6 run --env BASE_URL=https://api.perf.passage.dev --env TOKEN=$TOKEN \
//         --env POOL_FILE=/path/to/pool.json --summary-export=results/summary.json load/k6/scenarios.js
// Then: python3 load/tools/check_campaign.py results/summary.json
//
// Thresholds are generated from load/slo/slo.json so the SLO table has one source.
// Runs go against a masked copy of the first customer's data, never production (BR-50.5);
// ids come from the data pool (load/k6/data/pool.example.json shows its shape).
// Paths go through the gateway, which rewrites /api/v1/<svc> to the service's /v1/<svc>
// (deploy/gateway). Bodies follow services/<svc>/api/openapi.yaml. A 4xx/5xx counts as failed.

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import { SharedArray } from 'k6/data';
import { journalEntry, thresholds, uuid } from './lib.js';

const SLO = JSON.parse(open('../slo/slo.json'));
const BASE = __ENV.BASE_URL || 'https://api.perf.passage.dev';
const TOKEN = __ENV.TOKEN || '';
const CAMPAIGN = __ENV.CAMPAIGN_ID || 'local';
const POOL_FILE = __ENV.POOL_FILE || './data/pool.example.json';

// Scale knob: 1.0 = full FR-50.1 targets. Smoke runs use a small fraction; the
// checker must then be given the same --scale.
const SCALE = Number(__ENV.LOAD_SCALE || '1');

// The pool is parsed once and shared read-only across VUs. The 50k MRP item ids
// live in their own array so only the S5 VU materialises them.
const POOL = new SharedArray('pool', () => {
  const p = JSON.parse(open(POOL_FILE));
  delete p.mrp.itemIds;
  return [p];
})[0];
const MRP_ITEMS = new SharedArray('mrpItems', () => JSON.parse(open(POOL_FILE)).mrp.itemIds);

// Routes verified against the services' OpenAPI files on main.
const PATHS = {
  journalEntries: '/api/v1/ledger/journal-entries', // ledger postEntry
  salesOrders: '/api/v1/sales/orders', // sales createOrder
  pricingResolve: '/api/v1/pricing/resolve', // pricing resolvePrice
  atpCheck: '/api/v1/sales/atp:check', // sales ATP check
  generatedData: '/api/v1/data', // metadata listGeneratedEntityRecords ("entity read (generated)")
  parties: '/api/v1/party/parties', // party createParty (form save)
  analyticsQuery: '/api/v1/analytics/query', // analytics planQuery (explorer)
  planRun: '/api/v1/planning/plans', // planning runPlan: /{planCode}:run
  allocationRuns: '/api/v1/controlling/allocation-runs', // costacct allocation run
};

// No route on main serves these yet (docs/ops/NOT-IMPLEMENTED.md). They stay so
// the scenario mix is complete; they show as failed requests until the owning
// waves publish them, which fails the campaign as it should.
const UNVERIFIED_PATHS = {
  dashboards: '/api/v1/analytics/dashboards',
  scans: '/api/v1/warehouse/scans',
  inventoryClose: '/api/v1/inventory/close',
  fxRevaluation: '/api/v1/ledger/fx-revaluations',
  freshness: '/api/v1/analytics/freshness',
};

// Seconds, so not time trends (k6 treats time trends as milliseconds).
const lakehouseLag = new Trend('lakehouse_lag_seconds');
const mrpRun = new Trend('mrp_run_seconds');
const journalLines = new Counter('journal_lines_posted');
const orderLines = new Counter('order_lines_accepted');
const mrpCompleted = new Counter('mrp_runs_completed');

const JOURNAL_BATCH_LINES = 500;
const ORDER_LINES = 10;

// Load shape. Arrival rates are per minute and derived from FR-50.1 with about
// 2% headroom so the counter rate, which k6 divides by the whole run including
// graceful stop, still clears the target:
// S1 1M lines/h at 500 lines per entry = 2000 entries/h = 33.3/min -> 34/min (1.02M lines/h).
// S2 100k lines/h at 10 lines per order = 10000 orders/h = 166.7/min -> 170/min (102k lines/h).
// S4 2000 scanners at one scan per 5 s = 400 scans/s.
function rate(perUnit) {
  return Math.max(1, Math.round(perUnit * SCALE));
}

// Virtual-user counts scale the same way; they are counts, not rates.
function scaled(n) {
  return Math.max(1, Math.round(n * SCALE));
}

export const options = {
  scenarios: {
    s1_journal_post: {
      executor: 'constant-arrival-rate',
      exec: 's1JournalPost',
      rate: rate(34),
      timeUnit: '1m',
      duration: '2h',
      preAllocatedVUs: scaled(40),
      maxVUs: scaled(400),
    },
    s2_order_intake: {
      executor: 'constant-arrival-rate',
      exec: 's2OrderIntake',
      rate: rate(170),
      timeUnit: '1m',
      duration: '2h',
      preAllocatedVUs: scaled(200),
      maxVUs: scaled(1000),
    },
    s3_interactive: {
      executor: 'constant-vus',
      exec: 's3Interactive',
      vus: scaled(10000),
      duration: '2h',
    },
    // Arrival rate, not VUs with sleep, so slow responses cannot lower the scan rate.
    s4_warehouse_mobile: {
      executor: 'constant-arrival-rate',
      exec: 's4Scan',
      rate: rate(400),
      timeUnit: '1s',
      duration: '2h',
      preAllocatedVUs: scaled(500),
      maxVUs: scaled(2000),
    },
    s5_mrp: {
      executor: 'per-vu-iterations',
      exec: 's5Mrp',
      vus: 1,
      iterations: 1,
      maxDuration: '30m',
      startTime: '10m',
    },
    // Month-end jobs run alongside S1, which is at full rate here (the PRD asks
    // for at least 25%); 9/min is a quarter of the S1 entry rate.
    s6_month_end: {
      executor: 'constant-arrival-rate',
      exec: 's6MonthEnd',
      rate: rate(9),
      timeUnit: '1m',
      duration: '2h',
      preAllocatedVUs: scaled(10),
      maxVUs: scaled(60),
    },
    s7_cdc_lag: {
      executor: 'constant-vus',
      exec: 's7Freshness',
      vus: 1,
      duration: '2h',
    },
  },
  thresholds: thresholds(SLO, SCALE),
  // k6 exports avg/min/med/max/p(90)/p(95) by default; the p99 SLOs need p(99).
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
  tags: { campaign: CAMPAIGN },
};

function headers(extra) {
  return Object.assign({ Authorization: `Bearer ${TOKEN}`, 'Content-Type': 'application/json' }, extra || {});
}

// Idempotency-Key on every creating POST (CONVENTIONS); unique per request.
function idem(prefix) {
  return { 'Idempotency-Key': `${prefix}-${uuid()}` };
}

function ok(res) {
  return check(res, { 'status 2xx': (r) => r.status >= 200 && r.status < 300 });
}

function pick(weights) {
  const r = Math.random();
  let acc = 0;
  for (const [name, w] of weights) {
    acc += w;
    if (r < acc) return name;
  }
  return weights[weights.length - 1][0];
}

function any(list, i) {
  return list[i % list.length];
}

// S1: journal posting, one 500-line entry per request (FR-50.1).
export function s1JournalPost() {
  const res = http.post(`${BASE}${PATHS.journalEntries}`, JSON.stringify(journalEntry(POOL, JOURNAL_BATCH_LINES, CAMPAIGN)), {
    headers: headers(idem('s1')),
    tags: { op: 'journal_post' },
  });
  if (ok(res)) journalLines.add(JOURNAL_BATCH_LINES);
}

// S2: order intake with pricing and ATP per order (10 lines each).
export function s2OrderIntake() {
  const customer = any(POOL.customers, __VU + __ITER);
  const products = [];
  for (let i = 0; i < ORDER_LINES; i++) products.push(any(POOL.products, __ITER * ORDER_LINES + i));

  const price = http.post(
    `${BASE}${PATHS.pricingResolve}`,
    JSON.stringify({
      legalEntityId: POOL.legalEntityId,
      customerId: customer.customerId,
      currency: POOL.currency,
      pricingDate: POOL.requestedShipDate,
      lines: products.map((p) => ({ productId: p.productId, unit: p.unit, quantity: '1' })),
    }),
    { headers: headers(), tags: { op: 'pricing' } },
  );
  ok(price);

  const atp = http.post(
    `${BASE}${PATHS.atpCheck}`,
    JSON.stringify({
      requested: POOL.requestedShipDate,
      quantity: String(ORDER_LINES),
      periods: [{ date: POOL.requestedShipDate, supply: '1000', demand: '0' }],
    }),
    { headers: headers(), tags: { op: 'atp' } },
  );
  ok(atp);

  const res = http.post(
    `${BASE}${PATHS.salesOrders}`,
    JSON.stringify({
      legalEntityId: POOL.legalEntityId,
      customerId: customer.customerId,
      invoiceAccountId: customer.invoiceAccountId,
      currency: POOL.currency,
      requestedShipDate: POOL.requestedShipDate,
      channel: 'LOAD',
      lines: products.map((p) => ({
        productId: p.productId,
        quantity: '1',
        unit: p.unit,
        siteId: POOL.siteId,
        warehouseId: POOL.warehouseId,
        unitPrice: p.unitPrice,
        requestedShipDate: POOL.requestedShipDate,
      })),
    }),
    { headers: headers(idem('s2')), tags: { op: 'order_intake' } },
  );
  if (ok(res)) orderLines.add(ORDER_LINES);
}

// S3: interactive mix, 60% list/detail reads, 25% form saves, 10% dashboards, 5% explorer.
// Think time 10 s between actions.
export function s3Interactive() {
  const action = pick([
    ['read', 0.6],
    ['save', 0.25],
    ['dashboard', 0.1],
    ['explorer', 0.05],
  ]);
  if (action === 'read') {
    ok(http.get(`${BASE}${PATHS.generatedData}/${POOL.generatedEntity}?limit=50`, {
      headers: headers(),
      tags: { op: 'entity_read' },
    }));
  } else if (action === 'save') {
    ok(http.post(`${BASE}${PATHS.parties}`, JSON.stringify({ partyType: 'organization', legalName: `Load ${CAMPAIGN} ${__VU}-${__ITER}` }), {
      headers: headers(idem('s3')),
      tags: { op: 'form_save' },
    }));
  } else if (action === 'dashboard') {
    ok(http.get(`${BASE}${UNVERIFIED_PATHS.dashboards}/executive`, { headers: headers(), tags: { op: 'dashboard' } }));
  } else {
    ok(http.post(`${BASE}${PATHS.analyticsQuery}`, JSON.stringify(POOL.analytics), {
      headers: headers(),
      tags: { op: 'explorer' },
    }));
  }
  sleep(10);
}

// S4: one scan per arrival (2000 scanners at one scan per 5 s).
export function s4Scan() {
  ok(http.post(`${BASE}${UNVERIFIED_PATHS.scans}`, JSON.stringify({ action: 'PICK', barcode: `LP-${__VU}-${__ITER}` }), {
    headers: headers(idem('s4')),
    tags: { op: 'mobile_scan' },
  }));
}

// S5: MRP regeneration of the pool's items (50k in fx-load-10k). runPlan is
// synchronous: 201 means the version was created and made current.
export function s5Mrp() {
  const items = [];
  for (let i = 0; i < MRP_ITEMS.length; i++) items.push({ itemId: MRP_ITEMS[i] });
  const start = Date.now();
  const res = http.post(
    `${BASE}${PATHS.planRun}/${POOL.mrp.planCode}:run`,
    JSON.stringify({ asOf: POOL.postingDate, siteId: POOL.siteId, items }),
    { headers: headers(idem('s5')), tags: { op: 'mrp' }, timeout: '20m' },
  );
  mrpRun.add((Date.now() - start) / 1000);
  if (ok(res)) mrpCompleted.add(1);
}

// S6: month-end work: inventory close, FX revaluation, allocations.
export function s6MonthEnd() {
  const action = pick([
    ['close', 0.4],
    ['fx', 0.3],
    ['alloc', 0.3],
  ]);
  const period = POOL.postingDate.slice(0, 7);
  let path = PATHS.allocationRuns;
  // costacct RunRequest: an allocation across the pool's cost objects.
  let body = Object.assign({ legalEntityId: POOL.legalEntityId, period, currency: POOL.currency }, POOL.allocation);
  if (action !== 'alloc') {
    path = action === 'close' ? UNVERIFIED_PATHS.inventoryClose : UNVERIFIED_PATHS.fxRevaluation;
    body = { legalEntityId: POOL.legalEntityId, period };
  }
  ok(http.post(`${BASE}${path}`, JSON.stringify(body), {
    headers: headers(idem('s6')),
    tags: { op: 'month_end' },
  }));
}

// S7: lakehouse freshness, sampled every 10 s during S1+S2.
export function s7Freshness() {
  const res = http.get(`${BASE}${UNVERIFIED_PATHS.freshness}`, { headers: headers(), tags: { op: 'freshness' } });
  if (ok(res)) {
    const lag = res.json('lagSeconds');
    if (typeof lag === 'number') lakehouseLag.add(lag);
  }
  sleep(10);
}
