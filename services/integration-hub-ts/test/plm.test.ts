import { test } from "vitest";
import assert from "node:assert/strict";

import {
  CODE_EFFECTIVITY_MOVED,
  compareRevisions,
  decideEcoImport,
  importKey,
  validateRevision,
  validateSyncInterval,
  type ImportState,
  type PlmEco,
} from "../src/plm.ts";

const today = "2026-10-26";
const emptyState: ImportState = { importedKeys: new Set(), releasedBomVersions: new Set() };
const eco: PlmEco = {
  ecoId: "ECO-100",
  partNumber: "P-1001",
  revision: "B",
  bomVersion: "BOM-B",
  effectivityDate: "2026-11-01",
};

test("FR-35.15: a new revision with a 3-line BOM creates an ECR at its effectivity", () => {
  const d = decideEcoImport(eco, today, emptyState, "alpha");
  assert.deepEqual(d, { action: "CREATE_ECR", effectivityDate: "2026-11-01", issues: [] });
});

test("AC-35.14: importing the same part and revision twice changes nothing the second time", () => {
  const first = decideEcoImport(eco, today, emptyState, "alpha");
  assert.equal(first.action, "CREATE_ECR");
  const after: ImportState = {
    importedKeys: new Set([importKey(eco.partNumber, eco.revision)]),
    latestRevision: "B",
    releasedBomVersions: new Set(),
  };
  const second = decideEcoImport(eco, today, after, "alpha");
  assert.equal(second.action, "DUPLICATE");
});

test("BR-35.16: an effectivity before today moves to today and raises EDI-0139", () => {
  const backdated = { ...eco, effectivityDate: "2026-10-01" };
  const d = decideEcoImport(backdated, today, emptyState, "alpha");
  assert.equal(d.action, "CREATE_ECR");
  assert.equal(d.effectivityDate, today);
  assert.deepEqual(d.issues, [CODE_EFFECTIVITY_MOVED]);
  assert.equal(CODE_EFFECTIVITY_MOVED, "EDI-0139");
});

test("BR-35.16: an ECO targeting a BOM version already released in Passage becomes a task", () => {
  const state: ImportState = { importedKeys: new Set(), releasedBomVersions: new Set(["BOM-B"]) };
  const d = decideEcoImport(eco, today, state, "alpha");
  assert.equal(d.action, "TASK");
  assert.match(d.reason ?? "", /BOM-B/);
});

test("BR-35.16: an older revision than the latest imported one is stale, not applied", () => {
  const state: ImportState = { importedKeys: new Set(), latestRevision: "C", releasedBomVersions: new Set() };
  const d = decideEcoImport({ ...eco, revision: "B" }, today, state, "alpha");
  assert.equal(d.action, "STALE");
});

test("BR-35.16: duplicate check takes precedence over the stale check", () => {
  const state: ImportState = {
    importedKeys: new Set([importKey("P-1001", "B")]),
    latestRevision: "C",
    releasedBomVersions: new Set(),
  };
  assert.equal(decideEcoImport(eco, today, state, "alpha").action, "DUPLICATE");
});

test("compareRevisions orders alpha revisions by length then letter", () => {
  const order = ["A", "B", "Z", "AA", "AB", "AZ", "BA"];
  for (let i = 0; i < order.length; i += 1) {
    for (let j = 0; j < order.length; j += 1) {
      const sign = Math.sign(compareRevisions(order[i]!, order[j]!, "alpha"));
      assert.equal(sign, Math.sign(i - j), `${order[i]} vs ${order[j]}`);
    }
  }
});

test("compareRevisions orders numeric revisions by value, not text", () => {
  assert.ok(compareRevisions("10", "9", "numeric") > 0);
  assert.equal(compareRevisions("007", "7", "numeric"), 0);
});

test("property: alpha ordering agrees with the bijective base-26 value for 1..2000", () => {
  const toBase26 = (n: number): string => {
    let s = "";
    let x = n;
    while (x > 0) {
      const r = (x - 1) % 26;
      s = String.fromCharCode(65 + r) + s;
      x = Math.floor((x - 1) / 26);
    }
    return s;
  };
  for (let n = 1; n <= 2000; n += 1) {
    const a = toBase26(n);
    const b = toBase26(n + 1);
    assert.ok(compareRevisions(a, b, "alpha") < 0, `${a} < ${b}`);
  }
});

test("validation rejects malformed revisions, dates and sync intervals", () => {
  assert.throws(() => validateRevision("b", "alpha"), RangeError);
  assert.throws(() => validateRevision("1A", "numeric"), RangeError);
  assert.throws(() => decideEcoImport({ ...eco, effectivityDate: "01.11.2026" }, today, emptyState, "alpha"), RangeError);
  assert.throws(() => decideEcoImport({ ...eco, effectivityDate: "2026-02-30" }, today, emptyState, "alpha"), RangeError);
  assert.throws(() => validateSyncInterval(0), RangeError);
  assert.throws(() => validateSyncInterval(1441), RangeError);
  assert.doesNotThrow(() => validateSyncInterval(15));
});
