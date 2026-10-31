// Passage soak test (PSG-50, PRD 9.50.9 "Soak": 24 h at 50% load, no memory growth > 10%).
//
// Run:  k6 run --env BASE_URL=https://api.perf.passage.dev --env TOKEN=$TOKEN \
//         --env POOL_FILE=/path/to/pool.json --env CAMPAIGN_ID=soak-2026-10 \
//         --summary-export=results/soak-summary.json load/k6/soak.js
// Then: python3 load/tools/check_campaign.py --profile soak results/soak-summary.json
//       python3 load/tools/check_soak.py results/soak-memory-*.csv
//
// Shape: S1 journal posting and S3 interactive mix of scenarios.js, at the
// `soak.fraction` of slo.json (0.5) for `soak.duration` (24h). The request code
// is the campaign's own (same routes, bodies, op tags and Idempotency-Keys), so
// the two cannot drift. Only the SLO rows those scenarios measure are thresholds
// here (`soak.slos`); the checker's soak profile checks the same rows.
// LOAD_SCALE shrinks a smoke soak further and must be passed to the checker as
// --scale; SOAK_DURATION shortens it, but check_soak.py fails a memory series
// that spans less than `soak.minSpanHours` unless told otherwise.
// Memory growth is not visible to k6: export container_memory_working_set_bytes
// per service for the run window and check it with check_soak.py.

import { arrivalRate, soakFraction, thresholds } from './lib.js';
import { s1JournalPost, s3Interactive } from './scenarios.js';

// k6 calls scenario functions by their export name from the entry module.
export { s1JournalPost, s3Interactive };

const SLO = JSON.parse(open('../slo/slo.json'));
const CAMPAIGN = __ENV.CAMPAIGN_ID || 'soak-local';
const SCALE = Number(__ENV.LOAD_SCALE || '1');
const FRACTION = soakFraction(SLO, SCALE);
const DURATION = __ENV.SOAK_DURATION || SLO.soak.duration;

function part(n) {
  return Math.max(1, Math.round(n * FRACTION));
}

// The campaign's S1 and S3 numbers (scenarios.js) at FRACTION.
export const options = {
  scenarios: {
    soak_journal_post: {
      executor: 'constant-arrival-rate',
      exec: 's1JournalPost',
      rate: arrivalRate(34, FRACTION),
      timeUnit: '1m',
      duration: DURATION,
      preAllocatedVUs: part(40),
      maxVUs: part(400),
    },
    soak_interactive: {
      executor: 'constant-vus',
      exec: 's3Interactive',
      vus: part(10000),
      duration: DURATION,
    },
  },
  thresholds: thresholds(SLO, FRACTION, SLO.soak.slos),
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
  tags: { campaign: CAMPAIGN, kind: 'soak' },
};
