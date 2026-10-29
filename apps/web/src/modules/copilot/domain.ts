/** Copilot modes and the rules the panel follows (FR-47.3, 9.47.6). Pure functions. */

export const COPILOT_MODES = ["ask", "explain", "draft", "summarize"] as const;
export type CopilotMode = (typeof COPILOT_MODES)[number];

export const MODE_LABELS: Readonly<Record<CopilotMode, string>> = {
  ask: "Ask the data",
  explain: "Explain this record",
  draft: "Draft",
  summarize: "Summarize",
};

/** Every generated answer carries this label; it is never hidden. */
export const GENERATED_BADGE = "AI-generated: check before you use it";

export function isCopilotMode(value: string): value is CopilotMode {
  return (COPILOT_MODES as readonly string[]).includes(value);
}

/** Drafts are inserted only by an explicit click, and only for a draft that was not refused. */
export function canInsertDraft(mode: CopilotMode, refused: boolean): boolean {
  return mode === "draft" && !refused;
}

export function toolCallLabel(call: { tool: string; status: string; code?: string | undefined }): string {
  return call.code ? `${call.tool}: ${call.status} (${call.code})` : `${call.tool}: ${call.status}`;
}

export type ApprovalModeName = "none" | "suggest" | "approve_before_commit";

const APPROVAL_LABELS: Readonly<Record<ApprovalModeName, string>> = {
  none: "No approval",
  suggest: "Suggest",
  approve_before_commit: "Approve before commit",
};

/** x-passage-approval as shown to users (FR-47.1). */
export function approvalLabel(mode: ApprovalModeName): string {
  return APPROVAL_LABELS[mode];
}

export function approvalTone(mode: ApprovalModeName): "neutral" | "info" | "warning" {
  return mode === "approve_before_commit" ? "warning" : mode === "suggest" ? "info" : "neutral";
}

/** Minimal key-value store; the browser's localStorage satisfies it. */
export interface ModeStore {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

/** Storage key for the mode a user last chose in a workspace (9.47.6: "remembers mode per workspace per user"). */
export function modeMemoryKey(userId: string, workspace: string): string {
  return `passage.copilot.mode:${encodeURIComponent(userId)}:${encodeURIComponent(workspace)}`;
}

/**
 * The remembered mode, or undefined when nothing valid was stored. Invalid
 * stored values are ignored, and so is a store that throws (browser storage
 * is unavailable in private windows or when site data is blocked).
 */
export function readRememberedMode(store: ModeStore, userId: string, workspace: string): CopilotMode | undefined {
  try {
    const raw = store.getItem(modeMemoryKey(userId, workspace));
    return raw !== null && isCopilotMode(raw) ? raw : undefined;
  } catch {
    return undefined;
  }
}

/** Saves the mode; a store that refuses the write leaves the panel working without memory. */
export function rememberMode(store: ModeStore, userId: string, workspace: string, mode: CopilotMode): void {
  try {
    store.setItem(modeMemoryKey(userId, workspace), mode);
  } catch {
    // Remembering the mode is a convenience; never fail the panel over it.
  }
}
