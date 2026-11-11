import { randomUUID } from "node:crypto";
import type { RetrievedChunk } from "../domain/knowledge.js";
import type { KnowledgeText, KeywordIndex } from "../runtime/knowledge.js";
import type { PgAgentStore } from "./pg.js";

interface Row {
  chunk_id: string;
  source_type: RetrievedChunk["sourceType"];
  source_ref: string;
  section: string;
  score?: number;
}

const toChunk = (r: Row): RetrievedChunk => ({ chunkId: r.chunk_id, sourceType: r.source_type, sourceRef: r.source_ref, section: r.section, score: r.score ?? 0 });

/**
 * `knowledge_chunk` in Postgres: the keyword half of the hybrid search is full-text search ranked by `ts_rank_cd`
 * (cover density, the BM25-like score Postgres has), every statement in the caller's tenant under row-level security.
 */
export class PgKeywordIndex implements KeywordIndex {
  constructor(private readonly store: PgAgentStore) {}

  async upsert(tenantId: string, chunks: readonly KnowledgeText[]): Promise<string[]> {
    return this.store.withTenant(tenantId, async (c) => {
      const out: string[] = [];
      for (const k of chunks) {
        const r = await c.query<{ chunk_id: string }>(
          `insert into knowledge_chunk (tenant_id, chunk_id, source_type, source_ref, section, body) values ($1, $2, $3, $4, $5, $6)
           on conflict (tenant_id, source_type, source_ref, section) do update set body = excluded.body, indexed_at = now()
           returning chunk_id`,
          [tenantId, randomUUID(), k.sourceType, k.sourceRef, k.section, k.body],
        );
        out.push(r.rows[0]?.chunk_id ?? "");
      }
      return out;
    });
  }

  async keyword(tenantId: string, query: string, limit: number): Promise<RetrievedChunk[]> {
    return this.store.withTenant(tenantId, async (c) =>
      (
        await c.query<Row>(
          `select chunk_id, source_type, source_ref, section, ts_rank_cd(tsv, q)::float8 as score
             from knowledge_chunk, websearch_to_tsquery('english', $2) q
            where tenant_id = $1 and tsv @@ q order by score desc, chunk_id limit $3`,
          [tenantId, query, limit],
        )
      ).rows.map(toChunk),
    );
  }

  async byIds(tenantId: string, ids: readonly string[]): Promise<RetrievedChunk[]> {
    if (ids.length === 0) return [];
    return this.store.withTenant(tenantId, async (c) =>
      (await c.query<Row>("select chunk_id, source_type, source_ref, section from knowledge_chunk where tenant_id = $1 and chunk_id = any($2::uuid[])", [tenantId, ids])).rows.map(toChunk),
    );
  }
}
