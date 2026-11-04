// In-memory mobile service used by `vite serve` and Playwright (the real service is a
// handoff). Devices and the designer share this process so a publish reaches the menu
// without an app release (AC-32.4).

import { CORE_FLOWS, SHIP_LOAD_FLOW } from "../domain/coreFlows";
import type { FlowDraft } from "../domain/flowGraph";
import type { FlowPublished, MobileMenu, SavedFlow } from "./client";

export interface ReplayRequest {
  idempotencyKey: string;
  command: string;
  payload?: unknown;
  capturedAt?: string;
}

export interface ReplayReceipt {
  applied: true;
  idempotencyKey: string;
  duplicate: boolean;
}

export interface MobileStore {
  saveDraft(flow: FlowDraft): SavedFlow;
  publish(flowId: string, ifMatch?: string): SavedFlow;
  menu(id: string): MobileMenu;
  replay(tx: ReplayRequest): ReplayReceipt;
  replayed(): readonly ReplayReceipt[];
  onPublish(listener: (event: FlowPublished) => void): () => void;
}

function clone(flow: FlowDraft, extra: Pick<SavedFlow, "flowId" | "version" | "status">): SavedFlow {
  return { code: flow.code, entryStepId: flow.entryStepId, steps: flow.steps, ...extra };
}

export function createMobileStore(): MobileStore {
  const drafts = new Map<string, SavedFlow>();
  const published = new Map<string, SavedFlow>();
  const listeners = new Set<(event: FlowPublished) => void>();
  const receipts: ReplayReceipt[] = [];
  const seenKeys = new Set<string>();

  for (const flow of [...CORE_FLOWS, SHIP_LOAD_FLOW]) {
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
      const duplicate = seenKeys.has(tx.idempotencyKey);
      seenKeys.add(tx.idempotencyKey);
      const receipt: ReplayReceipt = { applied: true, idempotencyKey: tx.idempotencyKey, duplicate };
      if (!duplicate) receipts.push(receipt);
      return receipt;
    },
    replayed() {
      return receipts;
    },
    onPublish(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}
