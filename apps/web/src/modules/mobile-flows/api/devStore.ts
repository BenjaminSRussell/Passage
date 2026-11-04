// In-memory mobile service used by `vite serve` and Playwright (the real service is a
// handoff). Devices and the designer share this process so a publish reaches the menu
// without an app release (AC-32.4).

import { CORE_FLOWS } from "../domain/coreFlows";
import type { FlowDraft } from "../domain/flowGraph";
import type { FlowPublished, MobileMenu, SavedFlow } from "./client";

export interface ReplayedTx {
  idempotencyKey: string;
  headerKey: string | undefined;
  command: string;
  payload: Record<string, unknown>;
}

export interface MobileStore {
  saveDraft(flow: FlowDraft): SavedFlow;
  publish(flowId: string, ifMatch?: string): SavedFlow;
  menu(id: string): MobileMenu;
  onPublish(listener: (event: FlowPublished) => void): () => void;
  replay(tx: ReplayedTx): void;
  replays(): ReplayedTx[];
}

function clone(flow: FlowDraft, extra: Pick<SavedFlow, "flowId" | "version" | "status">): SavedFlow {
  return { code: flow.code, entryStepId: flow.entryStepId, steps: flow.steps, ...extra };
}

export function createMobileStore(): MobileStore {
  const drafts = new Map<string, SavedFlow>();
  const published = new Map<string, SavedFlow>();
  const replayed = new Map<string, ReplayedTx>();
  const listeners = new Set<(event: FlowPublished) => void>();

  for (const flow of CORE_FLOWS) {
    published.set(flow.code, clone(flow, { flowId: flow.code, version: 1, status: "PUBLISHED" }));
  }

  return {
    saveDraft(flow) {
      const current = drafts.get(flow.code) ?? published.get(flow.code);
      const saved = clone(flow, { flowId: flow.code, version: (current?.version ?? 0) + 1, status: "DRAFT" });
      drafts.set(flow.code, saved);
      return saved;
    },
    publish(flowId, ifMatch) {
      const draft = drafts.get(flowId);
      if (!draft) {
        throw Object.assign(new Error(`no draft ${flowId}`), { status: 404 });
      }
      if (ifMatch && ifMatch !== `"${draft.version}"` && ifMatch !== String(draft.version)) {
        throw Object.assign(new Error("If-Match does not match the draft version"), { status: 412 });
      }
      const next = { ...draft, status: "PUBLISHED" as const };
      drafts.delete(flowId);
      published.set(next.code, next);
      const event: FlowPublished = { flowId: next.flowId, code: next.code, version: next.version };
      for (const listener of listeners) listener(event);
      return next;
    },
    menu(id) {
      return { id, flows: [...published.values()] };
    },
    replay(tx) {
      if (!replayed.has(tx.idempotencyKey)) replayed.set(tx.idempotencyKey, tx);
    },
    replays() {
      return [...replayed.values()];
    },
    onPublish(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}
