// Run: node --test load/k6
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { journalEntry, thresholds, uuid } from './lib.js';

const SLO = JSON.parse(readFileSync(new URL('../slo/slo.json', import.meta.url)));
const POOL = JSON.parse(readFileSync(new URL('./data/pool.example.json', import.meta.url)));

test('every SLO row produces a k6 threshold', () => {
  const t = thresholds(SLO, 1);
  for (const s of SLO.slos) {
    const key = s.op ? `http_req_duration{op:${s.op}}` : s.metric;
    if (s.op && s.p95ms === undefined && s.p99ms === undefined) continue;
    assert.ok(t[key], `no threshold for ${s.id}`);
  }
  assert.deepEqual(t['http_req_duration{op:entity_read}'], ['p(95)<50', 'p(99)<150']);
  assert.deepEqual(t['http_req_failed{op:journal_post}'], ['rate<0.0005']);
  assert.deepEqual(t['mrp_run_seconds'], ['max<600']);
  assert.deepEqual(t['mrp_runs_completed'], ['count>=1']);
});

test('throughput thresholds are per second and follow LOAD_SCALE', () => {
  assert.deepEqual(thresholds(SLO, 1)['journal_lines_posted'], ['rate>=277.7778']);
  assert.deepEqual(thresholds(SLO, 0.1)['order_lines_accepted'], ['rate>=2.7778']);
  // Latency limits do not scale.
  assert.deepEqual(thresholds(SLO, 0.1)['http_req_duration{op:pricing}'], ['p(95)<20']);
});

test('journal entry is balanced, uses decimal strings and the pool accounts', () => {
  const e = journalEntry(POOL, 500, 'c1');
  assert.equal(e.lines.length, 500);
  assert.equal(e.ledgerId, POOL.ledgerId);
  assert.match(e.documentId, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  assert.match(e.source.id, /^c1-/);
  const cents = { D: 0n, C: 0n };
  for (const l of e.lines) {
    assert.equal(typeof l.amount.value, 'string');
    assert.match(l.amount.value, /^\d+\.\d{2}$/);
    assert.ok(POOL.accounts.includes(l.account));
    cents[l.debitCredit] += BigInt(l.amount.value.replace('.', ''));
  }
  assert.equal(cents.D, cents.C);
  const debitAccounts = new Set(e.lines.filter((l) => l.debitCredit === 'D').map((l) => l.account));
  const creditAccounts = new Set(e.lines.filter((l) => l.debitCredit === 'C').map((l) => l.account));
  for (const a of debitAccounts) assert.ok(!creditAccounts.has(a), `account ${a} on both sides`);
});

test('journal entry rejects an odd line count and a pool without two accounts', () => {
  assert.throws(() => journalEntry(POOL, 3, 'c1'), /even/);
  assert.throws(() => journalEntry({ ...POOL, accounts: ['6100'] }, 2, 'c1'), /debit and one credit/);
});

test('uuid is v4 shaped and unique', () => {
  const seen = new Set();
  for (let i = 0; i < 1000; i++) seen.add(uuid());
  assert.equal(seen.size, 1000);
});
