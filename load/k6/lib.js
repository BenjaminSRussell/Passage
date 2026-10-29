// Pure helpers for scenarios.js. No k6 imports, so `node --test load/k6` can
// exercise them (lib.test.mjs).

// k6 thresholds from the SLO table. Latency SLOs map to tagged request metrics
// (http_req_duration{op:<tag>}); throughput SLOs map to counters, whose rate k6
// reports per second. scale is LOAD_SCALE and lowers only throughput targets.
export function thresholds(slo, scale) {
  const t = {};
  const add = (k, v) => {
    t[k] = (t[k] || []).concat([v]);
  };
  for (const s of slo.slos) {
    if (s.op) {
      if (s.p95ms !== undefined) add(`http_req_duration{op:${s.op}}`, `p(95)<${s.p95ms}`);
      if (s.p99ms !== undefined) add(`http_req_duration{op:${s.op}}`, `p(99)<${s.p99ms}`);
      if (s.errorBudget !== undefined) add(`http_req_failed{op:${s.op}}`, `rate<${s.errorBudget}`);
    } else if (s.metric) {
      if (s.p95Seconds !== undefined) add(s.metric, `p(95)<${s.p95Seconds}`);
      if (s.maxSeconds !== undefined) add(s.metric, `max<${s.maxSeconds}`);
      if (s.minPerHour !== undefined) add(s.metric, `rate>=${((s.minPerHour * scale) / 3600).toFixed(4)}`);
      if (s.minCount !== undefined) add(s.metric, `count>=${s.minCount}`);
    }
  }
  return t;
}

export function uuid() {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === 'x' ? r : (r & 0x3) | 0x8).toString(16);
  });
}

// One balanced ledger entry of `lines` lines (ledger PostEntryRequest): debit
// lines on the first half of pool.accounts, credit lines on the second half,
// decimal-string amounts only (money rule, never float).
export function journalEntry(pool, lines, campaign) {
  if (lines % 2 !== 0) throw new Error('journal entry needs an even line count');
  const half = Math.floor(pool.accounts.length / 2);
  if (half < 1) throw new Error('pool.accounts needs at least one debit and one credit account');
  const out = [];
  for (let i = 0; i < lines / 2; i++) {
    const amount = { currency: pool.currency, value: (10 + (i % 90)).toFixed(2) };
    out.push({ account: pool.accounts[i % half], debitCredit: 'D', amount });
    out.push({ account: pool.accounts[half + (i % half)], debitCredit: 'C', amount });
  }
  return {
    ledgerId: pool.ledgerId,
    documentId: uuid(),
    postingDate: pool.postingDate,
    source: { system: 'k6', type: 'load-campaign', id: `${campaign}-${uuid()}` },
    lines: out,
  };
}
