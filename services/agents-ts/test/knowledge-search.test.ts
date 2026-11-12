import { afterAll, beforeAll, afterEach, describe, expect, it } from "vitest";
import { CopilotAnswerCache } from "../src/domain/copilot.js";
import type { RetrievedChunk } from "../src/domain/knowledge.js";
import { createCopilotService } from "../src/runtime/copilot.js";
import type { ModelResponse } from "../src/runtime/engine.js";
import { createKnowledgeService, MemoryVectorIndex, type EmbeddingPort, type KeywordIndex, type KnowledgeText } from "../src/runtime/knowledge.js";
import { PgKeywordIndex } from "../src/store/knowledge.js";
import { ScriptedProvider } from "./fakes.js";
import { fixture, ownerUrl, type Fixture } from "./pgHelper.js";
import { FakeMcp, headers, MemoryStore, serve, T1 } from "./surface-fakes.js";

const T2 = "00000000-0000-7000-8000-0000000000bb";
const SEARCH = "passage.knowledge.search";

/** A bag-of-words embedder: deterministic, no network (the fake embedding port of the tests). */
class FakeEmbeddings implements EmbeddingPort {
  readonly model = "fake-bow";
  readonly calls: string[][] = [];
  private readonly vocab = ["meal", "travel", "hotel", "invoice", "approve", "limit", "dinner", "policy"];
  async embed(texts: readonly string[]): Promise<number[][]> {
    this.calls.push([...texts]);
    return texts.map((t) => this.vocab.map((w) => (t.toLowerCase().includes(w) ? 1 : 0)));
  }
}

/** The keyword index in memory: matches when every query word is a word of the text, scored by word count. */
class MemoryKeywords implements KeywordIndex {
  private readonly rows: Array<KnowledgeText & { tenantId: string; chunkId: string }> = [];
  async upsert(tenantId: string, chunks: readonly KnowledgeText[]): Promise<string[]> {
    return chunks.map((c) => {
      const held = this.rows.find((r) => r.tenantId === tenantId && r.sourceType === c.sourceType && r.sourceRef === c.sourceRef && r.section === c.section);
      if (held !== undefined) {
        held.body = c.body;
        return held.chunkId;
      }
      const chunkId = `00000000-0000-7000-8000-${String(this.rows.length + 1).padStart(12, "0")}`;
      this.rows.push({ ...c, tenantId, chunkId });
      return chunkId;
    });
  }
  async keyword(tenantId: string, query: string, limit: number): Promise<RetrievedChunk[]> {
    const words = query.toLowerCase().split(/\W+/).filter((w) => w !== "");
    return this.rows
      .filter((r) => r.tenantId === tenantId)
      .map((r) => ({ r, hits: words.filter((w) => `${r.section} ${r.body}`.toLowerCase().includes(w)).length }))
      .filter((x) => x.hits > 0)
      .sort((a, b) => b.hits - a.hits)
      .slice(0, limit)
      .map(({ r, hits }) => ({ chunkId: r.chunkId, sourceType: r.sourceType, sourceRef: r.sourceRef, section: r.section, score: hits }));
  }
  async byIds(tenantId: string, ids: readonly string[]): Promise<RetrievedChunk[]> {
    return this.rows.filter((r) => r.tenantId === tenantId && ids.includes(r.chunkId)).map((r) => ({ chunkId: r.chunkId, sourceType: r.sourceType, sourceRef: r.sourceRef, section: r.section, score: 0 }));
  }
}

const POLICY: KnowledgeText = { sourceType: "POLICY", sourceRef: "policy/travel", section: "Meals", body: "Travel meal policy: meal and dinner costs are reimbursed up to the daily limit." };
const OTHER: KnowledgeText = { sourceType: "DOC", sourceRef: "doc/ap", section: "Invoice approval", body: "An invoice over the approval limit needs a second approver." };

describe("hybrid knowledge search", () => {
  it("fuses the keyword and embedding rankings and reports the similarity the citation gate needs", async () => {
    const emb = new FakeEmbeddings();
    const svc = createKnowledgeService({ keywords: new MemoryKeywords(), embeddings: emb, vectors: new MemoryVectorIndex() });
    expect(await svc.index(T1, [POLICY, OTHER])).toEqual({ indexed: 2, embedded: 2 });
    const { chunks } = await svc.search(T1, "travel meal policy");
    expect(chunks[0]).toMatchObject({ sourceRef: "policy/travel", section: "Meals" });
    expect(chunks[0]?.similarity).toBeGreaterThan(0.75);
    expect(chunks[0]?.fusedScore).toBeGreaterThan(chunks[1]?.fusedScore ?? 1);
    // Another tenant sees nothing, in either half.
    expect((await svc.search(T2, "travel meal policy")).chunks).toEqual([]);
  });

  it("without an embedding port is keyword only and its chunks carry no similarity", async () => {
    const svc = createKnowledgeService({ keywords: new MemoryKeywords() });
    expect(await svc.index(T1, [POLICY])).toEqual({ indexed: 1, embedded: 0 });
    const { chunks } = await svc.search(T1, "meal");
    expect(chunks).toHaveLength(1);
    expect(chunks[0]?.similarity).toBeUndefined();
  });

  it("indexing a chunk again replaces its text instead of adding a copy", async () => {
    const keywords = new MemoryKeywords();
    const svc = createKnowledgeService({ keywords });
    await svc.index(T1, [POLICY]);
    await svc.index(T1, [{ ...POLICY, body: "Meals are not reimbursed." }]);
    const { chunks } = await svc.search(T1, "reimbursed");
    expect(chunks).toHaveLength(1);
  });
});

let close: (() => Promise<void>) | undefined;
afterEach(async () => {
  await close?.();
  close = undefined;
});

const SEARCH_DUTIES = "mcp.copilot.use,knowledge.write";
const post = (base: string, path: string, body: unknown, duties: string) => fetch(`${base}${path}`, { method: "POST", headers: headers(duties), body: JSON.stringify(body) });

describe("knowledge routes", () => {
  it("indexing needs knowledge.write and searching the copilot duty and take only well-formed bodies", async () => {
    const knowledge = createKnowledgeService({ keywords: new MemoryKeywords(), embeddings: new FakeEmbeddings(), vectors: new MemoryVectorIndex() });
    const srv = await serve({ approvals: new MemoryStore(), knowledge });
    close = srv.close;
    expect((await post(srv.base, "/api/v1/knowledge/chunks", { chunks: [POLICY] }, "mcp.copilot.use")).status).toBe(403);
    expect((await post(srv.base, "/api/v1/knowledge/chunks", { chunks: [POLICY] }, SEARCH_DUTIES)).status).toBe(200);
    expect((await post(srv.base, "/api/v1/knowledge/chunks", { chunks: [{ ...POLICY, sourceType: "WIKI" }] }, SEARCH_DUTIES)).status).toBe(400);
    expect((await post(srv.base, "/api/v1/knowledge/chunks", { chunks: [] }, SEARCH_DUTIES)).status).toBe(400);
    expect((await post(srv.base, "/api/v1/knowledge/search", { query: "meal" }, "knowledge.write")).status).toBe(403);
    const ok = await post(srv.base, "/api/v1/knowledge/search", { query: "travel meal policy", limit: 3 }, SEARCH_DUTIES);
    expect(ok.status).toBe(200);
    expect(((await ok.json()) as { chunks: Array<{ sourceRef: string }> }).chunks[0]?.sourceRef).toBe("policy/travel");
    expect((await post(srv.base, "/api/v1/knowledge/search", { query: "x", limit: 99 }, SEARCH_DUTIES)).status).toBe(400);
  });
});

describe("the copilot answers from the knowledge index with a citation, or refuses (AGT-0212)", () => {
  const call = (args: Record<string, unknown>): ModelResponse => ({ text: "", proposal: { tool: SEARCH, args, confidence: 1, amount: { amount: "0", currency: "USD" } }, usage: { tokensIn: 100, tokensOut: 10 } });
  const answer = (text: string): ModelResponse => ({ text, usage: { tokensIn: 100, tokensOut: 20 } });

  async function run(question: string, indexed: KnowledgeText[], duties: string) {
    const knowledge = createKnowledgeService({ keywords: new MemoryKeywords(), embeddings: new FakeEmbeddings(), vectors: new MemoryVectorIndex() });
    await knowledge.index(T1, indexed);
    const store = new MemoryStore();
    const provider = new ScriptedProvider([call({ query: question }), answer("Meals are reimbursed up to the daily limit.")]);
    const copilot = createCopilotService({
      providerFor: () => provider,
      mcp: new FakeMcp([]),
      store,
      killSwitch: { isHalted: () => false },
      cache: new CopilotAnswerCache(),
      config: { tokenCap: 20_000, maxTurns: 3, pseudonymTtlMs: 60_000 },
      modelId: "claude-sonnet-5-5",
      knowledge,
    });
    const srv = await serve({ approvals: store, copilot });
    close = srv.close;
    const res = await fetch(`${srv.base}/api/v1/copilot/chat`, { method: "POST", headers: headers(duties), body: JSON.stringify({ mode: "ask", message: question }) });
    return { body: (await res.json()) as { answer: string; citations: unknown[]; refusal: { code: string } | null; toolCalls: unknown[] }, provider, store };
  }

  it("cites the grounded chunk the search found", async () => {
    const r = await run("What is the travel meal policy?", [POLICY, OTHER], "mcp.copilot.use");
    expect(r.body.refusal).toBeNull();
    expect(r.body.toolCalls).toEqual([{ tool: SEARCH, status: "OK" }]);
    expect(r.body.citations).toEqual([{ sourceType: "POLICY", sourceRef: "policy/travel", label: "Meals" }]);
    expect(r.provider.seen[1]?.user).toContain("Result of passage.knowledge.search");
  });

  it("refuses with AGT-0212 when nothing is similar enough", async () => {
    const r = await run("What is the travel meal policy?", [{ sourceType: "DOC", sourceRef: "doc/misc", section: "Parking", body: "Cars park in the yard." }], "mcp.copilot.use");
    expect(r.body.refusal?.code).toBe("AGT-0212");
    expect(r.body.citations).toEqual([]);
  });
});

describe.runIf(ownerUrl)("Postgres full-text keyword index", () => {
  let fx: Fixture;
  let index: PgKeywordIndex;
  beforeAll(async () => {
    fx = await fixture();
    index = new PgKeywordIndex(fx.store);
  });
  afterAll(async () => {
    await fx.close();
  });

  it("ranks by text search, replaces a chunk in place, and keeps tenants apart", async () => {
    const [a, b] = await index.upsert(T1, [POLICY, OTHER]);
    expect(a).not.toBe(b);
    expect((await index.upsert(T1, [{ ...POLICY, body: "Dinner and meal costs on travel are reimbursed up to the daily limit." }]))[0]).toBe(a);
    const hits = await index.keyword(T1, "travel meal reimbursed", 5);
    expect(hits.map((h) => h.chunkId)).toEqual([a]);
    expect(hits[0]?.score).toBeGreaterThan(0);
    expect(await index.keyword(T2, "travel meal", 5)).toEqual([]);
    expect((await index.byIds(T1, [a ?? "", b ?? ""])).map((c) => c.sourceRef).sort()).toEqual(["doc/ap", "policy/travel"]);
    expect(await index.byIds(T2, [a ?? ""])).toEqual([]);
  });
});
