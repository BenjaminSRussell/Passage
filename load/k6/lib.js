// Pure helpers for scenarios.js. No k6 imports, so `node --test load/k6` can
// exercise them (lib.test.mjs).

// k6 thresholds from the SLO table. Latency SLOs map to tagged request metrics
// (http_req_duration{op:<tag>}); throughput SLOs map to counters, whose rate k6
// reports per second. scale is LOAD_SCALE and lowers only throughput targets.
// ids, when given, limits the thresholds to those SLO rows (the soak profile).
export function thresholds(slo, scale, ids) {
  const t = {};
  const add = (k, v) => {
    t[k] = (t[k] || []).concat([v]);
  };
  const only = ids ? new Set(ids) : null;
  if (only) {
    const known = new Set(slo.slos.map((s) => s.id));
    for (const id of only) if (!known.has(id)) throw new Error(`unknown SLO id ${id}`);
  }
  for (const s of slo.slos) {
    if (only && !only.has(s.id)) continue;
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

// Soak load fraction: the profile's fraction (PRD: 50%) times LOAD_SCALE, so a
// smoke soak shrinks the same way a smoke campaign does. The checker gets the
// same LOAD_SCALE as --scale and applies the fraction itself.
export function soakFraction(slo, scale) {
  if (!slo.soak || !(slo.soak.fraction > 0)) throw new Error('slo.json has no soak profile');
  if (!(scale > 0)) throw new Error('LOAD_SCALE must be positive');
  return slo.soak.fraction * scale;
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
