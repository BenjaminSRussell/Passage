import { test } from "node:test";
import assert from "node:assert/strict";

import { matchesAny, matchesType, validatePattern } from "../src/filter.ts";

const received = "dev.passage.edi.message.received.v1";

test("literal segments match exactly and the whole type must be covered", () => {
  assert.equal(matchesType(received, received), true);
  assert.equal(matchesType("dev.passage.edi.message.received", received), false);
  assert.equal(matchesType(`${received}.extra`, received), false);
  assert.equal(matchesType("dev.passage.edi.message.acked.v1", received), false);
});

test("'*' matches exactly one segment", () => {
  assert.equal(matchesType("dev.passage.*.message.received.v1", received), true);
  assert.equal(matchesType("dev.passage.edi.message.*.v1", "dev.passage.edi.message.acked.v1"), true);
  assert.equal(matchesType("dev.passage.*.received.v1", received), false);
});

test("a trailing '**' matches one or more segments, never zero", () => {
  assert.equal(matchesType("dev.passage.edi.**", received), true);
  assert.equal(matchesType("dev.passage.edi.message.received.v1.**", received), false);
  assert.equal(matchesType("dev.passage.wms.**", received), false);
});

test("invalid patterns are explained and never match", () => {
  const cases: Array<[string, RegExp]> = [
    ["", /empty/],
    ["**", /first segment/],
    ["**.edi", /first segment/],
    ["dev.**.v1", /last segment/],
    ["dev.Passage.edi", /lowercase/],
    ["dev..edi", /lowercase/],
  ];
  for (const [pattern, reason] of cases) {
    const message = validatePattern(pattern);
    assert.ok(message !== null && reason.test(message), `${pattern}: ${message}`);
    assert.equal(matchesType(pattern, received), false, pattern);
  }
  assert.equal(validatePattern("dev.passage.*.message.**"), null);
});

test("a subscription matches when any of its patterns does; none means nothing", () => {
  assert.equal(matchesAny(["dev.passage.wms.**", "dev.passage.edi.message.*.v1"], received), true);
  assert.equal(matchesAny(["dev.passage.wms.**"], received), false);
  assert.equal(matchesAny([], received), false);
});
