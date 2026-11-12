import { fuseRankings, type Citation, type RetrievedChunk } from "../domain/knowledge.js";
import { ApiError, asObject } from "../api/principal.js";

/** The text of one chunk as it is indexed. */
export interface KnowledgeText {
  sourceType: RetrievedChunk["sourceType"];
  sourceRef: string;
  section: string;
  body: string;
}

/** Keyword retrieval and the chunk store: Postgres full-text search in production (`src/store/knowledge.ts`). */
export interface KeywordIndex {
  /** Stores or replaces chunks by (source type, source ref, section) and returns their ids in order. */
  upsert(tenantId: string, chunks: readonly KnowledgeText[]): Promise<string[]>;
  keyword(tenantId: string, query: string, limit: number): Promise<RetrievedChunk[]>;
  byIds(tenantId: string, ids: readonly string[]): Promise<RetrievedChunk[]>;
}

/** Turns texts into vectors. Only the tenant's own text is sent; the model and its region follow the cloud profile. */
export interface EmbeddingPort {
  readonly model: string;
  embed(texts: readonly string[]): Promise<number[][]>;
}

/** Nearest-neighbour retrieval over chunk vectors (pgvector, or any vector store, behind this port). */
export interface VectorIndex {
  upsert(tenantId: string, chunkId: string, vector: readonly number[]): Promise<void>;
  /** Chunk ids with cosine similarity, best first. */
  nearest(tenantId: string, vector: readonly number[], limit: number): Promise<Array<{ chunkId: string; similarity: number }>>;
}

export const MAX_CHUNKS_PER_REQUEST = 100;
export const MAX_QUERY_CHARS = 1000;
const FETCH = 30;

export interface KnowledgeService {
  index(tenantId: string, chunks: readonly KnowledgeText[]): Promise<{ indexed: number; embedded: number }>;
  search(tenantId: string, query: string, limit?: number): Promise<{ chunks: Citation[] }>;
}

/**
 * Hybrid retrieval (FR-47.14): the keyword ranking and the embedding ranking are fused by reciprocal rank
 * (`fuseRankings`), and each chunk found by the embedding side carries its cosine similarity, which the citation gate
 * (AGT-0212) judges. Without an embedding port or a vector index the search is keyword only: its chunks carry no
 * similarity, so the gate refuses to treat them as grounded rather than inventing a score.
 */
export function createKnowledgeService(deps: { keywords: KeywordIndex; embeddings?: EmbeddingPort; vectors?: VectorIndex }): KnowledgeService {
  const semantic = deps.embeddings !== undefined && deps.vectors !== undefined;
  return {
    async index(tenantId, chunks) {
      const ids = await deps.keywords.upsert(tenantId, chunks);
      if (!semantic || chunks.length === 0) return { indexed: ids.length, embedded: 0 };
      const vectors = await deps.embeddings!.embed(chunks.map((c) => `${c.section}\n${c.body}`));
      if (vectors.length !== ids.length) throw new Error("the embedding port returned a different number of vectors than texts");
      for (const [i, id] of ids.entries()) await deps.vectors!.upsert(tenantId, id, vectors[i] ?? []);
      return { indexed: ids.length, embedded: ids.length };
    },
    async search(tenantId, query, limit = 10) {
      const keyword = await deps.keywords.keyword(tenantId, query, FETCH);
      let near: Array<RetrievedChunk & { similarity: number }> = [];
      if (semantic) {
        const [vector] = await deps.embeddings!.embed([query]);
        const hits = vector === undefined ? [] : await deps.vectors!.nearest(tenantId, vector, FETCH);
        const known = new Map((await deps.keywords.byIds(tenantId, hits.map((h) => h.chunkId))).map((c) => [c.chunkId, c]));
        near = hits.flatMap((h) => {
          const c = known.get(h.chunkId);
          return c === undefined ? [] : [{ ...c, score: h.similarity, similarity: h.similarity }];
        });
      }
      return { chunks: fuseRankings(keyword, near, limit) };
    },
  };
}

/** In-memory vector index and embeddings for tests and local runs. Cosine similarity, exact search. */
export class MemoryVectorIndex implements VectorIndex {
  private readonly rows = new Map<string, Map<string, readonly number[]>>();
  async upsert(tenantId: string, chunkId: string, vector: readonly number[]): Promise<void> {
    const m = this.rows.get(tenantId) ?? new Map<string, readonly number[]>();
    m.set(chunkId, vector);
    this.rows.set(tenantId, m);
  }
  async nearest(tenantId: string, vector: readonly number[], limit: number): Promise<Array<{ chunkId: string; similarity: number }>> {
    return [...(this.rows.get(tenantId) ?? new Map<string, readonly number[]>())]
      .map(([chunkId, v]) => ({ chunkId, similarity: cosine(vector, v) }))
      .sort((a, b) => b.similarity - a.similarity || a.chunkId.localeCompare(b.chunkId))
      .slice(0, limit);
  }
}

export function cosine(a: readonly number[], b: readonly number[]): number {
  let dot = 0;
  let na = 0;
  let nb = 0;
  for (let i = 0; i < Math.max(a.length, b.length); i += 1) {
    const x = a[i] ?? 0;
    const y = b[i] ?? 0;
    dot += x * y;
    na += x * x;
    nb += y * y;
  }
  return na === 0 || nb === 0 ? 0 : dot / Math.sqrt(na * nb);
}

const SOURCE_TYPES = ["ARTICLE", "DOC", "POLICY"] as const;

/** Validates POST /api/v1/knowledge/chunks. */
export function parseChunks(raw: unknown): KnowledgeText[] {
  const body = asObject(raw, ["chunks"]);
  const list = body["chunks"];
  if (!Array.isArray(list) || list.length === 0 || list.length > MAX_CHUNKS_PER_REQUEST) {
    throw new ApiError(400, "AGT-0400", `chunks must hold 1 to ${MAX_CHUNKS_PER_REQUEST} entries`);
  }
  return list.map((item, i) => {
    const c = asObject(item, ["sourceType", "sourceRef", "section", "body"]);
    const { sourceType, sourceRef, section, body: text } = c;
    const bad = (field: string): never => {
      throw new ApiError(400, "AGT-0400", `chunks[${i}].${field} is invalid`);
    };
    if (typeof sourceType !== "string" || !(SOURCE_TYPES as readonly string[]).includes(sourceType)) bad("sourceType");
    if (typeof sourceRef !== "string" || sourceRef === "" || sourceRef.length > 200) bad("sourceRef");
    if (typeof section !== "string" || section === "" || section.length > 200) bad("section");
    if (typeof text !== "string" || text === "" || text.length > 20_000) bad("body");
    return { sourceType: sourceType as KnowledgeText["sourceType"], sourceRef: sourceRef as string, section: section as string, body: text as string };
  });
}

/** Validates POST /api/v1/knowledge/search. */
export function parseSearch(raw: unknown): { query: string; limit: number } {
  const body = asObject(raw, ["query", "limit"]);
  const { query, limit } = body;
  if (typeof query !== "string" || query.trim() === "" || query.length > MAX_QUERY_CHARS) throw new ApiError(400, "AGT-0400", `query is required, at most ${MAX_QUERY_CHARS} characters`);
  if (limit !== undefined && (typeof limit !== "number" || !Number.isInteger(limit) || limit < 1 || limit > 20)) throw new ApiError(400, "AGT-0400", "limit must be an integer from 1 to 20");
  return { query, limit: typeof limit === "number" ? limit : 10 };
}
