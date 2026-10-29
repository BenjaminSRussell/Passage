import { AgentError } from "../errors.js";

/** A candidate chunk from one retriever (BM25 keyword or embedding similarity). */
export interface RetrievedChunk {
  chunkId: string;
  sourceType: "ARTICLE" | "DOC" | "POLICY";
  sourceRef: string;
  section: string;
  /** Retriever-specific score; only the order within one retriever matters here. */
  score: number;
}

export interface Citation {
  chunkId: string;
  sourceType: RetrievedChunk["sourceType"];
  sourceRef: string;
  section: string;
  /** Fused rank score in (0, 1]; higher is better. */
  fusedScore: number;
  /** Cosine similarity from the embedding retriever, when the chunk was found by it. */
  similarity?: number;
}

/** FR-47.14: answers without a citation above this similarity say so instead of guessing. */
export const CITATION_SIMILARITY_THRESHOLD = 0.75;

/** Reciprocal-rank-fusion constant (the conventional k = 60). */
export const RRF_K = 60;

/**
 * Hybrid search fusion: reciprocal rank fusion over the BM25 and embedding
 * rankings. Each retriever contributes 1 / (k + rank), so a chunk found by
 * both ranks above one found by only one. Ties break on chunk id for
 * determinism. Scores are normalised so the best possible fusion is 1.
 */
export function fuseRankings(
  keyword: RetrievedChunk[],
  semantic: Array<RetrievedChunk & { similarity: number }>,
  limit = 10,
): Citation[] {
  const byId = new Map<string, { chunk: RetrievedChunk; fused: number; similarity?: number }>();
  const add = (chunk: RetrievedChunk, rank: number, similarity?: number) => {
    const contribution = 1 / (RRF_K + rank);
    const existing = byId.get(chunk.chunkId);
    if (existing) {
      existing.fused += contribution;
      existing.similarity ??= similarity;
    } else {
      byId.set(chunk.chunkId, { chunk, fused: contribution, similarity });
    }
  };
  [...keyword].sort(byScoreThenId).forEach((c, i) => add(c, i + 1));
  [...semantic].sort((a, b) => b.similarity - a.similarity || a.chunkId.localeCompare(b.chunkId)).forEach((c, i) => add(c, i + 1, c.similarity));

  // The best possible fusion is rank 1 in both retrievers.
  const best = 2 / (RRF_K + 1);
  return [...byId.values()]
    .sort((a, b) => b.fused - a.fused || a.chunk.chunkId.localeCompare(b.chunk.chunkId))
    .slice(0, limit)
    .map(({ chunk, fused, similarity }) => ({
      chunkId: chunk.chunkId,
      sourceType: chunk.sourceType,
      sourceRef: chunk.sourceRef,
      section: chunk.section,
      fusedScore: fused / best,
      ...(similarity === undefined ? {} : { similarity }),
    }));
}

function byScoreThenId(a: RetrievedChunk, b: RetrievedChunk): number {
  return b.score - a.score || a.chunkId.localeCompare(b.chunkId);
}

export type KnowledgeAnswerGate =
  | { answerable: true; citations: Citation[] }
  | { answerable: false; code: "AGT-0212"; message: string };

/**
 * Citation gate: a grounded answer needs at least one citation whose embedding
 * similarity is at or above 0.75. Otherwise the copilot returns AGT-0212 and
 * must not guess. Keyword-only hits do not pass the gate on their own.
 */
export function gateCitations(citations: Citation[], threshold = CITATION_SIMILARITY_THRESHOLD): KnowledgeAnswerGate {
  const grounded = citations.filter((c) => c.similarity !== undefined && c.similarity >= threshold);
  if (grounded.length === 0) {
    const err = new AgentError("AGT-0212", "no knowledge source matches your question with sufficient confidence");
    return { answerable: false, code: "AGT-0212", message: err.message };
  }
  return { answerable: true, citations: grounded };
}
